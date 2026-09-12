package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestPythonDynamicMemberChainFallsBackToBoundedMethods(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "cache.py::clear", FilePath: "cache.py", Language: "python", Kind: core.KindMethod,
		Name: "clear", QualifiedName: "Cache.clear", ParentSymbol: "Cache", Span: core.LineRange{Start: 1},
		RawText:   "def clear(self):\n    connection = connections[db]\n    return connection.ops.quote_name(self.table)\n",
		CallSites: []core.CallSite{{Callee: "connection.ops.quote_name", Line: 3, Argc: 1, Args: []string{""}}},
	}
	base := core.SymbolRecord{ID: "base/operations.py::BaseOps.quote_name", FilePath: "base/operations.py", Language: "python", Kind: core.KindMethod, Name: "quote_name", QualifiedName: "BaseOps.quote_name", ParentSymbol: "BaseOps"}
	postgres := core.SymbolRecord{ID: "postgres/operations.py::Ops.quote_name", FilePath: "postgres/operations.py", Language: "python", Kind: core.KindMethod, Name: "quote_name", QualifiedName: "Ops.quote_name", ParentSymbol: "Ops"}
	edges := BuildEdges([]core.SymbolRecord{caller, base, postgres})
	if !javaHasCall(edges, caller.ID, base.ID) || !javaHasCall(edges, caller.ID, postgres.ID) {
		t.Fatal("untyped multi-hop member call must retain the bounded dynamic method family")
	}
}

func TestPythonCallableAttributeAliasFallsBackToBoundedMethods(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "cache.py::get_many", FilePath: "cache.py", Language: "python", Kind: core.KindMethod,
		Name: "get_many", QualifiedName: "Cache.get_many", ParentSymbol: "Cache", Span: core.LineRange{Start: 1},
		RawText:   "def get_many(self):\n    quote_name = connection.ops.quote_name\n    return quote_name(self.table)\n",
		CallSites: []core.CallSite{{Callee: "quote_name", Line: 3, Argc: 1, Args: []string{""}}},
	}
	method := core.SymbolRecord{ID: "base.py::BaseOps.quote_name", FilePath: "base.py", Language: "python", Kind: core.KindMethod, Name: "quote_name", QualifiedName: "BaseOps.quote_name", ParentSymbol: "BaseOps"}
	unrelated := core.SymbolRecord{ID: "util.py::quote_name", FilePath: "util.py", Language: "python", Kind: core.KindFunction, Name: "quote_name", QualifiedName: "quote_name"}
	edges := BuildEdges([]core.SymbolRecord{caller, method, unrelated})
	if !javaHasCall(edges, caller.ID, method.ID) {
		t.Fatal("call through a dotted attribute alias must resolve its dynamic method family")
	}
	if javaHasCall(edges, caller.ID, unrelated.ID) {
		t.Fatal("callable attribute alias must not bind by its local alias name")
	}
}

func TestPythonDynamicMemberChainUsesOneAdditionalImportHop(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "introspection.py::describe", FilePath: "pkg/introspection.py", Language: "python", Kind: core.KindMethod,
		Name: "describe", QualifiedName: "Introspection.describe", ParentSymbol: "Introspection", Span: core.LineRange{Start: 1},
		Imports:   []string{"pkg.backend"},
		RawText:   "def describe(self):\n    return self.connection.ops.quote_name(self.table)\n",
		CallSites: []core.CallSite{{Callee: "self.connection.ops.quote_name", Line: 2, Argc: 1, Args: []string{""}}},
	}
	backend := core.SymbolRecord{ID: "backend.py::<top-level>", FilePath: "pkg/backend.py", Language: "python", Kind: core.KindFunction, Name: "<top-level>", QualifiedName: "<top-level>", Imports: []string{"pkg.operations"}}
	method := core.SymbolRecord{ID: "operations.py::Ops.quote_name", FilePath: "pkg/operations.py", Language: "python", Kind: core.KindMethod, Name: "quote_name", QualifiedName: "Ops.quote_name", ParentSymbol: "Ops"}
	edges := BuildEdges([]core.SymbolRecord{caller, backend, method})
	if !javaHasCall(edges, caller.ID, method.ID) {
		t.Fatal("dynamic member call must follow the imported backend's operations module")
	}
}
