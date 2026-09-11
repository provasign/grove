package parser

import (
	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"testing"
)

// Nested functions are not currently emitted by the extractor. Their calls
// must remain unresolved, not bind to an unrelated class member with the name.
func TestPythonUnindexedNestedFunctionDoesNotBindClassMember(t *testing.T) {
	src := "def outer():\n    def inner():\n        return 1\n    return inner()\nclass Decoy:\n    def inner(self): return 2\n"
	syms, ok, _ := extractSymbolsFromAST("python", "lexical.py", "sha", []byte(src), extractImports("python", src))
	if !ok {
		t.Fatal("parse")
	}
	byID := map[string]core.SymbolRecord{}
	for _, s := range syms {
		byID[s.ID] = s
	}
	for _, e := range graph.BuildEdges(syms) {
		if e.Type != core.EdgeCalls || byID[e.From].Name != "outer" {
			continue
		}
		target := byID[e.To]
		if target.QualifiedName == "Decoy.inner" {
			t.Fatal("bare call resolved as class member")
		}
	}
}
