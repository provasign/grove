package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestStatus_ClosedDB(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = st.Close()
	if _, err := st.Status(context.Background()); err == nil {
		t.Error("expected error on closed db")
	}
}

func TestMigrate_Idempotent_v2(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-run: must succeed despite "duplicate column" errors
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
}
