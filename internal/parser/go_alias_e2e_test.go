package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestGoImportAliasesDisambiguateCalls(t *testing.T) {
	files := map[string]string{
		"a/a.go": "package a\nfunc Run() {}\n",
		"b/b.go": "package b\nfunc Run() {}\n",
		"use.go": "package aliases\nimport (\n aa \"example.com/aliases/a\"\n bb \"example.com/aliases/b\"\n)\nfunc UseA() { aa.Run() }\nfunc UseB() { bb.Run() }\n",
	}
	var symbols []core.SymbolRecord
	for file, src := range files {
		syms, ok, _ := extractSymbolsFromAST("go", file, "sha", []byte(src), extractImports("go", src))
		if !ok {
			t.Fatalf("parse %s", file)
		}
		symbols = append(symbols, syms...)
	}
	byID := map[string]string{}
	for _, s := range symbols {
		byID[s.ID] = s.FilePath + ":" + s.QualifiedName
	}
	got := map[string]bool{}
	for _, edge := range graph.BuildEdges(symbols) {
		if edge.Type == core.EdgeCalls {
			got[byID[edge.From]+" -> "+byID[edge.To]] = true
		}
	}
	for _, want := range []string{"use.go:UseA -> a/a.go:Run", "use.go:UseB -> b/b.go:Run"} {
		if !got[want] {
			t.Errorf("missing %s; calls=%v", want, got)
		}
	}
	for _, bad := range []string{"use.go:UseA -> b/b.go:Run", "use.go:UseB -> a/a.go:Run"} {
		if got[bad] {
			t.Errorf("alias leaked: %s", bad)
		}
	}
}
