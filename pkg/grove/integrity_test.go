package grove

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/store"
)

func TestCheckIntegrity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := CheckIntegrity(ctx, root); err == nil {
		t.Fatal("CheckIntegrity on a repository with no index should error")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package main\n\nfunc A() {}\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := CheckIntegrity(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 0 || r.Stale() {
		t.Fatalf("fresh index: %+v", r)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(ctx, "resolver-version", "old"); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if r, err = CheckIntegrity(ctx, root); err != nil {
		t.Fatal(err)
	}
	if !r.Stale() || r.ResolverVersion != "old" {
		t.Fatalf("restamped index should be stale: %+v", r)
	}

	// Overwrite every page after the first: the header still opens, the
	// b-trees do not.
	db := filepath.Join(root, ".grove", "grove.db")
	data, err := os.ReadFile(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 8192 {
		t.Fatalf("index file is %d bytes, too small to corrupt", len(data))
	}
	for i := 4096; i < len(data); i++ {
		data[i] = 0xA5
	}
	if err := os.WriteFile(db, data, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = CheckIntegrity(ctx, root)
	if err == nil && len(r.Problems) == 0 {
		t.Fatal("corrupted index passed the integrity check")
	}
}
