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
	partial := carriedPartialEdges(edges, []core.SymbolRecord{iface, caller}, nil, map[string][]string{"go": {"impl"}})
	if len(partial) != len(edges) {
		t.Fatalf("partial carry dropped synthetic endpoint: %#v", partial)
	}
	skipped := carriedNativeEdges(edges, []core.SymbolRecord{iface, caller}, nil, []string{"go"})
	if len(skipped) != len(edges) {
		t.Fatalf("skipped-language carry dropped synthetic endpoint: %#v", skipped)
	}
	changed := []core.SymbolRecord{
		{ID: "api/api.go::Writer@changed", FilePath: "api/api.go", Language: "go"},
		{ID: caller.ID, FilePath: caller.FilePath, Language: caller.Language},
	}
	if got := carriedPartialEdges(edges, changed, nil, map[string][]string{"go": {"impl"}}); len(got) != 0 {
		t.Fatalf("stale synthetic endpoint survived interface edit: %#v", got)
	}
}

// File-level import edges between files that declare no symbols (an
// __init__.py that only imports) carry forward like any other: they used to
// be dropped because the carry looked the file's language up by its symbols
// (werkzeug rename: 68 import edges from unchanged dirs lost).
func TestNativeEdgeCarryKeepsSymbolLessFiles(t *testing.T) {
	edges := []core.Edge{
		{From: "file:examples/app/__init__.py", To: "file:src/pkg/__init__.py", Type: core.EdgeImports, Source: core.EvidenceSourceNative},
	}
	files := map[string]bool{"examples/app/__init__.py": true, "src/pkg/__init__.py": true}
	if got := carriedPartialEdges(edges, nil, files, map[string][]string{"python": {"src/other"}}); len(got) != 1 {
		t.Fatalf("partial carry dropped a symbol-less file edge: %#v", got)
	}
	if got := carriedNativeEdges(edges, nil, files, []string{"python"}); len(got) != 1 {
		t.Fatalf("skipped-language carry dropped a symbol-less file edge: %#v", got)
	}
	delete(files, "src/pkg/__init__.py")
	if got := carriedPartialEdges(edges, nil, files, map[string][]string{"python": {"src/other"}}); len(got) != 0 {
		t.Fatalf("edge into a deleted file survived: %#v", got)
	}
}
