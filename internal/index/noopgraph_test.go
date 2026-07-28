package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// TestNoopSkipGraphReturnsNilWithSameCounts pins the SkipNoopGraph contract:
// a no-change index returns a nil graph, and its counts match a non-skip
// no-change run exactly.
func TestNoopSkipGraphReturnsNilWithSameCounts(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc Login() {}\n\nfunc Caller() { Login() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()

	if _, _, err := idx.Index(ctx, root); err != nil {
		t.Fatal(err)
	}

	gDefault, resDefault, err := idx.IndexWithOptions(ctx, root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if gDefault == nil {
		t.Fatal("default no-op index must still return a graph")
	}

	gSkip, resSkip, err := idx.IndexWithOptions(ctx, root, Options{SkipNoopGraph: true})
	if err != nil {
		t.Fatal(err)
	}
	if gSkip != nil {
		t.Fatal("SkipNoopGraph no-op index must return a nil graph")
	}
	if resSkip.SymbolCount != resDefault.SymbolCount || resSkip.EdgeCount != resDefault.EdgeCount ||
		resSkip.FilesSeen != resDefault.FilesSeen || resSkip.FilesSkipped != resDefault.FilesSkipped {
		t.Fatalf("counts diverge: skip=%#v default=%#v", resSkip, resDefault)
	}
	// The default graph's contents must match the counts (sanity that the
	// stored graph is what SkipNoopGraph callers would rehydrate lazily).
	syms, edges := gDefault.Snapshot()
	if len(syms) != resDefault.SymbolCount || len(edges) != resDefault.EdgeCount {
		t.Fatalf("default graph %d/%d != counts %d/%d", len(syms), len(edges), resDefault.SymbolCount, resDefault.EdgeCount)
	}
}

// TestNoopLegacyStoreWithoutEdgesStillRebuilds pins the legacy-DB guard: a
// store holding symbols but zero edges must fall through to a full rebuild
// even when nothing changed, exactly as before the restructure.
func TestNoopLegacyStoreWithoutEdgesStillRebuilds(t *testing.T) {
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

	if _, _, err := idx.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	// Simulate a legacy DB: symbols persisted, edges table emptied.
	if err := st.ReplaceEdges(ctx, nil); err != nil {
		t.Fatal(err)
	}

	g, res, err := idx.IndexWithOptions(ctx, root, Options{SkipNoopGraph: true})
	if err != nil {
		t.Fatal(err)
	}
	if g == nil {
		t.Fatal("legacy store (symbols, no edges) must rebuild, not skip")
	}
	if res.EdgeCount == 0 {
		t.Fatalf("rebuild produced no edges: %#v", res)
	}
}
