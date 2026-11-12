package grove

import (
	"context"

	"github.com/provasign/grove/internal/store"
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
