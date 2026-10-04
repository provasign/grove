package index

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/store"
)

func TestNativeEdgeCarryPreservesSyntheticMemberEndpoints(t *testing.T) {
	iface := core.SymbolRecord{ID: "api/api.go::Writer@same", FilePath: "api/api.go", Language: "go"}
	caller := core.SymbolRecord{ID: "api/api.go::Stream@same", FilePath: "api/api.go", Language: "go"}
	synthetic := iface.ID + "#CloseNotify"
	edges := []core.Edge{
		{From: iface.ID, To: synthetic, Type: core.EdgeContains, Source: core.EvidenceSourceNative},
		{From: caller.ID, To: synthetic, Type: core.EdgeCalls, Source: core.EvidenceSourceNative},
	}
	partial := carriedPartialEdges(edges, []core.SymbolRecord{iface, caller}, nil, nil, map[string][]string{"go": {"impl"}})
	if len(partial) != len(edges) {
		t.Fatalf("partial carry dropped synthetic endpoint: %#v", partial)
	}
	skipped := carriedNativeEdges(edges, []core.SymbolRecord{iface, caller}, nil, nil, []string{"go"})
	if len(skipped) != len(edges) {
		t.Fatalf("skipped-language carry dropped synthetic endpoint: %#v", skipped)
	}
	changed := []core.SymbolRecord{
		{ID: "api/api.go::Writer@changed", FilePath: "api/api.go", Language: "go"},
		{ID: caller.ID, FilePath: caller.FilePath, Language: caller.Language},
	}
	if got := carriedPartialEdges(edges, changed, nil, nil, map[string][]string{"go": {"impl"}}); len(got) != 0 {
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
	indexed := map[string]store.FileMeta{"examples/app/__init__.py": {}, "src/pkg/__init__.py": {}}
	if got := carriedPartialEdges(edges, nil, files, indexed, map[string][]string{"python": {"src/other"}}); len(got) != 1 {
		t.Fatalf("partial carry dropped a symbol-less file edge: %#v", got)
	}
	if got := carriedNativeEdges(edges, nil, files, indexed, []string{"python"}); len(got) != 1 {
		t.Fatalf("skipped-language carry dropped a symbol-less file edge: %#v", got)
	}
	delete(files, "src/pkg/__init__.py")
	if got := carriedPartialEdges(edges, nil, files, indexed, map[string][]string{"python": {"src/other"}}); len(got) != 0 {
		t.Fatalf("edge into a deleted file survived: %#v", got)
	}
}

// Compiler edges into files the index never contained (dependencies under
// node_modules) carry forward as a full build keeps them; they used to be
// dropped as "endpoint no longer exists" on every carry.
func TestNativeEdgeCarryKeepsExternalEndpoints(t *testing.T) {
	edges := []core.Edge{
		{From: "file:src/platform/Tools.ts", To: "file:node_modules/redis/dist/index.d.ts", Type: core.EdgeImports, Source: core.EvidenceSourceNative},
		{From: "file:src/platform/Tools.ts", To: "src/gone.ts::Gone@x", Type: core.EdgeImports, Source: core.EvidenceSourceNative},
	}
	files := map[string]bool{"src/platform/Tools.ts": true}
	indexed := map[string]store.FileMeta{"src/platform/Tools.ts": {}, "src/gone.ts": {}}
	for name, got := range map[string][]core.Edge{
		"partial": carriedPartialEdges(edges, nil, files, indexed, map[string][]string{"typescript": {"src/other"}}),
		"skipped": carriedNativeEdges(edges, nil, files, indexed, []string{"typescript"}),
	} {
		if len(got) != 1 || got[0].To != edges[0].To {
			t.Fatalf("%s carry: want only the dependency import kept, got %#v", name, got)
		}
	}
}
