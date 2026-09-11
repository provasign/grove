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

func TestPythonDottedImportUsesOnlyItsRealRootBinding(t *testing.T) {
	files := map[string]string{
		"lib/engine.py": "def start(): return 1\n",
		"other.py":      "def start(): return 2\n",
		"app.py":        "import lib.engine\ndef good(): return lib.engine.start()\ndef invalid(): return engine.start()\n",
	}
	var symbols []core.SymbolRecord
	for file, src := range files {
		imports := extractImports("python", src)
		if file == "app.py" {
			bindings := map[string]string{}
			for _, imp := range imports {
				if _, local, target, ok := core.ParsePythonImportBinding(imp); ok {
					bindings[local] = target
				}
			}
			if bindings["lib"] != "lib.engine" || bindings["engine"] != "" {
				t.Fatalf("dotted import bindings = %v", bindings)
			}
		}
		syms, ok, _ := extractSymbolsFromAST("python", file, "sha", []byte(src), imports)
		if !ok {
			t.Fatalf("parse %s", file)
		}
		symbols = append(symbols, syms...)
	}
	labels := map[string]string{}
	for _, symbol := range symbols {
		labels[symbol.ID] = symbol.FilePath + ":" + symbol.QualifiedName
	}
	calls := map[string]bool{}
	for _, edge := range graph.BuildEdges(symbols) {
		if edge.Type == core.EdgeCalls {
			calls[labels[edge.From]+" -> "+labels[edge.To]] = true
		}
	}
	if !calls["app.py:good -> lib/engine.py:start"] {
		t.Fatalf("full dotted binding did not resolve: %v", calls)
	}
	if calls["app.py:invalid -> lib/engine.py:start"] || calls["app.py:invalid -> other.py:start"] {
		t.Fatalf("fabricated leaf binding resolved invalid engine.start(): %v", calls)
	}
}
