// Tests that AST-extracted CallSites produce high-confidence calls edges
// (0.95) regardless of whether the textual regex would have matched.
package graph

import (
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

func TestBuildCalls_HighConfidenceFromCallSites(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "a.go::Caller@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		Imports:   []string{"b"},
		CallSites: []core.CallSite{{Callee: "Helper", Line: 3}},
		// Deliberately leave RawText blank — the regex fallback must NOT be
		// what produces the edge. Only the CallSite path can match.
		RawText: "",
	}
	callee := core.SymbolRecord{
		ID: "b.go::Helper@1", FilePath: "b.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Helper", QualifiedName: "Helper",
		RawText: "func Helper() {}",
	}

	edges := BuildEdges([]core.SymbolRecord{caller, callee})

	var found *core.Edge
	for i := range edges {
		e := &edges[i]
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("expected calls edge Caller→Helper; got %d edges", len(edges))
	}
	if found.Confidence < 0.9 {
		t.Errorf("confidence = %v, want ≥ 0.9 (AST-extracted)", found.Confidence)
	}
}

func TestBuildCalls_CallSitesScopedToImports(t *testing.T) {
	// Caller has a CallSite to Helper, but the file does not import Helper's
	// file → edge must NOT be produced.
	caller := core.SymbolRecord{
		ID: "a.go::Caller@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		Imports:   nil, // no imports
		CallSites: []core.CallSite{{Callee: "Helper", Line: 3}},
	}
	callee := core.SymbolRecord{
		ID: "b.go::Helper@1", FilePath: "b.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Helper", QualifiedName: "Helper",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, callee})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			t.Fatalf("unexpected cross-file edge without import: %#v", e)
		}
	}
}

func TestBuildCalls_StripsReceiverPrefix(t *testing.T) {
	// CallSite recorded as "user.save" — must still match the bare "save" symbol.
	caller := core.SymbolRecord{
		ID: "a.go::Caller@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		CallSites: []core.CallSite{{Callee: "user.save", Line: 5}},
	}
	callee := core.SymbolRecord{
		ID: "a.go::save@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "save", QualifiedName: "save",
		ParentSymbol: "User",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, callee})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			return
		}
	}
	t.Fatal("expected calls edge after stripping receiver prefix")
}
