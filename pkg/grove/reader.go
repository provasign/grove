package grove

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/index"
	"github.com/provasign/grove/internal/store"
	"golang.org/x/mod/modfile"
)

// Reader answers from a repository's existing index without opening an
// engine: no graph is built, no file is indexed, nothing in the repository
// is created or changed. It is for consulting a second repository on behalf
// of the one being worked in, where an Engine per repository would cost
// seconds per call and would write into a tree the caller does not own.
//
// A Reader sees the index as it was last written. It does not know whether
// files changed since; FileBlobSHA lets the caller compare.
type Reader struct {
	root string
	st   *store.Store
}

// OpenReader opens the index under root read-only. It fails when root has
// never been indexed.
func OpenReader(root string) (*Reader, error) {
	st, err := store.OpenReadOnly(root)
	if err != nil {
		return nil, err
	}
	return &Reader{root: root, st: st}, nil
}

// Close releases the index.
func (r *Reader) Close() error { return r.st.Close() }

// Root is the repository this reader answers for.
func (r *Reader) Root() string { return r.root }

// SymbolsNamed returns the indexed symbols whose name or qualified name is
// one of names.
func (r *Reader) SymbolsNamed(ctx context.Context, names []string) ([]Symbol, error) {
	return r.st.SymbolsNamed(ctx, names)
}

// FileBlobSHA returns the content hash the index recorded for a
// repository-relative file, and whether the file is indexed.
func (r *Reader) FileBlobSHA(ctx context.Context, relPath string) (string, bool, error) {
	return r.st.FileBlobSHA(ctx, relPath)
}

// ExternalRef is one place in this repository that calls a function or
// method another module declares.
type ExternalRef struct {
	Key  string `json:"key"`  // "go:example.com/lib/pkg#Type.Method"
	File string `json:"file"` // repository-relative
	From string `json:"from"` // qualified name of the calling symbol
	Line int    `json:"line"`
	Col  int    `json:"col,omitempty"`
}

// ExternalRefs returns the places in this repository where a function
// names, by call or as a value, a function, method or type another module
// declares under one of keys, ordered by file and line. Only languages with
// a compiler pass that records them are covered (Go today). A reference
// outside any function (a package-level variable's type, a struct field's
// type) is not recorded.
func (r *Reader) ExternalRefs(ctx context.Context, keys []string) ([]ExternalRef, error) {
	records, err := r.st.ExternalRefRecords(ctx, keys)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	return parseExternalRefs(records, want), nil
}

// parseExternalRefs expands marker records into sites, keeping the keys in
// want (all of them when want is nil), ordered by file, line and column.
func parseExternalRefs(records []core.Edge, want map[string]bool) []ExternalRef {
	var out []ExternalRef
	for _, rec := range records {
		file, from := splitSymbolID(rec.From)
		for _, item := range strings.Split(rec.Lines, ",") {
			at := strings.LastIndexByte(item, '@')
			if at < 0 || (want != nil && !want[item[:at]]) {
				continue
			}
			lineStr, colStr, _ := strings.Cut(item[at+1:], ":")
			line, _ := strconv.Atoi(lineStr)
			col, _ := strconv.Atoi(colStr)
			out = append(out, ExternalRef{Key: item[:at], File: file, From: from, Line: line, Col: col})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		return out[i].Col < out[j].Col
	})
	return out
}

// ExternalImpl is a method in this repository that implements a method of
// an interface another module declares.
type ExternalImpl struct {
	Interface string `json:"interface"` // "import/path.Interface"
	File      string `json:"file"`
	Method    string `json:"method"` // Type.Method
	Line      int    `json:"line"`
}

// ExternalImplementers returns this repository's methods that fulfil the
// contract of any of ifaces ("import/path.Interface"). Go only: Go satisfies
// interfaces implicitly, and the compiler pass records, per method, the
// interfaces of directly imported packages it fulfils. A type that
// implements an interface it never imports is not found.
func (r *Reader) ExternalImplementers(ctx context.Context, ifaces []string) ([]ExternalImpl, error) {
	rows, err := r.st.ExternalContracts(ctx, ifaces)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, name := range ifaces {
		want[name] = true
	}
	var out []ExternalImpl
	for _, c := range rows {
		for _, name := range strings.Split(c.Interfaces, ",") {
			if want[name] {
				out = append(out, ExternalImpl{Interface: name, File: c.File, Method: c.Method, Line: c.Line})
			}
		}
	}
	return out, nil
}

// AllExternalRefs returns every external reference the index records, in
// the order of ExternalRefs. It is what a link bundle is built from.
func (r *Reader) AllExternalRefs(ctx context.Context) ([]ExternalRef, error) {
	records, err := r.st.AllExternalRefRecords(ctx)
	if err != nil {
		return nil, err
	}
	return parseExternalRefs(records, nil), nil
}

// AllSymbols returns every indexed symbol.
func (r *Reader) AllSymbols(ctx context.Context) ([]Symbol, error) {
	return r.st.AllSymbols(ctx)
}

// splitSymbolID takes "path::Qualified.Name@blob#N" apart.
func splitSymbolID(id string) (file, qualified string) {
	file, rest, ok := strings.Cut(id, "::")
	if !ok {
		return id, ""
	}
	if at := strings.LastIndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	}
	return file, rest
}

// GoSymbolKey is the key other repositories record when they call sym, a Go
// function or method declared under root: the import path of its package
// and its qualified name. The import path comes from the nearest go.mod at
// or above the file. ok is false for other languages and kinds, and when no
// go.mod names the module.
func GoSymbolKey(root string, sym Symbol) (key string, ok bool) {
	if sym.Language != "go" {
		return "", false
	}
	switch sym.Kind {
	case "function", "method", "struct", "interface", "type", "class", "variable", "const", "field":
		return GoKey(root, sym.FilePath, sym.QualifiedName)
	}
	return "", false
}

// GoKey is GoSymbolKey for a Go function or method given by its
// repository-relative file and qualified name.
func GoKey(root, filePath, qualifiedName string) (key string, ok bool) {
	if qualifiedName == "" || !strings.HasSuffix(filePath, ".go") {
		return "", false
	}
	dir := path.Dir(filepath.ToSlash(filePath))
	for d := dir; ; d = path.Dir(d) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d), "go.mod"))
		if err == nil {
			mod := modfile.ModulePath(data)
			if mod == "" {
				return "", false
			}
			pkg := mod
			if rel := strings.TrimPrefix(strings.TrimPrefix(dir, d), "/"); d == "." && dir != "." {
				pkg = mod + "/" + dir
			} else if d != "." && rel != "" {
				pkg = mod + "/" + rel
			}
			return "go:" + pkg + "#" + qualifiedName, true
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}

// IndexState says which tree state an index describes.
type IndexState struct {
	Head     string // commit checked out when the index was last written; "" if unknown
	Ref      string // branch at that time; "" when detached or unknown
	Finished string // RFC 3339 time the last run finished; "" while one is running
	Linked   string // LinkedDigest of the linked repositories the last run was given
	Resolver string // resolver version that wrote the edges
	// NativeComplete names the compiler passes that have completed on this
	// index ("go", "java", "js-ts", ...). A language whose pass has not run
	// was resolved by name only, and holds no compiler-resolved external
	// references: their absence then means "not analysed", not "none".
	NativeComplete []string
}

// LinkedDigest identifies a set of linked repositories, for comparing with
// IndexState.Linked.
func LinkedDigest(roots []string) string { return index.LinkedDigest(roots) }

// ResolverVersion is the resolver version this build writes. An index
// stamped with another version was resolved by different rules; one from
// before external references existed holds none.
const ResolverVersion = graph.ResolverVersion

// State reports the tree state the index was built from. An index written
// before these were recorded has an empty Head: unknown, not unchanged.
func (r *Reader) State(ctx context.Context) IndexState {
	var st IndexState
	st.Head, _, _ = r.st.GetMeta(ctx, core.MetaIndexHead)
	st.Ref, _, _ = r.st.GetMeta(ctx, core.MetaIndexRef)
	st.Finished, _, _ = r.st.GetMeta(ctx, core.MetaIndexFinished)
	st.Linked, _, _ = r.st.GetMeta(ctx, core.MetaIndexLinked)
	st.Resolver, _, _ = r.st.GetMeta(ctx, "resolver-version")
	if raw, ok, _ := r.st.GetMeta(ctx, core.MetaNativeComplete); ok && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st.NativeComplete)
	}
	return st
}

// FileStat returns the size and modification time (Unix nanoseconds) the
// index recorded for a repository-relative file, and whether it is indexed.
func (r *Reader) FileStat(ctx context.Context, relPath string) (size, mtime int64, ok bool) {
	return r.st.FileStat(ctx, relPath)
}

// SymbolKey is the key other repositories record when they name sym,
// declared under root. ok is false for a language or kind with no key.
func SymbolKey(root string, sym Symbol) (key string, ok bool) {
	return KeyFor(root, sym.Language, string(sym.Kind), sym.FilePath, sym.QualifiedName)
}

// KeyFor is SymbolKey for a symbol given by its fields, for one that is no
// longer in the index. An empty language is inferred from the file
// extension and an empty kind is taken to be a function.
func KeyFor(root, language, kind, filePath, qualifiedName string) (key string, ok bool) {
	if language == "" {
		switch strings.ToLower(path.Ext(filePath)) {
		case ".java":
			language = "java"
		case ".go":
			language = "go"
		case ".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs":
			language = "typescript"
		}
	}
	if kind == "" {
		kind = "function"
	}
	switch language {
	case "go":
		switch kind {
		case "function", "method", "struct", "interface", "type", "class", "variable", "const", "field":
			return GoKey(root, filePath, qualifiedName)
		}
	case "typescript", "tsx", "javascript":
		switch kind {
		case "function", "method", "class", "interface", "type":
			return npmKey(root, filePath, qualifiedName, kind)
		}
	case "python":
		if keys := PythonKeys(filePath, qualifiedName); len(keys) > 0 {
			return keys[0], true
		}
	case "java":
		switch kind {
		case "function", "method", "constructor", "class", "interface", "enum", "annotation", "type":
			return JavaKey(root, filePath, qualifiedName)
		}
	}
	return "", false
}

var javaPackageDecl = regexp.MustCompile(`(?m)^\s*package\s+([A-Za-z_][\w.]*)\s*;`)

// JavaKey is the key for a Java type or member: its file's package and its
// qualified name within that package, "jvm:<package>#<Outer.Inner>.<member>".
// javac reports the same two parts for a call's target, whichever jar or
// source tree it was resolved from. Overloads share a key. ok is false for
// a file with no package statement: the unnamed package cannot be imported.
func JavaKey(root, filePath, qualifiedName string) (key string, ok bool) {
	if qualifiedName == "" || !strings.HasSuffix(filePath, ".java") {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(filePath)))
	if err != nil {
		return "", false
	}
	m := javaPackageDecl.FindSubmatch(data)
	if m == nil {
		return "", false
	}
	return "jvm:" + string(m[1]) + "#" + qualifiedName, true
}

// KeysFor returns every key other repositories may have recorded for a
// symbol. Most languages have one. Python has several: a consumer records
// the module it imported the name from, which may be the defining module or
// any package above it that re-exports the name.
func KeysFor(root, language, kind, filePath, qualifiedName string) []string {
	if language == "" && strings.HasSuffix(strings.ToLower(filePath), ".py") {
		language = "python"
	}
	if language == "python" {
		switch kind {
		case "", "function", "method", "class":
			return PythonKeys(filePath, qualifiedName)
		}
		return nil
	}
	if k, ok := KeyFor(root, language, kind, filePath, qualifiedName); ok {
		return []string{k}
	}
	return nil
}

// PythonKeys lists the keys a Python function, class or method may be
// recorded under: "py:<module>#<name>" for its own module and for each
// package above it, since `from pkg import Name` reaches a name that
// pkg/__init__.py re-exports from pkg/sub.py. A leading conventional source
// root (src/, lib/, python/) is not part of the module path. The match is
// by module and name, not resolved: a different object bound to the same
// name in a parent package would match too.
func PythonKeys(filePath, qualifiedName string) []string {
	p := strings.TrimSuffix(filepath.ToSlash(filePath), ".py")
	if p == filepath.ToSlash(filePath) || qualifiedName == "" {
		return nil
	}
	parts := strings.Split(p, "/")
	if len(parts) > 1 && (parts[0] == "src" || parts[0] == "lib" || parts[0] == "python") {
		parts = parts[1:]
	}
	if parts[len(parts)-1] == "__init__" {
		parts = parts[:len(parts)-1]
	}
	var keys []string
	for n := len(parts); n >= 1; n-- {
		keys = append(keys, "py:"+strings.Join(parts[:n], ".")+"#"+qualifiedName)
	}
	return keys
}

// NPMKey is the key for a JavaScript or TypeScript declaration: the name of
// the nearest package.json at or above the file, and the symbol's name. A
// member keeps its owner ("Box.open"), which is how the TypeScript pass
// records a call it resolved to a class or interface member; a top-level
// function, class, interface or type is its own name. Overloads share a key.
func NPMKey(root, filePath, qualifiedName string) (key string, ok bool) {
	return npmKey(root, filePath, qualifiedName, "")
}

func npmKey(root, filePath, qualifiedName, kind string) (key string, ok bool) {
	segs := strings.Split(qualifiedName, ".")
	name := segs[len(segs)-1]
	if kind == "method" && len(segs) >= 2 {
		name = segs[len(segs)-2] + "." + name
	}
	if name == "" {
		return "", false
	}
	for d := path.Dir(filepath.ToSlash(filePath)); ; d = path.Dir(d) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(d), "package.json"))
		if err == nil {
			var pkg struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(data, &pkg) == nil && pkg.Name != "" {
				return "npm:" + pkg.Name + "#" + name, true
			}
		}
		if d == "." || d == "/" {
			return "", false
		}
	}
}
