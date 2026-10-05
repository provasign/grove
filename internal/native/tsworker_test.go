package native

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// The resident TypeScript worker must produce what the one-shot process
// produces, and narrow an edit's re-walk to the changed directory only when
// the changed file's declaration shape (inferred types included) is
// unchanged.
func TestJsTSResidentWorker(t *testing.T) {
	root := t.TempDir()
	linkTypeScriptForTest(t, root)
	t.Cleanup(func() { StopTSWorkers(root) })
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{"lib/lib.ts", "app/use.ts"}
	write("tsconfig.json", `{"compilerOptions":{"strict":true},"include":["**/*.ts"]}`)
	write("lib/lib.ts", "export function make() { return 1; }\n")
	write("app/use.ts", "import { make } from '../lib/lib';\nexport function run() { return make() + 1; }\n")
	for i := 0; i < 4; i++ {
		f := fmt.Sprintf("f%d/f.ts", i)
		write(f, "export function f() { return 0; }\n")
		files = append(files, f)
	}
	symbols := []core.SymbolRecord{
		{ID: "make", FilePath: "lib/lib.ts", Language: "typescript", Kind: core.KindFunction, Name: "make", Span: core.LineRange{Start: 1, End: 1}},
		{ID: "run", FilePath: "app/use.ts", Language: "typescript", Kind: core.KindFunction, Name: "run", Span: core.LineRange{Start: 2, End: 2}},
	}
	edgeSet := func(r Result) []string {
		var out []string
		for _, e := range r.Edges {
			out = append(out, e.From+"|"+string(e.Type)+"|"+e.To)
		}
		sort.Strings(out)
		return out
	}
	ctx := context.Background()
	full := Request{Root: root, Files: files, Symbols: symbols}
	oneShot := (jsTSAnalyzer{}).Analyze(ctx, full)
	full.Resident = true
	resident := (jsTSAnalyzer{}).Analyze(ctx, full)
	if !slices.Contains(resident.Diagnostics, "resident worker") {
		t.Fatalf("resident run did not use the worker: %v", resident.Diagnostics)
	}
	if a, b := edgeSet(oneShot), edgeSet(resident); !slices.Equal(a, b) {
		t.Fatalf("worker edges differ from one-shot:\n one-shot %v\n worker   %v", a, b)
	}
	assertNativeEdge(t, resident.Edges, "run", "make", core.EdgeCalls)

	edit := func(body string) Result {
		t.Helper()
		write("lib/lib.ts", body)
		return (jsTSAnalyzer{}).Analyze(ctx, Request{Root: root, Files: files, Symbols: symbols,
			ChangedFiles: []string{"lib/lib.ts"}, Resident: true})
	}
	narrowedNote := "changed files' declaration shape unchanged: importers not re-walked"
	same := edit("export function make() { return 2; }\n")
	if !slices.Contains(same.Diagnostics, narrowedNote) || !slices.Equal(same.Partial["typescript"], []string{"lib"}) {
		t.Fatalf("body edit with an unchanged inferred type was not narrowed to lib: partial=%v diags=%v", same.Partial, same.Diagnostics)
	}
	changed := edit("export function make() { return 'x'; }\n")
	if slices.Contains(changed.Diagnostics, narrowedNote) || !slices.Contains(changed.Partial["typescript"], "app") {
		t.Fatalf("an edit that changes make's inferred return type must re-walk its importer: partial=%v diags=%v", changed.Partial, changed.Diagnostics)
	}
	if !strings.Contains(strings.Join(changed.Diagnostics, "\n"), "resident worker") {
		t.Fatalf("worker was not used: %v", changed.Diagnostics)
	}
}

// A caller cancelled while its request is in flight (Close during the
// background warm-up) stops the worker; the in-flight goroutine must not
// touch the cleared fields. Run under -race.
func TestTSWorkerCancelDuringCall(t *testing.T) {
	root := t.TempDir()
	linkTypeScriptForTest(t, root)
	t.Cleanup(func() { StopTSWorkers(root) })
	if err := os.WriteFile(filepath.Join(root, "tsconfig.json"), []byte(`{"include":["*.ts"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.ts"), []byte("export const a = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		WarmTSWorker(ctx, root, []string{"a.ts"})
	}
}
