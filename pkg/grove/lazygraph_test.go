package grove

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLazyRehydrationServesQueriesBeforeIndex pins the lazy-graph contract:
// reopening an engine over a previously-indexed store must serve queries
// without an Index call (rehydration happens on first access), and a no-op
// Index must not degrade query results.
func TestLazyRehydrationServesQueriesBeforeIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First engine: index and close.
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	wantSyms := fileSyms(t, ctx, eng, "a.go")
	if len(wantSyms) != 2 {
		t.Fatalf("seed index produced %d symbols, want 2", len(wantSyms))
	}
	_ = eng.Close()

	// Second engine: query BEFORE any Index call — lazy rehydration.
	eng2, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng2.Close()
	got := fileSyms(t, ctx, eng2, "a.go")
	if len(got) != len(wantSyms) {
		t.Fatalf("pre-Index query after reopen: got %d symbols, want %d", len(got), len(wantSyms))
	}

	// No-op Index must keep the graph serving identical results.
	res, err := eng2.Index(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 0 {
		t.Fatalf("expected no-op index, got %#v", res)
	}
	if res.SymbolCount != len(wantSyms) {
		t.Fatalf("no-op index counts: %d symbols, want %d", res.SymbolCount, len(wantSyms))
	}
	if got := fileSyms(t, ctx, eng2, "a.go"); len(got) != len(wantSyms) {
		t.Fatalf("post-noop query: got %d symbols, want %d", len(got), len(wantSyms))
	}

	// A real delta must refresh the resident graph.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n\nfunc B() { A() }\n\nfunc C() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = eng2.Index(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 1 {
		t.Fatalf("expected delta index, got %#v", res)
	}
	if got := fileSyms(t, ctx, eng2, "a.go"); len(got) != 3 {
		t.Fatalf("post-delta query: got %d symbols, want 3", len(got))
	}
}

// An interrupted edge write must not make every query rebuild a partial graph
// in memory. The existing no-change Index recovery persists the repaired edges.
func TestIncompleteIndexRequiresRecoveryBeforeQuery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := seed.store.ReplaceEdges(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}

	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	for range 2 {
		if _, err := eng.FileSymbols(ctx, "a.go"); err == nil || !strings.Contains(err.Error(), "run 'grove index'") {
			t.Fatalf("incomplete index query error = %v", err)
		}
	}
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := fileSyms(t, ctx, eng, "a.go"); len(got) != 2 {
		t.Fatalf("recovered index returned %d symbols, want 2", len(got))
	}
	status, err := eng.Status(ctx)
	if err != nil || status.EdgeCount == 0 {
		t.Fatalf("recovery did not persist edges: status=%+v err=%v", status, err)
	}
}

// TestFreshRepoQueryBeforeIndexIsEmptyNotFatal ensures the lazy path on a
// never-indexed store returns empty results (and caches the empty graph)
// rather than erroring.
func TestFreshRepoQueryBeforeIndexIsEmptyNotFatal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if got := fileSyms(t, ctx, eng, "a.go"); len(got) != 0 {
		t.Fatalf("fresh repo query returned %+v", got)
	}
	// Indexing afterwards must still populate the graph.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := fileSyms(t, ctx, eng, "a.go"); len(got) != 1 {
		t.Fatalf("post-index query returned %d symbols, want 1", len(got))
	}
}
