package graph

import (
	"fmt"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
	"sort"
)

// edgeIndex holds per-build symbol indexes used by the edge constructors.
type edgeIndex struct {
	byName map[string][]*core.SymbolRecord // lowercase name → symbols
	byFile map[string][]*core.SymbolRecord
	byID   map[string]*core.SymbolRecord

	// fileImports maps filePath → set of import strings declared in that file.
	// We pick the union over all symbols in the file since the parser sets
	// Imports per-symbol from the same file-level import list.
	fileImports map[string]map[string]struct{}

	// dirToFiles maps directory → []filePath for O(1) Go same-package lookup.
	// Without this index, importedFiles would scan byFile (O(n)) for every
	// file in the same directory, yielding O(n²) total for a repo with many
	// same-directory files (e.g. a 50-file package).
	dirToFiles map[string][]string

	// dirFilesLower / dirFilesByBase support O(import-depth) resolution of
	// package/directory imports: a dir matches an import when the import
	// path equals it or ends with "/"+dir (looked up via the import's
	// slash-suffixes), or when the dir's last segment equals the import's
	// last segment. The previous implementation scanned every directory per
	// import — ~0.5 billion string ops on a 19k-file monorepo.
	dirFilesLower  map[string][]string
	dirFilesByBase map[string][]string

	// importPathToFiles maps a slash-separated import path without extension to
	// files whose path matches that import exactly or by package directory.
	importPathToFiles map[string][]string

	// baseToFiles maps lowercase basename without extension to files.
	baseToFiles map[string][]string

	// importedFilesCache memoizes the result of importedFiles() per file.
	importedFilesCache map[string]map[string]struct{}

	// qualifierFiles memoizes importFilesForQualifier per (file, qualifier).
	// Unlike importedFilesCache it cannot be pre-warmed — the qualifier set
	// is only known while resolving call sites — so it is a sync.Map: the
	// parallel call workers hit it concurrently, and a caller resolving
	// "json.Marshal" in a 400-call file recomputes the identical import
	// walk once per call site otherwise. Values are READ-ONLY; nothing
	// mutates a returned map.
	qualifierFiles sync.Map // string("file\x00qualifier") → qualifierFileSet

	// Rust crate topology: visibility in Rust is crate-wide (any item is
	// reachable through crate:: paths without a per-file use), so scope is
	// the enclosing crate plus used workspace crates. A crate root is a
	// directory holding lib.rs or main.rs; files attach to the nearest
	// root above them.
	rustCrateOfFile map[string]string   // .rs file → crate root dir
	rustCrateFiles  map[string][]string // crate root dir → files under it
	rustCrateByName map[string]string   // normalized crate name → root dir
	// rustInlineRefs lists, per .rs file, the leading path segments of
	// inline paths in its bodies (`grep::regex::X::new()` → grep): a crate
	// used only through fully qualified paths has no `use` line, and its
	// files were out of scope (ripgrep's hiargs.rs never imports grep).
	rustInlineRefs map[string][]string

	// pyModuleGlobals maps a Python module-level global's name to its declared
	// class type ("g" → "_AppCtxGlobalsProxy"), built from KindVariable symbols
	// astkit emits for annotated module-level assignments. Only unambiguous
	// names are kept (a name declared with conflicting types across modules is
	// dropped). Empty when the indexer predates module-var extraction, so the
	// resolution paths that consult it are no-ops on older indexes.
	pyModuleGlobals map[string]string

	// pySubclasses is the inverse of pyBaseClasses over every indexed
	// Python class (base name → direct subclass names), built lazily for
	// template-method dispatch.
	subclasses     map[string]map[string][]string // language → base name → direct subclass names
	subclassesOnce sync.Once
}

func newEdgeIndex(symbols []core.SymbolRecord) *edgeIndex {
	idx := &edgeIndex{
		byName:             make(map[string][]*core.SymbolRecord),
		byFile:             make(map[string][]*core.SymbolRecord),
		byID:               make(map[string]*core.SymbolRecord),
		fileImports:        make(map[string]map[string]struct{}),
		dirToFiles:         make(map[string][]string),
		dirFilesLower:      make(map[string][]string),
		dirFilesByBase:     make(map[string][]string),
		importPathToFiles:  make(map[string][]string),
		baseToFiles:        make(map[string][]string),
		importedFilesCache: make(map[string]map[string]struct{}),
	}
	for i := range symbols {
		s := &symbols[i]
		idx.byID[s.ID] = s
		idx.byName[strings.ToLower(s.Name)] = append(idx.byName[strings.ToLower(s.Name)], s)
		idx.byFile[s.FilePath] = append(idx.byFile[s.FilePath], s)
		if _, ok := idx.fileImports[s.FilePath]; !ok {
			idx.fileImports[s.FilePath] = make(map[string]struct{})
		}
		for _, imp := range s.Imports {
			if strings.Contains(imp, "#") {
				continue // Python from-import member: validated below
			}
			idx.fileImports[s.FilePath][imp] = struct{}{}
		}
	}
	// Candidate order is resolution order: first-match rules, fan-out caps
	// and native (file, name) lookups all read byName/byFile slices in
	// sequence, and the indexer hands symbols over in no fixed order (a
	// parallel walk). Sort every slice by (file, span, ID) so the graph is a
	// function of the code, not of scheduling — socket.io's Socket.run vs
	// nested `function run` flipped 3 edges run-to-run before this.
	for _, syms := range idx.byName {
		sortSymbolRecords(syms)
	}
	for _, syms := range idx.byFile {
		sortSymbolRecords(syms)
	}
	// Build dirToFiles after byFile is populated so each directory maps to
	// all its files in one pass (O(n) total, vs O(n) per-file scan later).
	// Iterate file paths SORTED: these derived slices feed first-match and
	// capped resolution downstream, so their order must not depend on map
	// iteration (which made ~12 of 859k edges flap run-to-run on grafana).
	for _, f := range sortedFileKeys(idx.byFile) {
		d := dirOf(f)
		idx.dirToFiles[d] = append(idx.dirToFiles[d], f)
		if dLower := strings.ToLower(d); dLower != "" && dLower != "." {
			idx.dirFilesLower[dLower] = append(idx.dirFilesLower[dLower], f)
			idx.dirFilesByBase[baseOf(dLower)] = append(idx.dirFilesByBase[baseOf(dLower)], f)
		}
		idx.importPathToFiles[strings.ToLower(trimExt(f))] = append(idx.importPathToFiles[strings.ToLower(trimExt(f))], f)
		idx.baseToFiles[strings.ToLower(baseNameNoExt(f))] = append(idx.baseToFiles[strings.ToLower(baseNameNoExt(f))], f)
	}
	idx.buildRustCrates()
	if traceCalls && len(idx.rustCrateByName) > 0 {
		fmt.Fprintf(os.Stderr, "grove-trace rust-crates %v\n", idx.rustCrateByName)
	}
	idx.buildRustInlineRefs(symbols)
	idx.buildPyModuleGlobals(symbols)
	idx.bindPySubmoduleImports(symbols)
	return idx
}

// bindPySubmoduleImports turns "module#name" from-import members into plain
// module imports when the member is an in-repo submodule (`from . import
// cli` → ".cli"). A member that resolves to no file is a class/function
// binding and is dropped: recording it would make its bare name look like
// an import whose files are empty, and narrowByImport would then drop every
// call qualified by it.
func (idx *edgeIndex) bindPySubmoduleImports(symbols []core.SymbolRecord) {
	done := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "python" {
			continue
		}
		for _, imp := range s.Imports {
			mod, name, ok := strings.Cut(imp, "#")
			if !ok {
				continue
			}
			key := s.FilePath + "\x00" + imp
			if done[key] {
				continue
			}
			done[key] = true
			path := mod + "." + name
			if strings.HasSuffix(mod, ".") {
				path = mod + name
			}
			if len(idx.pyModuleFiles(s.FilePath, path)) > 0 {
				idx.fileImports[s.FilePath][path] = struct{}{}
			}
		}
	}
}

// pyModuleFiles resolves a Python module path ("flask.cli", ".cli",
// "..sansio.app") from fromFile to its indexed files: the module file or
// the package directory's files. Relative paths anchor on fromFile's
// directory; absolute ones match any file whose path ends with the module
// segments.
func (idx *edgeIndex) pyModuleFiles(fromFile, modPath string) []string {
	dots := 0
	for dots < len(modPath) && modPath[dots] == '.' {
		dots++
	}
	segs := strings.Split(modPath[dots:], ".")
	if len(segs) == 1 && segs[0] == "" {
		return nil
	}
	if dots > 0 {
		// One dot is the current package; each further dot climbs one.
		return idx.resolveRelativeImport(fromFile, strings.Repeat("../", dots-1)+strings.Join(segs, "/"))
	}
	joined := strings.ToLower(strings.Join(segs, "/"))
	var out []string
	for pathKey, files := range idx.importPathToFiles {
		if pathKey == joined || strings.HasSuffix(pathKey, "/"+joined) {
			out = append(out, files...)
		}
	}
	for dir, files := range idx.dirFilesLower {
		if dir == joined || strings.HasSuffix(dir, "/"+joined) {
			out = append(out, files...)
		}
	}
	return out
}

// rustPinByPath disambiguates same-named Rust types across crates for a
// type-path call: `grep::pcre2::RegexMatcherBuilder::new()` keeps the
// candidates whose file lies under the last module segment before the
// type (pcre2). Without a path nothing is preferred — an own-crate
// preference was tried and lost 143 real cross-crate edges on ripgrep.
func rustPinByPath(idx *edgeIndex, symbol *core.SymbolRecord, cs core.CallSite, qualifier string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if qualifier == "" || qualifier[0] < 'A' || qualifier[0] > 'Z' || cs.Line <= 0 || symbol.Span.Start <= 0 {
		return cands
	}
	name := cs.Callee
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	mod := ""
	off := cs.Line - symbol.Span.Start
	lines := strings.Split(symbol.RawText, "\n")
	if off >= 0 && off < len(lines) {
		re := localFnRes.get(`((?:[a-z_][a-z0-9_]*::)+)` + regexp.QuoteMeta(qualifier) + `::` + regexp.QuoteMeta(name) + `\b`)
		if m := re.FindStringSubmatch(lines[off]); m != nil {
			segs := strings.Split(strings.TrimSuffix(m[1], "::"), "::")
			mod = segs[len(segs)-1]
		}
	}
	if mod == "" {
		// A bare `RegexMatcherBuilder::new()` is disambiguated by the
		// `use grep::regex::RegexMatcherBuilder;` that brought it in.
		// Plain `grep_regex::RegexMatcher` or grouped
		// `grep_regex::{RegexMatcher, RegexMatcherBuilder}`.
		re := localFnRes.get(`([a-z_][a-z0-9_]*)::(?:\{[^}]*\b)?` + regexp.QuoteMeta(qualifier) + `\b`)
		imps := make([]string, 0, len(idx.fileImports[symbol.FilePath]))
		for imp := range idx.fileImports[symbol.FilePath] {
			imps = append(imps, imp)
		}
		sort.Strings(imps)
		for _, imp := range imps {
			if m := re.FindStringSubmatch(imp); m != nil {
				mod = m[1]
				break
			}
		}
	}
	switch mod {
	case "", "crate", "self", "super":
		return cands
	}
	// Package names commonly prefix the directory (grep_regex →
	// crates/regex): try the module name, then its last underscore token.
	names := []string{mod}
	if i := strings.LastIndexByte(mod, '_'); i >= 0 && i+1 < len(mod) {
		names = append(names, mod[i+1:])
	}
	for _, n := range names {
		var out []*core.SymbolRecord
		for _, c := range cands {
			if strings.Contains(c.FilePath, "/"+n+"/") || strings.HasSuffix(c.FilePath, "/"+n+".rs") || strings.Contains(c.FilePath, "/"+n+"-") {
				out = append(out, c)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return cands
}

// anyInFile reports whether a candidate is declared in file — a local
// definition shadows any import of the same name.
func anyInFile(cands []*core.SymbolRecord, file string) bool {
	for _, c := range cands {
		if c.FilePath == file {
			return true
		}
	}
	return false
}

// rustImportedExternal reports whether the file imports a bare name from
// a crate outside the workspace (`use regex_syntax::escape;`,
// `use std::mem::replace;`, grouped forms included).
func rustImportedExternal(idx *edgeIndex, symbol *core.SymbolRecord, name string) bool {
	// The name must be a bound member: preceded by "::", "{" or ", " and
	// followed by a delimiter — never a module segment of a longer path.
	re := localFnRes.get(`(?:::|\{|,\s*)` + regexp.QuoteMeta(name) + `(?:\s*[,}]|\s+as\s|\s*$)`)
	for imp := range idx.fileImports[symbol.FilePath] {
		imp = strings.TrimSpace(strings.TrimPrefix(imp, "pub "))
		if !re.MatchString(imp) {
			continue
		}
		// The statement's ROOT head decides (`crate::{pathutil::{is_hidden}}`
		// is ours however deep the group nests).
		head := imp
		if strings.HasPrefix(head, "{") {
			// `use {a::x, b::y}`: find the member's own group.
			continue
		}
		if i := strings.Index(head, "::"); i >= 0 {
			head = head[:i]
		}
		head = strings.ToLower(strings.TrimSpace(head))
		switch head {
		case "crate", "self", "super":
			return false
		case "std", "core", "alloc":
			return true
		}
		if _, ok := idx.rustCrateByName[head]; ok {
			return false
		}
		if i := strings.LastIndexByte(head, '_'); i >= 0 && idx.rustCrateByName[head[i+1:]] != "" {
			return false
		}
		return true
	}
	return false
}

// rustImportHeads returns the crate-name heads of one `use` statement:
// "grep::regex::X" → [grep]; "pub use grep_printer as printer" →
// [grep_printer]; the 2018 grouped form "{grep::matcher::Matcher,
// termcolor::WriteColor}" → [grep termcolor] (it used to read as the
// crate "{grep" and ripgrep's grouped imports never joined scope).
// crate/self/super/std/core/alloc are dropped.
func rustImportHeads(imp string) []string {
	seg := strings.TrimPrefix(imp, "pub ")
	seg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(seg), "use "))
	var parts []string
	if strings.HasPrefix(seg, "{") {
		inner := strings.TrimSuffix(strings.TrimPrefix(seg, "{"), "}")
		parts = splitTopLevel(inner, ',')
	} else {
		parts = []string{seg}
	}
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if i := strings.Index(p, "::"); i >= 0 {
			p = p[:i]
		}
		// "grep_printer as printer" — the crate name ends at the first space.
		if i := strings.IndexByte(p, ' '); i >= 0 {
			p = p[:i]
		}
		p = strings.ToLower(strings.TrimSpace(p))
		switch p {
		case "", "crate", "super", "self", "std", "core", "alloc":
			continue
		}
		out = append(out, p)
	}
	return out
}

// rustInlinePathRe matches the leading segment of an inline path
// (`grep::regex::`), excluding `::` continuations of a longer path.
var rustInlinePathRe = regexp.MustCompile(`(?:^|[^\w:])([a-z][a-z0-9_]*)::`)

// buildRustInlineRefs records, per Rust file, the distinct leading path
// segments used inline in its symbols' bodies, restricted to names that
// are workspace crates (the only ones scope can act on).
func (idx *edgeIndex) buildRustInlineRefs(symbols []core.SymbolRecord) {
	if len(idx.rustCrateByName) == 0 {
		return
	}
	idx.rustInlineRefs = map[string][]string{}
	seen := map[string]map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "rust" || s.RawText == "" {
			continue
		}
		for _, m := range rustInlinePathRe.FindAllStringSubmatch(s.RawText, -1) {
			seg := m[1]
			switch seg {
			case "crate", "super", "self", "std", "core", "alloc":
				continue
			}
			if _, ok := idx.rustCrateByName[seg]; !ok {
				if i := strings.LastIndexByte(seg, '_'); i < 0 || idx.rustCrateByName[seg[i+1:]] == "" {
					continue
				}
			}
			if seen[s.FilePath] == nil {
				seen[s.FilePath] = map[string]bool{}
			}
			if !seen[s.FilePath][seg] {
				seen[s.FilePath][seg] = true
				idx.rustInlineRefs[s.FilePath] = append(idx.rustInlineRefs[s.FilePath], seg)
			}
		}
	}
}

// buildPyModuleGlobals collects the name→class-type map from Python
// module-level annotated variables (KindVariable symbols). A name declared
// with two different types in different modules is ambiguous and dropped, so
// resolution through it never guesses.
func (idx *edgeIndex) buildPyModuleGlobals(symbols []core.SymbolRecord) {
	var globals map[string]string
	var ambiguous map[string]bool
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "python" || s.Kind != core.KindVariable {
			continue
		}
		ct := pyModuleGlobalType(s.Signature)
		if ct == "" {
			continue
		}
		if globals == nil {
			globals = map[string]string{}
			ambiguous = map[string]bool{}
		}
		if prev, ok := globals[s.Name]; ok && prev != ct {
			ambiguous[s.Name] = true
		}
		globals[s.Name] = ct
	}
	for name := range ambiguous {
		delete(globals, name)
	}
	idx.pyModuleGlobals = globals
}

// creationSite reports whether the call site's source line instantiates
// the callee (`new Name(` / `new Name<`), for the languages whose
// extractors emit the type name as the callee of an object creation.
func creationSite(symbol *core.SymbolRecord, cs core.CallSite) bool {
	switch symbol.Language {
	case "java", "csharp", "typescript", "tsx", "javascript", "php":
	default:
		return false
	}
	if cs.Callee == "" || strings.ContainsAny(cs.Callee, ".") || cs.Line <= 0 || symbol.Span.Start <= 0 {
		return false
	}
	off := cs.Line - symbol.Span.Start
	if off < 0 {
		return false
	}
	lines := strings.Split(symbol.RawText, "\n")
	if off >= len(lines) {
		return false
	}
	return localFnRes.get(`\bnew\s+(?:[\w.]+\.)?` + regexp.QuoteMeta(cs.Callee) + `\s*[(<{]`).MatchString(lines[off])
}

// declaresLocalFunction reports whether the caller's body declares a nested
// function named name: JS/TS `function name(`, Python `def name(`, Go
// `name := func`, Rust `fn name(`. The declaration line of the caller
// itself (which also matches `def name(` when the caller IS name) is
// excluded by requiring the match to start after the first line.
func declaresLocalFunction(symbol *core.SymbolRecord, name string) bool {
	body := symbol.RawText
	if body == "" || name == "" {
		return false
	}
	nl := strings.IndexByte(body, '\n')
	if nl < 0 {
		return false
	}
	body = body[nl+1:]
	var pat string
	switch symbol.Language {
	case "javascript", "typescript", "tsx":
		pat = `(?m)^\s*(?:async\s+)?function\*?\s+` + regexp.QuoteMeta(name) + `\s*\(`
	case "python":
		pat = `(?m)^\s*(?:async\s+)?def\s+` + regexp.QuoteMeta(name) + `\s*\(`
	case "go":
		pat = `(?m)^\s*` + regexp.QuoteMeta(name) + `\s*:=\s*func\b`
	case "rust":
		pat = `(?m)^\s*fn\s+` + regexp.QuoteMeta(name) + `\s*[(<]`
	default:
		return false
	}
	re := localFnRes.get(pat)
	return re.MatchString(body)
}

// localFnRes caches the per-name regexps declaresLocalFunction builds.
var localFnRes = &regexpCache{m: map[string]*regexp.Regexp{}}

type regexpCache struct {
	mu sync.Mutex
	m  map[string]*regexp.Regexp
}

func (c *regexpCache) get(pat string) *regexp.Regexp {
	c.mu.Lock()
	defer c.mu.Unlock()
	if re, ok := c.m[pat]; ok {
		return re
	}
	re := regexp.MustCompile(pat)
	c.m[pat] = re
	return re
}

// traceCalls (GROVE_TRACE_CALLS=1) prints each call site's candidate set
// to stderr — the first question in every resolution bug is "did the
// callee even reach the candidate stage".
var traceCalls = os.Getenv("GROVE_TRACE_CALLS") != ""

// sortSymbolRecords orders a candidate slice by (file, span start, ID).
func sortSymbolRecords(syms []*core.SymbolRecord) {
	sort.SliceStable(syms, func(i, j int) bool {
		if syms[i].FilePath != syms[j].FilePath {
			return syms[i].FilePath < syms[j].FilePath
		}
		if syms[i].Span.Start != syms[j].Span.Start {
			return syms[i].Span.Start < syms[j].Span.Start
		}
		return syms[i].ID < syms[j].ID
	})
}

// sortedKeys returns a set-map's keys in lexicographic order — any loop
// whose RESULT can depend on processing order (first-match, len()==0
// fallbacks) must iterate deterministically.
func sortedKeys(m map[string]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedFileKeys returns the map's keys in lexicographic order, so every
// slice derived from file iteration is deterministic.
func sortedFileKeys(m map[string][]*core.SymbolRecord) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// buildRustCrates derives the crate topology from file layout alone: every
// directory containing lib.rs or main.rs roots a crate; each .rs file
// belongs to the nearest root above it (its own directory when no root
// exists, e.g. integration-test targets). The crate's referenceable name is
// the root's directory name — or its parent's for src/ layouts — registered
// with hyphens normalized to underscores plus its last underscore token,
// because package names commonly prefix the directory name (grep-searcher
// lives in crates/searcher).
func (idx *edgeIndex) buildRustCrates() {
	roots := map[string]bool{}
	for _, f := range sortedFileKeys(idx.byFile) {
		if !strings.HasSuffix(f, ".rs") {
			continue
		}
		if base := baseNameNoExt(f); base == "lib" || base == "main" {
			roots[dirOf(f)] = true
		}
	}
	if len(roots) == 0 {
		return
	}
	idx.rustCrateOfFile = map[string]string{}
	idx.rustCrateFiles = map[string][]string{}
	idx.rustCrateByName = map[string]string{}
	for _, f := range sortedFileKeys(idx.byFile) {
		if !strings.HasSuffix(f, ".rs") {
			continue
		}
		root := dirOf(f)
		for d := root; ; {
			if roots[d] {
				root = d
				break
			}
			parent := dirOf(d)
			if parent == d || parent == "." || parent == "" {
				break
			}
			d = parent
		}
		idx.rustCrateOfFile[f] = root
		idx.rustCrateFiles[root] = append(idx.rustCrateFiles[root], f)
	}
	crateName := func(root string) string {
		name := baseOf(root)
		if name == "src" {
			name = baseOf(dirOf(root))
		}
		return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	}
	for root := range roots {
		if name := crateName(root); name != "" && name != "." {
			idx.rustCrateByName[name] = root
		}
	}
	// Token aliases never displace an exact crate name.
	for root := range roots {
		name := crateName(root)
		if i := strings.LastIndexByte(name, '_'); i >= 0 && i+1 < len(name) {
			if tok := name[i+1:]; idx.rustCrateByName[tok] == "" {
				idx.rustCrateByName[tok] = root
			}
		}
	}
}

// importedFiles returns the set of file paths that are reachable through
// the import declarations of fromFile. Resolution is heuristic: an import
// string matches a candidate file when the candidate's path or basename
// shares the import's last path segment. Always includes fromFile itself.
func (idx *edgeIndex) importedFiles(fromFile string) map[string]struct{} {
	if cached, ok := idx.importedFilesCache[fromFile]; ok {
		return cached
	}
	out := map[string]struct{}{fromFile: {}}

	// C#: `using` imports a namespace, not a file, and namespaces don't map
	// to directories — so file-level import resolution can't see the target.
	// Within one assembly every type is mutually visible, so scope is the
	// whole repo; precision is held by type narrowing (qualified calls must
	// resolve to a known type or an inferable local — see the csharp static
	// block in buildCalls), not by scope.
	if lang := fileLanguage(idx, fromFile); lang == "csharp" || lang == "php" || lang == "c" || lang == "cpp" {
		// C#/PHP resolve types through namespace imports (`using`/`use`),
		// which don't map to directories; within one library every type is
		// mutually visible, so scope is the whole repo and precision is held
		// by type narrowing (the static block in buildCalls), not by scope.
		for f := range idx.byFile {
			out[f] = struct{}{}
		}
		idx.importedFilesCache[fromFile] = out
		return out
	}

	// Same-package scope (Go only): a Go file does not import its own package,
	// yet calls between files in the same directory are extremely common
	// (compressor.go ↔ compressor_test.go, split implementation files). In Go a
	// directory IS a package, so every file sharing fromFile's directory is in
	// scope. This is NOT true for TS/JS/Java/Python, where imports are always
	// explicit per file regardless of directory — so we gate on language to
	// avoid linking unrelated same-folder modules there.
	lang := fileLanguage(idx, fromFile)
	if lang == "go" || lang == "java" {
		// Go: a directory IS a package. Java: a directory is a package too —
		// same-package classes need no import.
		fromDir := dirOf(fromFile)
		for _, f := range idx.dirToFiles[fromDir] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}
		// Java's package identity SPANS source roots: src/test/java/com/x
		// and src/main/java/com/x are the same package, and test classes
		// conventionally sit in the package of the code under test exactly
		// so no import is needed. Same-directory-only scope made every
		// such caller invisible — measured 2026-09-02 (dubbo
		// MetadataInfo.ServiceInfo.getMethodParameter): change-impact
		// answered "callers (2), completeness: closed" while
		// MetadataInfoTest.java called the method twice; a minimal
		// two-file fixture reproduced it (same-package caller missed,
		// explicitly-imported caller found). Include every directory
		// whose package path (the suffix after the src/<set>/java/
		// source-root marker) matches.
		if lang == "java" {
			if pkg, ok := javaPackageSuffix(fromDir); ok {
				for dir, files := range idx.dirToFiles {
					if dir == fromDir {
						continue
					}
					if p2, ok2 := javaPackageSuffix(dir); ok2 && p2 == pkg {
						for _, f := range files {
							if f != fromFile {
								out[f] = struct{}{}
							}
						}
					}
				}
			}
		}
	}
	if lang == "rust" && idx.rustCrateOfFile != nil {
		// Rust visibility is crate-wide: crate::/super:: paths reach any
		// sibling module with no per-file use declaration. Scope is the
		// whole enclosing crate plus used workspace crates — transitively,
		// because facade crates re-export their dependencies (ripgrep's
		// core uses grep::printer::..., where crates/grep is a shim over
		// grep-printer) and paths through a re-export reach the underlying
		// crate's items directly.
		ownRoot := idx.rustCrateOfFile[fromFile]
		visited := map[string]bool{}
		queue := []string{ownRoot}
		for len(queue) > 0 {
			root := queue[0]
			queue = queue[1:]
			if visited[root] {
				continue
			}
			visited[root] = true
			for _, f := range idx.rustCrateFiles[root] {
				if f != fromFile {
					out[f] = struct{}{}
				}
				imps := make([]string, 0, len(idx.fileImports[f])+len(idx.rustInlineRefs[f]))
				for imp := range idx.fileImports[f] {
					imps = append(imps, imp)
				}
				if root == ownRoot {
					imps = append(imps, idx.rustInlineRefs[f]...)
				}
				sort.Strings(imps)
				if traceCalls && f == fromFile {
					fmt.Fprintf(os.Stderr, "grove-trace rust-scope %s ownRoot=%q root=%q heads=%v\n", fromFile, ownRoot, root, imps)
				}
				for _, imp := range imps {
					if root != ownRoot && !strings.HasPrefix(imp, "pub ") {
						// Any own-crate import is dependency evidence (the
						// extern prelude names deps crate-wide), but other
						// crates extend reachability only through pub use
						// re-export chains.
						continue
					}
					for _, seg := range rustImportHeads(imp) {
						crateRoot, ok := idx.rustCrateByName[seg]
						if !ok {
							// Package names commonly prefix the directory name
							// (use grep_searcher → crates/searcher): retry on
							// the path's last underscore token.
							if i := strings.LastIndexByte(seg, '_'); i >= 0 && i+1 < len(seg) {
								crateRoot, ok = idx.rustCrateByName[seg[i+1:]]
							}
						}
						if ok && !visited[crateRoot] {
							queue = append(queue, crateRoot)
						}
					}
				}
			}
		}
		if traceCalls {
			roots := make([]string, 0, len(visited))
			for r := range visited {
				roots = append(roots, r)
			}
			sort.Strings(roots)
			fmt.Fprintf(os.Stderr, "grove-trace rust-scope-done %s files=%d roots=%v\n", fromFile, len(out), roots)
		}
		idx.importedFilesCache[fromFile] = out
		return out
	}

	imports, ok := idx.fileImports[fromFile]
	if !ok {
		idx.importedFilesCache[fromFile] = out
		return out
	}
	for _, imp := range sortedKeys(imports) {
		// Strip an `as <alias>` binding: the alias is a local name, not part
		// of the module path, and left in place it broke resolution of the
		// whole import (Python `import driver.dbapi as Database`, JS
		// `import X as Y`). Do it first so every branch below sees the bare
		// module path.
		if i := strings.Index(imp, " as "); i >= 0 {
			imp = imp[:i]
		}
		raw := strings.Trim(imp, "\"' ;")
		// Relative imports name one specific file or directory: resolve them
		// against the importing file's location and skip fuzzy matching —
		// basename fallback would pull every same-named file in a monorepo
		// ("./socket" matching all socket.ts files) into scope.
		if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
			if resolved := idx.resolveRelativeImport(fromFile, raw); len(resolved) > 0 {
				for _, f := range resolved {
					if f != fromFile {
						out[f] = struct{}{}
					}
				}
				continue
			}
		}
		impNorm := strings.ToLower(strings.Trim(imp, "\"' ;"))
		impNorm = strings.TrimPrefix(impNorm, "./")
		impNorm = strings.TrimSuffix(impNorm, ".go")
		impNorm = strings.TrimSuffix(impNorm, ".py")
		impNorm = strings.TrimSuffix(impNorm, ".ts")
		impNorm = strings.TrimSuffix(impNorm, ".tsx")
		impNorm = strings.TrimSuffix(impNorm, ".js")
		impNorm = strings.TrimSuffix(impNorm, ".jsx")
		impNorm = strings.TrimSuffix(impNorm, ".java")
		impNorm = strings.TrimSuffix(impNorm, ".rs")

		// Java wildcard import: `import com.example.util.*;` names a package,
		// not a class. The package path is a *suffix* of the repo-relative
		// source dir (src/main/java/com/example/util), so the equality-keyed
		// suffix loop below can never match it — resolve by reverse suffix
		// lookup instead: candidate dirs sharing the package's last segment,
		// kept when the dir ends with the full package path. Without this the
		// import resolves to nothing (its last segment is "*") and every
		// cross-package callee behind it is silently out of scope.
		if lang == "java" && strings.HasSuffix(impNorm, ".*") {
			pkgPath := strings.ReplaceAll(strings.TrimSuffix(impNorm, ".*"), ".", "/")
			for _, f := range idx.dirFilesByBase[baseOf(pkgPath)] {
				d := strings.ToLower(dirOf(f))
				if (d == pkgPath || strings.HasSuffix(d, "/"+pkgPath)) && f != fromFile {
					out[f] = struct{}{}
				}
			}
			continue
		}

		// Last path segment of the import (e.g., "lodash/fp" → "fp",
		// "./auth" → "auth", "fmt" → "fmt", "com.example.Auth" → "Auth").
		seg := lastImportSegment(imp)
		if seg == "" {
			continue
		}
		segLower := strings.ToLower(seg)

		// Fast path: direct file-path match (e.g. relative imports, or same-depth imports
		// where the import string matches the file path exactly after extension strip).
		for _, f := range idx.importPathToFiles[impNorm] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}

		// (1) Package / directory imports — the common case for Go, Rust,
		// and Python, where one import names a DIRECTORY and pulls in every
		// file under it. A directory matches when the (module-prefixed)
		// import path equals it or ends with "/"+dir — i.e. when one of the
		// import's slash-suffixes equals the dir — or when the dir's last
		// segment equals the import's last segment. Suffix lookups make this
		// O(import-depth) instead of a scan over every directory, which on a
		// 19k-file monorepo was ~0.5 billion string comparisons per index.
		// Slash-suffixes of impNorm, taken as substrings. The previous
		// Split+Join per suffix allocated a slice and a fresh string for
		// every segment of every import of every call site — the single
		// hottest allocation in call resolution.
		for i := 0; i <= len(impNorm); i++ {
			if i != 0 && impNorm[i-1] != '/' {
				continue
			}
			suffix := impNorm[i:]
			if suffix == "" || suffix == "." {
				continue
			}
			for _, f := range idx.dirFilesLower[suffix] {
				if f != fromFile {
					out[f] = struct{}{}
				}
			}
		}
		for _, f := range idx.dirFilesByBase[segLower] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}

		// (2) File-name imports — e.g. a JS/TS relative import "./auth"
		// resolving to "auth.ts".
		for _, c := range idx.baseToFiles[segLower] {
			if c == fromFile {
				continue
			}
			lower := strings.ToLower(c)
			base := strings.ToLower(baseNameNoExt(c))
			if base == segLower || strings.HasSuffix(lower, "/"+segLower) ||
				strings.HasSuffix(lower, "/"+segLower+".go") ||
				strings.HasSuffix(lower, "/"+segLower+".py") ||
				strings.HasSuffix(lower, "/"+segLower+".ts") ||
				strings.HasSuffix(lower, "/"+segLower+".tsx") ||
				strings.HasSuffix(lower, "/"+segLower+".js") ||
				strings.HasSuffix(lower, "/"+segLower+".jsx") ||
				strings.HasSuffix(lower, "/"+segLower+".java") ||
				strings.HasSuffix(lower, "/"+segLower+".rs") {
				out[c] = struct{}{}
			}
		}
	}
	idx.importedFilesCache[fromFile] = out
	return out
}

// resolveRelativeImport maps "./socket" / "../parser/index.js" to concrete
// indexed files: exact file (with source-extension probing), or a directory
// (returning its files).
func (idx *edgeIndex) resolveRelativeImport(fromFile, raw string) []string {
	base := dirOf(fromFile)
	joined := strings.ToLower(pathJoin(base, raw))
	var out []string
	if files, ok := idx.dirFilesLower[joined]; ok {
		out = append(out, files...)
	}
	out = append(out, idx.importPathToFiles[joined]...)
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs", ".py", ".rs"} {
		out = append(out, idx.importPathToFiles[strings.TrimSuffix(joined, ext)]...)
	}
	// index-file convention: "./parser" → parser/index.ts
	out = append(out, idx.importPathToFiles[joined+"/index"]...)
	return out
}

// pathJoin resolves "." and ".." segments without touching the filesystem.
func pathJoin(base, rel string) string {
	segs := []string{}
	if base != "" && base != "." {
		segs = strings.Split(base, "/")
	}
	for _, s := range strings.Split(rel, "/") {
		switch s {
		case "", ".":
		case "..":
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, s)
		}
	}
	return strings.Join(segs, "/")
}

func lastImportSegment(imp string) string {
	imp = strings.Trim(imp, "\"' ;")
	// Java: dot-separated; everything else: slash-separated.
	if strings.Contains(imp, "/") {
		parts := strings.Split(imp, "/")
		return parts[len(parts)-1]
	}
	if strings.Contains(imp, ".") {
		parts := strings.Split(imp, ".")
		return parts[len(parts)-1]
	}
	return imp
}

// fileLanguage returns the language recorded for any symbol in fromFile.
func fileLanguage(idx *edgeIndex, fromFile string) string {
	for _, s := range idx.byFile[fromFile] {
		if s.Language != "" {
			return strings.ToLower(s.Language)
		}
	}
	return ""
}

// dirOf returns the directory portion of a slash-separated file path
// ("internal/ranking/budget.go" → "internal/ranking"; "main.go" → "").
func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// baseOf returns the last segment of a slash-separated path
// ("internal/ranking" → "ranking").
func baseOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func baseNameNoExt(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		path = path[:i]
	}
	return path
}

func trimExt(path string) string {
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		return path[:i]
	}
	return path
}

// stripCommentsAndStrings removes // line comments, /* */ block comments,
// # python comments, and string literals from a source body so that
// regex-based call matching does not produce false positives.
func stripCommentsAndStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		ch := src[i]
		// Block comment
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
			continue
		}
		// Line comment // and #
		if (ch == '/' && i+1 < n && src[i+1] == '/') || ch == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// String literal — preserve newlines so call matching keeps line layout.
		if ch == '"' || ch == '\'' || ch == '`' {
			quote := ch
			out.WriteByte(' ')
			i++
			for i < n && src[i] != quote {
				if src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			if i < n {
				i++ // closing quote
			}
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// buildDefinesAndImports emits "file → symbol" defines edges and
// deduplicated "file → import:path" imports edges.
func buildDefinesAndImports(symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seenImports := make(map[string]bool)
	seenFiles := make(map[string]bool)
	for _, symbol := range symbols {
		fileNode := "file:" + symbol.FilePath
		edges = append(edges, core.Edge{
			From:       fileNode,
			To:         symbol.ID,
			Type:       core.EdgeDefines,
			Confidence: 1.0,
			Source:     core.EvidenceSourceASTKit,
			Reason:     core.ReasonStructural,
		})
		if seenFiles[symbol.FilePath] {
			continue
		}
		seenFiles[symbol.FilePath] = true
		for _, imp := range symbol.Imports {
			if strings.Contains(imp, "#") {
				continue // Python from-import member candidate, not a module
			}
			key := fileNode + "::import:" + imp
			if seenImports[key] {
				continue
			}
			seenImports[key] = true
			edges = append(edges, core.Edge{
				From:       fileNode,
				To:         "import:" + imp,
				Type:       core.EdgeImports,
				Confidence: 0.9,
				Source:     core.EvidenceSourceASTKit,
				Reason:     core.ReasonStructural,
			})
		}
	}
	return edges
}

// buildContains emits parent-symbol → child-symbol edges.
func buildContains(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		if symbol.ParentSymbol == "" {
			continue
		}
		for _, parent := range idx.byName[strings.ToLower(symbol.ParentSymbol)] {
			// The byName key is lowercase but identifiers are case-sensitive
			// (PHP class names excepted): without this check a method whose
			// receiver is `responseWriter` also attached to the interface
			// `ResponseWriter` — Go idiomatically pairs exported/unexported
			// names, so the conflation poisoned real interfaces.
			if parent.Name != symbol.ParentSymbol && symbol.Language != "php" {
				continue
			}
			if parent.FilePath != symbol.FilePath {
				// Go receiver methods attach to their type across files:
				// `func (s *T) M()` may live in any file of T's package.
				// Same directory ≈ same package (Go enforces one package
				// per directory).
				if symbol.Language != "go" || parent.Language != "go" ||
					path.Dir(parent.FilePath) != path.Dir(symbol.FilePath) {
					continue
				}
			}
			// KindType included: a method can attach to a named non-struct
			// type (`type Status int; func (s Status) String()`) — Go's most
			// idiomatic non-struct receiver. Excluding it made every such
			// type's methods invisible to change-impact / missing-impl.
			if parent.Kind != core.KindStruct && parent.Kind != core.KindClass &&
				parent.Kind != core.KindInterface && parent.Kind != core.KindTrait &&
				parent.Kind != core.KindType {
				continue
			}
			edges = append(edges, core.Edge{
				From:       parent.ID,
				To:         symbol.ID,
				Type:       core.EdgeContains,
				Confidence: 1.0,
				Source:     core.EvidenceSourceASTKit,
				Reason:     core.ReasonStructural,
			})
		}
	}
	return edges
}

// extendsRe / implementsRe match the inheritance clauses of class/interface
// declarations across JS/TS/Java. Python uses parenthesized base classes.
var (
	extendsRe       = regexp.MustCompile(`\bextends\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	implementsRe    = regexp.MustCompile(`\bimplements\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	pythonClassBase = regexp.MustCompile(`^\s*class\s+[A-Za-z_][A-Za-z0-9_]*\s*\(([^)]+)\)`)
	rustImplForRe   = regexp.MustCompile(`\bimpl\s+(?:<[^>]+>\s+)?([A-Za-z_][A-Za-z0-9_:]*)\s+for\s+([A-Za-z_][A-Za-z0-9_:]*)`)
	usesTypeIdent   = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]+)\b`)
)

// stripAngleBrackets removes balanced `<...>` sections (including nested
// generics like `Map<String, List<T>>`) so type arguments and bounds do not
// leak into name-list parsing.
func stripAngleBrackets(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			} else {
				b.WriteByte(s[i])
			}
		default:
			if depth == 0 {
				b.WriteByte(s[i])
			}
		}
	}
	return b.String()
}

// buildExtendsImplements emits inheritance edges. It reads the symbol's
// Signature (and RawText for Python/Rust where the signature is sparse) to
// detect parent classes, implemented interfaces, and trait impls.
func buildExtendsImplements(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		switch symbol.Language {
		case "typescript", "tsx", "javascript", "java":
			if symbol.Kind != core.KindClass && symbol.Kind != core.KindInterface {
				continue
			}
			text := symbol.Signature
			if text == "" {
				text = firstLine(symbol.RawText)
			}
			// Drop balanced <...> sections so generic arguments and bounds
			// (`class Foo<T extends Bar> extends Baz<Qux>`) neither confuse
			// the extends regex nor emit bogus edges to type parameters.
			text = stripAngleBrackets(text)
			for _, name := range matchNameList(extendsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.85)...)
			}
			for _, name := range matchNameList(implementsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeImplements, 0.85)...)
			}
		case "csharp":
			// C# uses `class X : Base, IFoo` (colon, not keywords), so it
			// needs its own parse. Without this graph-layer fallback, C#
			// inheritance edges existed ONLY when the native (roslyn/project)
			// analyzer ran — a bare source tree with no .csproj got zero
			// extends/implements edges, so change-impact/missing-impl silently
			// saw an empty closure. Both edge kinds are walked identically by
			// the closure, so the base-vs-interface split is cosmetic; the
			// I-prefix convention labels them for readers.
			if symbol.Kind != core.KindClass && symbol.Kind != core.KindInterface {
				continue
			}
			text := symbol.Signature
			if text == "" {
				text = firstLine(symbol.RawText)
			}
			for i, name := range csharpBaseNames(text) {
				edgeType := core.EdgeImplements
				if symbol.Kind == core.KindClass && i == 0 && !strings.HasPrefix(name, "I") {
					edgeType = core.EdgeExtends
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, name, edgeType, 0.85)...)
			}
		case "python":
			if symbol.Kind != core.KindClass {
				continue
			}
			line := firstLine(symbol.RawText)
			matches := pythonClassBase.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}
			for _, base := range splitTrim(matches[1], ',') {
				base = stripPythonBase(base)
				if base == "" {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, base, core.EdgeExtends, 0.85)...)
			}
		case "rust":
			// Rust uses `impl Trait for Type` to implement traits; we attach
			// the implements edge to the *type* symbol.
			if symbol.Kind != core.KindStruct && symbol.Kind != core.KindEnum {
				continue
			}
			body := symbol.RawText
			matches := rustImplForRe.FindAllStringSubmatch(body, -1)
			for _, m := range matches {
				traitName, typeName := m[1], m[2]
				if typeName != symbol.Name {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, traitName, core.EdgeImplements, 0.85)...)
			}
		case "go":
			// Go has structural interface satisfaction; broad implements edges
			// are emitted by buildInterfaceSatisfaction. Here we detect
			// EMBEDDING (extends): a struct embedding a type, or an interface
			// embedding another interface — both promote the embedded type's
			// method set onto the embedder, so an extends edge is correct.
			if symbol.Kind != core.KindStruct && symbol.Kind != core.KindInterface {
				continue
			}
			for _, name := range goEmbeddedTypes(symbol.RawText) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.7)...)
			}
		}
	}
	return edges
}

// buildUsesType emits uses-type edges from a symbol's signature, scoped to
// same-file and imported-file symbols (per Implementation Plan). The "to"
// side of each edge is a concrete symbol ID when resolvable.
func buildUsesType(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seen := make(map[string]bool)
	for _, symbol := range symbols {
		if symbol.Signature == "" {
			continue
		}
		if symbol.Language == "cobol" || symbol.Language == "jcl" {
			// Mainframe signatures are lists of field names — matching them
			// against in-scope names manufactured millions of vague edges
			// (measured on a real estate: 2.08M, 63% of the whole index).
			// The mainframe builder emits the precise vocabulary instead
			// (reads/writes/redefines/binds-dataset).
			continue
		}
		if symbol.Language == "python" && symbol.Kind == core.KindField {
			// Python class attributes (new 2026-08-30): their signatures
			// are attribute declarations ("name: str = ..."), and token-
			// matching them re-created the mainframe failure at django
			// scale — +23.7k uses-type edges including gems like
			// ListFilter.title -> CSP.NONE (the token "None" matching a
			// field named NONE). Scoped to python fields only so every
			// pre-existing language's counts stay byte-identical.
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)
		matches := usesTypeIdent.FindAllStringSubmatch(symbol.Signature, -1)
		for _, m := range matches {
			candidateName := m[1]
			if candidateName == symbol.Name {
				continue
			}
			for _, target := range idx.byName[strings.ToLower(candidateName)] {
				if target.Language == "python" && target.Kind == core.KindField {
					// Same scoping as the source-side skip above: 166
					// django fields named "model" as uses-type TARGETS of
					// every class signature containing that token is
					// noise, not typing evidence.
					continue
				}
				if _, inScope := scope[target.FilePath]; !inScope {
					continue
				}
				key := symbol.ID + "::uses-type::" + target.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				edges = append(edges, core.Edge{
					From:       symbol.ID,
					To:         target.ID,
					Type:       core.EdgeUsesType,
					Confidence: 0.5,
					Source:     core.EvidenceSourceHeuristic,
					Reason:     core.ReasonTypeRef,
				})
			}
		}
	}
	return edges
}

// callIdentRe extracts call-shaped identifiers ("name(") from a stripped
// body in a single pass.
var callIdentRe = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)

// maxCalleeFanout bounds how many cross-file targets a single callee name
// may resolve to. When a bare name matches more candidates than this, the
// reference is ambiguous (typically generated or templated code repeating
// one name across many files); emitting an edge to every candidate carries
// no signal and inflates Impact blast radii quadratically.
const maxCalleeFanout = 16

// resolveCallees resolves a bare callee name within symbol's import scope.
// A same-file match wins outright (unless sameFileWins is false): in every
// supported language a local definition shadows imported ones for BARE calls.
// For a call through an explicit typed receiver that shadowing is wrong —
// `serializer.serialize(...)` inside IndexedListSerializer.java must reach
// JsonSerializer.serialize even though the file declares its own serialize
// override — so callers pass sameFileWins=false when a non-self qualifier is
// present and type narrowing will pin the target. Cross-file matches are
// returned only when unambiguous enough to be meaningful.
func resolveCallees(idx *edgeIndex, symbol *core.SymbolRecord, calleeName string, scope map[string]struct{}, exactCase bool, sameFileWins bool) ([]*core.SymbolRecord, bool) {
	var sameFile, crossFile []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(calleeName)] {
		if cand.ID == symbol.ID {
			continue
		}
		if exactCase && cand.Name != calleeName {
			continue
		}
		if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod && cand.Kind != core.KindConstructor {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		if cand.FilePath == symbol.FilePath {
			sameFile = append(sameFile, cand)
		} else {
			crossFile = append(crossFile, cand)
		}
	}
	if len(sameFile) > 0 && sameFileWins {
		return sameFile, false
	}
	crossFile = append(crossFile, sameFile...)
	if len(crossFile) > maxCalleeFanout {
		// Over the fan-out cap — but narrowing may still pin these down, so
		// return them with the flag; the caller caps AFTER narrowing.
		return crossFile, true
	}
	return crossFile, false
}

// buildCalls emits same-file + imported-file call edges with strings/comments
// stripped from the body before matching.
//
// The fallback path extracts every call-shaped identifier from the body in
// one pass and resolves it through the name index. The previous
// implementation matched a per-callable compiled regex against every other
// callable's body — O(callables²) regex scans, which on a 10K-symbol
// single-package corpus took ~40s.
// buildCalls resolves every function/method's call and property-read sites.
// Per-symbol resolution is independent (dedup keys embed the caller ID, and
// idx/sat are read-only here), so symbols resolve in parallel; results
// concatenate in symbol order, keeping output byte-identical to the previous
// sequential build.
func buildCalls(idx *edgeIndex, symbols []core.SymbolRecord, sat *interfaceSatisfaction) []core.Edge {
	// Pre-warm the importedFiles memo for every file serially: it is the
	// only lazily-written edgeIndex state, and warming it here makes the
	// index strictly read-only for the parallel workers below.
	for f := range idx.byFile {
		idx.importedFiles(f)
	}
	results := make([][]core.Edge, len(symbols))
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	taskCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range taskCh {
				results[i] = resolveCallEdges(idx, symbols[i], sat)
			}
		}()
	}
	for i := range symbols {
		taskCh <- i
	}
	close(taskCh)
	wg.Wait()
	var edges []core.Edge
	for _, r := range results {
		edges = append(edges, r...)
	}
	return edges
}

// resolveCallEdges resolves one caller's outgoing call/property edges.
func resolveCallEdges(idx *edgeIndex, symbol core.SymbolRecord, sat *interfaceSatisfaction) []core.Edge {
	if symbol.Language == "cobol" || symbol.Language == "jcl" {
		// Mainframe semantics live in their own resolver; the machinery
		// below never sees these symbols (docs/mainframe-build-plan.md).
		return resolveMainframeCallEdges(idx, symbol)
	}
	var edges []core.Edge
	seen := make(map[string]bool)

	addEdge := func(fromID, toID string, confidence float64, source core.EvidenceSource, reason core.EdgeReason) {
		key := fromID + "::calls::" + toID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: fromID, To: toID,
			Type: core.EdgeCalls, Confidence: confidence, Source: source, Reason: reason,
		})
	}

	if symbol.Kind != core.KindFunction && symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor {
		return nil
	}
	scope := idx.importedFiles(symbol.FilePath)

	// ── Property reads (AST-extracted AttrSites) ────────────────────────
	// An attribute access ("request.blueprints") executes @property code
	// with no call syntax. Resolve strictly against property-annotated
	// methods so plain field reads never produce edges. Independent of
	// CallSites: a function may only read properties.
	if len(symbol.AttrSites) > 0 {
		attrSelfVars := callerSelfQualifiers(&symbol)
		var attrLocalTypes map[string]string
		if symbol.Language == "python" {
			attrLocalTypes = pyLocalTypes(idx, &symbol)
		}
		for _, as := range symbol.AttrSites {
			name := as.Callee
			qualifier := ""
			if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
				qualifier = name[:idx]
				name = name[idx+1:]
			}
			if j := strings.LastIndexByte(qualifier, '.'); j >= 0 {
				qualifier = qualifier[j+1:]
			}
			if name == "" {
				continue
			}
			cands := resolvePropertyTargets(idx, &symbol, name, scope)
			cands = narrowByReceiver(cands, &symbol, qualifier, attrSelfVars)
			if _, isSelf := attrSelfVars[qualifier]; !isSelf && qualifier != "" && len(cands) > 0 {
				// ctx.request with ctx typed AppContext reads that class's
				// property (or an ancestor's), not every `request` property
				// in scope; an untyped receiver keeps the set as before.
				if kept, dispatch, decided := narrowByLocalType(idx, nil, attrLocalTypes, qualifier, name, cands, scope); decided && len(kept)+len(dispatch) > 0 {
					cands = append(kept, dispatch...)
				}
			}
			if _, isSelf := attrSelfVars[qualifier]; isSelf && classLanguage(symbol.Language) && len(filterByParent(cands, symbol.ParentSymbol)) == 0 {
				if inherited := inheritedTargets(idx, &symbol, name, true); len(inherited) > 0 {
					cands = inherited
				}
			}
			for _, cand := range cands {
				addEdge(symbol.ID, cand.ID, 0.7, core.EvidenceSourceASTKit, core.ReasonProperty)
			}
		}
	}

	// ── Implicit dunder invocation: attribute assignment → __setattr__ ──
	// "g.foo = value" has no call syntax, so a class overriding attribute
	// assignment is otherwise invisible to the calls graph regardless of
	// CallSites (see pySetattrTargets).
	if symbol.Language == "python" {
		dunderSelfVars := callerSelfQualifiers(&symbol)
		dunderLocalTypes := pyLocalTypes(idx, &symbol)
		for _, cand := range pySetattrTargets(idx, &symbol, dunderLocalTypes, dunderSelfVars) {
			addEdge(symbol.ID, cand.ID, 0.7, core.EvidenceSourceHeuristic, core.ReasonImplicitDunder)
		}
		for _, cand := range pyWithTargets(idx, &symbol, dunderLocalTypes, dunderSelfVars) {
			addEdge(symbol.ID, cand.ID, 0.7, core.EvidenceSourceHeuristic, core.ReasonImplicitDunder)
		}
	}

	// ── High-confidence path: AST-extracted CallSites ───────────────────
	if len(symbol.CallSites) > 0 {
		selfVars := callerSelfQualifiers(&symbol)
		var localTypes map[string]string
		var pyParams map[string]bool
		switch symbol.Language {
		case "go":
			localTypes = goLocalTypes(idx, &symbol)
		case "python":
			localTypes = pyLocalTypes(idx, &symbol)
			pyParams = pyParamNames(symbol.RawText)
		case "typescript", "tsx", "javascript":
			localTypes = tsLocalTypes(idx, &symbol)
		case "java":
			localTypes = javaLocalTypes(idx, &symbol)
		case "rust":
			localTypes = rustLocalTypes(idx, &symbol)
		case "csharp":
			localTypes = csharpLocalTypes(idx, &symbol)
		case "php":
			localTypes = phpLocalTypes(idx, &symbol)
		case "c", "cpp":
			localTypes = cFamilyLocalTypes(idx, &symbol)
		}
		var javaArgTypeCache map[string]string
		var csArgTypeCache map[string]string
		for _, cs := range symbol.CallSites {
			calleeName := cs.Callee
			// Split receiver prefix (e.g. "user.save" → qualifier "user",
			// name "save"); chains keep only the last segment ("a.b.Get" → "b").
			qualifier := ""
			fullChain := "" // full receiver chain, e.g. "this.connection.driver"
			if idx := strings.LastIndexByte(calleeName, '.'); idx >= 0 {
				qualifier = calleeName[:idx]
				calleeName = calleeName[idx+1:]
			}
			fullChain = qualifier
			if j := strings.LastIndexByte(qualifier, '.'); j >= 0 {
				qualifier = qualifier[j+1:]
			}
			// astkit collapses a member-chain receiver to its last segment
			// (`this.connection.driver.escape` → callee "driver.escape"), so
			// fullChain lost the intermediate hops. For TS/JS/Java recover the
			// full chain from the caller's own source line to enable
			// multi-hop field-type resolution below (Java shares the dotted
			// receiver syntax, so the same source recovery applies:
			// `w.serializer.serialize(...)` → chain "w.serializer").
			if (tsFamilyLang(symbol.Language) || symbol.Language == "java") && qualifier != "" && cs.Line > 0 {
				if chain := tsReceiverChainAt(symbol.RawText, cs.Line-symbol.Span.Start, calleeName); chain != "" {
					fullChain = chain
				}
			}
			if calleeName == "" || calleeName == "constructor" || calleeName == "super" {
				// "new X" and "super(...)" are invocation forms, not names:
				// a bare "constructor" callee would match every class's
				// constructor in scope.
				continue
			}
			if qualifier == "" && declaresLocalFunction(&symbol, calleeName) {
				// A function declared inside the caller's own body
				// (`function run(i)` nested in Socket.run, a nested `def`)
				// is not indexed as a symbol; the bare call binds that
				// closure, so any same-named function elsewhere is a
				// fabricated target. Emit nothing.
				continue
			}
			if symbol.Language == "python" && qualifier == "" && pyParams[calleeName] {
				if _, typed := localTypes[calleeName]; !typed {
					// Bare call through a parameter name (`loads(value)`
					// where `loads` is a `Callable` parameter) invokes
					// whatever the caller actually passed in — not any
					// particular symbol in scope. Resolving it by matching
					// the parameter's name against an unrelated function
					// elsewhere fabricates an edge (see pyParamNames). A
					// parameter localTypes DID resolve (e.g. `x: type[X]`,
					// used as a factory) keeps its existing, deliberate
					// constructor-narrowing path below — only the
					// unresolvable case is suppressed here.
					continue
				}
			}
			// AST-extracted names are exact by construction: case-insensitive
			// matching here let "writeContentType" (free function) claim every
			// type's "WriteContentType" method.
			//
			// Same-file shadowing applies to bare calls only. A Java call
			// through an explicit non-self receiver binds by the receiver's
			// type, not the enclosing file — a file that overrides serialize
			// still calls OTHER types' serialize through typed receivers,
			// and the typed-receiver narrowing below pins (or precisely
			// drops) the widened candidate set.
			sameFileWins := true
			if qualifier == "super" || qualifier == "super()" || (symbol.Language == "csharp" && qualifier == "base") {
				// The target lives in the base class's file; a same-file
				// override must not shadow it.
				sameFileWins = false
			}
			if (symbol.Language == "java" || symbol.Language == "rust" || symbol.Language == "csharp") &&
				qualifier != "" && qualifier != "this" && qualifier != "super" && qualifier != "self" && qualifier != "Self" && qualifier != "base" {
				// Statically typed receiver or type path: the target is the
				// named type's, wherever it lives. Same-file-wins bound a
				// printer test's `SearcherBuilder::new()` to the printer
				// file's own `new`s and the typed narrowing never saw the
				// searcher crate's.
				if _, isSelf := selfVars[qualifier]; !isSelf {
					sameFileWins = false
				}
			}
			cands, capped := resolveCallees(idx, &symbol, calleeName, scope, true, sameFileWins)
			if traceCalls {
				ids := make([]string, 0, 4)
				for i, c := range cands {
					if i == 4 {
						break
					}
					ids = append(ids, c.ID)
				}
				fmt.Fprintf(os.Stderr, "grove-trace %s: callee=%q qual=%q args=%v cands=%d capped=%v scope=%d first=%v\n", symbol.QualifiedName, cs.Callee, qualifier, cs.Args, len(cands), capped, len(scope), ids)
			}
			if capped && symbol.Language != "java" && symbol.Language != "rust" {
				// Only narrowing with real evidence may keep very large
				// same-name sets: Java (arity, arg types) and Rust
				// (typed receivers/qualifiers — crate-wide scope makes
				// "new" or "update" routinely exceed the cap before the
				// type evidence has had its chance). For the rest an
				// over-cap set stays dropped (dispatch rescue below
				// still applies); anything Rust's narrowing fails to
				// pin back down is re-capped after narrowing.
				cands = nil
			}
			if symbol.Language == "csharp" || symbol.Language == "php" ||
				symbol.Language == "c" || symbol.Language == "cpp" {
				// Overload disambiguation by arity. C#: JsonConvert has
				// five DeserializeObject overloads, Roslyn picks one by
				// args. PHP has no overloads but default/variadic params
				// mean a same-named method on an unrelated class with a
				// different arity is still a wrong candidate. filterByArgc
				// keeps variadic/default-friendly candidates and never
				// zeroes the set.
				cands = filterByArgc(cands, cs.Argc)
			}
			if symbol.Language == "csharp" && len(cands) > 1 {
				// Generic split: DeserializeObject<T>(string) and
				// DeserializeObject(string) collide on name + arity + value
				// arg type (both take a string). The call's explicit type
				// args (cs.Generic) are the only signal — a generic call
				// binds a generic overload, a non-generic call a
				// non-generic one. Roslyn's 5-overload JsonConvert fanout
				// was the dominant C# false-positive source.
				cands = filterByGeneric(cands, cs.Generic)
				if len(cands) > 1 && len(cs.Args) > 0 {
					// Then argument types, as for Java: 71% of the
					// remaining C# false edges were same-arity overloads
					// (new BsonWriter(stream) → BsonWriter(BinaryWriter)).
					if csArgTypeCache == nil {
						csArgTypeCache = csharpArgTypes(idx, &symbol)
					}
					cands = csNarrowOverloads(idx, cands, cs.Args, csArgTypeCache)
				}
			}
			if creationSite(&symbol, cs) {
				// `new X(...)`: only a constructor can be the target. A
				// same-named method elsewhere (a test named List() for
				// `new List<Animal>()`) is never it.
				var ctorsOnly []*core.SymbolRecord
				for _, cand := range cands {
					if cand.Kind == core.KindConstructor {
						ctorsOnly = append(ctorsOnly, cand)
					}
				}
				cands = ctorsOnly
			}
			if symbol.Language == "java" {
				// Overload disambiguation: arity first, then exact
				// argument-type evidence (positive matches only — see
				// narrowOverloadsByArgTypes).
				cands = filterByArgc(cands, cs.Argc)
				if len(cands) > 1 && len(cs.Args) > 0 {
					if javaArgTypeCache == nil {
						javaArgTypeCache = javaArgTypes(idx, &symbol)
					}
					javaResolveCallReturnTypes(idx, cs.Args, scope, javaArgTypeCache)
					cands = narrowOverloadsByArgTypes(cands, cs.Args, javaArgTypeCache)
				}
				// Static typing makes unknowns meaningful: a lowercase
				// receiver with no inferable type is almost always a JDK
				// or library object (map.isEmpty, list.forEach) — its
				// methods aren't in our index, so name collisions are
				// noise. Call-result receivers resolve through the inner
				// call's return type (append().append keeps builder
				// chains); unresolvable ones drop too.
				if qualifier != "" && qualifier != "super" {
					if localTypes == nil {
						localTypes = javaLocalTypes(idx, &symbol)
					}
					_, isSelf := selfVars[qualifier]
					_, typed := localTypes[qualifier]
					if !isSelf && !typed && !typeSymbolExists(idx, qualifier) {
						if strings.HasSuffix(qualifier, "()") {
							if rets := javaCallResultTypes(idx, qualifier, scope); len(rets) > 0 {
								var byType []*core.SymbolRecord
								for t := range rets {
									byType = append(byType, filterByParent(cands, t)...)
								}
								if len(byType) == 0 {
									// The result type's file is not in
									// the caller's import scope (it never
									// has to be — see javaMethodsOfTypes).
									byType = javaMethodsOfTypes(idx, rets, calleeName, dirOf(symbol.FilePath))
								}
								cands = byType
							} else {
								cands = nil
							}
						} else if qualifier[0] >= 'a' && qualifier[0] <= 'z' {
							cands = nil
						}
					}
				}
			}
			if symbol.Language == "rust" && qualifier == "" && calleeName == "drop" {
				// Prelude mem::drop: a bare drop(x) never targets an
				// in-repo Drop impl by name.
				continue
			}
			if symbol.Language == "python" && strings.HasSuffix(qualifier, "()") && len(cands) > 0 {
				// Call-result receiver (self.app_context().push()): the
				// called def's return annotation names the class. An
				// unannotated result keeps the unnarrowed set — Python's
				// typing is optional, so silence is not evidence.
				if ret := pyCallResultType(idx, strings.TrimSuffix(qualifier, "()"), &symbol); ret != "" {
					if byType := filterByParent(cands, ret); len(byType) > 0 {
						cands = byType
					} else {
						cands = nil
					}
				}
			}
			if symbol.Language == "php" && strings.HasSuffix(qualifier, "()") && len(cands) > 0 {
				// Fluent-chain receiver ($builder->make()->addStmt()): resolve
				// the call result's class and keep only its methods. An
				// unresolvable/ambiguous result (self-returning builder method
				// that exists on many classes) drops — mirrors Java/Rust — so
				// the chain does not fan out to every same-named method.
				if ret := phpCallResultType(idx, qualifier, scope); ret != "" {
					if byType := filterByParent(cands, ret); len(byType) > 0 {
						cands = byType
					} else {
						cands = nil
					}
				} else {
					cands = nil
				}
			}
			if (symbol.Language == "csharp" || symbol.Language == "php" ||
				symbol.Language == "c" || symbol.Language == "cpp") &&
				qualifier != "" && qualifier != "this" && qualifier != "base" &&
				qualifier != "self" && qualifier != "parent" && qualifier != "static" &&
				!strings.HasSuffix(qualifier, "()") && len(cands) > 0 {
				// Static typing, C#/PHP edition of the Java/Rust rule. A
				// qualifier names a type directly (JsonConvert.ToString,
				// Foo::bar) or a typed variable (reader.Read, $repo->save).
				// If it's neither a known indexed type nor an inferable
				// local, the receiver is a library object (sb.Append,
				// $logger->info) whose method isn't ours — a same-name
				// match is noise: drop. A resolvable type narrows by parent.
				if held, ok := localTypes[qualifier]; ok {
					byType := filterByParent(cands, held)
					// Inherited: `BsonReader reader; reader.ReadAsBytes()`
					// runs JsonReader's. Walk the declared type's bases
					// before concluding the method is not ours.
					bases := baseClassesFor(idx, symbol.Language, held, dirOf(symbol.FilePath))
					for level := 0; level < 4 && len(bases) > 0 && len(byType) == 0; level++ {
						var next []string
						for _, b := range bases {
							byType = append(byType, filterByParent(cands, b)...)
							next = append(next, baseClassesFor(idx, symbol.Language, b, dirOf(symbol.FilePath))...)
						}
						bases = next
					}
					if len(byType) > 0 {
						cands = byType
					} else {
						cands = nil
					}
				} else if byQual := filterByParent(cands, qualifier); len(byQual) > 0 {
					cands = byQual
				} else if !typeSymbolExists(idx, qualifier) {
					cands = nil
				}
			}
			if symbol.Language == "rust" && qualifier != "" && len(cands) > 1 {
				cands = rustPinByPath(idx, &symbol, cs, qualifier, cands)
			}
			if symbol.Language == "rust" && qualifier == "" && len(cands) > 0 && !anyInFile(cands, symbol.FilePath) && rustImportedExternal(idx, &symbol, calleeName) {
				// `use regex_syntax::escape;` then `escape(pattern)`: the
				// function is the external crate's, not any same-named
				// function of ours.
				cands = nil
			}
			if symbol.Language == "rust" && qualifier != "" && len(cands) > 0 {
				// Static typing, Rust edition of the Java rule. An
				// uppercase qualifier is a type path (PathBuf::from,
				// Regex::new): if no candidate belongs to that type and
				// no local resolves it, the callee lives outside the
				// repo — drop. A lowercase qualifier with no local type
				// is a module path or an uninferable variable: keep
				// only candidates declared in a matching module file
				// (parse::parse_low_raw → flags/parse.rs), else drop.
				_, isSelf := selfVars[qualifier]
				_, typed := localTypes[qualifier]
				if strings.HasSuffix(qualifier, "()") {
					// Call-result receiver: builder chains live here
					// (.line_number(true).build() narrows build by
					// line_number's return type). Unknown results are
					// external (.unwrap().x, .iter().y) — drop.
					if rets := rustCallResultTypes(idx, qualifier, &symbol, scope); len(rets) > 0 {
						var byType []*core.SymbolRecord
						for _, cand := range cands {
							if (cand.Kind == core.KindMethod || cand.Kind == core.KindConstructor) && rets[cand.ParentSymbol] {
								byType = append(byType, cand)
							}
						}
						cands = byType
					} else {
						cands = nil
					}
				} else if isSelf && symbol.ParentSymbol != "" && len(filterByParent(cands, symbol.ParentSymbol)) == 0 {
					// Default trait methods: self.is_match() inside
					// impl Matcher for X, where X declares no
					// is_match, executes the trait's declaration.
					if trait := rustImplTrait(&symbol); trait != "" {
						if byTrait := filterByParent(cands, trait); len(byTrait) > 0 {
							cands = byTrait
						}
					}
				} else if !isSelf && !typed && len(filterByParent(cands, qualifier)) == 0 {
					if qualifier[0] >= 'A' && qualifier[0] <= 'Z' {
						cands = nil
					} else {
						// Module-named files win; a single same-file
						// candidate stays for inline modules (mod
						// convert { fn str... } inside defs.rs), but a
						// same-named set in one file is receiver
						// ambiguity, not module scoping — drop it.
						var inModule, sameFile []*core.SymbolRecord
						for _, cand := range cands {
							base := baseNameNoExt(cand.FilePath)
							if base == qualifier || (base == "mod" && baseOf(dirOf(cand.FilePath)) == qualifier) {
								inModule = append(inModule, cand)
							} else if cand.FilePath == symbol.FilePath {
								sameFile = append(sameFile, cand)
							}
						}
						cands = inModule
						if len(cands) == 0 && len(sameFile) == 1 {
							cands = sameFile
						}
					}
				}
			}
			// super().method() / super.method() resolves on the caller's
			// base classes; bare super() invokes the base constructor.
			if qualifier == "super()" || qualifier == "super" || (symbol.Language == "csharp" && qualifier == "base") {
				if traceCalls {
					fmt.Fprintf(os.Stderr, "grove-trace %s: super bases=%v matched=%d\n", symbol.QualifiedName, baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath)), len(narrowBySuper(idx, &symbol, cands)))
				}
				for _, cand := range narrowBySuper(idx, &symbol, cands) {
					addEdge(symbol.ID, cand.ID, 0.85, core.EvidenceSourceHeuristic, core.ReasonInheritance)
				}
				continue
			}
			if calleeName == "super()" && symbol.ParentSymbol != "" {
				for _, base := range baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath)) {
					targets := constructorTargets(idx, base, scope)
					if len(targets) == 0 {
						// Inheritance crosses imports — but prefer the twin
						// in the caller's own package over same-named
						// classes elsewhere in a monorepo.
						for _, cand := range idx.byName["constructor"] {
							if cand.ParentSymbol == base && cand.Kind == core.KindConstructor &&
								samePackageRoot(cand.FilePath, symbol.FilePath) {
								targets = append(targets, cand)
							}
						}
					}
					for _, ctor := range targets {
						addEdge(symbol.ID, ctor.ID, 0.85, core.EvidenceSourceHeuristic, core.ReasonConstructor)
					}
				}
				continue
			}
			narrowed := narrowByReceiver(cands, &symbol, qualifier, selfVars)
			// resolvedByType records that the receiver's type was known and
			// used to pin (or precisely drop) the targets — in which case the
			// blanket dispatch rescue below must NOT also fire, or it floods a
			// precisely-resolved interface call (x.Get on a SecretsKVStore)
			// with every same-named method across the repo.
			resolvedByType := false
			_, isSelf := selfVars[qualifier]
			// Bare unqualified call in Java/C#/C++ is implicit this.method():
			// member lookup binds it to the caller's own class first, so it
			// must not fan out to every same-named method across unrelated
			// classes. Narrow to own-class candidates when the own class
			// declares the method; otherwise fall through to the inherited
			// lookup below (ancestor) or, if neither, leave it (free function).
			implicitSelf := qualifier == "" && symbol.ParentSymbol != "" &&
				(symbol.Kind == core.KindMethod || symbol.Kind == core.KindConstructor) &&
				implicitSelfLanguage(symbol.Language)
			if implicitSelf {
				if own := filterByParent(cands, symbol.ParentSymbol); len(own) > 0 {
					narrowed = own
				}
			}
			if (isSelf || implicitSelf) && classLanguage(symbol.Language) {
				if len(filterByParent(narrowed, symbol.ParentSymbol)) == 0 {
					// Not a method on the caller's own class: inheritance
					// reaches files import scope never sees.
					if inherited := inheritedTargets(idx, &symbol, calleeName, false); len(inherited) > 0 {
						// A monorepo declares `Transport` in both the client
						// and the server package; the caller's base class is
						// the one its import scope reaches. Prefer in-scope
						// ancestors, fall back to all when none is.
						var inScope []*core.SymbolRecord
						for _, cand := range inherited {
							if _, ok := scope[cand.FilePath]; ok {
								inScope = append(inScope, cand)
							}
						}
						if len(inScope) > 0 {
							inherited = inScope
						}
						for _, cand := range inherited {
							addEdge(symbol.ID, cand.ID, 0.85, core.EvidenceSourceHeuristic, core.ReasonInheritance)
						}
						continue
					}
				} else {
					// Template method: self.to_json() inside the base class
					// runs whichever subclass override the instance carries
					// (flask's JSONTag.tag → TagDict.to_json and friends).
					// The own-class edge stays; the overrides join as
					// dispatch so change-impact on the base call sees them.
					for _, m := range subclassOverrides(idx, symbol.Language, symbol.ParentSymbol, calleeName, dirOf(symbol.FilePath)) {
						if m.ID != symbol.ID {
							addEdge(symbol.ID, m.ID, 0.7, core.EvidenceSourceHeuristic, core.ReasonDispatch)
						}
					}
				}
			}
			if len(narrowed) == len(cands) {
				// Receiver narrowing didn't fire; try the inferred type of
				// the receiver variable, then import qualification.
				kept, dispatch, decided := narrowByLocalType(idx, sat, localTypes, qualifier, calleeName, cands, scope)
				if !decided && (tsFamilyLang(symbol.Language) || symbol.Language == "java") && strings.Contains(fullChain, ".") {
					// Multi-hop receiver (`this.connection.driver.escape`):
					// walk the field-type chain and dispatch to the resolved
					// type's implementors. The single-segment lookup above
					// cannot see `driver` because it is a field of the chain's
					// intermediate type, not of the enclosing class.
					kept, dispatch, decided = narrowByChainType(idx, sat, localTypes, &symbol, fullChain, calleeName, cands)
				}
				if decided {
					// The receiver type was known: this call site is resolved
					// (to kept, to dispatch implementors, or to nothing). Mark
					// it so the blanket dispatch rescue below stays out.
					resolvedByType = true
					narrowed = kept
					for _, m := range dispatch {
						if m.ID != symbol.ID {
							addEdge(symbol.ID, m.ID, 0.7, core.EvidenceSourceHeuristic, core.ReasonDispatch)
						}
					}
				} else {
					narrowed = narrowByImport(idx, &symbol, qualifier, cands)
				}
			}
			if len(narrowed) > maxCalleeFanout {
				// Still unresolvably broad after every narrowing pass:
				// drop (the dispatch rescue below may still apply).
				narrowed = nil
				capped = true
			} else if len(narrowed) > 0 {
				capped = false
			}
			if traceCalls && len(narrowed) > 0 {
				ids := make([]string, 0, len(narrowed))
				for _, c := range narrowed {
					ids = append(ids, c.ID)
				}
				fmt.Fprintf(os.Stderr, "grove-trace %s: callee=%q narrowed=%v\n", symbol.QualifiedName, cs.Callee, ids)
			}
			for _, cand := range narrowed {
				addEdge(symbol.ID, cand.ID, 0.95, core.EvidenceSourceASTKit, core.ReasonASTNarrowed)
			}
			// Class instantiation: "Flask(...)" executes Flask.__init__.
			// Route class-named calls to the class's constructor method;
			// "cls(...)" constructs the caller's own class, and a variable
			// holding a class (null_session_class = NullSession) constructs
			// the held class.
			if len(narrowed) == 0 && !capped {
				ctorName := calleeName
				if held, ok := localTypes[calleeName]; ok && strings.HasPrefix(held, "class:") {
					ctorName = strings.TrimPrefix(held, "class:")
				} else if calleeName == "cls" && symbol.ParentSymbol != "" {
					ctorName = symbol.ParentSymbol
				}
				ctors := constructorTargets(idx, ctorName, scope)
				if symbol.Language == "java" {
					ctors = filterByArgc(ctors, cs.Argc)
					if len(ctors) > 1 && len(cs.Args) > 0 {
						if javaArgTypeCache == nil {
							javaArgTypeCache = javaArgTypes(idx, &symbol)
						}
						ctors = narrowOverloadsByArgTypes(ctors, cs.Args, javaArgTypeCache)
					}
				}
				if symbol.Language == "php" {
					ctors = phpNarrowNewByNamespace(idx, &symbol, cs, ctorName, ctors)
				}
				if symbol.Language == "csharp" {
					ctors = filterByArgc(ctors, cs.Argc)
					if len(ctors) > 1 && len(cs.Args) > 0 {
						if csArgTypeCache == nil {
							csArgTypeCache = csharpArgTypes(idx, &symbol)
						}
						ctors = csNarrowOverloads(idx, ctors, cs.Args, csArgTypeCache)
					}
				}
				for _, ctor := range ctors {
					if ctor.ID != symbol.ID {
						addEdge(symbol.ID, ctor.ID, 0.85, core.EvidenceSourceHeuristic, core.ReasonConstructor)
					}
				}
			}
			// Fan-out the cap dropped is legitimate dynamic dispatch when an
			// in-scope interface declares the method: emit edges to its
			// implementations at reduced confidence.
			if capped && !resolvedByType && sat != nil {
				for _, m := range sat.dispatchTargets(calleeName, scope) {
					if m.ID != symbol.ID {
						addEdge(symbol.ID, m.ID, 0.7, core.EvidenceSourceHeuristic, core.ReasonDispatch)
					}
				}
			}
		}
		return edges // CallSites authoritative; skip regex fallback for this symbol
	}

	// ── Fallback: one identifier-extraction pass over the stripped body ──
	// Only for languages without AST call-site extraction: where the
	// extractor ran, an empty CallSites list is authoritative — a method
	// with zero calls would otherwise regex-match its own signature
	// ("append(final int value)" edging every sibling overload).
	if astCallSiteLanguages[symbol.Language] {
		return edges
	}
	if symbol.RawText == "" {
		return edges
	}
	stripped := stripCommentsAndStrings(symbol.RawText)
	seenCallee := make(map[string]bool)
	for _, m := range callIdentRe.FindAllStringSubmatch(stripped, -1) {
		calleeName := m[1]
		if seenCallee[calleeName] {
			continue
		}
		if calleeName == "constructor" || calleeName == "super" {
			continue
		}
		seenCallee[calleeName] = true
		cands, fbCapped := resolveCallees(idx, &symbol, calleeName, scope, true, true)
		if fbCapped {
			// The fallback has no narrowing evidence; over-cap stays dropped.
			continue
		}
		for _, cand := range cands {
			confidence := 0.6
			if cand.FilePath == symbol.FilePath {
				confidence = 0.85
			}
			addEdge(symbol.ID, cand.ID, confidence, core.EvidenceSourceRegex, core.ReasonRegexFallbck)
		}
	}
	return edges
}

// astCallSiteLanguages lists the languages whose astkit extractors emit
// CallSites; for them the AST path is authoritative and the regex fallback
// never runs.
var astCallSiteLanguages = map[string]bool{
	"go": true, "python": true, "javascript": true, "typescript": true,
	"tsx": true, "java": true, "rust": true, "csharp": true, "php": true,
	"c": true, "cpp": true,
}

// classLanguage reports whether the language has class inheritance our
// base-class parsers understand.
func classLanguage(lang string) bool {
	return lang == "python" || lang == "typescript" || lang == "javascript" || lang == "java" || lang == "csharp" || lang == "php" || lang == "cpp"
}

// implicitSelfLanguage reports whether a bare, unqualified call inside a method
// is an implicit this.method() — member lookup binds it to the caller's own
// class (or an ancestor) before any other scope. True for Java, C#, and C++.
// Excludes PHP/Python/JS/TS, where a bare call() is a free function or local,
// not self.method(), so narrowing it to the caller's class would be wrong.
func implicitSelfLanguage(lang string) bool {
	return lang == "java" || lang == "csharp" || lang == "cpp"
}

// callerSelfQualifiers returns the receiver spellings that mean "a method on
// my own type" inside this symbol's body: self/this plus, for Go, the
// receiver variable parsed from the method signature ("func (r JSON) ...").
func callerSelfQualifiers(symbol *core.SymbolRecord) map[string]struct{} {
	out := map[string]struct{}{"self": {}, "this": {}, "cls": {}}
	if symbol.Language == "go" && symbol.Kind == core.KindMethod {
		if v := goReceiverVar(symbol.Signature); v != "" {
			out[v] = struct{}{}
		}
	}
	return out
}

// goReceiverVar extracts the receiver variable name from a Go method
// signature like "func (r JSON) Render(w http.ResponseWriter) error".
func goReceiverVar(signature string) string {
	rest, ok := strings.CutPrefix(signature, "func (")
	if !ok {
		return ""
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return ""
	}
	recv := strings.TrimSpace(rest[:end])
	// "r JSON" / "r *JSON" → "r"; bare "*JSON" / "JSON" has no variable.
	if i := strings.IndexByte(recv, ' '); i > 0 {
		return recv[:i]
	}
	return ""
}

// narrowByReceiver tightens name-resolved callee candidates using the call
// site's receiver qualifier. Two cases resolve without a type checker:
//
//   - the qualifier is the caller's own receiver (r./self./this.) → keep only
//     methods on the caller's parent type
//   - the qualifier names a type directly (JSON.WriteContentType) → keep only
//     methods on that type
//
// When the qualifier matches neither pattern (an arbitrary local variable, an
// external type, a package alias), candidates pass through unchanged: this
// narrows known-wrong fanout, it never invents matches.
func narrowByReceiver(cands []*core.SymbolRecord, caller *core.SymbolRecord, qualifier string, selfVars map[string]struct{}) []*core.SymbolRecord {
	if qualifier == "" || len(cands) < 2 {
		return cands
	}
	if _, isSelf := selfVars[qualifier]; isSelf && caller.ParentSymbol != "" {
		if same := filterByParent(cands, caller.ParentSymbol); len(same) > 0 {
			return same
		}
		return cands
	}
	if byType := filterByParent(cands, qualifier); len(byType) > 0 {
		return byType
	}
	return cands
}

func filterByParent(cands []*core.SymbolRecord, parent string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range cands {
		// Constructors count: Rust's Type::new is a constructor-kind
		// method and must narrow by its parent like any other.
		if (cand.Kind == core.KindMethod || cand.Kind == core.KindConstructor) && cand.ParentSymbol == parent {
			out = append(out, cand)
		}
	}
	return out
}

// resolvePropertyTargets finds in-scope property-annotated methods matching
// an attribute access name. Same-file candidates win; cross-file fan-out is
// capped like calls resolution.
func resolvePropertyTargets(idx *edgeIndex, symbol *core.SymbolRecord, name string, scope map[string]struct{}) []*core.SymbolRecord {
	var sameFile, crossFile []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.ID == symbol.ID || cand.Name != name || cand.Kind != core.KindMethod {
			continue
		}
		if !hasPropertyAnnotation(cand) {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		if cand.FilePath == symbol.FilePath {
			sameFile = append(sameFile, cand)
		} else {
			crossFile = append(crossFile, cand)
		}
	}
	if len(sameFile) > 0 {
		return sameFile
	}
	if len(crossFile) > maxCalleeFanout {
		return nil
	}
	return crossFile
}

func hasPropertyAnnotation(s *core.SymbolRecord) bool {
	for _, ann := range s.Annotations {
		if ann == "property" || ann == "cached_property" ||
			strings.HasSuffix(ann, ".setter") || strings.HasSuffix(ann, ".getter") || strings.HasSuffix(ann, ".deleter") {
			return true
		}
	}
	return false
}

// filterByArgc keeps candidates whose declared parameter count is
// compatible with the call site's argument count: exact match, or varargs
// ("...") accepting argc >= fixed params. Candidates whose parameter list
// can't be parsed pass through.
func filterByArgc(cands []*core.SymbolRecord, argc int) []*core.SymbolRecord {
	if len(cands) < 2 {
		return cands
	}
	var out []*core.SymbolRecord
	for _, cand := range cands {
		n, variadic, ok := declParamCount(cand)
		if !ok || n == argc || (variadic && argc >= n-1) {
			out = append(out, cand)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// filterByGeneric keeps the overloads whose generic-ness matches the call's:
// a call with explicit type arguments (Foo<T>()) binds a generic overload, a
// plain call binds a non-generic one. Never zeroes the set — if no candidate
// matches (e.g. signatures didn't parse), all pass through so a real edge is
// not lost to a parse gap.
func filterByGeneric(cands []*core.SymbolRecord, wantGeneric bool) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range cands {
		if csIsGenericMethod(cand) == wantGeneric {
			out = append(out, cand)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// csIsGenericMethod reports whether a C# overload declares type parameters,
// detected as the method name immediately followed by "<" in the signature
// head ("DeserializeObject<T>(...)"). A generic return type ("List<int> Foo()")
// does not count: the "<" there does not follow the method name.
func csIsGenericMethod(s *core.SymbolRecord) bool {
	head := s.Signature
	if i := strings.IndexByte(head, '('); i >= 0 {
		head = head[:i]
	}
	return strings.Contains(head, s.Name+"<")
}

// declParamCount counts declared parameters from a callable's first
// balanced paren group (signature if complete, else raw text).
func declParamCount(s *core.SymbolRecord) (int, bool, bool) {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = s.RawText
	}
	if s.Language == "java" {
		src = javaDeclSource(s)
	}
	params := tsDeclParams(src)
	if params == "" {
		if strings.Contains(src, "()") {
			return 0, false, true
		}
		return 0, false, false
	}
	groups := splitTopLevel(params, ',')
	n := 0
	variadic := false
	for _, g := range groups {
		if strings.TrimSpace(g) == "" {
			continue
		}
		if s.Language == "csharp" && strings.HasPrefix(strings.TrimSpace(g), "this ") {
			continue // extension-method receiver is not an argument
		}
		n++
		if strings.Contains(g, "...") || strings.HasPrefix(strings.TrimSpace(g), "params ") {
			variadic = true // Java `T...` / C# `params T[]`
		}
	}
	return n, variadic, true
}

// constructorTargets resolves a class-named call ("Flask(...)") to the
// class's constructor method (__init__, constructor, or any
// KindConstructor child) so instantiation produces a call edge to the code
// that actually runs. A class without its own constructor inherits one:
// the base-class chain is walked until a constructor is found.
func constructorTargets(idx *edgeIndex, calleeName string, scope map[string]struct{}) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cls := range idx.byName[strings.ToLower(calleeName)] {
		if cls.Name != calleeName {
			continue
		}
		if cls.Kind != core.KindClass && cls.Kind != core.KindStruct {
			continue
		}
		if _, ok := scope[cls.FilePath]; !ok {
			continue
		}
		out = append(out, classConstructors(idx, cls.Name, cls.FilePath)...)
		if len(out) == 0 {
			bases := baseClassesFor(idx, languageOfFile(idx, cls.FilePath), cls.Name, dirOf(cls.FilePath))
			for level := 0; level < 3 && len(bases) > 0 && len(out) == 0; level++ {
				var next []string
				for _, base := range bases {
					for _, baseCls := range idx.byName[strings.ToLower(base)] {
						if baseCls.Name == base && baseCls.Kind == core.KindClass {
							out = append(out, classConstructors(idx, base, baseCls.FilePath)...)
							next = append(next, baseClassesFor(idx, languageOfFile(idx, baseCls.FilePath), base, dirOf(baseCls.FilePath))...)
							break
						}
					}
				}
				bases = next
			}
		}
	}
	return out
}

func classConstructors(idx *edgeIndex, className, filePath string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range idx.byFile[filePath] {
		if cand.ParentSymbol != className {
			continue
		}
		if cand.Kind == core.KindConstructor ||
			(cand.Kind == core.KindMethod && (cand.Name == "__init__" || cand.Name == "constructor")) {
			out = append(out, cand)
		}
	}
	return out
}

func languageOfFile(idx *edgeIndex, filePath string) string {
	for _, s := range idx.byFile[filePath] {
		if s.Language != "" {
			return s.Language
		}
	}
	return ""
}

// samePackageRoot reports whether two repo-relative paths share their first
// two path segments ("packages/engine.io/...") — the monorepo package
// boundary heuristic.
func samePackageRoot(a, b string) bool {
	segA, segB := strings.SplitN(a, "/", 3), strings.SplitN(b, "/", 3)
	if len(segA) < 2 || len(segB) < 2 {
		return dirOf(a) == dirOf(b)
	}
	return segA[0] == segB[0] && segA[1] == segB[1]
}

// narrowByImport handles package-qualified call sites ("json.Marshal",
// "render.New"): when the qualifier exactly matches the last segment of one
// of the caller file's imports, the call most likely targets that package —
// so candidates restrict to that import's in-repo files. An import that
// resolves to no in-repo file is an external dependency: the call can't
// target anything we index, so all candidates drop. Qualifiers that match no
// import pass through unchanged.
//
// Matching is case-exact: a field named "Session" must not be confused with
// an "internal/session" import. Methods stay in the restriction — the Go
// pattern of naming a field after its package ("h.grove.Index()") means a
// package-looking qualifier can still be a value receiver.
func narrowByImport(idx *edgeIndex, symbol *core.SymbolRecord, qualifier string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if qualifier == "" || len(cands) == 0 || strings.HasSuffix(qualifier, "()") {
		return cands
	}
	files, isImport := idx.importFilesForQualifier(symbol.FilePath, qualifier)
	if !isImport {
		// Java: an uppercase qualifier is a class reference. With no import
		// and no indexed type of that name, it's an implicit-JDK class
		// (System, Math, Objects...) — the call can't target our index.
		if symbol.Language == "java" && qualifier[0] >= 'A' && qualifier[0] <= 'Z' &&
			!typeSymbolExists(idx, qualifier) {
			return nil
		}
		return cands
	}
	var out []*core.SymbolRecord
	for _, cand := range cands {
		if _, ok := files[cand.FilePath]; ok {
			out = append(out, cand)
		}
	}
	return out
}

// importFilesForQualifier resolves the import whose last segment equals the
// qualifier (case-exact) to its in-repo files, using only the precise
// resolvers: exact path match and slash-suffix directory match. The fuzzy
// basename resolvers importedFiles uses for scope would mis-resolve external
// imports to same-named in-repo dirs here ("encoding/json" → internal/json).
// The second return reports whether such an import exists at all.
func (idx *edgeIndex) importFilesForQualifier(fromFile, qualifier string) (map[string]struct{}, bool) {
	key := fromFile + "\x00" + qualifier
	if v, ok := idx.qualifierFiles.Load(key); ok {
		hit := v.(qualifierFileSet)
		return hit.files, hit.found
	}
	files, found := idx.computeImportFilesForQualifier(fromFile, qualifier)
	idx.qualifierFiles.Store(key, qualifierFileSet{files: files, found: found})
	return files, found
}

// qualifierFileSet is the memoized result of one qualifier resolution.
type qualifierFileSet struct {
	files map[string]struct{}
	found bool
}

func (idx *edgeIndex) computeImportFilesForQualifier(fromFile, qualifier string) (map[string]struct{}, bool) {
	imports, ok := idx.fileImports[fromFile]
	if !ok {
		return nil, false
	}
	found := false
	out := map[string]struct{}{}
	for _, imp := range sortedKeys(imports) {
		if lastImportSegment(imp) != qualifier {
			continue
		}
		found = true
		if strings.HasSuffix(fromFile, ".py") {
			// Python module paths: a relative ".cli" anchors on the
			// importing file's package (the generic resolver below reads
			// the leading dot as a JS "./" and never finds it); an
			// absolute "flask.cli" matches by trailing segments.
			hit := false
			for _, f := range idx.pyModuleFiles(fromFile, strings.Trim(imp, "\"' ;")) {
				if f != fromFile {
					out[f] = struct{}{}
					hit = true
				}
			}
			if hit {
				continue
			}
		}
		impNorm := strings.ToLower(strings.Trim(imp, "\"' ;"))
		impNorm = strings.TrimPrefix(impNorm, "./")
		// Java/Kotlin imports are dot-separated paths; the precise
		// resolvers below are slash-keyed.
		if !strings.Contains(impNorm, "/") && strings.Contains(impNorm, ".") {
			impNorm = strings.ReplaceAll(impNorm, ".", "/")
		}
		// Trim only known source extensions — a naive last-dot trim would
		// truncate module paths at their domain ("example.com/…" → "example").
		for _, ext := range []string{".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".java", ".rs"} {
			impNorm = strings.TrimSuffix(impNorm, ext)
		}
		for _, f := range idx.importPathToFiles[impNorm] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}
		// Slash-suffixes of impNorm, taken as substrings. The previous
		// Split+Join per suffix allocated a slice and a fresh string for
		// every segment of every import of every call site — the single
		// hottest allocation in call resolution.
		for i := 0; i <= len(impNorm); i++ {
			if i != 0 && impNorm[i-1] != '/' {
				continue
			}
			suffix := impNorm[i:]
			if suffix == "" || suffix == "." {
				continue
			}
			for _, f := range idx.dirFilesLower[suffix] {
				if f != fromFile {
					out[f] = struct{}{}
				}
			}
		}
		// Maven/Gradle layouts prefix source dirs ("src/main/java/org/..."),
		// so the import path is a SUFFIX of the dir or file, not equal to it.
		if len(out) == 0 {
			slashImp := "/" + impNorm // hoisted: the loops below are over every dir / import path
			for dir, files := range idx.dirFilesLower {
				if strings.HasSuffix(dir, slashImp) || dir == impNorm {
					for _, f := range files {
						if f != fromFile {
							out[f] = struct{}{}
						}
					}
				}
			}
			for pathKey, files := range idx.importPathToFiles {
				if strings.HasSuffix(pathKey, slashImp) {
					for _, f := range files {
						if f != fromFile {
							out[f] = struct{}{}
						}
					}
				}
			}
		}
	}
	return out, found
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func matchNameList(re *regexp.Regexp, text string) []string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	var out []string
	for _, name := range splitTrim(m[1], ',') {
		name = strings.TrimSpace(name)
		if i := strings.Index(name, "<"); i >= 0 {
			name = name[:i]
		}
		// Dotted qualifiers are KEPT ("ValueInstantiator.Base", "java.util.List"):
		// a Capitalized qualifier names a nested type and scopes resolution —
		// stripping it here made `extends ValueInstantiator.Base` resolve to
		// every type named Base in the repo. Consumers that only need the
		// simple name strip the prefix themselves.
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func splitTrim(s string, sep byte) []string {
	parts := strings.Split(s, string(sep))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripPythonBase(b string) string {
	b = strings.TrimSpace(b)
	// Strip default ABC bases that add noise (object, Generic[T], etc.)
	if strings.HasPrefix(b, "metaclass=") || b == "object" {
		return ""
	}
	if i := strings.Index(b, "["); i >= 0 {
		b = b[:i]
	}
	if i := strings.LastIndexByte(b, '.'); i >= 0 {
		b = b[i+1:]
	}
	return b
}

// csharpBaseListRe captures the base-list of a C# type declaration — the
// comma-separated names after the top-level `:`, stopping at a `where`
// constraint clause or the body brace. [ \t] (not \s) keeps it on the
// declaration line even if the signature carries a trailing newline.
var csharpBaseListRe = regexp.MustCompile(`\b(?:class|struct|record|interface)\s+[A-Za-z_]\w*(?:<[^>{}]+>)?[ \t]*:[ \t]*([A-Za-z_][\w.,<> \t]*?)(?:[ \t]+where\b|[ \t]*\{|$)`)

// csharpBaseNames extracts the simple base-type names from a C# declaration
// signature (generic arguments and namespace qualifiers stripped).
func csharpBaseNames(text string) []string {
	m := csharpBaseListRe.FindStringSubmatch(text)
	if len(m) != 2 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(stripAngleBrackets(m[1]), ",") {
		part = strings.TrimSpace(part)
		if i := strings.LastIndexByte(part, '.'); i >= 0 {
			part = part[i+1:]
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func goEmbeddedTypes(body string) []string {
	// Look at lines between the first `{` and the matching `}` of the struct
	// declaration. We consider each non-empty line consisting of a single
	// identifier (or *Identifier) to be an embedded type.
	open := strings.IndexByte(body, '{')
	if open < 0 {
		return nil
	}
	body = body[open+1:]
	close := strings.LastIndexByte(body, '}')
	if close >= 0 {
		body = body[:close]
	}
	var out []string
	// A lone type reference on its own line is an embed: `Foo`, `*Foo`, or a
	// cross-package `pkg.Foo` (the qualified form never matched before, so a
	// struct/interface embedding an imported type was invisible). The final
	// segment must be exported (uppercase) — cross-package embeds always are,
	// and it avoids matching a bare unexported field-less line. A method spec
	// (`Read() string`) has a `(` and does not match; a normal field
	// (`name string`) has two tokens and does not match.
	embeddedRe := regexp.MustCompile(`^\s*\*?((?:[A-Za-z_][A-Za-z0-9_]*\.)?[A-Z][A-Za-z0-9_]*)\s*(?://.*)?$`)
	for _, line := range strings.Split(body, "\n") {
		if m := embeddedRe.FindStringSubmatch(line); len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

// resolveTypeEdges returns 0 or more edges from `symbol` to a target type
// resolved by name. If no concrete symbol is found, no edge is emitted.
// resolveTypeEdges resolves a supertype reference from an extends/implements
// clause to indexed type symbols and emits edges. targetName may be dotted:
// a Capitalized qualifier names a nested type ("ValueInstantiator.Base") and
// SCOPES resolution to candidates declared inside that parent — without the
// scope, every same-named type in the repo matched (jackson: `extends
// ValueInstantiator.Base` fanned out to 30+ unrelated Base types, polluting
// every closure walk downstream). A lowercase qualifier is a package/module
// path ("java.util.List") and resolution falls back to the simple name.
// Candidates must be type declarations — the byName index also holds
// constructors, which share their class's name.
func resolveTypeEdges(idx *edgeIndex, symbol core.SymbolRecord, targetName string, edgeType core.EdgeType, confidence float64) []core.Edge {
	simple := targetName
	qualifier := ""
	if i := strings.LastIndexByte(simple, '.'); i >= 0 {
		qualifier = simple[:i]
		simple = simple[i+1:]
		if j := strings.LastIndexByte(qualifier, '.'); j >= 0 {
			qualifier = qualifier[j+1:] // innermost qualifier segment
		}
	}
	if simple == "" {
		return nil
	}
	nestedQualifier := qualifier != "" && isUpperIdent(qualifier)
	var cands []*core.SymbolRecord
	for _, target := range idx.byName[strings.ToLower(simple)] {
		if target.ID == symbol.ID {
			continue
		}
		// Identifiers are case-sensitive in every indexed language except
		// PHP class names; the lowercase index key must not conflate
		// case-distinct names (Go pairs ResponseWriter/responseWriter).
		if target.Name != simple && symbol.Language != "php" {
			continue
		}
		switch target.Kind {
		case core.KindClass, core.KindInterface, core.KindStruct, core.KindTrait, core.KindEnum, core.KindType:
		default:
			continue // constructors and methods share their class's name
		}
		if nestedQualifier && target.ParentSymbol != qualifier &&
			target.QualifiedName != qualifier+"."+simple &&
			// Leading dot keeps the boundary: NumberSerializers.Base must
			// not satisfy a qualifier of Serializers.
			!strings.HasSuffix(target.QualifiedName, "."+qualifier+"."+simple) {
			continue
		}
		cands = append(cands, target)
	}
	// Resolution tiers, most-specific first: (1) same file; (2) a file the
	// referencing file imports; (3) same directory; (4) the historical
	// cross-package fan-out. The import tier disambiguates a Capitalized
	// module-alias base (Python `class C(Mod.Base)` from `import pkg as Mod`,
	// stripped to `Base`) to the imported module's class rather than an
	// arbitrary same-named local decoy — the same misresolution the TS
	// receiver-chain fix addressed, here for supertype references. Import
	// resolution the graph cannot model still falls through to the fan-out,
	// so real cross-file hierarchies are never dropped.
	var local []*core.SymbolRecord
	for _, c := range cands {
		if c.FilePath == symbol.FilePath {
			local = append(local, c)
		}
	}
	if local == nil && len(cands) > 1 {
		scope := idx.importedFiles(symbol.FilePath)
		for _, c := range cands {
			if _, ok := scope[c.FilePath]; ok {
				local = append(local, c)
			}
		}
	}
	if local == nil {
		dir := dirOf(symbol.FilePath)
		for _, c := range cands {
			if dirOf(c.FilePath) == dir {
				local = append(local, c)
			}
		}
	}
	if local != nil {
		cands = local
	}
	var out []core.Edge
	for _, target := range cands {
		out = append(out, core.Edge{
			From:       symbol.ID,
			To:         target.ID,
			Type:       edgeType,
			Confidence: confidence,
			Source:     core.EvidenceSourceHeuristic,
			Reason:     core.ReasonTypeRef,
		})
	}
	return out
}

// isUpperIdent reports whether the identifier starts with an uppercase
// letter — the Java/TS convention separating a nested-type qualifier
// (Outer.Inner) from a package/module path segment (java.util).
func isUpperIdent(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return strings.TrimSpace(text[:i])
	}
	return strings.TrimSpace(text)
}

// javaPackageSuffix maps a directory to its Java package path — the suffix
// after the conventional source-root marker (src/<sourceSet>/java/, covering
// Maven and Gradle main/test/it/generated source sets). ok=false when the
// path carries no such marker; directory identity is then the only
// same-package signal (handled by the caller's same-dir pass).
func javaPackageSuffix(dir string) (string, bool) {
	d := strings.ReplaceAll(dir, "\\", "/")
	if i := javaSrcRootRe.FindStringIndex(d); i != nil {
		return d[i[1]:], true
	}
	return "", false
}

var javaSrcRootRe = regexp.MustCompile(`(^|/)src/[^/]+/java/`)
