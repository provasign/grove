package store

import (
	"context"
	"testing"
	"time"

	"github.com/provasign/grove/internal/core"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestUpsertFileAndAllSymbols(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	err := st.UpsertFile(ctx, "auth.go", "sha1", "go", []core.SymbolRecord{
		{ID: "auth.go::Login@sha1", FilePath: "auth.go", BlobSHA: "sha1", Language: "go",
			Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			Signature: "func Login()", Docstring: "Authenticate the user",
			Span: core.LineRange{Start: 1, End: 3}, Imports: []string{"fmt"}, Exports: true},
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := st.AllSymbols(ctx)
	if err != nil {
		t.Fatalf("all symbols: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Login" || got[0].Docstring != "Authenticate the user" {
		t.Fatalf("unexpected symbols: %+v", got)
	}

	// Delta detection
	sha, found, err := st.FileBlobSHA(ctx, "auth.go")
	if err != nil || !found || sha != "sha1" {
		t.Fatalf("FileBlobSHA = %s,%v,%v", sha, found, err)
	}
}

func TestUpsertReplacesExistingSymbolsForFile(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	first := []core.SymbolRecord{{ID: "a.go::Old@s", FilePath: "a.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction, Name: "Old", QualifiedName: "Old"}}
	if err := st.UpsertFile(ctx, "a.go", "s", "go", first); err != nil {
		t.Fatal(err)
	}
	second := []core.SymbolRecord{{ID: "a.go::New@s2", FilePath: "a.go", BlobSHA: "s2", Language: "go", Kind: core.KindFunction, Name: "New", QualifiedName: "New"}}
	if err := st.UpsertFile(ctx, "a.go", "s2", "go", second); err != nil {
		t.Fatal(err)
	}

	got, _ := st.AllSymbols(ctx)
	if len(got) != 1 || got[0].Name != "New" {
		t.Fatalf("expected only the new symbol, got: %+v", got)
	}
}

func TestSearchFTS5MatchesNameAndDocstring(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	must(t, st.UpsertFile(ctx, "auth.go", "s", "go", []core.SymbolRecord{
		{ID: "auth.go::Login@s", FilePath: "auth.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction,
			Name: "Login", QualifiedName: "Login", Docstring: "authenticate the calling user"},
		{ID: "auth.go::Logout@s", FilePath: "auth.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction,
			Name: "Logout", QualifiedName: "Logout", Docstring: "terminate the session"},
	}))

	byName, err := st.SearchFTS5(ctx, "Login", 10)
	if err != nil {
		t.Fatalf("fts5 by name: %v", err)
	}
	if len(byName) != 1 || byName[0].Name != "Login" {
		t.Fatalf("expected Login, got %+v", byName)
	}

	byDoc, err := st.SearchFTS5(ctx, "session", 10)
	if err != nil {
		t.Fatalf("fts5 by docstring: %v", err)
	}
	if len(byDoc) != 1 || byDoc[0].Name != "Logout" {
		t.Fatalf("expected Logout via docstring, got %+v", byDoc)
	}
}

func TestSearchFTS5SyncsOnUpdate(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	must(t, st.UpsertFile(ctx, "f.go", "s1", "go", []core.SymbolRecord{
		{ID: "f.go::OriginalName@s1", FilePath: "f.go", BlobSHA: "s1", Language: "go", Kind: core.KindFunction,
			Name: "OriginalName", QualifiedName: "OriginalName"},
	}))
	must(t, st.UpsertFile(ctx, "f.go", "s2", "go", []core.SymbolRecord{
		{ID: "f.go::RenamedName@s2", FilePath: "f.go", BlobSHA: "s2", Language: "go", Kind: core.KindFunction,
			Name: "RenamedName", QualifiedName: "RenamedName"},
	}))

	old, _ := st.SearchFTS5(ctx, "OriginalName", 10)
	if len(old) != 0 {
		t.Fatalf("expected stale FTS5 row to be deleted, got %+v", old)
	}
	got, err := st.SearchFTS5(ctx, "RenamedName", 10)
	if err != nil {
		t.Fatalf("fts5: %v", err)
	}
	if len(got) != 1 || got[0].Name != "RenamedName" {
		t.Fatalf("expected RenamedName, got %+v", got)
	}
}

func TestReplaceEdgesAndStatus(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	must(t, st.UpsertFile(ctx, "a.go", "s", "go", []core.SymbolRecord{
		{ID: "a.go::F@s", FilePath: "a.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction, Name: "F", QualifiedName: "F"},
	}))
	must(t, st.ReplaceEdges(ctx, []core.Edge{
		{From: "file:a.go", To: "a.go::F@s", Type: core.EdgeDefines, Confidence: 1.0},
	}))
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.FilesIndexed != 1 || status.SymbolCount != 1 || status.EdgeCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
}

func TestDeleteFilesNotInPrunesStaleEntries(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	must(t, st.UpsertFile(ctx, "a.go", "s", "go", []core.SymbolRecord{
		{ID: "a.go::F@s", FilePath: "a.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction, Name: "F", QualifiedName: "F"},
	}))
	must(t, st.UpsertFile(ctx, "b.go", "s", "go", []core.SymbolRecord{
		{ID: "b.go::G@s", FilePath: "b.go", BlobSHA: "s", Language: "go", Kind: core.KindFunction, Name: "G", QualifiedName: "G"},
	}))

	pruned, err := st.DeleteFilesNotIn(ctx, map[string]bool{"a.go": true})
	if err != nil {
		t.Fatal(err)
	}
	if pruned != 1 {
		t.Fatalf("expected to prune 1 file, got %d", pruned)
	}
	got, _ := st.AllSymbols(ctx)
	if len(got) != 1 || got[0].FilePath != "a.go" {
		t.Fatalf("expected only a.go to remain, got %+v", got)
	}
}

func TestAcquireLockConflictAndRelease(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if _, err := st.AcquireLocks(ctx, "intent-1", []string{"grove:lock:file:a.go"}, time.Minute); err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	if _, err := st.AcquireLocks(ctx, "intent-2", []string{"grove:lock:file:a.go"}, time.Minute); err == nil {
		t.Fatalf("expected conflict on second acquire")
	}
	// Same intent re-acquires fine.
	if _, err := st.AcquireLocks(ctx, "intent-1", []string{"grove:lock:file:a.go"}, time.Minute); err != nil {
		t.Fatalf("intent-1 re-acquire failed: %v", err)
	}
	count, err := st.ReleaseLocks(ctx, "intent-1")
	if err != nil || count != 1 {
		t.Fatalf("release: count=%d err=%v", count, err)
	}
	if _, err := st.AcquireLocks(ctx, "intent-2", []string{"grove:lock:file:a.go"}, time.Minute); err != nil {
		t.Fatalf("intent-2 acquire after release: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
