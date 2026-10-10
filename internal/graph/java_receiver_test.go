package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestJavaLocalTypes_GenericParametersWithSpacedArguments(t *testing.T) {
	// guava TableCollectors.mergeTables: strings.Fields tore
	// "Table<R, C, V> table" into four tokens and the parameter went
	// untyped (it used to be typed by accident, from another method's
	// local read as a class field).
	m := core.SymbolRecord{
		ID: "T.java::T.merge", FilePath: "T.java", Language: "java", Kind: core.KindMethod,
		Name: "merge", QualifiedName: "T.merge", ParentSymbol: "T",
		RawText: "private static <R, C, V> void merge(\n    Table<R, C, V> table,\n    @ParametricNullness R row,\n    Map<? extends K, ? extends V> map) {\n  table.get(row, row);\n}",
	}
	types := javaLocalTypes(newEdgeIndex([]core.SymbolRecord{m}), &m)
	if types["table"] != "Table" || types["map"] != "Map" {
		t.Fatalf("table = %q, map = %q; want Table, Map", types["table"], types["map"])
	}
	if got := javaArgTypes(newEdgeIndex([]core.SymbolRecord{m}), &m)["table"]; got == "" {
		t.Fatal("javaArgTypes: table untyped")
	}
}
