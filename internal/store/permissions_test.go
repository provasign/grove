package store

import (
	"os"
	"path/filepath"
	"testing"
)

// TestOpenCreatesPrivateStore: the database stores full source bodies, so
// .grove must be 0700 and grove.db 0600 — including when a pre-existing
// directory was created 0755 by an earlier Grove version.
func TestOpenCreatesPrivateStore(t *testing.T) {
	root := t.TempDir()
	groveDir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil { // legacy perms
		t.Fatal(err)
	}
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if fi, err := os.Stat(groveDir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf(".grove perms = %v (err %v), want 0700", fi.Mode().Perm(), err)
	}
	if fi, err := os.Stat(filepath.Join(groveDir, "grove.db")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("grove.db perms = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
}
