// Tests that AST-extracted CallSites produce high-confidence calls edges
// (0.95) regardless of whether the textual regex would have matched.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestBuildCalls_HighConfidenceFromCallSites(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "a.go::Caller@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		Imports:   []string{"b"},
		CallSites: []core.CallSite{{Callee: "Helper", Line: 3}},
		// Deliberately leave RawText blank — the regex fallback must NOT be
		// what produces the edge. Only the CallSite path can match.
		RawText: "",
	}
	callee := core.SymbolRecord{
		ID: "b.go::Helper@1", FilePath: "b.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Helper", QualifiedName: "Helper",
		RawText: "func Helper() {}",
	}

	edges := BuildEdges([]core.SymbolRecord{caller, callee})

	var found *core.Edge
	for i := range edges {
		e := &edges[i]
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			found = e
			break
		}
	}
	if found == nil {
		t.Fatalf("expected calls edge Caller→Helper; got %d edges", len(edges))
	}
	if found.Confidence < 0.9 {
		t.Errorf("confidence = %v, want ≥ 0.9 (AST-extracted)", found.Confidence)
	}
}

func TestBuildCalls_CallSitesScopedToImports(t *testing.T) {
	// Caller calls Helper, but Helper lives in a DIFFERENT directory (different
	// Go package) and the caller does not import it → edge must NOT be produced.
	// (Same-directory Go files are the same package and DO resolve without an
	// import — that case is covered by TestBuildCalls_SamePackageNoImport.)
	caller := core.SymbolRecord{
		ID: "pkga/a.go::Caller@1", FilePath: "pkga/a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		Imports:   nil, // no imports
		CallSites: []core.CallSite{{Callee: "Helper", Line: 3}},
	}
	callee := core.SymbolRecord{
		ID: "pkgb/b.go::Helper@1", FilePath: "pkgb/b.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Helper", QualifiedName: "Helper",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, callee})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			t.Fatalf("unexpected cross-package edge without import: %#v", e)
		}
	}
}

func TestBuildCalls_SamePackageNoImport(t *testing.T) {
	// Two Go files in the same directory are the same package: a call resolves
	// even though neither file imports the other.
	caller := core.SymbolRecord{
		ID: "pkg/a.go::Caller@1", FilePath: "pkg/a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		CallSites: []core.CallSite{{Callee: "Helper", Line: 3}},
	}
	callee := core.SymbolRecord{
		ID: "pkg/b.go::Helper@1", FilePath: "pkg/b.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Helper", QualifiedName: "Helper",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, callee})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			return
		}
	}
	t.Fatal("expected same-package call edge across files in the same directory")
}

func TestBuildCalls_StripsReceiverPrefix(t *testing.T) {
	// CallSite recorded as "user.save" — must still match the bare "save" symbol.
	caller := core.SymbolRecord{
		ID: "a.go::Caller@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Caller", QualifiedName: "Caller",
		CallSites: []core.CallSite{{Callee: "user.save", Line: 5}},
	}
	callee := core.SymbolRecord{
		ID: "a.go::save@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "save", QualifiedName: "save",
		ParentSymbol: "User",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, callee})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == callee.ID {
			return
		}
	}
	t.Fatal("expected calls edge after stripping receiver prefix")
}
