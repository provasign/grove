package graph

import (
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

func TestTestsFor(t *testing.T) {
	codeGraph := New()
	codeGraph.Replace([]core.SymbolRecord{
		{ID: "main.go::Login@sha", FilePath: "main.go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login"},
		{ID: "main_test.go::TestLogin@sha", FilePath: "main_test.go", Kind: core.KindFunction, Name: "TestLogin", QualifiedName: "TestLogin"},
	}, 2)

	tests := codeGraph.TestsFor("login")
	if len(tests) != 1 || tests[0].Name != "TestLogin" {
		t.Fatalf("unexpected tests: %+v", tests)
	}

	impact := codeGraph.Impact("Login", 3)
	if len(impact) != 1 || impact[0].Name != "TestLogin" {
		t.Fatalf("unexpected impact: %+v", impact)
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
