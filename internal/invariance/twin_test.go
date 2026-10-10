// Package invariance holds metamorphic regression tests for grove's code
// graph: a comment, string literal, docstring, or formatting change must not
// change the graph.
//
// Each twin case is a tiny repository in two forms. CLEAN carries no
// comment/string trap; DECOY differs only by comments, strings, docstrings,
// or layout that a correct indexer ignores. Both are indexed end-to-end
// through the public engine (pkg/grove: parser -> store -> edges -> native
// merge), normalized (symbol IDs carry blob SHAs and lines, so endpoints are
// rewritten to file::qualifiedName#kind), and compared. Every case also
// lists want/wantNot facts so equality alone cannot pass a case where both
// forms are equally wrong.
//
// Cases are named by the comment/string-as-code audit IDs (2026-10-10).
// They were written against branch fix/comment-string-masking before the
// fixes landed: most fail there by design.
//
// Native analyzers: every case runs twice, mode "astkit" (native disabled
// via grove.Config.NativeAnalyzers=false) and mode "native" (enabled). The
// native mode is skipped when no native analyzer completed a pass for the
// fixture (toolchain missing on this machine, e.g. no dotnet for C#), which
// is read from IndexResult.Native diagnostics.
//
// Run: go test ./internal/invariance/ -run TestCommentStringInvariance
// One case: -run 'TestCommentStringInvariance/astkit/L1-python'
package invariance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/provasign/grove/pkg/grove"
)

// twinCase is one audit finding as a metamorphic pair.
type twinCase struct {
	id    string            // audit ID + short tag, used as the subtest name
	clean map[string]string // repo-relative path -> content
	decoy map[string]string // same paths; differs only in comments/strings/layout
	// want / wantNot are facts checked on BOTH forms (see holds):
	//   "E <src> <edge-type> <dst>"  an edge; src/dst match a symbol's
	//        qualified name, Parent.Name, or bare name (no dot), optionally
	//        suffixed "@<file>"; "file:<path>" for file nodes; "*" for any
	//   "S <name> [kind=K] [mod=M] [exported] [unexported] [lang=L]
	//        [doc~TEXT] [start=N] [end=N]"  a symbol with all those properties
	//        (TEXT uses "_" for spaces)
	want, wantNot []string
	// wantNative are facts only a native analyzer produces (e.g. uses-type
	// from a local variable declaration); checked in native mode only.
	wantNative []string
	doc        bool // compare docstrings
	sig        bool // compare signatures (whitespace/comments collapsed)
	spans      bool // compare spans (decoy must keep line numbers)
	// rawAnnotations: the decoy edits an attribute/decorator argument, whose
	// raw text annotations carry verbatim, so annotations are not compared.
	rawAnnotations bool
}

func allTwinCases() []twinCase {
	var all []twinCase
	all = append(all, casesP()...)
	all = append(all, casesL()...)
	all = append(all, casesN()...)
	all = append(all, casesA()...)
	all = append(all, casesE()...)
	all = append(all, casesC()...)
	all = append(all, casesJ()...)
	return all
}

func TestCommentStringInvariance(t *testing.T) {
	cases := allTwinCases()
	seen := map[string]bool{}
	for _, tc := range cases {
		if seen[tc.id] {
			t.Fatalf("duplicate case id %s", tc.id)
		}
		seen[tc.id] = true
	}
	for _, mode := range []string{"astkit", "native"} {
		mode := mode
		t.Run(mode, func(t *testing.T) {
			if mode == "native" && testing.Short() && runtime.GOOS == "windows" {
				// Native analyzers start compilers per fixture, which Windows
				// runners do slowly; Linux and macOS cover this mode in -short.
				t.Skip("native mode skipped in -short on Windows")
			}
			for _, tc := range cases {
				tc := tc
				t.Run(tc.id, func(t *testing.T) {
					// Each case indexes its own temp repos twice; serially
					// the table exceeded CI's 2-minute budget on Windows.
					t.Parallel()
					runTwin(t, tc, mode == "native")
				})
			}
		})
	}
}

func runTwin(t *testing.T, tc twinCase, nativeOn bool) {
	clean, cleanDiag := indexFixture(t, tc.clean, nativeOn)
	decoy, decoyDiag := indexFixture(t, tc.decoy, nativeOn)
	if nativeOn && !nativeRan(cleanDiag) {
		t.Skipf("no native analyzer completed a pass for this fixture here: %v", cleanDiag)
	}

	// 1. The right answer, on both forms.
	for _, form := range []struct {
		name string
		g    *view
		diag []string
	}{{"clean", clean, cleanDiag}, {"decoy", decoy, decoyDiag}} {
		wants := tc.want
		if nativeOn {
			wants = append(append([]string(nil), wants...), tc.wantNative...)
		}
		for _, f := range wants {
			if !form.g.holds(t, f) {
				t.Errorf("[%s] want %q: not found\n%s", form.name, f, form.g.dump(f))
			}
		}
		for _, f := range tc.wantNot {
			if form.g.holds(t, f) {
				t.Errorf("[%s] wantNot %q: present\n%s", form.name, f, form.g.dump(f))
			}
		}
	}

	// 2. Metamorphic equality.
	cs, ds := clean.symbolKeys(tc), decoy.symbolKeys(tc)
	if extra, missing := setDiff(ds, cs); len(extra)+len(missing) > 0 {
		t.Errorf("symbols differ (decoy vs clean)\n  only in decoy: %s\n  only in clean: %s",
			strings.Join(extra, "\n                 "), strings.Join(missing, "\n                 "))
	}
	ce, de := clean.edgeKeys(), decoy.edgeKeys()
	if extra, missing := setDiff(de, ce); len(extra)+len(missing) > 0 {
		t.Errorf("edges differ (decoy vs clean)\n  only in decoy: %s\n  only in clean: %s",
			strings.Join(decoy.annotate(extra), "\n                 "),
			strings.Join(clean.annotate(missing), "\n                 "))
	}
	if t.Failed() && nativeOn {
		t.Logf("native diagnostics (decoy): %v", decoyDiag)
	}
}

func nativeRan(diags []string) bool {
	for _, d := range diags {
		if strings.Contains(d, ": full pass over ") {
			return true
		}
	}
	return false
}

// indexFixture writes files to a fresh root and indexes it through the
// public engine.
func indexFixture(t *testing.T, files map[string]string, nativeOn bool) (*view, []string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	if nativeOn {
		files = withNativeConfig(t, root, files)
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	on := nativeOn
	eng, err := grove.Open(ctx, grove.Config{RepoRoot: root, NativeAnalyzers: &on, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	res, err := eng.Index(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	syms, edges, err := eng.SnapshotGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return newView(syms, edges), res.Native
}

// withNativeConfig adds the project files each native analyzer requires
// before it runs (identical for both forms of a twin, so the comparison is
// unaffected). TypeScript is resolved from GROVE_TEST_TYPESCRIPT (a
// node_modules/typescript dir), as in internal/index's compiler tests.
func withNativeConfig(t *testing.T, root string, files map[string]string) map[string]string {
	t.Helper()
	exts := map[string]bool{}
	var cfiles []string
	for rel := range files {
		ext := strings.ToLower(filepath.Ext(rel))
		exts[ext] = true
		if ext == ".c" || ext == ".cpp" || ext == ".cc" || ext == ".m" {
			cfiles = append(cfiles, rel)
		}
	}
	sort.Strings(cfiles)
	add := func(name, body string) {
		if _, ok := files[name]; !ok {
			files = with(files, name, body)
		}
	}
	if exts[".go"] {
		add("go.mod", goMod)
	}
	if exts[".py"] {
		add("pyproject.toml", "[project]\nname = \"p\"\nversion = \"0.1.0\"\n")
	}
	if exts[".php"] {
		add("composer.json", `{"autoload":{"psr-4":{"App\\":"src/"}}}`)
	}
	if exts[".cs"] {
		add("p.csproj", `<Project Sdk="Microsoft.NET.Sdk"><PropertyGroup><TargetFramework>net8.0</TargetFramework></PropertyGroup></Project>`)
	}
	if exts[".rs"] {
		add("Cargo.toml", "[package]\nname = \"p\"\nversion = \"0.1.0\"\nedition = \"2021\"\n")
	}
	if exts[".c"] || exts[".h"] || exts[".cpp"] || exts[".hpp"] || exts[".cc"] {
		var cmds []string
		for _, f := range cfiles {
			cmds = append(cmds, fmt.Sprintf(`{"directory":%q,"file":%q,"arguments":["cc","-c",%q]}`, root, f, f))
		}
		add("compile_commands.json", "["+strings.Join(cmds, ",")+"]")
	}
	if exts[".ts"] || exts[".tsx"] || exts[".js"] || exts[".jsx"] {
		add("tsconfig.json", `{"compilerOptions":{"allowJs":true,"checkJs":false,"jsx":"react","target":"es2020","strict":false},"include":["**/*"]}`)
		if ts := os.Getenv("GROVE_TEST_TYPESCRIPT"); ts != "" {
			if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(ts, filepath.Join(root, "node_modules", "typescript")); err != nil {
				t.Fatal(err)
			}
		}
	}
	return files
}

// view is a normalized graph.
type view struct {
	syms  []grove.Symbol
	byID  map[string]*grove.Symbol
	edges []grove.Edge
}

func newView(syms []grove.Symbol, edges []grove.Edge) *view {
	v := &view{syms: syms, byID: map[string]*grove.Symbol{}, edges: edges}
	for i := range v.syms {
		v.byID[v.syms[i].ID] = &v.syms[i]
	}
	return v
}

// node renders an edge endpoint without blob SHAs or line numbers.
func (v *view) node(id string) string {
	if s := v.byID[id]; s != nil {
		return fmt.Sprintf("%s::%s#%s", s.FilePath, s.QualifiedName, s.Kind)
	}
	if i := strings.LastIndexByte(id, '@'); i >= 0 && strings.Contains(id, "::") {
		return id[:i] + "@?"
	}
	return id
}

var (
	reBlockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	reLineComment  = regexp.MustCompile(`(?m)(//|#).*$`)
	reSpace        = regexp.MustCompile(`\s+`)
)

func collapseSig(s string) string {
	s = reBlockComment.ReplaceAllString(s, " ")
	s = reLineComment.ReplaceAllString(s, " ")
	return reSpace.ReplaceAllString(s, "")
}

func (v *view) symbolKeys(tc twinCase) []string {
	var out []string
	for _, s := range v.syms {
		mods := append([]string(nil), s.Modifiers...)
		sort.Strings(mods)
		var anns []string
		if !tc.rawAnnotations {
			anns = append(anns, s.Annotations...)
			sort.Strings(anns)
		}
		k := fmt.Sprintf("%s|%s|%s|%s|parent=%s|exported=%v|mods=%s|annotations=%s",
			s.FilePath, s.Language, s.Kind, s.QualifiedName, s.ParentSymbol, s.Exports, strings.Join(mods, ","), strings.Join(anns, ","))
		if tc.doc {
			k += "|doc=" + strconv.Quote(strings.TrimSpace(s.Docstring))
		}
		if tc.sig {
			k += "|sig=" + strconv.Quote(collapseSig(s.Signature))
		}
		if tc.spans {
			k += fmt.Sprintf("|span=%d-%d", s.Span.Start, s.Span.End)
		}
		out = append(out, k)
	}
	return out
}

func (v *view) edgeKeys() []string {
	var out []string
	for _, e := range v.edges {
		out = append(out, fmt.Sprintf("%s -%s-> %s", v.node(e.From), e.Type, v.node(e.To)))
	}
	return out
}

// annotate appends confidence/source/reason (ignored by the comparison) to
// edge keys for failure output.
func (v *view) annotate(keys []string) []string {
	info := map[string][]string{}
	for _, e := range v.edges {
		k := fmt.Sprintf("%s -%s-> %s", v.node(e.From), e.Type, v.node(e.To))
		info[k] = append(info[k], fmt.Sprintf("conf=%.2f src=%s reason=%s", e.Confidence, e.Source, e.Reason))
	}
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = k + "  [" + strings.Join(info[k], "; ") + "]"
	}
	return out
}

func setDiff(a, b []string) (onlyA, onlyB []string) {
	ma, mb := map[string]bool{}, map[string]bool{}
	for _, x := range a {
		ma[x] = true
	}
	for _, x := range b {
		mb[x] = true
	}
	for x := range ma {
		if !mb[x] {
			onlyA = append(onlyA, x)
		}
	}
	for x := range mb {
		if !ma[x] {
			onlyB = append(onlyB, x)
		}
	}
	sort.Strings(onlyA)
	sort.Strings(onlyB)
	return
}

// symMatches reports whether a symbol answers to pattern p ("name",
// "Parent.name", a qualified name, or any of those + "@" + file path).
func symMatches(s *grove.Symbol, p string) bool {
	if p == "*" {
		return true
	}
	if name, file, ok := strings.Cut(p, "@"); ok {
		if s.FilePath != file {
			return false
		}
		p = name
	}
	if s.QualifiedName == p || s.Name == p && !strings.ContainsAny(p, ".:") {
		return true
	}
	if s.ParentSymbol != "" {
		parent := s.ParentSymbol
		if i := strings.LastIndexAny(parent, ".:"); i >= 0 {
			parent = parent[i+1:]
		}
		if parent+"."+s.Name == p || s.ParentSymbol+"."+s.Name == p {
			return true
		}
	}
	// C++/Rust/PHP qualified forms written with "." in the pattern.
	return strings.NewReplacer("::", ".", "\\", ".").Replace(s.QualifiedName) == p
}

func (v *view) nodeMatches(id, p string) bool {
	if p == "*" {
		return true
	}
	if strings.HasPrefix(p, "file:") {
		return id == p
	}
	if s := v.byID[id]; s != nil {
		return symMatches(s, p)
	}
	return false
}

func (v *view) holds(t *testing.T, fact string) bool {
	t.Helper()
	f := strings.Fields(fact)
	switch {
	case len(f) == 4 && f[0] == "E":
		for _, e := range v.edges {
			if string(e.Type) == f[2] && v.nodeMatches(e.From, f[1]) && v.nodeMatches(e.To, f[3]) {
				return true
			}
		}
		return false
	case len(f) >= 2 && f[0] == "S":
		for i := range v.syms {
			if v.symHolds(t, &v.syms[i], f[1], f[2:]) {
				return true
			}
		}
		return false
	}
	t.Fatalf("bad fact %q", fact)
	return false
}

func (v *view) symHolds(t *testing.T, s *grove.Symbol, name string, props []string) bool {
	if !symMatches(s, name) {
		return false
	}
	for _, p := range props {
		key, val, _ := strings.Cut(p, "=")
		if strings.Contains(p, "~") {
			key, val, _ = strings.Cut(p, "~")
		}
		switch key {
		case "kind":
			if string(s.Kind) != val {
				return false
			}
		case "mod":
			has := false
			for _, m := range s.Modifiers {
				has = has || m == val
			}
			if !has {
				return false
			}
		case "exported":
			if !s.Exports {
				return false
			}
		case "unexported":
			if s.Exports {
				return false
			}
		case "lang":
			if s.Language != val {
				return false
			}
		case "doc":
			if !strings.Contains(s.Docstring, strings.ReplaceAll(val, "_", " ")) {
				return false
			}
		case "start", "end":
			n, err := strconv.Atoi(val)
			if err != nil {
				t.Fatalf("bad %s", p)
			}
			if key == "start" && s.Span.Start != n || key == "end" && s.Span.End != n {
				return false
			}
		case "file":
			if s.FilePath != val {
				return false
			}
		default:
			t.Fatalf("bad symbol property %q", p)
		}
	}
	return true
}

// dump prints the graph facts near a failed check: symbols and edges whose
// rendering mentions any name in the fact.
func (v *view) dump(fact string) string {
	var names []string
	for _, w := range strings.Fields(fact)[1:] {
		if w == "*" || strings.ContainsAny(w, "=~") {
			continue
		}
		if i := strings.LastIndexAny(w, ".:"); i >= 0 && !strings.HasPrefix(w, "file:") {
			names = append(names, w[i+1:])
		} else {
			names = append(names, strings.TrimPrefix(w, "file:"))
		}
	}
	mention := func(s string) bool {
		for _, n := range names {
			if n != "" && strings.Contains(s, n) {
				return true
			}
		}
		return false
	}
	var b strings.Builder
	for _, s := range v.syms {
		line := fmt.Sprintf("    sym %s %s %s parent=%q exported=%v mods=%v annotations=%v span=%d-%d lang=%s doc=%q",
			s.FilePath, s.Kind, s.QualifiedName, s.ParentSymbol, s.Exports, s.Modifiers, s.Annotations, s.Span.Start, s.Span.End, s.Language, s.Docstring)
		if mention(s.QualifiedName) {
			b.WriteString(line + "\n")
		}
	}
	var es []string
	for _, e := range v.edges {
		k := fmt.Sprintf("    edge %s -%s-> %s  [conf=%.2f src=%s reason=%s]", v.node(e.From), e.Type, v.node(e.To), e.Confidence, e.Source, e.Reason)
		if e.Type != "contains" && e.Type != "defines" && mention(k) {
			es = append(es, k)
		}
	}
	sort.Strings(es)
	b.WriteString(strings.Join(es, "\n"))
	return b.String()
}
