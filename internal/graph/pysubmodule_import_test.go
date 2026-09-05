package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// `from . import cli` binds the submodule cli; `cli.show_server_banner()`
// must resolve into cli.py. The from-import member arrives as ".#cli" and
// is bound only because src/flask/cli.py exists; a member that is a class
// (".helpers#_CollectErrors") must not become an import.
func TestPyFromImportSubmoduleQualifier(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "src/flask/cli.py::show_server_banner@1", FilePath: "src/flask/cli.py", BlobSHA: "1", Language: "python",
			Kind: core.KindFunction, Name: "show_server_banner", QualifiedName: "show_server_banner",
			RawText: "def show_server_banner(debug, name):\n    pass\n"},
		{ID: "other/cli.py::show_server_banner@1", FilePath: "other/cli.py", BlobSHA: "1", Language: "python",
			Kind: core.KindFunction, Name: "show_server_banner", QualifiedName: "show_server_banner",
			RawText: "def show_server_banner(debug, name):\n    pass\n"},
		{ID: "src/flask/helpers.py::_CollectErrors@1", FilePath: "src/flask/helpers.py", BlobSHA: "1", Language: "python",
			Kind: core.KindClass, Name: "_CollectErrors", QualifiedName: "_CollectErrors",
			RawText: "class _CollectErrors:\n    pass\n"},
		{ID: "src/flask/helpers.py::_CollectErrors.collect@2", FilePath: "src/flask/helpers.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "collect", QualifiedName: "_CollectErrors.collect", ParentSymbol: "_CollectErrors",
			RawText: "def collect(cls):\n    pass\n"},
		{ID: "src/flask/app.py::Flask.run@1", FilePath: "src/flask/app.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "run", QualifiedName: "Flask.run", ParentSymbol: "Flask",
			RawText:   "def run(self):\n    cli.show_server_banner(self.debug, self.name)\n    _CollectErrors.collect()\n",
			Imports:   []string{".", ".#cli", ".helpers", ".helpers#_CollectErrors"},
			CallSites: []core.CallSite{{Callee: "cli.show_server_banner", Line: 2}, {Callee: "_CollectErrors.collect", Line: 3}}},
	}
	idx := newEdgeIndex(syms)
	imps := idx.fileImports["src/flask/app.py"]
	if _, ok := imps[".cli"]; !ok {
		t.Fatalf("submodule import .cli not bound: %v", imps)
	}
	if _, ok := imps[".helpers._CollectErrors"]; ok {
		t.Fatalf("class member bound as module import: %v", imps)
	}
	got := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.From == "src/flask/app.py::Flask.run@1" {
			got[e.To] = true
		}
	}
	if !got["src/flask/cli.py::show_server_banner@1"] {
		t.Errorf("cli.show_server_banner did not resolve into src/flask/cli.py: %v", got)
	}
	if got["other/cli.py::show_server_banner@1"] {
		t.Errorf("resolved to the wrong cli module")
	}
	if !got["src/flask/helpers.py::_CollectErrors.collect@2"] {
		t.Errorf("_CollectErrors.collect lost: %v", got)
	}
}
