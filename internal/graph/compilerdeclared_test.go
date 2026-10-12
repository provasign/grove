package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A compiler-declared symbol takes references only from the compiler that
// declared it: name guesses into it are dropped, structural edges and native
// references stay (typeorm, 2026-10-12: getConnectionManager().get(...) and
// Map.get callers guessed onto the container's anonymous get).
func TestDropGuessesIntoCompilerDeclared(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "anon.get", Modifiers: []string{"compiler-declared", "anonymous"}},
		{ID: "Reader.get"},
	}
	edges := []core.Edge{
		{From: "caller", To: "anon.get", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit},
		{From: "caller", To: "anon.get", Type: core.EdgeUsesType, Source: core.EvidenceSourceHeuristic},
		{From: "caller", To: "anon.get", Type: core.EdgeCalls, Source: core.EvidenceSourceNative},
		{From: "anon", To: "anon.get", Type: core.EdgeContains, Source: core.EvidenceSourceASTKit},
		{From: "anon.get", To: "Iface.get", Type: core.EdgeOverrides, Source: core.EvidenceSourceHeuristic},
		{From: "caller", To: "Reader.get", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit},
	}
	got := dropGuessesIntoCompilerDeclared(symbols, edges)
	want := []string{"caller>anon.get:calls:native", "anon>anon.get:contains:astkit", "anon.get>Iface.get:overrides:heuristic", "caller>Reader.get:calls:astkit"}
	if len(got) != len(want) {
		t.Fatalf("got %d edges, want %d: %+v", len(got), len(want), got)
	}
	for i, e := range got {
		if s := e.From + ">" + e.To + ":" + string(e.Type) + ":" + string(e.Source); s != want[i] {
			t.Errorf("edge %d = %s, want %s", i, s, want[i])
		}
	}
}
