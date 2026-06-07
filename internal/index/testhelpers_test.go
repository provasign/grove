package index

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

func newIdx(t *testing.T) (*Indexer, func()) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	eng := parser.NewEngine()
	idx := New(eng, st)
	return idx, func() { _ = st.Close() }
}
