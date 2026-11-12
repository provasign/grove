package grove

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

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

// ExternalRefs returns the call sites in this repository whose compiler-
// resolved target is one of keys, ordered by file and line. Only languages
// with a compiler pass that records them are covered (Go today), and only
// calls: a function passed as a value is not listed.
func (r *Reader) ExternalRefs(ctx context.Context, keys []string) ([]ExternalRef, error) {
	records, err := r.st.ExternalRefRecords(ctx, keys)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []ExternalRef
	for _, rec := range records {
		file, from := splitSymbolID(rec.From)
		for _, item := range strings.Split(rec.Lines, ",") {
			at := strings.LastIndexByte(item, '@')
			if at < 0 || !want[item[:at]] {
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
	return out, nil
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
	if sym.Language != "go" || (sym.Kind != "function" && sym.Kind != "method") {
		return "", false
	}
	return GoKey(root, sym.FilePath, sym.QualifiedName)
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
