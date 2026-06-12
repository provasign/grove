// Tests for decorator-derived call edges (wrapper→wrapped and
// caller→wrapper).
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func decoFixture() []core.SymbolRecord {
	return []core.SymbolRecord{{
		ID: "scaffold.py::setupmethod@10", FilePath: "scaffold.py", BlobSHA: "1",
		Language: "python", Kind: core.KindFunction,
		Name: "setupmethod", QualifiedName: "setupmethod",
	}, {
		ID: "scaffold.py::Scaffold.get@40", FilePath: "scaffold.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "get", QualifiedName: "Scaffold.get", ParentSymbol: "Scaffold",
		Annotations: []string{"setupmethod"},
	}, {
		ID: "cli.py::locate_app@5", FilePath: "cli.py", BlobSHA: "1",
		Language: "python", Kind: core.KindFunction,
		Name: "locate_app", QualifiedName: "locate_app",
		Imports:   []string{"scaffold"},
		CallSites: []core.CallSite{{Callee: "app.get", Line: 7}},
	}}
}

func TestDecoratorEdges_WrapperCallsWrapped(t *testing.T) {
	edges := BuildEdges(decoFixture())
	var wrapperToWrapped, callerToWrapper bool
	for _, e := range edges {
		if e.Type != core.EdgeCalls {
			continue
		}
		if e.From == "scaffold.py::setupmethod@10" && e.To == "scaffold.py::Scaffold.get@40" {
			wrapperToWrapped = true
			if e.Confidence > 0.75 {
				t.Errorf("decorator edge confidence = %v, want reduced", e.Confidence)
			}
		}
		if e.From == "cli.py::locate_app@5" && e.To == "scaffold.py::setupmethod@10" {
			callerToWrapper = true
		}
	}
	if !wrapperToWrapped {
		t.Error("expected wrapper→wrapped edge setupmethod → Scaffold.get")
	}
	if !callerToWrapper {
		t.Error("expected caller→wrapper edge locate_app → setupmethod")
	}
}

func TestDecoratorEdges_BuiltinsAndDottedSkipped(t *testing.T) {
	symbols := []core.SymbolRecord{{
		ID: "a.py::prop_impl@1", FilePath: "a.py", BlobSHA: "1",
		Language: "python", Kind: core.KindFunction,
		Name: "property", QualifiedName: "property",
	}, {
		ID: "a.py::Cfg.debug@10", FilePath: "a.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "debug", QualifiedName: "Cfg.debug", ParentSymbol: "Cfg",
		Annotations: []string{"property", "app.route"},
	}}
	for _, e := range BuildEdges(symbols) {
		if e.Type == core.EdgeCalls && e.To == "a.py::Cfg.debug@10" {
			t.Fatalf("builtin/dotted decorators must not produce edges, got %+v", e)
		}
	}
}
