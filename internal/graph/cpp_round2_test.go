package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestCPPUsesTypePinsNamespaces(t *testing.T) {
	netHandle := core.SymbolRecord{ID: "ns.cpp::net::Handle", FilePath: "ns.cpp", Language: "cpp", Kind: core.KindClass, Name: "Handle", QualifiedName: "net::Handle"}
	fsHandle := core.SymbolRecord{ID: "ns.cpp::fs::Handle", FilePath: "ns.cpp", Language: "cpp", Kind: core.KindClass, Name: "Handle", QualifiedName: "fs::Handle"}
	netOpen := core.SymbolRecord{ID: "ns.cpp::net::open", FilePath: "ns.cpp", Language: "cpp", Kind: core.KindFunction, Name: "open", QualifiedName: "net::open", Signature: "net::Handle open()"}
	fsOpen := core.SymbolRecord{ID: "ns.cpp::fs::open", FilePath: "ns.cpp", Language: "cpp", Kind: core.KindFunction, Name: "open", QualifiedName: "fs::open", Signature: "fs::Handle open()"}
	edges := BuildEdges([]core.SymbolRecord{netHandle, fsHandle, netOpen, fsOpen})
	for _, check := range []struct{ from, want, reject string }{{netOpen.ID, netHandle.ID, fsHandle.ID}, {fsOpen.ID, fsHandle.ID, netHandle.ID}} {
		var wanted, rejected bool
		for _, edge := range edges {
			if edge.Type != core.EdgeUsesType || edge.From != check.from {
				continue
			}
			wanted = wanted || edge.To == check.want
			rejected = rejected || edge.To == check.reject
		}
		if !wanted || rejected {
			t.Fatalf("namespace type-use from %s: wanted=%v rejected=%v edges=%+v", check.from, wanted, rejected, edges)
		}
	}
}

func TestUsesTypeRejectsSelfAndWrongKinds(t *testing.T) {
	field := core.SymbolRecord{ID: "x.ts::leaf", FilePath: "x.ts", Language: "typescript", Kind: core.KindField, Name: "leaf", QualifiedName: "Box.leaf", Signature: "leaf: Leaf"}
	method := core.SymbolRecord{ID: "x.ts::method", FilePath: "x.ts", Language: "typescript", Kind: core.KindMethod, Name: "Leaf", QualifiedName: "Box.Leaf"}
	for _, edge := range BuildEdges([]core.SymbolRecord{field, method}) {
		if edge.Type == core.EdgeUsesType && edge.From == field.ID {
			t.Fatalf("uses-type must not target a field/method homonym: %+v", edge)
		}
	}
}
