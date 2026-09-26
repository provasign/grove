package graph

import (
	"fmt"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestReplaceStatusAndSearch(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::AuthService@sha",
			FilePath:      "auth.go",
			Kind:          core.KindStruct,
			Name:          "AuthService",
			QualifiedName: "AuthService",
		},
	}, 1)

	status := codeGraph.Status()
	if status.FilesIndexed != 1 || status.SymbolCount != 1 {
		t.Fatalf("unexpected status: %+v", status)
	}
	// At minimum 1 defines edge
	if status.EdgeCount < 1 {
		t.Fatalf("expected at least 1 edge, got %d", status.EdgeCount)
	}

	results := codeGraph.Search("auth", 10)
	if len(results) != 1 || results[0].Name != "AuthService" {
		t.Fatalf("unexpected search results: %+v", results)
	}
}

func TestSearchScopedFiltersBeforeLimit(t *testing.T) {
	codeGraph := New()
	var symbols []core.SymbolRecord
	for i := 0; i < 20; i++ {
		symbols = append(symbols, core.SymbolRecord{
			ID: fmt.Sprintf("outside/%02d.go::Target@sha", i), FilePath: fmt.Sprintf("outside/%02d.go", i),
			Kind: core.KindFunction, Name: "Target", QualifiedName: "Target",
		})
	}
	symbols = append(symbols,
		core.SymbolRecord{ID: "inside/a.go::Target@sha", FilePath: "inside/a.go", Kind: core.KindFunction, Name: "Target", QualifiedName: "Target"},
		core.SymbolRecord{ID: "inside/a_test.go::TargetTest@sha", FilePath: "inside/a_test.go", Kind: core.KindFunction, Name: "TargetTest", QualifiedName: "TargetTest"},
	)
	codeGraph.Replace(symbols, 22)

	got := codeGraph.SearchScoped("Target", 2, []string{"inside"}, []string{"*.go"})
	if len(got) != 2 || got[0].FilePath != "inside/a.go" || got[1].FilePath != "inside/a_test.go" {
		t.Fatalf("scoped search was filtered after the global limit: %+v", got)
	}
	if got := codeGraph.SearchScoped("Target", 2, []string{"inside/a.go"}, nil); len(got) != 1 || got[0].FilePath != "inside/a.go" {
		t.Fatalf("exact file scope failed: %+v", got)
	}
	if got := codeGraph.SearchScoped("Target", 2, nil, []string{"*_test.go"}); len(got) != 1 || got[0].FilePath != "inside/a_test.go" {
		t.Fatalf("basename glob scope failed: %+v", got)
	}
}

func TestContainsEdgeForMethod(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{
			ID:            "auth.go::Service@sha",
			FilePath:      "auth.go",
			Kind:          core.KindStruct,
			Name:          "Service",
			QualifiedName: "Service",
		},
		{
			ID:            "auth.go::Login@sha",
			FilePath:      "auth.go",
			Kind:          core.KindMethod,
			Name:          "Login",
			QualifiedName: "Login",
			ParentSymbol:  "Service",
		},
	}, 1)

	_, edges := codeGraph.Snapshot()
	hasContains := false
	for _, e := range edges {
		if e.Type == core.EdgeContains &&
			e.From == "auth.go::Service@sha" && e.To == "auth.go::Login@sha" {
			hasContains = true
		}
	}
	if !hasContains {
		t.Fatalf("expected contains edge Service→Login, edges: %+v", edges)
	}
}

func TestCallsEdgesDetectedInRawText(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{
			ID:            "main.go::Caller@sha",
			FilePath:      "main.go",
			Kind:          core.KindFunction,
			Name:          "Caller",
			QualifiedName: "Caller",
			RawText:       "func Caller() {\n\tCalled()\n}",
		},
		{
			ID:            "main.go::Called@sha",
			FilePath:      "main.go",
			Kind:          core.KindFunction,
			Name:          "Called",
			QualifiedName: "Called",
			RawText:       "func Called() {}",
		},
	}, 1)

	_, edges := codeGraph.Snapshot()
	hasCalls := false
	for _, e := range edges {
		if e.Type == core.EdgeCalls &&
			e.From == "main.go::Caller@sha" && e.To == "main.go::Called@sha" {
			hasCalls = true
		}
	}
	if !hasCalls {
		t.Fatalf("expected calls edge Caller→Called, edges: %+v", edges)
	}

	// Impact of Called should include Caller
	impact := codeGraph.Impact("Called", 3)
	found := false
	for _, s := range impact {
		if s.Name == "Caller" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected Caller in impact of Called, got: %+v", impact)
	}
}

func TestImpactTraversesOverrideEdges(t *testing.T) {
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{
		{ID: "contract", FilePath: "api.java", Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Runner.run", ParentSymbol: "Runner"},
		{ID: "impl", FilePath: "worker.java", Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Worker.run", ParentSymbol: "Worker"},
	}, []core.Edge{{From: "impl", To: "contract", Type: core.EdgeOverrides, Confidence: 0.95}}, 2)

	found := false
	for _, sym := range g.Impact("Runner.run", 1) {
		if sym.ID == "impl" {
			found = true
		}
	}
	if !found {
		t.Fatal("impact did not reach overriding implementation")
	}
}

func TestICRConfidenceDeclinesAsSeedSetBroadens(t *testing.T) {
	counts := []int{1, 2, 8, 20}
	for i := 1; i < len(counts); i++ {
		if confidenceForSeeds(counts[i-1]) <= confidenceForSeeds(counts[i]) {
			t.Fatalf("confidence did not decline: count %d => %.2f, count %d => %.2f",
				counts[i-1], confidenceForSeeds(counts[i-1]), counts[i], confidenceForSeeds(counts[i]))
		}
	}
	if confidenceForSeeds(0) > 0.2 {
		t.Fatalf("zero-seed confidence = %.2f", confidenceForSeeds(0))
	}
}

func TestImportEdgesAttachedToFileNodeNotPerSymbol(t *testing.T) {
	codeGraph := New()
	// Two symbols in same file, both with same imports
	codeGraph.Replace([]core.SymbolRecord{
		{
			ID:       "main.go::Foo@sha",
			FilePath: "main.go",
			Name:     "Foo",
			Kind:     core.KindFunction,
			Imports:  []string{"fmt", "os"},
		},
		{
			ID:       "main.go::Bar@sha",
			FilePath: "main.go",
			Name:     "Bar",
			Kind:     core.KindFunction,
			Imports:  []string{"fmt", "os"},
		},
	}, 1)

	_, edges := codeGraph.Snapshot()
	importCount := 0
	for _, e := range edges {
		if e.Type == core.EdgeImports {
			importCount++
			// Import edges must come FROM the file node, not from a symbol
			if e.From != "file:main.go" {
				t.Fatalf("import edge From=%q, want file:main.go", e.From)
			}
		}
	}
	// Exactly 2 unique imports (fmt and os), not 4 (2 symbols × 2 imports)
	if importCount != 2 {
		t.Fatalf("expected 2 import edges (deduplicated), got %d", importCount)
	}
}

func TestDepsExactPrefixNotSubstring(t *testing.T) {
	// Regression: "auth.go" must not match "main_auth.go"
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{ID: "auth.go::Login@sha", FilePath: "auth.go", Name: "Login", Kind: core.KindFunction},
		{ID: "main_auth.go::Helper@sha", FilePath: "main_auth.go", Name: "Helper", Kind: core.KindFunction},
	}, 2)

	deps := codeGraph.Deps("auth.go")
	for _, e := range deps {
		if e.From == "file:main_auth.go" || e.To == "main_auth.go::Helper@sha" {
			t.Fatalf("deps for auth.go incorrectly includes main_auth.go edge: %+v", e)
		}
	}
}

func TestDepsIncludesInboundFileImports(t *testing.T) {
	g := New()
	g.ReplaceWithEdges(nil, []core.Edge{
		{From: "file:consumer.go", To: "file:target.go", Type: core.EdgeImports},
		{From: "file:other.go", To: "import:target.go", Type: core.EdgeImports},
		{From: "file:wrong.go", To: "file:not-target.go", Type: core.EdgeImports},
	}, 0)
	deps := g.Deps("target.go")
	if len(deps) != 2 {
		t.Fatalf("inbound deps = %+v, want resolved and unresolved import targets", deps)
	}
}

func TestComputeICRAndDetectConflicts(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{ID: "auth.go::Login@sha", FilePath: "auth.go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login"},
		{ID: "billing.go::Charge@sha", FilePath: "billing.go", Kind: core.KindFunction, Name: "Charge", QualifiedName: "Charge"},
	}, 2)

	first := codeGraph.ComputeICR("Login")
	second := codeGraph.ComputeICR("Login")
	if first.IntentID == "" || len(first.Exclusive) == 0 || len(first.LockKeys) == 0 {
		t.Fatalf("unexpected ICR: %+v", first)
	}
	conflict := DetectConflicts(first, second)
	if !conflict.Conflicts || len(conflict.OverlapSymbols) == 0 {
		t.Fatalf("expected conflict, got %+v", conflict)
	}

	// Login and Charge should be in separate ICRs with no conflict
	loginICR := codeGraph.ComputeICR("Login")
	chargeICR := codeGraph.ComputeICR("Charge")
	noConflict := DetectConflicts(loginICR, chargeICR)
	if noConflict.Conflicts {
		t.Fatalf("expected no conflict between Login and Charge ICRs, got: %+v", noConflict)
	}
}

// TestComputeICRNoMatchReturnsEmptyLowConfidenceRegion guards against the
// fallback bug where a no-match intent seeded the region from the first 20
// symbols alphabetically and reported confidence 0.9 with real lock keys.
func TestComputeICRNoMatchReturnsEmptyLowConfidenceRegion(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{ID: "auth.go::Login@sha", FilePath: "auth.go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login"},
		{ID: "billing.go::Charge@sha", FilePath: "billing.go", Kind: core.KindFunction, Name: "Charge", QualifiedName: "Charge"},
	}, 2)

	icr := codeGraph.ComputeICR("implement quantum flux capacitor")
	if len(icr.Exclusive) != 0 || len(icr.ExclusiveFiles) != 0 || len(icr.LockKeys) != 0 {
		t.Fatalf("no-match ICR should be empty, got %+v", icr)
	}
	if icr.Confidence > 0.2 {
		t.Fatalf("no-match ICR confidence = %v, want <= 0.2", icr.Confidence)
	}

	// Two unrelated no-match intents must not conflict with each other.
	other := codeGraph.ComputeICR("rewrite the warp drive scheduler")
	conflict := DetectConflicts(icr, other)
	if conflict.Conflicts {
		t.Fatalf("two no-match ICRs must not conflict, got %+v", conflict)
	}
}

// click pr3471: scope=symbols glob=["**/types.py"] returned no symbols while
// text scope (rg --glob) matched src/click/types.py.
func TestSearchScopedDoubleStarGlob(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{ID: "src/click/types.py::Choice.shell_complete@sha", FilePath: "src/click/types.py", Kind: core.KindMethod, Name: "shell_complete", QualifiedName: "Choice.shell_complete"},
		{ID: "src/click/core.py::Option.shell_complete@sha", FilePath: "src/click/core.py", Kind: core.KindMethod, Name: "shell_complete", QualifiedName: "Option.shell_complete"},
		{ID: "types.py::shell_complete@sha", FilePath: "types.py", Kind: core.KindFunction, Name: "shell_complete", QualifiedName: "shell_complete"},
	}, 3)
	got := codeGraph.SearchScoped("shell_complete", 10, nil, []string{"**/types.py"})
	if len(got) != 2 {
		t.Fatalf("**/types.py must match types.py at any depth (including the root): %+v", got)
	}
	if got := codeGraph.SearchScoped("shell_complete", 10, nil, []string{"src/**/core.py"}); len(got) != 1 || got[0].FilePath != "src/click/core.py" {
		t.Fatalf("src/**/core.py: %+v", got)
	}
	if got := codeGraph.SearchScoped("shell_complete", 10, nil, []string{"src/**"}); len(got) != 2 {
		t.Fatalf("src/** must select everything below src: %+v", got)
	}
}
