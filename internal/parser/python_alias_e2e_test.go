package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestPythonImportAliasesResolveCallsAndTypes(t *testing.T) {
	files := map[string]string{
		"pkg/types.py":   "class ParamType:\n    def complete(self, value): return value\n",
		"pkg/factory.py": "from .types import ParamType\ndef make() -> ParamType:\n    return ParamType()\n",
		"pkg/member.py":  "from .types import ParamType as PT\nfrom .factory import make as create\nclass Parameter:\n    def __init__(self, value: PT): self.kind: PT = value\n    def complete(self, value): return self.kind.complete(value)\ndef build(): return create()\n",
		"pkg/module.py":  "import pkg.factory as fac\ndef build(): return fac.make()\n",
		"pkg/scoped.py":  "def before_import(): return create()\ndef owns_import():\n    from .factory import make as create\n    return create()\n",
	}
	var symbols []core.SymbolRecord
	for file, src := range files {
		syms, ok, _ := extractSymbolsFromAST("python", file, "sha", []byte(src), extractImports("python", src))
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
	for _, want := range []string{
		"pkg/member.py:Parameter.complete -> pkg/types.py:ParamType.complete",
		"pkg/member.py:build -> pkg/factory.py:make",
		"pkg/module.py:build -> pkg/factory.py:make",
		"pkg/scoped.py:owns_import -> pkg/factory.py:make",
	} {
		if !got[want] {
			t.Errorf("missing alias edge %s; calls=%v", want, got)
		}
	}
	if got["pkg/scoped.py:before_import -> pkg/factory.py:make"] {
		t.Fatal("function-local import leaked into a sibling function")
	}
}
