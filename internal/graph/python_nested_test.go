package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestPythonIndexedLocalFunctionReceivesCallEdge(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "app.py::outer", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "outer", QualifiedName: "outer", RawText: "def outer():\n    def inner(): pass\n    inner()", CallSites: []core.CallSite{{Callee: "inner", Line: 3}}},
		{ID: "app.py::outer.inner", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "inner", QualifiedName: "outer.inner", ParentSymbol: "outer", RawText: "def inner(): pass"},
		{ID: "other.py::inner", FilePath: "other.py", Language: "python", Kind: core.KindFunction, Name: "inner", QualifiedName: "inner", RawText: "def inner(): pass"},
	}
	idx := newEdgeIndex(symbols)
	if got := pythonLexicalChild(idx, &symbols[0], "inner"); len(got) != 1 {
		t.Fatalf("lexical child lookup = %+v", got)
	}
	g := New()
	g.Replace(symbols, 2)

	if !hasEdge(g, core.EdgeCalls, "app.py::outer", "app.py::outer.inner") {
		t.Fatal("outer call did not resolve to its indexed lexical child")
	}
	if hasEdge(g, core.EdgeCalls, "app.py::outer", "other.py::inner") {
		t.Fatal("local function call leaked to a same-named module function")
	}
}

func TestDeadCodeTreatsTopLevelAsExecutionRoot(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "app.py::<top-level>", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "<top-level>", QualifiedName: "<top-level>", RawText: "entry()", CallSites: []core.CallSite{{Callee: "entry", Line: 1}}},
		{ID: "app.py::entry", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "entry", QualifiedName: "entry", RawText: "def entry(): pass"},
	}, 1)

	result := g.DeadCode(nil)
	if names(result.Dead)["entry"] || names(result.Dead)["<top-level>"] {
		t.Fatalf("top-level execution path reported dead: %+v", result.Dead)
	}
}

func TestDeadCodeDoesNotReportSyntheticLambda(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{{
		ID: "App.java::<lambda@2:10>", FilePath: "App.java", Language: "java",
		Kind: core.KindFunction, Name: "<lambda@2:10>", QualifiedName: "App.<lambda@2:10>",
		RawText: "() -> work()",
	}}, 1)
	if len(g.DeadCode(nil).Dead) != 0 {
		t.Fatal("synthetic lambda was reported as independently deletable dead code")
	}
}
