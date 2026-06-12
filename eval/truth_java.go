package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Java call-edge ground truth from the JDK alone: compile with javac -g,
// then read invoke* instructions and LineNumberTables out of javap.
// Bytecode is what actually runs, so this oracle sees through overloads
// and static dispatch exactly; dynamic dispatch (invokevirtual/interface)
// records the declared receiver type — the same "may affect" altitude as
// the Go VTA oracle. Lambdas and synthetic accessors are skipped.

var (
	javapSourceRe = regexp.MustCompile(`^Compiled from "([^"]+)"`)
	// "  public int run(java.lang.String);"  /  "  public demo.Demo();"
	javapMethodRe = regexp.MustCompile(`^  [\w<>\[\].$, ]*?([\w$.]+|"<init>")\((.*)\);$`)
	// 5: invokevirtual #16  // Method demo/Helper.size:(...)I
	javapInvokeRe = regexp.MustCompile(`// (?:Interface)?Method (?:([\w/$]+)\.)?"?([\w$<>]+)"?:(\(.*?\)\S+)`)
	javapLineRe   = regexp.MustCompile(`^        line (\d+): \d+`)
)

type javaMethod struct {
	classFQN string
	name     string // "<init>" for constructors
	argc     int    // parameter count, for overload disambiguation
	file     string // repo-relative source path
	line     int    // first LineNumberTable line (inside the body)
	invokes  []javaInvoke
}

type javaInvoke struct {
	classFQN string
	name     string
	argc     int
}

// JavaCallTruth compiles the repo's main sources and derives caller→callee
// edges between in-repo declarations from the bytecode.
func JavaCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	srcRoot := root
	if st, err := os.Stat(filepath.Join(root, "src", "main", "java")); err == nil && st.IsDir() {
		srcRoot = filepath.Join(root, "src", "main", "java")
	}
	var sources []string
	_ = filepath.WalkDir(srcRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".java") && !strings.Contains(path, string(filepath.Separator)+"test") {
			sources = append(sources, path)
		}
		return nil
	})
	if len(sources) == 0 {
		return TruthFile{}, nil, fmt.Errorf("no .java sources under %s", srcRoot)
	}

	tmp, err := os.MkdirTemp("", "grove-java-truth-*")
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer os.RemoveAll(tmp)
	classesDir := filepath.Join(tmp, "classes")
	_ = os.MkdirAll(classesDir, 0o755)
	listFile := filepath.Join(tmp, "sources.txt")
	if err := os.WriteFile(listFile, []byte(strings.Join(sources, "\n")), 0o644); err != nil {
		return TruthFile{}, nil, err
	}
	cmd := exec.Command("javac", "-g", "-nowarn", "-proc:none", "-d", classesDir, "@"+listFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		return TruthFile{}, nil, fmt.Errorf("javac: %v\n%s", err, lastLines(string(out), 8))
	}

	var classFiles []string
	_ = filepath.WalkDir(classesDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(path, ".class") {
			classFiles = append(classFiles, path)
		}
		return nil
	})

	methods := map[string][]*javaMethod{} // FQN.name → overloads
	var ordered []*javaMethod
	for _, cf := range classFiles {
		fqn := strings.TrimSuffix(filepath.ToSlash(strings.TrimPrefix(cf, classesDir+string(filepath.Separator))), ".class")
		if isSyntheticJavaName(lastSegment(fqn, '/')) {
			continue
		}
		out, err := exec.Command("javap", "-c", "-p", "-l", cf).Output()
		if err != nil {
			continue
		}
		parsed := parseJavap(string(out), strings.ReplaceAll(fqn, "/", "."))
		pkgDir := filepath.Dir(strings.ReplaceAll(fqn, "/", string(filepath.Separator)))
		for _, m := range parsed {
			if m.file == "" || m.line == 0 || isSyntheticJavaName(m.name) {
				continue
			}
			// "Compiled from" gives the bare filename; the package dir
			// completes the path relative to the source root.
			full := filepath.Join(srcRoot, pkgDir, m.file)
			rel, err := filepath.Rel(root, full)
			if err != nil {
				continue
			}
			m.file = filepath.ToSlash(rel)
			key := m.classFQN + "." + m.name
			methods[key] = append(methods[key], m)
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
			if isSyntheticJavaName(inv.name) {
				continue
			}
			overloads := methods[strings.ReplaceAll(inv.classFQN, "/", ".")+"."+inv.name]
			var target *javaMethod
			for _, cand := range overloads {
				if cand.argc == inv.argc {
					target = cand
					break
				}
			}
			if target == nil && len(overloads) == 1 {
				target = overloads[0]
			}
			if target == nil {
				continue // outside the repo (JDK, deps), or unmatched overload
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
		Generator: "javac-javap",
		Functions: len(funcs),
		Edges:     len(edges),
	}
	return header, edges, nil
}

// parseJavap extracts methods, their first source line, and their invoke
// instructions from one class's javap -c -p -l output.
func parseJavap(out, classFQN string) []*javaMethod {
	var methods []*javaMethod
	var cur *javaMethod
	var sourceFile string
	inLineTable := false
	for _, line := range strings.Split(out, "\n") {
		if m := javapSourceRe.FindStringSubmatch(line); m != nil {
			sourceFile = m[1]
			continue
		}
		if m := javapMethodRe.FindStringSubmatch(line); m != nil {
			name := strings.Trim(m[1], `"`)
			if strings.ContainsRune(name, '.') {
				name = "<init>" // constructor headers carry the class FQN
			}
			cur = &javaMethod{classFQN: classFQN, name: name, file: sourceFile, argc: javaHeaderArgc(m[2])}
			methods = append(methods, cur)
			inLineTable = false
			continue
		}
		if cur == nil {
			continue
		}
		if strings.HasPrefix(line, "      LineNumberTable:") {
			inLineTable = true
			continue
		}
		if inLineTable {
			if m := javapLineRe.FindStringSubmatch(line); m != nil {
				if cur.line == 0 {
					fmt.Sscanf(m[1], "%d", &cur.line)
				}
				continue
			}
			inLineTable = false
		}
		if m := javapInvokeRe.FindStringSubmatch(line); m != nil {
			cls := m[1]
			if cls == "" {
				cls = strings.ReplaceAll(classFQN, ".", "/")
			}
			cur.invokes = append(cur.invokes, javaInvoke{
				classFQN: cls,
				name:     strings.Trim(m[2], `"`),
				argc:     javaDescriptorArgc(m[3]),
			})
		}
	}
	return methods
}

// javaHeaderArgc counts parameters in a javap method header's Java-syntax
// parameter list ("java.lang.String, int...").
func javaHeaderArgc(params string) int {
	params = strings.TrimSpace(params)
	if params == "" {
		return 0
	}
	depth, n := 0, 1
	for i := 0; i < len(params); i++ {
		switch params[i] {
		case '<':
			depth++
		case '>':
			depth--
		case ',':
			if depth == 0 {
				n++
			}
		}
	}
	return n
}

// javaDescriptorArgc counts parameters in a JVM method descriptor
// ("(Ljava/lang/String;I[J)V" → 3).
func javaDescriptorArgc(desc string) int {
	inner := desc
	if i := strings.IndexByte(inner, '('); i >= 0 {
		inner = inner[i+1:]
	}
	if i := strings.IndexByte(inner, ')'); i >= 0 {
		inner = inner[:i]
	}
	n := 0
	for i := 0; i < len(inner); {
		switch inner[i] {
		case '[':
			i++
			continue
		case 'L':
			j := strings.IndexByte(inner[i:], ';')
			if j < 0 {
				return n
			}
			i += j + 1
		default:
			i++
		}
		n++
	}
	return n
}

func isSyntheticJavaName(name string) bool {
	return strings.Contains(name, "lambda$") || strings.Contains(name, "access$") ||
		name == "$values" || strings.Contains(name, "$deserializeLambda$") ||
		name == "<clinit>"
}

func lastSegment(s string, sep byte) string {
	if i := strings.LastIndexByte(s, sep); i >= 0 {
		return s[i+1:]
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
