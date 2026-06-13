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

// Rust call-edge ground truth from rust-analyzer's SCIP export. The indexer
// type-checks the whole cargo workspace, so every reference occurrence is a
// resolved, typed use of a symbol. We take function-kind definition
// occurrences (each carries its body span as enclosing_range) as the
// declaration universe, and attribute every in-body reference to an in-repo
// function symbol as a caller→callee edge — calls, method calls through
// inference, and function-as-value references alike, the same "may affect"
// altitude as the Go VTA oracle. Calls inside closures attribute to the
// enclosing named declaration, mirroring Grove. Dynamic dispatch through a
// trait object records the trait method rust-analyzer resolves to, like
// invokevirtual recording the declared receiver in the Java oracle.

// rustFuncKinds are the SymbolInformation kinds that count as callable
// declarations. Macros are excluded: their "calls" expand at compile time
// and Grove has no corresponding symbol kind.
var rustFuncKinds = map[scip.SymbolInformation_Kind]bool{
	scip.SymbolInformation_Function:     true,
	scip.SymbolInformation_Method:       true,
	scip.SymbolInformation_StaticMethod: true,
	scip.SymbolInformation_TraitMethod:  true,
	scip.SymbolInformation_Constructor:  true,
}

type rustPos struct{ line, col int32 }

type rustSpan struct {
	start, end rustPos
}

func (s rustSpan) contains(p rustPos) bool {
	if p.line < s.start.line || p.line > s.end.line {
		return false
	}
	if p.line == s.start.line && p.col < s.start.col {
		return false
	}
	if p.line == s.end.line && p.col > s.end.col {
		return false
	}
	return true
}

func (s rustSpan) size() int64 {
	return int64(s.end.line-s.start.line)<<16 + int64(s.end.col-s.start.col)
}

// scipRange decodes SCIP's compact range encoding: [startLine, startChar,
// endLine, endChar], or [startLine, startChar, endChar] when single-line.
func scipRange(r []int32) (rustSpan, bool) {
	switch len(r) {
	case 3:
		return rustSpan{rustPos{r[0], r[1]}, rustPos{r[0], r[2]}}, true
	case 4:
		return rustSpan{rustPos{r[0], r[1]}, rustPos{r[2], r[3]}}, true
	}
	return rustSpan{}, false
}

type rustDef struct {
	sym  string
	ref  FuncRef
	span rustSpan // enclosing_range: the full declaration body
}

// RustCallTruth runs rust-analyzer's SCIP indexer over the workspace and
// derives caller→callee edges between in-repo function declarations.
func RustCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	bin, err := rustAnalyzerBin()
	if err != nil {
		return TruthFile{}, nil, err
	}

	tmp, err := os.MkdirTemp("", "grove-rust-truth-*")
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer os.RemoveAll(tmp)
	indexPath := filepath.Join(tmp, "index.scip")

	cmd := exec.Command(bin, "scip", ".", "--output", indexPath)
	cmd.Dir = root
	cmd.Env = truthEnvForRust()
	if out, err := cmd.CombinedOutput(); err != nil {
		return TruthFile{}, nil, fmt.Errorf("rust-analyzer scip: %w\n%s", err, tail(out))
	}
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		return TruthFile{}, nil, err
	}
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		return TruthFile{}, nil, fmt.Errorf("parse %s: %w", indexPath, err)
	}

	// Pass 1: symbol kinds (SymbolInformation lives in the defining
	// document) and function definitions with body spans.
	kinds := map[string]scip.SymbolInformation_Kind{}
	for _, doc := range idx.Documents {
		for _, si := range doc.Symbols {
			kinds[si.Symbol] = si.Kind
		}
	}
	// A symbol string is NOT unique across targets of one package: the
	// build-script crate's main() and the bin crate's main() share it, as
	// can cfg-variant declarations. So caller identity always comes from
	// the definition in the referencing file, and a multi-definition
	// callee symbol resolves same-file first, else drops as ambiguous.
	defs := map[string][]rustDef{} // symbol -> all declarations
	byFile := map[string][]rustDef{}
	lines := newLineCache(root)
	funcCount, macroGenerated := 0, 0
	for _, doc := range idx.Documents {
		file := filepath.ToSlash(doc.RelativePath)
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) == 0 {
				continue
			}
			if !rustFuncKinds[kinds[occ.Symbol]] {
				continue
			}
			nameSpan, ok := scipRange(occ.Range)
			if !ok {
				continue
			}
			// Macro-generated functions (rgtest!-style) map their name to
			// the token inside the macro invocation; no syntax-level tool
			// can see the declaration, so they are skipped like javap's
			// synthetics — otherwise they poison the universe canary.
			if !lines.fnKeywordBefore(file, nameSpan.start) {
				macroGenerated++
				continue
			}
			bodySpan, ok := scipRange(occ.EnclosingRange)
			if !ok {
				bodySpan = nameSpan
			}
			def := rustDef{
				sym:  occ.Symbol,
				ref:  FuncRef{File: file, Line: int(nameSpan.start.line) + 1, Name: rustDisplayName(occ.Symbol)},
				span: bodySpan,
			}
			defs[occ.Symbol] = append(defs[occ.Symbol], def)
			byFile[file] = append(byFile[file], def)
			funcCount++
		}
	}

	// Pass 2: references to in-repo functions, attributed to the tightest
	// enclosing function body in the referencing file. Module-level
	// references (use statements, const initializers) have no enclosing
	// function and drop out naturally; explicit import occurrences are
	// skipped as belt and braces.
	seen := map[[2]string]bool{}
	ambiguous := 0
	var edges []TruthEdge
	for _, doc := range idx.Documents {
		file := filepath.ToSlash(doc.RelativePath)
		funcs := byFile[file]
		for _, occ := range doc.Occurrences {
			if occ.SymbolRoles&int32(scip.SymbolRole_Definition) != 0 {
				continue
			}
			if occ.SymbolRoles&int32(scip.SymbolRole_Import) != 0 {
				continue
			}
			candidates, ok := defs[occ.Symbol]
			if !ok {
				continue
			}
			occSpan, ok := scipRange(occ.Range)
			if !ok {
				continue
			}
			var caller *rustDef
			var best int64 = 1<<62 - 1
			for i := range funcs {
				f := &funcs[i]
				if f.span.contains(occSpan.start) && f.span.size() < best {
					best = f.span.size()
					caller = f
				}
			}
			if caller == nil || caller.sym == occ.Symbol {
				continue
			}
			callee, ok := resolveRustCallee(candidates, file)
			if !ok {
				ambiguous++
				continue
			}
			key := [2]string{caller.ref.funcKey(), callee.funcKey()}
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, TruthEdge{Caller: caller.ref, Callee: callee})
		}
	}
	if ambiguous > 0 {
		fmt.Fprintf(os.Stderr, "rust truth: %d references to multi-definition symbols dropped as ambiguous\n", ambiguous)
	}
	if macroGenerated > 0 {
		fmt.Fprintf(os.Stderr, "rust truth: %d macro-generated declarations skipped (no fn syntax at the definition site)\n", macroGenerated)
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
		Generator: "rust-analyzer-scip",
		Functions: funcCount,
		Edges:     len(edges),
	}
	return header, edges, nil
}

// resolveRustCallee picks the declaration a reference points at. A unique
// definition wins outright; among multiple (target or cfg collisions) a
// single same-file definition wins; anything else is ambiguous.
func resolveRustCallee(candidates []rustDef, file string) (FuncRef, bool) {
	if len(candidates) == 1 {
		return candidates[0].ref, true
	}
	var local *FuncRef
	for i := range candidates {
		if candidates[i].ref.File == file {
			if local != nil {
				return FuncRef{}, false
			}
			local = &candidates[i].ref
		}
	}
	if local != nil {
		return *local, true
	}
	return FuncRef{}, false
}

// rustDisplayName turns a SCIP symbol into "Type.method" / "function" form.
func rustDisplayName(symbol string) string {
	parsed, err := scip.ParseSymbol(symbol)
	if err != nil || len(parsed.Descriptors) == 0 {
		return symbol
	}
	d := parsed.Descriptors
	name := d[len(d)-1].Name
	if len(d) >= 2 && d[len(d)-2].Suffix == scip.Descriptor_Type {
		return d[len(d)-2].Name + "." + name
	}
	return name
}

// rustAnalyzerBin locates rust-analyzer: explicit override, PATH, then
// rustup's component install location.
func rustAnalyzerBin() (string, error) {
	if env := os.Getenv("GROVE_EVAL_RUST_ANALYZER"); env != "" {
		return env, nil
	}
	if p, err := exec.LookPath("rust-analyzer"); err == nil {
		return p, nil
	}
	if out, err := exec.Command("rustup", "which", "rust-analyzer").Output(); err == nil {
		if p := strings.TrimSpace(string(out)); p != "" {
			return p, nil
		}
	}
	return "", fmt.Errorf("rust-analyzer not found (PATH, $GROVE_EVAL_RUST_ANALYZER, rustup which)")
}

// truthEnvForRust keeps the indexer hermetic to the pinned checkout: cargo
// must not auto-install toolchains mid-run.
func truthEnvForRust() []string {
	return append(os.Environ(), "RUSTUP_AUTO_INSTALL=0")
}

// lineCache lazily reads repo files split into lines for definition-site
// syntax checks.
type lineCache struct {
	root  string
	files map[string][]string
}

func newLineCache(root string) *lineCache {
	return &lineCache{root: root, files: map[string][]string{}}
}

// fnKeywordBefore reports whether the text preceding the name token on its
// declaration line ends with the `fn` keyword (covering pub/async/unsafe/
// const/extern prefixes). Macro-generated declarations fail this check.
func (c *lineCache) fnKeywordBefore(file string, pos rustPos) bool {
	ls, ok := c.files[file]
	if !ok {
		raw, err := os.ReadFile(filepath.Join(c.root, filepath.FromSlash(file)))
		if err != nil {
			ls = nil
		} else {
			ls = strings.Split(string(raw), "\n")
		}
		c.files[file] = ls
	}
	if int(pos.line) >= len(ls) {
		return false
	}
	line := ls[pos.line]
	col := int(pos.col)
	if col > len(line) {
		col = len(line)
	}
	prefix := strings.TrimRight(line[:col], " \t")
	return prefix == "fn" || strings.HasSuffix(prefix, " fn") || strings.HasSuffix(prefix, "\tfn")
}

func tail(b []byte) string {
	s := strings.TrimSpace(string(b))
	if lines := strings.Split(s, "\n"); len(lines) > 12 {
		return strings.Join(lines[len(lines)-12:], "\n")
	}
	return s
}
