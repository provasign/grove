package graph

import (
	"github.com/provasign/grove/internal/core"
	"testing"
)

func TestImpactScopedFreeFunctionDoesNotUnionNamesakes(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "a", Name: "run", QualifiedName: "run", FilePath: "a.py", Kind: core.KindFunction, Language: "python"},
		{ID: "b", Name: "run", QualifiedName: "run", FilePath: "b.py", Kind: core.KindFunction, Language: "python"},
		{ID: "c", Name: "run", QualifiedName: "Decoy.run", ParentSymbol: "Decoy", FilePath: "decoy.py", Kind: core.KindMethod, Language: "python"},
		{ID: "usea", Name: "usea", FilePath: "use.py", Kind: core.KindFunction, Language: "python"},
		{ID: "useb", Name: "useb", FilePath: "use.py", Kind: core.KindFunction, Language: "python"},
	}
	g := New()
	g.ReplaceWithStoredEdges(syms, []core.Edge{
		{From: "usea", To: "a", Type: core.EdgeCalls},
		{From: "useb", To: "b", Type: core.EdgeCalls},
	}, 3)
	r, err := g.ChangeImpactScoped("run", "a.py")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Declarations) != 1 || r.Declarations[0].ID != "a" || len(r.Callers) != 1 || r.Callers[0].ID != "usea" {
		t.Fatalf("file scope leaked: %+v", r)
	}
	if _, err := g.ChangeImpactScoped("run", "missing.py"); err == nil {
		t.Fatal("missing scope silently widened")
	}
}
