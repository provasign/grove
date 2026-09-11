package parser

import (
	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"testing"
)

func TestPythonNestedFunctionIsIndexedAndDoesNotBindClassMember(t *testing.T) {
	src := "def outer():\n    def inner():\n        return 1\n    return inner()\nclass Decoy:\n    def inner(self): return 2\n"
	syms, ok, _ := extractSymbolsFromAST("python", "lexical.py", "sha", []byte(src), extractImports("python", src))
	if !ok {
		t.Fatal("parse")
	}
	byID := map[string]core.SymbolRecord{}
	var outerID, innerID string
	for _, s := range syms {
		byID[s.ID] = s
		if s.QualifiedName == "outer" {
			outerID = s.ID
		}
		if s.QualifiedName == "outer.inner" {
			innerID = s.ID
		}
	}
	if outerID == "" || innerID == "" {
		t.Fatalf("nested symbols missing: %+v", syms)
	}
	sawInner := false
	for _, e := range graph.BuildEdges(syms) {
		if e.Type != core.EdgeCalls || e.From != outerID {
			continue
		}
		if e.To == innerID {
			sawInner = true
		}
		target := byID[e.To]
		if target.QualifiedName == "Decoy.inner" {
			t.Fatal("bare call resolved as class member")
		}
	}
	if !sawInner {
		t.Fatal("outer() did not call its indexed lexical inner()")
	}
}

func TestPythonTopLevelCallsReachGraph(t *testing.T) {
	src := "entry()\ndef entry():\n    return helper()\ndef helper():\n    return 1\n"
	syms, ok, _ := extractSymbolsFromAST("python", "module.py", "sha", []byte(src), extractImports("python", src))
	if !ok {
		t.Fatal("parse")
	}
	ids := map[string]string{}
	for _, symbol := range syms {
		ids[symbol.QualifiedName] = symbol.ID
	}
	if ids["<top-level>"] == "" || ids["entry"] == "" {
		t.Fatalf("missing top-level or entry symbol: %+v", syms)
	}
	found := false
	for _, edge := range graph.BuildEdges(syms) {
		if edge.Type == core.EdgeCalls && edge.From == ids["<top-level>"] && edge.To == ids["entry"] {
			found = true
		}
	}
	if !found {
		t.Fatal("module-level entry() call did not become a graph edge")
	}
}
