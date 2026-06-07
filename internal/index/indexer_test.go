package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

func TestIndexerPersistsAndSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

type AuthService struct{}

func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	idx := New(parser.NewEngine(), st)
	_, first, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesUpdated != 1 || first.FilesSkipped != 0 || first.SymbolCount != 2 {
		t.Fatalf("unexpected first index result: %#v", first)
	}

	_, second, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesUpdated != 0 || second.FilesSkipped != 1 || second.SymbolCount != 2 {
		t.Fatalf("unexpected second index result: %#v", second)
	}
}

func TestIndexerPrunesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	if err := os.WriteFile(filePath, []byte(`package main

func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	idx := New(parser.NewEngine(), st)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	_, result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesPruned != 1 || result.SymbolCount != 0 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
}

func TestNewWithNativeConfigAndSetNativeConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := native.Config{Enabled: false}
	idx := NewWithNativeConfig(parser.NewEngine(), st, cfg)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	cfg2 := native.Config{Enabled: false, Timeout: 1}
	idx.SetNativeConfig(cfg2)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}
