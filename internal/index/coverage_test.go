//go:build !windows

package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

// helper to build an Indexer with isolated store
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

func TestIndex_SkipsUnsupportedAndDirs(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "vendor"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "dist"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".grove"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "a.go"), []byte("package x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "image.png"), []byte("\x89PNG\r\n"), 0o644) // binary — unsupported
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc M(){}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "readme.md"), []byte("# Project\n"), 0o644) // plaintext — indexed
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 2 { // main.go + readme.md
		t.Errorf("want 2 updated, got %d", res.FilesUpdated)
	}
}

func TestIndex_WalkErr(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	sub := filepath.Join(root, "denied")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.go"), []byte("package x"), 0o644)
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Skip("filesystem did not error on chmod 000")
	}
}

func TestIndex_PrunesDeleted(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	_ = os.WriteFile(p, []byte("package x\nfunc A(){}\n"), 0o644)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesPruned != 1 {
		t.Errorf("want pruned=1, got %d", res.FilesPruned)
	}
}
