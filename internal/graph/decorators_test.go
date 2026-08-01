// Tests for decorator-derived call edges (wrapper→wrapped and
// caller→wrapper).
package graph

import (
	"fmt"
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

// deltaDecoFixture spreads the decorator triangle over three files so that
// editing the decorator's own file does NOT put the caller in the affected
// set: cli.py imports app.py, and only app.py imports deco.py.
func deltaDecoFixture(decoBody string) []core.SymbolRecord {
	out := []core.SymbolRecord{{
		ID: "deco.py::setupmethod@10", FilePath: "deco.py", BlobSHA: "1",
		Language: "python", Kind: core.KindFunction,
		Name: "setupmethod", QualifiedName: "setupmethod", RawText: decoBody,
	}, {
		ID: "app.py::Scaffold.get@40", FilePath: "app.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "get", QualifiedName: "Scaffold.get", ParentSymbol: "Scaffold",
		Imports:     []string{"deco"},
		Annotations: []string{"setupmethod"},
	}, {
		ID: "cli.py::locate_app@5", FilePath: "cli.py", BlobSHA: "1",
		Language: "python", Kind: core.KindFunction,
		Name: "locate_app", QualifiedName: "locate_app",
		Imports:   []string{"app"},
		CallSites: []core.CallSite{{Callee: "app.get", Line: 7}},
	}}
	// Filler keeps the affected set under maxAffectedFraction so the delta
	// actually runs instead of falling back (asserted in the test).
	for i := 0; i < 40; i++ {
		out = append(out, core.SymbolRecord{
			ID:       fmt.Sprintf("filler%d.py::pad%d@1", i, i),
			FilePath: fmt.Sprintf("filler%d.py", i), BlobSHA: "1",
			Language: "python", Kind: core.KindFunction,
			Name: fmt.Sprintf("pad%d", i), QualifiedName: fmt.Sprintf("pad%d", i),
		})
	}
	return out
}

// TestDecoratorEdges_DeltaMatchesFullRebuild pins the equivalence contract for
// the edit that used to break it: changing the BODY of the decorator itself.
// That makes the wrapper a semantically-new symbol, so the delta's endpoint
// normalization drops every carried caller→wrapper edge — and the caller is
// not in the affected set, so an owner-scoped decorator pass cannot put them
// back. Only regenerating decorators over the FULL symbol and call sets does.
func TestDecoratorEdges_DeltaMatchesFullRebuild(t *testing.T) {
	prev := deltaDecoFixture("def setupmethod(f):\n    return f")
	prevEdges := BuildEdges(prev)

	next := deltaDecoFixture("def setupmethod(f):\n    return f  # changed")
	next[0].BlobSHA, next[0].ID = "2", "deco.py::setupmethod@10b"
	changed := map[string]bool{"deco.py": true}

	before := deltaStats.fallback
	got := BuildEdgesDelta(prevEdges, prev, next, changed, nil)
	if deltaStats.fallback != before {
		t.Fatal("delta fell back to a full rebuild; the test asserts nothing")
	}
	want := BuildEdges(next)
	if h1, h2 := edgeSetHash(got), edgeSetHash(want); h1 != h2 {
		t.Fatalf("delta edge set != full rebuild\n got %d edges (%s)\nwant %d edges (%s)",
			len(got), h1, len(want), h2)
	}
	var callerToWrapper bool
	for _, e := range got {
		if e.From == "cli.py::locate_app@5" && e.To == "deco.py::setupmethod@10b" {
			callerToWrapper = true
		}
	}
	if !callerToWrapper {
		t.Error("delta lost the caller→wrapper edge locate_app → setupmethod")
	}
}
