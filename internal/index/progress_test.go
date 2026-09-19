package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// A finished index leaves its phase, timestamps and native verdict in the
// store so `grove status` (which never sees the index process's stdout)
// can report which tier built the database and that the run completed.
func TestIndex_RecordsRunStateInStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nfunc helper() {}\n\nfunc main() { helper() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	ctx := context.Background()
	if _, res, err := idx.Index(ctx, root); err != nil || res.SymbolCount == 0 {
		t.Fatalf("index: %v %#v", err, res)
	}
	status, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status.Phase != "complete" {
		t.Fatalf("phase = %q, want complete", status.Phase)
	}
	if status.IndexStarted == "" || status.IndexFinished == "" || status.IndexFinished < status.IndexStarted {
		t.Fatalf("timestamps = %q .. %q", status.IndexStarted, status.IndexFinished)
	}
	if status.Progress != "" {
		t.Fatalf("progress should clear on completion, got %q", status.Progress)
	}
	if len(status.Native) == 0 {
		t.Fatalf("native verdict missing from status")
	}
	// A no-change re-index keeps the verdict of the run that built the edges.
	if _, _, err := idx.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	again, err := st.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(again.Native, "|") != strings.Join(status.Native, "|") {
		t.Fatalf("no-change re-index changed the native verdict: %v -> %v", status.Native, again.Native)
	}
	if again.Phase != "complete" {
		t.Fatalf("phase after no-op = %q", again.Phase)
	}
}

// The call resolver reports a monotonic symbols-resolved counter that ends
// at the symbol total — the moving number a supervisor watches during the
// otherwise silent edge-construction phase.
func TestBuildEdgesWithProgress_ReportsCallResolution(t *testing.T) {
	var syms []core.SymbolRecord
	for i := 0; i < 1500; i++ {
		name := "f" + string(rune('a'+i%26)) + strings.Repeat("x", i%7)
		syms = append(syms, core.SymbolRecord{
			ID: "a.go::" + name + "@sha", FilePath: "a.go", BlobSHA: "sha", Language: "go",
			Kind: core.KindFunction, Name: name, QualifiedName: name, Signature: "func " + name + "()",
			CallSites: []core.CallSite{{Callee: "fb", Line: 1}},
		})
	}
	var steps []string
	last, final := -1, 0
	graph.BuildEdgesWithProgress(syms, func(step string, done, total int) {
		steps = append(steps, step)
		if step == "calls" {
			if done < last {
				t.Fatalf("progress went backwards: %d after %d", done, last)
			}
			last = done
			final = total
		}
	})
	if last != len(syms) || final != len(syms) {
		t.Fatalf("calls progress ended at %d/%d, want %d/%d", last, final, len(syms), len(syms))
	}
	if !strings.Contains(strings.Join(steps, ","), "calls") || !strings.Contains(strings.Join(steps, ","), "uses-type") {
		t.Fatalf("steps = %v", steps)
	}
}
