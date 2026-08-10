package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A .grove directory that exists but is not writable used to surface as
// sqlite's bare "unable to open database file (14)" — no path, no cause.
// (WAL mode needs to create -wal/-shm sidecars next to the db.) The error
// must now name the path and the fix.
func TestOpenReadOnlyGroveDirIsActionable(t *testing.T) {
	root := t.TempDir()
	groveDir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(groveDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(groveDir, 0o755) })

	st, err := Open(root)
	if err == nil {
		// Close before skipping: Windows cannot delete the TempDir while
		// grove.db is open, and a failed cleanup marks the skip as a FAIL.
		_ = st.Close()
		t.Skip("read-only dir is still writable (root, or Windows attribute semantics)")
	}
	for _, want := range []string{groveDir, "not writable", "chmod"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

// A read-only db file inside a writable dir gets the file-specific hint.
func TestOpenReadOnlyDBFileIsActionable(t *testing.T) {
	root := t.TempDir()
	groveDir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(groveDir, "grove.db")
	if err := os.WriteFile(dbPath, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o644) })

	st, err := Open(root)
	if err == nil {
		_ = st.Close()
		t.Skip("read-only file is still writable (root, or Windows attribute semantics)")
	}
	for _, want := range []string{dbPath, "read-only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}
