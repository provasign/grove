package index

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestRemoveIndexDeletesStoreAndCachesKeepsLock(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"grove.db", "grove.db-wal", "golist-cache.json", ".gitignore", "index.lock"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := RemoveIndex(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if got := len(removed); got != 3 || removed[0] != "golist-cache.json" || removed[1] != "grove.db" || removed[2] != "grove.db-wal" {
		t.Fatalf("removed = %v", removed)
	}
	for _, keep := range []string{".gitignore", "index.lock"} {
		if _, err := os.Stat(filepath.Join(dir, keep)); err != nil {
			t.Fatalf("%s was removed: %v", keep, err)
		}
	}
	if removed, err := RemoveIndex(context.Background(), root); err != nil || len(removed) != 0 {
		t.Fatalf("second remove = %v, %v", removed, err)
	}
}

func TestRemoveIndexWaitsForTheIndexLock(t *testing.T) {
	root := t.TempDir()
	release, err := acquireIndexLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if _, err := RemoveIndex(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RemoveIndex under a held lock = %v, want deadline exceeded", err)
	}
}
