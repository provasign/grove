package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// C/C++ call-edge ground truth from scip-clang's SCIP index. scip-clang
// type-checks every translation unit in a compile_commands.json, so each
// reference occurrence is a resolved use of a symbol. Unlike rust-analyzer
// it emits neither enclosing ranges nor symbol kinds, so we (a) recognize a
// function/method declaration from its SCIP descriptor (ends in "().") and
// (b) attribute each in-body reference to the nearest preceding function
// definition in the same file — exact for C, where functions don't nest,
// and a close approximation for C++.
//
// Generating the index needs cmake (for compile_commands.json) and the
// scip-clang binary; CI scores the committed snapshot with tree-sitter
// alone. $GROVE_EVAL_CFAMILY_SCIP points at a prebuilt index to skip the
// toolchain run.

func CFamilyCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}

	indexPath := os.Getenv("GROVE_EVAL_CFAMILY_SCIP")
	var tmp string
	if indexPath == "" {
		tmp, err = os.MkdirTemp("", "grove-cfamily-truth-*")
		if err != nil {
			return TruthFile{}, nil, err
		}
		defer os.RemoveAll(tmp)
		indexPath = filepath.Join(tmp, "index.scip")
		if err := runScipClang(root, indexPath); err != nil {
			return TruthFile{}, nil, err
		}
	}

	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return TruthFile{}, nil, err
	}
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		return TruthFile{}, nil, fmt.Errorf("parse %s: %w", indexPath, err)
	}

	defs := map[string]FuncRef{}  // symbol -> declaration
	byFile := map[string][]cfDef{} // file -> function defs, by line
	funcCount := 0
	for _, doc := range idx.Documents {
		file := filepath.ToSlash(doc.RelativePath)
		if cfamilyGenerated(file) {
			continue
		}
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) == 0 {
				continue
			}
			if !cfamilyIsFunction(occ.Symbol) {
				continue
			}
			span, ok := scipRange(occ.Range)
			if !ok {
				continue
			}
			ref := FuncRef{File: file, Line: int(span.start.line) + 1, Name: cfamilyDisplayName(occ.Symbol)}
			if _, dup := defs[occ.Symbol]; !dup {
				defs[occ.Symbol] = ref
				funcCount++
			}
			byFile[file] = append(byFile[file], cfDef{span.start.line, ref})
		}
	}
	for file := range byFile {
		sort.Slice(byFile[file], func(i, j int) bool { return byFile[file][i].line < byFile[file][j].line })
	}

	seen := map[[2]string]bool{}
	var edges []TruthEdge
	for _, doc := range idx.Documents {
		file := filepath.ToSlash(doc.RelativePath)
		if cfamilyGenerated(file) || cfamilyIsHeader(file) {
			// Call-edge truth lives in source-file (.c/.cc) bodies. Header
			// references are macros, prototypes, and inline functions whose
			// "nearest preceding definition" attribution is unreliable
			// (prototypes and macros create spurious partition boundaries).
			continue
		}
		fns := byFile[file]
		if len(fns) == 0 {
			continue
		}
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
				continue
			}
			callee, ok := defs[occ.Symbol]
			if !ok {
				continue
			}
			span, ok := scipRange(occ.Range)
			if !ok {
				continue
			}
			// Enclosing function: greatest def line <= reference line.
			caller := enclosingFunc(fns, span.start.line)
			if caller == nil || *caller == callee {
				continue
			}
			ck, ek := caller.funcKey(), callee.funcKey()
			if ck == ek {
				continue
			}
			key := [2]string{ck, ek}
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, TruthEdge{Caller: *caller, Callee: callee})
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller != b.Caller {
			return a.Caller.funcKey() < b.Caller.funcKey()
		}
		return a.Callee.funcKey() < b.Callee.funcKey()
	})
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "scip-clang",
		Functions: funcCount,
		Edges:     len(edges),
	}
	return header, edges, nil
}

// enclosingFunc returns the function whose definition line is the greatest
// not exceeding the reference line — the function the reference sits inside
// (functions don't nest in C).
// cfDef is a function definition's line and reference within one file.
type cfDef struct {
	line int32
	ref  FuncRef
}

func enclosingFunc(fns []cfDef, line int32) *FuncRef {
	lo, hi, found := 0, len(fns)-1, -1
	for lo <= hi {
		mid := (lo + hi) / 2
		if fns[mid].line <= line {
			found = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if found < 0 {
		return nil
	}
	return &fns[found].ref
}

// cfamilyGenerated reports whether a file is build output (cmake writes
// generated headers under build/), which a plain checkout — and so the
// tree-sitter index CI scores against — never contains.
func cfamilyGenerated(file string) bool {
	return strings.HasPrefix(file, "build/") || strings.Contains(file, "/build/") ||
		strings.HasPrefix(file, "cmake-build")
}

// cfamilyIsHeader reports whether a file is a C/C++ header.
func cfamilyIsHeader(file string) bool {
	switch {
	case strings.HasSuffix(file, ".h"), strings.HasSuffix(file, ".hpp"),
		strings.HasSuffix(file, ".hh"), strings.HasSuffix(file, ".hxx"),
		strings.HasSuffix(file, ".inc"):
		return true
	}
	return false
}

// cfamilyIsFunction reports whether a SCIP symbol denotes a function or
// method: its final descriptor is a method descriptor ("name().").
func cfamilyIsFunction(symbol string) bool {
	if strings.HasPrefix(symbol, "local ") {
		return false
	}
	return strings.HasSuffix(symbol, ").")
}

// cfamilyDisplayName turns a scip-clang symbol into "func" or "Type.method".
// scip-clang appends a disambiguating hash inside the parens
// ("json_init(620b9ac4).") which is stripped.
func cfamilyDisplayName(symbol string) string {
	parsed, err := scip.ParseSymbol(symbol)
	if err != nil || len(parsed.Descriptors) == 0 {
		// Fallback: last whitespace-separated token, hash and parens removed.
		s := symbol
		if i := strings.LastIndexByte(s, ' '); i >= 0 {
			s = s[i+1:]
		}
		if i := strings.IndexByte(s, '('); i >= 0 {
			s = s[:i]
		}
		return strings.TrimRight(s, ".#/")
	}
	d := parsed.Descriptors
	name := stripCparen(d[len(d)-1].Name)
	if len(d) >= 2 {
		switch d[len(d)-2].Suffix {
		case scip.Descriptor_Type, scip.Descriptor_Namespace:
			if owner := stripCparen(d[len(d)-2].Name); owner != "" && owner != "$" {
				return owner + "." + name
			}
		}
	}
	return name
}

func stripCparen(name string) string {
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	return name
}

// runScipClang generates compile_commands.json with cmake (if absent) and
// runs scip-clang to produce the SCIP index.
func runScipClang(root, indexPath string) error {
	bin := os.Getenv("GROVE_EVAL_SCIP_CLANG")
	if bin == "" {
		if p, err := exec.LookPath("scip-clang"); err == nil {
			bin = p
		} else if home := os.Getenv("HOME"); home != "" {
			if _, err := os.Stat(home + "/bin/scip-clang"); err == nil {
				bin = home + "/bin/scip-clang"
			}
		}
	}
	if bin == "" {
		return fmt.Errorf("scip-clang not found (PATH, ~/bin, or $GROVE_EVAL_SCIP_CLANG)")
	}
	compdb := filepath.Join(root, "build", "compile_commands.json")
	if _, err := os.Stat(compdb); err != nil {
		cmake, err := exec.LookPath("cmake")
		if err != nil {
			return fmt.Errorf("cmake not found and no build/compile_commands.json in %s", root)
		}
		cfg := exec.Command(cmake, "-B", "build",
			"-DCMAKE_EXPORT_COMPILE_COMMANDS=ON",
			"-DCMAKE_POLICY_VERSION_MINIMUM=3.5")
		cfg.Dir = root
		if out, err := cfg.CombinedOutput(); err != nil {
			return fmt.Errorf("cmake configure: %w\n%s", err, tail(out))
		}
	}
	cmd := exec.Command(bin, "--compdb-path="+compdb, "--index-output-path="+indexPath)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scip-clang: %w\n%s", err, tail(out))
	}
	return nil
}
