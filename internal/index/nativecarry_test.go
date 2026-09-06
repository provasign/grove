package index

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestNativeEdgeCarryPreservesSyntheticMemberEndpoints(t *testing.T) {
	iface := core.SymbolRecord{ID: "api/api.go::Writer@same", FilePath: "api/api.go", Language: "go"}
	caller := core.SymbolRecord{ID: "api/api.go::Stream@same", FilePath: "api/api.go", Language: "go"}
	synthetic := iface.ID + "#CloseNotify"
	edges := []core.Edge{
		{From: iface.ID, To: synthetic, Type: core.EdgeContains, Source: core.EvidenceSourceNative},
		{From: caller.ID, To: synthetic, Type: core.EdgeCalls, Source: core.EvidenceSourceNative},
	}
	partial := carriedPartialEdges(edges, []core.SymbolRecord{iface, caller}, map[string][]string{"go": {"impl"}})
	if len(partial) != len(edges) {
		t.Fatalf("partial carry dropped synthetic endpoint: %#v", partial)
	}
	skipped := carriedNativeEdges(edges, []core.SymbolRecord{iface, caller}, []string{"go"})
	if len(skipped) != len(edges) {
		t.Fatalf("skipped-language carry dropped synthetic endpoint: %#v", skipped)
	}
	changed := []core.SymbolRecord{
		{ID: "api/api.go::Writer@changed", FilePath: "api/api.go", Language: "go"},
		{ID: caller.ID, FilePath: caller.FilePath, Language: caller.Language},
	}
	if got := carriedPartialEdges(edges, changed, map[string][]string{"go": {"impl"}}); len(got) != 0 {
		t.Fatalf("stale synthetic endpoint survived interface edit: %#v", got)
	}
}
