package store

import (
	"context"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

func TestUpsert_ReplaceEdges_Delete_AllSymbols_FTS(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	syms := []core.SymbolRecord{
		{
			ID: "a.go::Foo@sha1", FilePath: "a.go", BlobSHA: "sha1", Language: "go",
			Kind: core.KindFunction, Name: "Foo", QualifiedName: "Foo",
			Span: core.LineRange{Start: 1, End: 3}, Exports: true,
			Imports:        []string{"fmt"},
			Modifiers:      []string{"public"},
			TypeParameters: []string{"T"},
			Annotations:    []string{"@Deprecated"},
			CallSites:      []core.CallSite{{Callee: "fmt.Println", Line: 2}},
		},
		// Duplicate ID should be skipped, not crash.
		{ID: "a.go::Foo@sha1", FilePath: "a.go", BlobSHA: "sha1", Name: "Foo", Kind: core.KindFunction},
	}
	if err := st.UpsertFile(ctx, "a.go", "sha1", "go", syms); err != nil {
		t.Fatal(err)
	}
	// FileBlobSHA round-trip
	sha, ok, err := st.FileBlobSHA(ctx, "a.go")
	if err != nil || !ok || sha != "sha1" {
		t.Errorf("FileBlobSHA got sha=%q ok=%v err=%v", sha, ok, err)
	}
	if _, ok, _ := st.FileBlobSHA(ctx, "missing.go"); ok {
		t.Error("missing should return ok=false")
	}

	all, err := st.AllSymbols(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("dedup failed: got %d", len(all))
	}
	if all[0].Imports[0] != "fmt" || all[0].Modifiers[0] != "public" {
		t.Errorf("slices lost: %+v", all[0])
	}

	// ReplaceEdges
	edges := []core.Edge{
		{From: "a.go::Foo@sha1", To: "fmt", Type: core.EdgeImports, Confidence: 1.0},
	}
	if err := st.ReplaceEdges(ctx, edges); err != nil {
		t.Fatal(err)
	}
	// duplicate replace -> conflict update path
	if err := st.ReplaceEdges(ctx, edges); err != nil {
		t.Fatal(err)
	}

	// SearchFTS5 with hits
	hits, err := st.SearchFTS5(ctx, "Foo", 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = hits

	// Status non-zero
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.FilesIndexed == 0 {
		t.Error("expected indexed files")
	}

	// DeleteFilesNotIn — keep none -> all removed
	n, err := st.DeleteFilesNotIn(ctx, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted %d, want 1", n)
	}

	// DeleteFilesNotIn idempotent (nothing left)
	n, err = st.DeleteFilesNotIn(ctx, map[string]bool{})
	if err != nil || n != 0 {
		t.Errorf("idempotent delete got n=%d err=%v", n, err)
	}
}
