package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpen_RootIsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Open expects to create .grove/ under root; MkdirAll on a path
	// whose parent is a regular file should fail.
	if _, err := Open(p); err == nil {
		t.Error("expected error opening under a file path")
	}
}

func TestStatusAndLocksRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.FilesIndexed != 0 || status.SymbolCount != 0 || status.EdgeCount != 0 {
		t.Errorf("non-zero on empty: %+v", status)
	}

	recs, err := st.AcquireLocks(ctx, "intent-A", []string{"k1", "k2"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records", len(recs))
	}

	// Re-acquire same locks for same intent: should succeed
	if _, err := st.AcquireLocks(ctx, "intent-A", []string{"k1"}, time.Minute); err != nil {
		t.Errorf("re-acquire same intent: %v", err)
	}

	// Conflict from a different intent: should error
	if _, err := st.AcquireLocks(ctx, "intent-B", []string{"k1"}, time.Minute); err == nil {
		t.Error("expected lock conflict")
	}

	n, err := st.ReleaseLocks(ctx, "intent-A")
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("released %d", n)
	}

	// Releasing again -> 0
	n, err = st.ReleaseLocks(ctx, "intent-A")
	if err != nil || n != 0 {
		t.Errorf("idempotent release got n=%d err=%v", n, err)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	dir := t.TempDir()
	st, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// Migrate again -> should not error (ALTERs idempotent).
	if err := st.Migrate(context.Background()); err != nil {
		t.Errorf("re-migrate: %v", err)
	}
}

func TestNilToEmpty(t *testing.T) {
	if out := nilToEmpty(nil); out == nil || len(out) != 0 {
		t.Errorf("got %v", out)
	}
}
