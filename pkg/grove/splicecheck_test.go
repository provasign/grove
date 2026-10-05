package grove

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// The post-splice store check is deferred off Index's critical path in a
// resident engine. It must still run before the next Index touches the store,
// heal a store that diverged from the in-memory graph, and say so.
func TestDeferredSpliceCheckHealsBeforeNextIndex(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	writeIncrRepo(t, root)
	// Unrelated packages keep a one-file edit under the delta path's
	// affected-fraction limit, so the run takes the splice write.
	for i := 0; i < 40; i++ {
		dir := filepath.Join(root, "filler", "p"+strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := "package p" + strconv.Itoa(i) + "\n\nfunc A() int { return B() }\n\nfunc B() int { return 1 }\n"
		if err := os.WriteFile(filepath.Join(dir, "p.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eng, err := Open(ctx, Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	eng.holdPendingCheck = true
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	edit := func(body string) IndexResult {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "app", "app.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := eng.Index(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := edit("package app\n\nimport \"example.com/incr/core\"\n\nfunc Run() int { return core.Resolve() }\n\nfunc RunTwice() int { return core.Resolve() + core.Resolve() }\n")
	if !strings.Contains(strings.Join(res.Native, "\n"), "(store check deferred)") {
		t.Fatalf("edit did not take the splice path: %v", res.Native)
	}

	// Corrupt the store the way a write-set enumeration bug would, while the
	// check is still pending (hold indexMu so the background run waits).
	eng.indexMu.Lock()
	if eng.pendingCheck == nil {
		eng.indexMu.Unlock()
		t.Fatal("splice check was not deferred")
	}
	db, err := sql.Open("sqlite", filepath.Join(root, ".grove", "grove.db"))
	if err == nil {
		_, err = db.ExecContext(ctx, `DELETE FROM edges WHERE rowid IN (SELECT rowid FROM edges LIMIT 1)`)
		db.Close()
	}
	if err != nil {
		eng.indexMu.Unlock()
		t.Fatal(err)
	}
	eng.indexMu.Unlock()

	res = edit("package app\n\nimport \"example.com/incr/core\"\n\nfunc Run() int { return core.Resolve() }\n\nfunc RunTwice() int { return core.Resolve() * 2 }\n")
	if !strings.Contains(strings.Join(res.Native, "\n"), "previous run: edge splice mismatch") {
		t.Fatalf("diverged store was not healed and reported before the next index: %v", res.Native)
	}
	eng.indexMu.Lock()
	eng.runPendingCheck(ctx)
	note := eng.spliceNote
	eng.indexMu.Unlock()
	if note != "" {
		t.Fatalf("store still diverged after heal: %s", note)
	}
}
