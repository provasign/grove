package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestPythonTopLevelSymbolDoesNotOwnModuleImports(t *testing.T) {
	const app = "app.py"
	syms := []core.SymbolRecord{
		{
			ID: "helpers.py::helper@1", FilePath: "helpers.py", Language: "python",
			Kind: core.KindFunction, Name: "helper", QualifiedName: "helper",
		},
		{
			ID: app + "::<top-level>@1", FilePath: app, Language: "python",
			Kind: core.KindFunction, Name: "<top-level>", QualifiedName: "<top-level>",
			Span: core.LineRange{Start: 1, End: 20},
			Imports: []string{
				".helpers",
				core.PythonImportBinding(2, "helper", ".helpers#helper"),
			},
		},
		{
			ID: app + "::run@5", FilePath: app, Language: "python",
			Kind: core.KindFunction, Name: "run", QualifiedName: "run",
			Span: core.LineRange{Start: 5, End: 10},
			Imports: []string{
				".helpers",
				core.PythonImportBinding(2, "helper", ".helpers#helper"),
			},
			CallSites: []core.CallSite{{Callee: "helper", Line: 6}},
		},
	}

	for _, edge := range BuildEdges(syms) {
		if edge.Type == core.EdgeCalls && edge.From == app+"::run@5" && edge.To == "helpers.py::helper@1" {
			return
		}
	}
	t.Fatal("module import was hidden from a real function by the synthetic top-level symbol")
}
