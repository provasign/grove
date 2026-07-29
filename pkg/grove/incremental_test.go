package grove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/store"
)

// writeIncrRepo writes a small multi-package Go repo (with go.mod, so the
// native analyzer participates) whose call graph crosses packages.
func writeIncrRepo(t *testing.T, root string) {
	t.Helper()
	files := map[string]string{
		"go.mod":          "module example.com/incr\n\ngo 1.22\n",
		"core/core.go":    "package core\n\nfunc Resolve() int { return 1 }\n\nfunc Helper() int { return Resolve() }\n",
		"app/app.go":      "package app\n\nimport \"example.com/incr/core\"\n\nfunc Run() int { return core.Resolve() }\n",
		"app/app_test.go": "package app\n\nimport \"testing\"\n\nfunc TestRun(t *testing.T) { Run() }\n",
	}
	for p, body := range files {
		abs := filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// storedEdgeDump returns the store's full edge set, canonically sorted.
func storedEdgeDump(t *testing.T, root string) []string {
	t.Helper()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	edges, err := st.AllEdges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(edges))
	for _, e := range edges {
		out = append(out, fmt.Sprintf("%s|%s|%s|%.6f|%s|%s", e.From, e.To, e.Type, e.Confidence, e.Source, e.Reason))
	}
	sort.Strings(out)
	return out
}

// TestIncrementalIndexMatchesFullRebuild is the engine-level equivalence
// gate: an edit indexed with GROVE_INCREMENTAL=1 (resident-graph baseline,
// native analyzers active) must persist the exact edge set a full rebuild
// persists.
func TestIncrementalIndexMatchesFullRebuild(t *testing.T) {
	ctx := context.Background()
	var queries string

	// rawQueries captures order-sensitive query surfaces verbatim — the
	// canonical install order must make full and incremental engines
	// byte-identical here, not just set-equal.
	rawQueries := func(t *testing.T, eng *Engine) string {
		t.Helper()
		var parts []string
		n, err := eng.Neighbors(ctx, "Resolve", "in", EdgeCalls, EdgeTests)
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, fmt.Sprintf("%+v", n))
		imp, err := eng.Impact(ctx, "Resolve", 3)
		if err == nil {
			parts = append(parts, fmt.Sprintf("%+v", imp))
		}
		tests, err := eng.Tests(ctx, "Run")
		if err == nil {
			parts = append(parts, fmt.Sprintf("%+v", tests))
		}
		ci, err := eng.ChangeImpact(ctx, "core.Resolve")
		if err == nil {
			parts = append(parts, fmt.Sprintf("%+v", ci))
		}
		parts = append(parts, fmt.Sprintf("%+v", eng.FileSymbols(ctx, "app/app.go")))
		return strings.Join(parts, "\n---\n")
	}

	run := func(t *testing.T, incremental bool) []string {
		root := t.TempDir()
		writeIncrRepo(t, root)
		if incremental {
			t.Setenv("GROVE_INCREMENTAL", "1")
		} else {
			t.Setenv("GROVE_INCREMENTAL", "")
		}
		eng, err := Open(ctx, Config{RepoRoot: root})
		if err != nil {
			t.Fatal(err)
		}
		defer eng.Close()
		if _, err := eng.Index(ctx, ""); err != nil {
			t.Fatal(err)
		}
		// The edit: a new cross-package caller of core.Resolve plus a rename
		// of Helper — exercises name-delta invalidation and the tests BFS.
		edited := "package app\n\nimport \"example.com/incr/core\"\n\nfunc Run() int { return core.Resolve() }\n\nfunc RunTwice() int { return core.Resolve() + core.Resolve() }\n"
		if err := os.WriteFile(filepath.Join(root, "app/app.go"), []byte(edited), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := eng.Index(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		if res.FilesUpdated != 1 {
			t.Fatalf("expected 1-file delta, got %#v", res)
		}
		if incremental {
			marked := false
			for _, d := range res.Native {
				if strings.Contains(d, "incremental") {
					marked = true
				}
			}
			if !marked {
				t.Fatalf("incremental path not taken; diagnostics: %v", res.Native)
			}
		}
		queries = rawQueries(t, eng)
		return storedEdgeDump(t, root)
	}

	var full, incr []string
	var fullQ, incrQ string
	t.Run("full", func(t *testing.T) { full = run(t, false); fullQ = queries })
	t.Run("incremental", func(t *testing.T) { incr = run(t, true); incrQ = queries })
	if fullQ == "" || fullQ != incrQ {
		t.Fatalf("RAW query outputs diverge between full and incremental engines:\nfull: %.400s\nincr: %.400s", fullQ, incrQ)
	}
	if len(full) == 0 || len(incr) == 0 {
		t.Fatal("one of the runs produced no edges")
	}
	if strings.Join(full, "\n") != strings.Join(incr, "\n") {
		diff := 0
		fs := map[string]bool{}
		for _, l := range full {
			fs[l] = true
		}
		for _, l := range incr {
			if !fs[l] {
				t.Logf("incremental-only: %s", l)
				diff++
			}
		}
		is := map[string]bool{}
		for _, l := range incr {
			is[l] = true
		}
		for _, l := range full {
			if !is[l] {
				t.Logf("full-only: %s", l)
				diff++
			}
		}
		t.Fatalf("stored edge sets diverge (%d differing rows; full=%d incr=%d)", diff, len(full), len(incr))
	}
}
