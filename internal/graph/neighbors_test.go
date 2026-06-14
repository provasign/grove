package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// TestNeighbors_TypedCallEdges verifies the typed-neighbor accessor returns the
// exact calls neighbors per direction, with edge types preserved (the thing
// Impact flattens away).
func TestNeighbors_TypedCallEdges(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Caller@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "Caller", QualifiedName: "Caller", RawText: "func Caller() { Real() }",
			CallSites: []core.CallSite{{Callee: "Real", Line: 1}}},
		{ID: "a.go::Real@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "Real", QualifiedName: "Real", RawText: "func Real() {}"},
	}, 1)

	// Outgoing calls of Caller -> Real.
	out := g.Neighbors("Caller", "out", map[core.EdgeType]bool{core.EdgeCalls: true})
	if len(out) != 1 || out[0].Symbol.Name != "Real" || out[0].EdgeType != core.EdgeCalls || out[0].Direction != "out" {
		t.Fatalf("Caller out/calls = %+v, want one calls->Real", out)
	}
	// Incoming calls of Real -> Caller (who calls Real).
	in := g.Neighbors("Real", "in", map[core.EdgeType]bool{core.EdgeCalls: true})
	if len(in) != 1 || in[0].Symbol.Name != "Caller" || in[0].Direction != "in" {
		t.Fatalf("Real in/calls = %+v, want one caller Caller", in)
	}
	// Filtering by tests kind only must exclude the calls edge.
	none := g.Neighbors("Caller", "out", map[core.EdgeType]bool{core.EdgeTests: true})
	if len(none) != 0 {
		t.Fatalf("Caller out/tests = %+v, want none", none)
	}
	// Unknown seed returns nil.
	if got := g.Neighbors("Nonexistent", "both", nil); got != nil {
		t.Fatalf("unknown seed = %+v, want nil", got)
	}
}
