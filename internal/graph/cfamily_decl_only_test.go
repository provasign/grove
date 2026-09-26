package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Enum constants and type aliases (2026-09-26) are declaration-only: their
// signatures name types, but they add no uses-type edges, so pre-existing
// graphs keep their counts. A pre-existing typedef still does.
func TestCFamilyEnumConstantsAndAliasesAddNoUsesType(t *testing.T) {
	box := core.SymbolRecord{ID: "b.hpp::Box", FilePath: "b.hpp", Language: "cpp", Kind: core.KindClass, Name: "Box", QualifiedName: "Box"}
	alias := core.SymbolRecord{ID: "b.hpp::IntBox", FilePath: "b.hpp", Language: "cpp", Kind: core.KindType, Name: "IntBox", QualifiedName: "IntBox",
		Signature: "using IntBox = Box;", Modifiers: []string{"type-alias"}}
	constant := core.SymbolRecord{ID: "b.hpp::Kind::Box", FilePath: "b.hpp", Language: "cpp", Kind: core.KindConst, Name: "Other", QualifiedName: "Kind::Other",
		ParentSymbol: "Kind", Signature: "Other = sizeof(Box)", Modifiers: []string{"enum-constant"}}
	typedef := core.SymbolRecord{ID: "b.hpp::BoxT", FilePath: "b.hpp", Language: "cpp", Kind: core.KindType, Name: "BoxT", QualifiedName: "BoxT",
		Signature: "typedef Box BoxT;"}
	usesType := map[string]bool{}
	for _, edge := range BuildEdges([]core.SymbolRecord{box, alias, constant, typedef}) {
		if edge.Type == core.EdgeUsesType && edge.To == box.ID {
			usesType[edge.From] = true
		}
	}
	if usesType[alias.ID] || usesType[constant.ID] {
		t.Fatalf("declaration-only symbols emitted uses-type: %v", usesType)
	}
	if !usesType[typedef.ID] {
		t.Fatalf("pre-existing typedef lost its uses-type edge: %v", usesType)
	}
}
