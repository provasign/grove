package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// mapIteratorFixture models the commons-collections shape that motivated
// contract-boundary detection: a project interface extending an external JDK
// interface (java.util.Iterator), one project implementor, and one caller.
func mapIteratorFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "MapIterator.java::MapIterator@sha", FilePath: "MapIterator.java", Language: "java", Kind: core.KindInterface,
			Name: "MapIterator", QualifiedName: "MapIterator",
			Signature: "public interface MapIterator<K, V> extends Iterator<K>"},
		{ID: "MapIterator.java::MapIterator.next@sha", FilePath: "MapIterator.java", Language: "java", Kind: core.KindMethod,
			Name: "next", QualifiedName: "MapIterator.next", ParentSymbol: "MapIterator",
			Signature: "K next()"},
		{ID: "EntrySetMapIterator.java::EntrySetMapIterator@sha", FilePath: "EntrySetMapIterator.java", Language: "java", Kind: core.KindClass,
			Name: "EntrySetMapIterator", QualifiedName: "EntrySetMapIterator",
			Signature: "public class EntrySetMapIterator implements MapIterator"},
		{ID: "EntrySetMapIterator.java::EntrySetMapIterator.next@sha", FilePath: "EntrySetMapIterator.java", Language: "java", Kind: core.KindMethod,
			Name: "next", QualifiedName: "EntrySetMapIterator.next", ParentSymbol: "EntrySetMapIterator",
			Signature: "public K next()",
			RawText:   "public K next() { return doNext(); }"},
		{ID: "Walker.java::Walker@sha", FilePath: "Walker.java", Language: "java", Kind: core.KindClass,
			Name: "Walker", QualifiedName: "Walker", Signature: "public class Walker"},
		{ID: "Walker.java::Walker.walk@sha", FilePath: "Walker.java", Language: "java", Kind: core.KindMethod,
			Name: "walk", QualifiedName: "Walker.walk", ParentSymbol: "Walker",
			Signature: "void walk(MapIterator it)",
			RawText:   "void walk(MapIterator it) { it.next(); }",
			CallSites: []core.CallSite{{Callee: "it.next", Line: 1, Argc: 0}},
			Imports:   []string{"MapIterator"}},
	}, 3)
	return g
}

func TestChangeImpactFlagsExternalContract(t *testing.T) {
	g := mapIteratorFixture()
	r, err := g.ChangeImpact("MapIterator.next")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.ExternalSupers) != 1 || r.ExternalSupers[0] != "Iterator" {
		t.Fatalf("ExternalSupers = %v, want [Iterator]", r.ExternalSupers)
	}
	if len(r.OverridesExternal) != 1 || r.OverridesExternal[0] != "Iterator#next" {
		t.Fatalf("OverridesExternal = %v, want [Iterator#next]", r.OverridesExternal)
	}
	if r.Completeness != "project-local" {
		t.Fatalf("Completeness = %q, want project-local", r.Completeness)
	}
	// The project-local closure itself must be unchanged by the flag.
	if len(r.Declarations) != 1 || len(r.Family) != 1 {
		t.Fatalf("decls=%d family=%d, want 1/1", len(r.Declarations), len(r.Family))
	}
}

func TestChangeImpactClosedHierarchyStaysClosed(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.java::Base@sha", FilePath: "a.java", Language: "java", Kind: core.KindInterface,
			Name: "Base", QualifiedName: "Base", Signature: "public interface Base"},
		{ID: "a.java::Base.run@sha", FilePath: "a.java", Language: "java", Kind: core.KindMethod,
			Name: "run", QualifiedName: "Base.run", ParentSymbol: "Base", Signature: "void run()"},
		{ID: "b.java::Impl@sha", FilePath: "b.java", Language: "java", Kind: core.KindClass,
			Name: "Impl", QualifiedName: "Impl", Signature: "public class Impl implements Base"},
		{ID: "b.java::Impl.run@sha", FilePath: "b.java", Language: "java", Kind: core.KindMethod,
			Name: "run", QualifiedName: "Impl.run", ParentSymbol: "Impl", Signature: "public void run()"},
	}, 2)
	r, err := g.ChangeImpact("Base.run")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.ExternalSupers) != 0 || len(r.OverridesExternal) != 0 {
		t.Fatalf("closed hierarchy flagged external: supers=%v overrides=%v", r.ExternalSupers, r.OverridesExternal)
	}
	if r.Completeness != "closed" {
		t.Fatalf("Completeness = %q, want closed", r.Completeness)
	}
}

// A hierarchy that touches an external type whose contract does NOT include
// the queried method stays "closed": ExternalSupers is informational, the
// strong flag needs a contract match.
func TestChangeImpactExternalSuperWithoutContractMatch(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.java::Codec@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass,
			Name: "Codec", QualifiedName: "Codec", Signature: "public class Codec implements Serializable"},
		{ID: "a.java::Codec.encode@sha", FilePath: "a.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Codec.encode", ParentSymbol: "Codec", Signature: "byte[] encode(String s)"},
	}, 1)
	r, err := g.ChangeImpact("Codec.encode")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.ExternalSupers) != 1 || r.ExternalSupers[0] != "Serializable" {
		t.Fatalf("ExternalSupers = %v, want [Serializable]", r.ExternalSupers)
	}
	if len(r.OverridesExternal) != 0 || r.Completeness != "closed" {
		t.Fatalf("encode is not a Serializable member: overrides=%v completeness=%q",
			r.OverridesExternal, r.Completeness)
	}
}

// Querying the external type directly ("Iterator.next") must return the
// project-local implementation closure instead of erroring.
func TestChangeImpactExternalRootedQuery(t *testing.T) {
	g := mapIteratorFixture()
	r, err := g.ChangeImpact("Iterator.next")
	if err != nil {
		t.Fatalf("external-rooted ChangeImpact: %v", err)
	}
	if len(r.Declarations) != 0 {
		t.Fatalf("external root has no indexed declaration, got %d", len(r.Declarations))
	}
	// Family: MapIterator.next (seed: MapIterator declares `extends Iterator`)
	// plus EntrySetMapIterator.next via the subtype closure.
	if len(r.Family) != 2 {
		t.Fatalf("Family = %d symbols %v, want 2", len(r.Family), symNames(r.Family))
	}
	if r.Completeness != "project-local" {
		t.Fatalf("Completeness = %q, want project-local", r.Completeness)
	}
	if len(r.OverridesExternal) != 1 || r.OverridesExternal[0] != "Iterator#next" {
		t.Fatalf("OverridesExternal = %v", r.OverridesExternal)
	}
}

func TestChangeImpactUnknownTypeStillErrors(t *testing.T) {
	g := mapIteratorFixture()
	if _, err := g.ChangeImpact("Nonexistent.next"); err == nil {
		t.Fatalf("expected error for a type that is neither indexed nor declared as a supertype")
	}
}

func symNames(ss []core.SymbolRecord) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.QualifiedName)
	}
	return out
}

// Go receiver methods live in any file of the type's package — the contains
// edge must resolve cross-file within the same directory.
func TestGoCrossFileMethodContains(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "pkg/store/store.go::SQLStore@sha", FilePath: "pkg/store/store.go", Language: "go", Kind: core.KindStruct,
			Name: "SQLStore", QualifiedName: "SQLStore", RawText: "type SQLStore struct { db *DB }"},
		{ID: "pkg/store/tx.go::SQLStore.WithTx@sha", FilePath: "pkg/store/tx.go", Language: "go", Kind: core.KindMethod,
			Name: "WithTx", QualifiedName: "SQLStore.WithTx", ParentSymbol: "SQLStore",
			Signature: "func (ss *SQLStore) WithTx(ctx context.Context) error"},
		// Same-named type in a DIFFERENT package must not capture the method.
		{ID: "pkg/other/store.go::SQLStore@sha", FilePath: "pkg/other/store.go", Language: "go", Kind: core.KindStruct,
			Name: "SQLStore", QualifiedName: "SQLStore", RawText: "type SQLStore struct{}"},
	}, 3)
	r, err := g.ChangeImpact("SQLStore.WithTx")
	if err != nil {
		t.Fatalf("cross-file Go method not resolved: %v", err)
	}
	if len(r.Declarations) != 1 || r.Declarations[0].FilePath != "pkg/store/tx.go" {
		t.Fatalf("declarations = %v", symNames(r.Declarations))
	}
	if hasEdge(g, core.EdgeContains, "pkg/other/store.go::SQLStore@sha", "pkg/store/tx.go::SQLStore.WithTx@sha") {
		t.Fatalf("cross-package contains edge must not exist")
	}
}
