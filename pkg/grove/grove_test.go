package grove

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileSymbols(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	syms := eng.FileSymbols(ctx, "a.go")
	if len(syms) != 1 || syms[0].Name != "A" {
		t.Fatalf("FileSymbols(a.go) = %+v", syms)
	}
	if got := eng.FileSymbols(ctx, "missing.go"); len(got) != 0 {
		t.Fatalf("missing file returned %+v", got)
	}
}

func TestSnapshotGraph(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// b.go calls A so the snapshot must contain at least one calls edge.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	symbols, edges := eng.SnapshotGraph(ctx)
	if len(symbols) == 0 {
		t.Fatal("SnapshotGraph returned no symbols")
	}
	var calls int
	for _, e := range edges {
		if e.Type == EdgeCalls {
			calls++
		}
	}
	if calls == 0 {
		t.Fatalf("SnapshotGraph returned no calls edges (got %d edges total)", len(edges))
	}
	// The snapshot is a copy: mutating it must not corrupt the graph.
	symbols[0].Name = "mutated"
	again, _ := eng.SnapshotGraph(ctx)
	for _, s := range again {
		if s.Name == "mutated" {
			t.Fatal("SnapshotGraph aliases internal graph state")
		}
	}
}

// TestDiffAgainstFileContent covers the merge-driver flow: the merged bytes
// exist only in memory (git writes %A to the worktree after the driver
// exits), so the drift must be computable without touching disk.
func TestDiffAgainstFileContent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(`package main

func Login(user string) error { return nil }

func Logout() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	before := eng.SnapshotSymbols(ctx)

	merged := []byte(`package main

func Login(user, password string) error { return nil }

func Logout() {}

func Refresh() {}
`)
	diff, err := eng.DiffAgainstFileContent(before, "auth.go", merged)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff.Added) != 1 || diff.Added[0].Name != "Refresh" {
		t.Fatalf("added = %+v", diff.Added)
	}
	if len(diff.Changed) != 1 || diff.Changed[0].Before.Name != "Login" || !diff.Changed[0].SignatureChanged {
		t.Fatalf("changed = %+v", diff.Changed)
	}
	if len(diff.BreakingChanges) != 1 {
		t.Fatalf("breaking = %+v", diff.BreakingChanges)
	}
	// The on-disk file is untouched; nothing was indexed.
	if got := eng.DiffSince(ctx, before); !got.Empty() {
		t.Fatalf("preview must not mutate the live graph: %+v", got)
	}
}

// TestDiffSinceAcrossReindex exercises the stale-context-loop primitive end
// to end: snapshot, edit a file, reindex, diff. Only the edited symbol may
// appear in the diff even though the edit changes every symbol ID in the
// file via the content SHA.
func TestDiffSinceAcrossReindex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	authPath := filepath.Join(root, "auth.go")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(authPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`package main

func Login(user string) error { return nil }

func Logout() {}
`)

	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}

	before := eng.SnapshotSymbols(ctx)
	if len(before) == 0 {
		t.Fatal("empty snapshot after index")
	}

	// Change Login's signature; Logout shifts lines but is untouched.
	write(`package main

// Login authenticates a user with a password.
func Login(user, password string) error { return nil }

func Logout() {}
`)
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}

	diff := eng.DiffSince(ctx, before)
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("expected no adds/removes, got %+v", diff)
	}
	if len(diff.Changed) != 1 || diff.Changed[0].Before.Name != "Login" || !diff.Changed[0].SignatureChanged {
		t.Fatalf("changed = %+v, want exactly Login with signature change", diff.Changed)
	}
	if len(diff.BreakingChanges) != 1 || diff.BreakingChanges[0].Before.Name != "Login" {
		t.Fatalf("breaking = %+v, want Login", diff.BreakingChanges)
	}
}

func TestQuickStatusMatchesEngineStatus(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	// b.go calls A so the index persists symbols AND edges — QuickStatus must
	// report the identical triple the engine path reports.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package main\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	full, err := eng.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Close the engine first: QuickStatus must work with no engine running.
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	quick, err := QuickStatus(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if quick != full {
		t.Fatalf("QuickStatus = %+v, Engine.Status = %+v", quick, full)
	}
	if quick.SymbolCount == 0 || quick.EdgeCount == 0 {
		t.Fatalf("expected non-empty index, got %+v", quick)
	}
	if _, err := QuickStatus(ctx, ""); err == nil {
		t.Fatal("QuickStatus(\"\") should error")
	}
}
