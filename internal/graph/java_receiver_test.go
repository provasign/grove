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

func TestJavaChainReceiver_ExternalFieldTypeDispatches(t *testing.T) {
	// jackson-databind TestUnknownPropertyDeserialization:
	// `result.values.size()` where MapWithoutX.values is a java.util.Map.
	// The in-repo Map implementation is a dispatch target, as for a
	// single-hop `Map` receiver.
	holder := core.SymbolRecord{
		ID: "p/T.java::T.MapWithoutX", FilePath: "p/T.java", Language: "java", Kind: core.KindClass,
		Name: "MapWithoutX", QualifiedName: "T.MapWithoutX", ParentSymbol: "T", Signature: "static class MapWithoutX",
		RawText: "static class MapWithoutX {\n    public Map<String,Integer> values;\n}",
	}
	field := core.SymbolRecord{
		ID: "p/T.java::T.MapWithoutX.values", FilePath: "p/T.java", Language: "java", Kind: core.KindField,
		Name: "values", QualifiedName: "T.MapWithoutX.values", ParentSymbol: "MapWithoutX",
		RawText: "    public Map<String,Integer> values;",
	}
	caller := core.SymbolRecord{
		ID: "p/T.java::T.test", FilePath: "p/T.java", Language: "java", Kind: core.KindMethod,
		Name: "test", QualifiedName: "T.test", ParentSymbol: "T", Span: core.LineRange{Start: 10, End: 12},
		RawText:   "public void test(MapWithoutX result) {\n    result.values.size();\n}",
		CallSites: []core.CallSite{{Callee: "values.size", Line: 11}},
	}
	impl := core.SymbolRecord{
		ID: "p/M1.java::M1", FilePath: "p/M1.java", Language: "java", Kind: core.KindClass,
		Name: "M1", QualifiedName: "M1", Signature: "class M1 implements Map<Integer, Integer>",
	}
	size := core.SymbolRecord{
		ID: "p/M1.java::M1.size", FilePath: "p/M1.java", Language: "java", Kind: core.KindMethod,
		Name: "size", QualifiedName: "M1.size", ParentSymbol: "M1", Signature: "public int size()",
	}
	edges := BuildEdges([]core.SymbolRecord{holder, field, caller, impl, size})
	if !javaHasCall(edges, caller.ID, size.ID) {
		t.Fatal("result.values.size() must dispatch to the in-repo Map implementation")
	}
}
