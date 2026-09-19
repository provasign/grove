package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// --min-confidence drops low-confidence edges from both the graph and the
// store, persists the floor so a later run keeps it, and 0 clears it.
// --vacuum runs and reports.
func TestIndex_ConfidenceFloorAndVacuum(t *testing.T) {
	root := t.TempDir()
	// Two files: cross-file calls and type uses carry the lower
	// confidence tiers the floor is meant to drop.
	files := map[string]string{
		"types.go": "package main\n\ntype T struct{ n int }\n\nfunc (t T) M() int { return t.n }\n\nfunc helper(t T) int { return t.M() }\n",
		"main.go":  "package main\n\nfunc main() {\n\tvar t T\n\t_ = helper(t)\n\t_ = t.M()\n}\n",
		// An untyped Python receiver resolves by dispatch at 0.7.
		"app.py": "class A:\n    def m(self):\n        pass\n\n\nclass B:\n    def m(self):\n        pass\n\n\ndef f(x):\n    x.m()\n",
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()

	g, res, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_, all := g.Snapshot()
	minAll := 1.0
	for _, e := range all {
		if e.Confidence < minAll {
			minAll = e.Confidence
		}
	}
	if minAll >= 0.9 {
		t.Skipf("fixture produced no low-confidence edges (min %.2f); nothing to floor", minAll)
	}
	before := res.EdgeCount

	g, res, err = idx.IndexWithOptions(ctx, root, Options{Force: true, MinConfidence: 0.9, MinConfidenceSet: true, Vacuum: true})
	if err != nil {
		t.Fatal(err)
	}
	_, kept := g.Snapshot()
	for _, e := range kept {
		if e.Confidence < 0.9 {
			t.Fatalf("edge below floor survived in graph: %+v", e)
		}
	}
	if res.EdgeCount >= before {
		t.Fatalf("edge count %d not below %d after floor", res.EdgeCount, before)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.EdgeCount != res.EdgeCount {
		t.Fatalf("store has %d edges, graph %d", status.EdgeCount, res.EdgeCount)
	}
	var sawVacuum bool
	for _, n := range res.Native {
		if n == "vacuum: database compacted" {
			sawVacuum = true
		}
	}
	if !sawVacuum {
		t.Fatalf("vacuum not reported: %v", res.Native)
	}

	// The floor persists: a forced run without the option keeps it.
	_, res, err = idx.IndexWithOptions(ctx, root, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.EdgeCount >= before {
		t.Fatalf("persisted floor not applied: %d edges", res.EdgeCount)
	}
	// Setting 0 clears it.
	_, res, err = idx.IndexWithOptions(ctx, root, Options{Force: true, MinConfidenceSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.EdgeCount != before {
		t.Fatalf("clearing the floor gave %d edges, want %d", res.EdgeCount, before)
	}
	if _, _, err := idx.IndexWithOptions(ctx, root, Options{Force: true, MinConfidence: 1.5, MinConfidenceSet: true}); err == nil {
		t.Fatalf("out-of-range floor must be rejected")
	}
	_ = core.EdgeCalls
}
