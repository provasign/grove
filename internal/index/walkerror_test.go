package index

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// TestIndexerNonexistentRootFails pins the guard against the worst prune
// accident: pointing an existing index at a root that does not exist must be
// an error, not a walk that sees zero files and empties the store.
func TestIndexerNonexistentRootFails(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Login() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()
	if _, res, err := idx.Index(ctx, root); err != nil || res.SymbolCount == 0 {
		t.Fatalf("seed index: %v %#v", err, res)
	}

	if _, _, err := idx.Index(ctx, filepath.Join(root, "does-not-exist")); err == nil {
		t.Fatal("indexing a nonexistent root must fail")
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.SymbolCount == 0 {
		t.Fatal("failed walk emptied the store")
	}
}

// TestIndexerShieldsUnreadableSubtreeFromPruning: files under a directory
// that cannot be read this run are absent from the walk for a transient
// reason — their stored records must survive the prune pass.
func TestIndexerShieldsUnreadableSubtreeFromPruning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod 000 does not make a directory unreadable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("permission bits do not bind root")
	}
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Top() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "sub.go"), []byte("package sub\n\nfunc Nested() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()
	if _, res, err := idx.Index(ctx, root); err != nil || res.FilesUpdated != 2 {
		t.Fatalf("seed index: %v %#v", err, res)
	}

	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(sub, 0o755) }()

	_, res, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("unreadable subtree should be reported in result.Errors")
	}
	if res.FilesPruned != 0 {
		t.Fatalf("unreadable subtree was pruned: %#v", res)
	}
	files, err := st.FilesNotIn(ctx, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	stored := map[string]bool{}
	for _, f := range files {
		stored[f] = true
	}
	if !stored["sub/sub.go"] {
		t.Fatalf("sub/sub.go missing from store after unreadable walk: %v", files)
	}
}

// TestIndexerDropsSymbolsOfUnreadableChangedFile (stack audit 2026-10-20 #1):
// a file whose new content cannot be read (here: over the size cap) must
// not keep serving the symbols of its last indexed version as current. Same
// contract as a parse failure: the stored facts are pruned, and the error is
// reported in result.Errors on every run until the file indexes again.
func TestIndexerDropsSymbolsOfUnreadableChangedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nfunc Target() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()
	if _, res, err := idx.Index(ctx, root); err != nil || res.FilesUpdated != 1 {
		t.Fatalf("seed index: %v %#v", err, res)
	}
	big := make([]byte, parser.MaxFileSizeBytes+1)
	copy(big, "package a\n\n// no Target here\n")
	for i := len("package a\n\n// no Target here\n"); i < len(big); i++ {
		big[i] = '\n'
	}
	if err := os.WriteFile(path, big, 0o644); err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		_, res, err := idx.Index(ctx, root)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Errors) == 0 || !strings.Contains(strings.Join(res.Errors, "\n"), "a.go") {
			t.Fatalf("run %d: oversize file not reported: %#v", run, res.Errors)
		}
		syms, err := st.SymbolsForFiles(ctx, []string{"a.go"})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range syms {
			if s.Name == "Target" {
				t.Fatalf("run %d: removed Target still indexed from the last readable version", run)
			}
		}
	}
	// Readable again: indexed again.
	if err := os.WriteFile(path, []byte("package a\n\nfunc Target() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, res, err := idx.Index(ctx, root); err != nil || len(res.Errors) != 0 || res.FilesUpdated != 1 {
		t.Fatalf("recovery index: %v %#v", err, res)
	}
}
