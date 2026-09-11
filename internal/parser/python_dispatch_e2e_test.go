package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

// Real extraction, same-name decoys, and generic annotations reproduce the
// Click completion failure without depending on a benchmark checkout.
func TestPythonCompletionDispatch(t *testing.T) {
	files := map[string]string{
		"pkg/types.py": `from typing import Generic, TypeVar
T = TypeVar("T")
class ParamType(Generic[T]):
    def shell_complete(self, ctx, param, incomplete):
        return []
class Choice(ParamType[T]):
    def shell_complete(self, ctx, param, incomplete):
        return []
`,
		"pkg/core.py": `from . import types
class Command:
    def shell_complete(self, ctx, incomplete):
        return []
    def main(self):
        from .completion import shell_complete
        return shell_complete(self)
class Parameter:
    def __init__(self, value):
        self.type: types.ParamType[object] = value
    def shell_complete(self, ctx, incomplete):
        return self.type.shell_complete(ctx, self, incomplete)
`,
		"pkg/completion.py": `from .core import Command, Parameter
def shell_complete(cmd):
    return []
def typed(param: Parameter):
    return param.shell_complete(None, "")
def dynamic(obj):
    return obj.shell_complete(None, "")
def imported(cmd):
    from .types import Choice
    return shell_complete(cmd)
`,
	}
	var all []core.SymbolRecord
	for file, src := range files {
		syms, ok, _ := extractSymbolsFromAST("python", file, "sha", []byte(src), extractImports("python", src))
		if !ok {
			t.Fatalf("parse %s", file)
		}
		all = append(all, syms...)
	}
	g := graph.New()
	g.Replace(all, len(files))
	byID := map[string]string{}
	for _, s := range all {
		byID[s.ID] = s.FilePath + ":" + s.QualifiedName
	}
	edges := map[string]core.Edge{}
	for _, e := range graph.BuildEdges(all) {
		if e.Type == core.EdgeCalls {
			edges[byID[e.From]+" -> "+byID[e.To]] = e
		}
	}
	for _, key := range []string{
		"pkg/core.py:Parameter.shell_complete -> pkg/types.py:ParamType.shell_complete",
		"pkg/core.py:Parameter.shell_complete -> pkg/types.py:Choice.shell_complete",
		"pkg/core.py:Command.main -> pkg/completion.py:shell_complete",
		"pkg/completion.py:typed -> pkg/core.py:Parameter.shell_complete",
	} {
		if _, ok := edges[key]; !ok {
			t.Errorf("missing %s; edges=%v", key, edges)
		}
	}
	for _, key := range []string{
		"pkg/core.py:Parameter.shell_complete -> pkg/core.py:Command.shell_complete",
		"pkg/core.py:Command.main -> pkg/core.py:Command.shell_complete",
		"pkg/core.py:Command.main -> pkg/types.py:Choice.shell_complete",
		"pkg/completion.py:dynamic -> pkg/completion.py:shell_complete",
		"pkg/completion.py:typed -> pkg/completion.py:shell_complete",
	} {
		if _, ok := edges[key]; ok {
			t.Errorf("false edge %s", key)
		}
	}
	r, err := g.ChangeImpact("Choice.shell_complete")
	if err != nil {
		t.Fatal(err)
	}
	if r.CallerCoverage != "partial" {
		t.Errorf("Python caller coverage=%q, want partial", r.CallerCoverage)
	}
	found := false
	for _, c := range r.Callers {
		if c.QualifiedName == "Parameter.shell_complete" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing Parameter caller: %v", r.Callers)
	}
}
