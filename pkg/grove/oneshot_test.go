package grove

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// A one-shot engine (a CLI command) has no resident graph to diff against;
// with changes it loads the stored baseline and still takes the incremental
// edge path, and the result equals a full rebuild.
func TestOneShotIndexUsesStoredBaseline(t *testing.T) {
	ctx := context.Background()
	write := func(root, rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	build := func(root string) {
		writeIncrRepo(t, root)
		for i := 0; i < 40; i++ {
			write(root, "filler/p"+strconv.Itoa(i)+"/p.go", "package p"+strconv.Itoa(i)+"\n\nfunc A() int { return B() }\n\nfunc B() int { return 1 }\n")
		}
	}
	edited := "package app\n\nimport \"example.com/incr/core\"\n\nfunc Run() int { return core.Resolve() }\n\nfunc RunTwice() int { return core.Resolve() + core.Resolve() }\n"
	index := func(root string) IndexResult {
		t.Helper()
		eng, err := Open(ctx, Config{RepoRoot: root, OneShot: true})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		res, err := eng.Index(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	inc := t.TempDir()
	build(inc)
	index(inc)
	write(inc, "app/app.go", edited)
	res := index(inc)
	if !strings.Contains(strings.Join(res.Native, "\n"), "edge construction: incremental") {
		t.Fatalf("one-shot re-index did not use the stored baseline: %v", res.Native)
	}
	full := t.TempDir()
	build(full)
	write(full, "app/app.go", edited)
	index(full)
	if a, b := storedEdgeDump(t, inc), storedEdgeDump(t, full); !slices.Equal(a, b) {
		t.Fatalf("one-shot incremental store differs from a full index (%d vs %d edges)", len(a), len(b))
	}
}
