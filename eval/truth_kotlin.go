package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Kotlin call-edge ground truth from the JVM bytecode kotlinc produces:
// compile with kotlinc, then read invoke* instructions and LineNumberTables
// out of javap — the exact mechanism JavaCallTruth uses (parseJavap is
// pure bytecode disassembly, reused unchanged), since Kotlin compiles to
// ordinary JVM class files javap disassembles identically. Bytecode is what
// actually runs, so this oracle sees through overloads and static dispatch
// exactly; dynamic dispatch (invokevirtual/interface) records the declared
// receiver type — the same "may affect" altitude as the Go VTA oracle.
//
// Kotlin generates callable JVM methods astkit's Kotlin strategy does not
// model as separate symbols — property getters/setters (every declared
// property, not just computed ones) and each class's compiler-synthesized
// primary constructor when there is no explicit `init` block extraction
// covers. Edges to/from those never match a Grove symbol and show up as
// recall misses rather than false positives; isSyntheticKotlinName only
// filters names with no source-level declaration at all (default-value
// dispatchers, lambda/access bridges), not that broader "unmodeled by
// astkit" set, which is a known, accepted limitation of the current Kotlin
// support tier (see capabilities.go).

// KotlinCallTruth compiles the repo's main sources with kotlinc and derives
// caller→callee edges between in-repo declarations from the bytecode.
func KotlinCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	srcRoot := root
	if st, err := os.Stat(filepath.Join(root, "src", "main", "kotlin")); err == nil && st.IsDir() {
		// A sibling src/main/java is common in Kotlin/Java interop projects
		// (annotations, legacy classes Kotlin code references); kotlinc
		// resolves against .java sources given alongside .kt ones — without
		// them, any Kotlin file referencing a Java-defined type fails to
		// compile at all, not just to resolve that one reference.
		srcRoot = filepath.Join(root, "src", "main")
	}
	var ktSources, javaSources []string
	_ = filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.Contains(path, string(filepath.Separator)+"test") {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".kt"):
			ktSources = append(ktSources, path)
		case strings.HasSuffix(path, ".java"):
			javaSources = append(javaSources, path)
		}
		return nil
	})
	if len(ktSources) == 0 {
		return TruthFile{}, nil, fmt.Errorf("no .kt sources under %s", srcRoot)
	}
	// kotlinc resolves symbols against the .java sources but — unlike a
	// real Gradle build, which runs javac separately — does not itself
	// emit class files for them, so their own declarations/edges are
	// invisible to this oracle; only the Kotlin side is measured.
	sources := append(append([]string{}, ktSources...), javaSources...)

	tmp, err := os.MkdirTemp("", "grove-kotlin-truth-*")
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer os.RemoveAll(tmp)
	classesDir := filepath.Join(tmp, "classes")
	if err := os.MkdirAll(classesDir, 0o755); err != nil {
		return TruthFile{}, nil, err
	}
	args := append([]string{"-nowarn", "-d", classesDir}, sources...)
	cmd := exec.Command("kotlinc", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return TruthFile{}, nil, fmt.Errorf("kotlinc: %v\n%s", err, lastLines(string(out), 8))
	}

	var classFiles []string
	_ = filepath.WalkDir(classesDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".class") {
			classFiles = append(classFiles, path)
		}
		return nil
	})

	methods := map[string]*javaMethod{} // FQN.name:descriptor → method
	var ordered []*javaMethod
	for _, cf := range classFiles {
		fqn := strings.TrimSuffix(filepath.ToSlash(strings.TrimPrefix(cf, classesDir+string(filepath.Separator))), ".class")
		if isSyntheticKotlinName(lastSegment(fqn, '/')) {
			continue
		}
		out, err := exec.Command("javap", "-v", "-p", cf).Output()
		if err != nil {
			continue
		}
		parsed := parseJavap(string(out), strings.ReplaceAll(fqn, "/", "."))
		for _, m := range parsed {
			if m.file == "" || m.line == 0 || isSyntheticKotlinName(m.name) {
				continue
			}
			// Kotlin's "Compiled from" gives the bare source filename, and
			// (unlike Java) file layout carries no package-directory
			// convention to reconstruct a path from — every source file
			// under srcRoot is searched by basename instead.
			full := kotlinFindSource(sources, m.file)
			if full == "" {
				continue
			}
			rel, err := filepath.Rel(root, full)
			if err != nil {
				continue
			}
			m.file = filepath.ToSlash(rel)
			methods[m.classFQN+"."+m.name+":"+m.descriptor] = m
			ordered = append(ordered, m)
		}
	}

	refOf := func(m *javaMethod) FuncRef {
		cls := lastSegment(m.classFQN, '.')
		cls = lastSegment(cls, '$')
		name := m.name
		if name == "<init>" {
			name = cls
		}
		return FuncRef{File: m.file, Line: m.line, Name: cls + "." + name}
	}

	seen := map[string]bool{}
	var edges []TruthEdge
	funcs := map[string]bool{}
	for _, m := range ordered {
		caller := refOf(m)
		funcs[caller.funcKey()] = true
		for _, inv := range m.invokes {
			if isSyntheticKotlinName(inv.name) {
				continue
			}
			target := methods[strings.ReplaceAll(inv.classFQN, "/", ".")+"."+inv.name+":"+inv.descriptor]
			if target == nil {
				continue // outside the repo (kotlin-stdlib, JDK, deps)
			}
			callee := refOf(target)
			funcs[callee.funcKey()] = true
			key := caller.funcKey() + "→" + callee.funcKey()
			if seen[key] || caller.funcKey() == callee.funcKey() {
				continue
			}
			seen[key] = true
			edges = append(edges, TruthEdge{Caller: caller, Callee: callee})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller.File != b.Caller.File {
			return a.Caller.File < b.Caller.File
		}
		if a.Caller.Line != b.Caller.Line {
			return a.Caller.Line < b.Caller.Line
		}
		return a.Callee.funcKey() < b.Callee.funcKey()
	})
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "kotlinc-javap",
		Functions: len(funcs),
		Edges:     len(edges),
	}
	return header, edges, nil
}

// kotlinFindSource resolves a "Compiled from" basename (e.g. "Person.kt")
// against the actual source list: Kotlin, unlike Java, has no
// package-must-match-directory rule, so the package-relative reconstruction
// JavaCallTruth uses cannot apply here.
func kotlinFindSource(sources []string, base string) string {
	for _, s := range sources {
		if filepath.Base(s) == base {
			return s
		}
	}
	return ""
}

// isSyntheticKotlinName excludes compiler-generated members that have no
// corresponding source-level declaration at all: default-argument
// dispatchers, lambda/lexical-scope access bridges, and the class
// initializer. It deliberately does NOT try to filter data-class-generated
// equals/hashCode/toString/copy/componentN — those sit at the class's own
// declaration line, where they simply fail to match any Grove symbol
// (astkit records no declaration there either) rather than needing an
// explicit name-based exclusion.
func isSyntheticKotlinName(name string) bool {
	return strings.Contains(name, "$default") ||
		strings.Contains(name, "access$") ||
		strings.HasPrefix(name, "lambda$") ||
		name == "<clinit>"
}
