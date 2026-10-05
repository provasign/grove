package grove

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// Close must not return while a background goroutine (TS worker warm-up,
// deferred splice check) can still use the store: a query in flight kept the
// database locked, and the next opener's write failed with SQLITE_BUSY on
// Windows (prism TestClient_AutoIndexRefreshesOldResolverVersion).
func TestCloseWaitsForBackgroundWork(t *testing.T) {
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc A() {}\nfunc B() { A() }\n"), 0o644); err != nil {
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
		db, err := sql.Open("sqlite", filepath.Join(root, ".grove", "grove.db"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.ExecContext(ctx, "UPDATE meta SET value = value WHERE key = 'resolver-version'")
		db.Close()
		if err != nil {
			t.Fatalf("iteration %d: store still in use after Close: %v", i, err)
		}
	}
}
