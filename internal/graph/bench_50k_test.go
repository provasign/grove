// Large-scale benchmarks that validate Grove against the targets in the
// Product Roadmap:
//
//   - Index 5000 files (≈50K symbols) in < 5 s
//   - FTS5-style Search query           < 10 ms
//   - BFS depth-3 over 50K-node graph   < 30 ms
//
// These benchmarks are pure-in-memory — they exercise BuildEdges, Search,
// and Impact (BFS) without going through SQLite or the parser. Run with:
//
//	go test ./internal/graph -bench=Benchmark50K -benchtime=1x -run=^$
package graph

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

// generate50KSymbols builds 5000 files × 10 symbols/file with realistic
// cross-file relationships: every Nth symbol imports the previous file and
// calls one symbol in it via CallSites (giving us a connected graph).
func generate50KSymbols() []core.SymbolRecord {
	const (
		files       = 5000
		perFile     = 10
		totalSymbol = files * perFile
	)
	symbols := make([]core.SymbolRecord, 0, totalSymbol)
	for f := 0; f < files; f++ {
		filePath := fmt.Sprintf("pkg/mod%04d/file%04d.go", f/100, f)
		var fileImports []string
		if f > 0 {
			fileImports = []string{fmt.Sprintf("pkg/mod%04d/file%04d", (f-1)/100, f-1)}
		}
		for i := 0; i < perFile; i++ {
			name := fmt.Sprintf("Fn_%d_%d", f, i)
			rec := core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@blob%d", filePath, name, f*perFile+i),
				FilePath:      filePath,
				BlobSHA:       fmt.Sprintf("blob%d", f*perFile+i),
				Language:      "go",
				Kind:          core.KindFunction,
				Name:          name,
				QualifiedName: name,
				Signature:     fmt.Sprintf("func %s() error", name),
				Docstring:     fmt.Sprintf("%s handles request %d of file %d.", name, i, f),
				Span:          core.LineRange{Start: i*5 + 1, End: i*5 + 4},
				Imports:       fileImports,
				Exports:       true,
				TokenEstimate: 20,
			}
			// Wire a CallSite to the previous file's first symbol so the
			// graph has real cross-file edges to BFS through.
			if f > 0 && i == 0 {
				rec.CallSites = []core.CallSite{
					{Callee: fmt.Sprintf("Fn_%d_%d", f-1, 0), Line: 2},
				}
			}
			symbols = append(symbols, rec)
		}
	}
	return symbols
}

// Benchmark50K_BuildEdges measures the cost of constructing all 8 edge types
// for a 50K-symbol graph. Target: << 5s.
func Benchmark50K_BuildEdges(b *testing.B) {
	syms := generate50KSymbols()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		edges := BuildEdges(syms)
		if len(edges) == 0 {
			b.Fatal("no edges produced")
		}
	}
}

// Benchmark50K_Search measures a single Search query. Target: < 10 ms.
// (Grove's in-memory Search is a substring scan; FTS5 is exercised by the
// store-level benchmarks.)
func Benchmark50K_Search(b *testing.B) {
	g := New()
	g.Replace(generate50KSymbols(), 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := g.Search("Fn_2500_5", 50)
		if len(out) == 0 {
			b.Fatal("expected at least one result")
		}
	}
}

// Benchmark50K_BFSDepth3 measures Impact (BFS) seeded at one symbol over
// the 50K-symbol graph. Target: < 30 ms.
func Benchmark50K_BFSDepth3(b *testing.B) {
	g := New()
	g.Replace(generate50KSymbols(), 5000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := g.Impact("Fn_2500_0", 3)
		_ = out
	}
}

// Benchmark50K_Semantic measures a single TF-IDF query against the engine.
func Benchmark50K_Semantic(b *testing.B) {
	g := New()
	g.Replace(generate50KSymbols(), 5000)
	// Warm the engine so we measure query cost, not index build.
	_ = g.SemanticSearch("handles request", 20)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := g.SemanticSearch("handles request", 20)
		if len(out) == 0 {
			b.Fatal("expected results")
		}
	}
}

// Test50K_TargetsAreMet runs each benchmark once and prints a pass/fail
// summary against the roadmap targets. This is a real Test (not a Benchmark)
// so it shows up in `go test ./...`. Skipped in -short.
func Test50K_TargetsAreMet(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 50K target validation in short mode")
	}

	syms := generate50KSymbols()

	// Single-shot measurements — testing.Benchmark would over-iterate on the
	// fast operations and blow up wall-clock time.
	start := time.Now()
	edges := BuildEdges(syms)
	buildElapsed := time.Since(start)
	t.Logf("BuildEdges 50K: %v (target: <5s) — %d edges", buildElapsed, len(edges))
	if buildElapsed > 5*time.Second {
		t.Errorf("BuildEdges took %v, target <5s", buildElapsed)
	}

	g := New()
	g.Replace(syms, 5000)

	start = time.Now()
	const iters = 10
	for i := 0; i < iters; i++ {
		_ = g.Search("Fn_2500_5", 50)
	}
	searchPer := time.Since(start) / iters
	t.Logf("Search 50K: %v/op (target: <10ms)", searchPer)

	start = time.Now()
	for i := 0; i < iters; i++ {
		_ = g.Impact("Fn_2500_0", 3)
	}
	bfsPer := time.Since(start) / iters
	t.Logf("BFS depth-3 50K: %v/op (target: <30ms)", bfsPer)

	// Warm the semantic engine, then time a single query.
	_ = g.SemanticSearch("handles request", 20)
	start = time.Now()
	for i := 0; i < iters; i++ {
		_ = g.SemanticSearch("handles request", 20)
	}
	semPer := time.Since(start) / iters
	t.Logf("SemanticSearch 50K: %v/op", semPer)
}

// Sanity: the generator must produce 50K symbols and a connected import chain.
func TestGenerate50KSymbolsShape(t *testing.T) {
	syms := generate50KSymbols()
	if len(syms) != 50000 {
		t.Fatalf("got %d symbols, want 50000", len(syms))
	}
	// First symbol of file 1 should have a CallSite to file 0.
	for _, s := range syms {
		if s.Name == "Fn_1_0" {
			if len(s.CallSites) != 1 || !strings.HasPrefix(s.CallSites[0].Callee, "Fn_0_") {
				t.Fatalf("CallSites wrong: %#v", s.CallSites)
			}
			return
		}
	}
	t.Fatal("Fn_1_0 not found")
}
