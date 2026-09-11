package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestRustTypedReceiverReachesTraitDefault(t *testing.T) {
	trait := core.SymbolRecord{ID: "lib.rs::Loud", FilePath: "lib.rs", Language: "rust", Kind: core.KindTrait, Name: "Loud", QualifiedName: "Loud", Signature: "trait Loud"}
	shout := core.SymbolRecord{ID: "lib.rs::Loud.shout", FilePath: "lib.rs", Language: "rust", Kind: core.KindMethod, Name: "shout", QualifiedName: "Loud.shout", ParentSymbol: "Loud", Signature: "fn shout(&self)", RawText: "fn shout(&self) { println!(\"loud\"); }"}
	dog := core.SymbolRecord{ID: "lib.rs::Dog", FilePath: "lib.rs", Language: "rust", Kind: core.KindStruct, Name: "Dog", QualifiedName: "Dog", Annotations: []string{"implements:Loud"}}
	caller := core.SymbolRecord{ID: "lib.rs::use", FilePath: "lib.rs", Language: "rust", Kind: core.KindFunction, Name: "use", QualifiedName: "use", Signature: "fn use(d: &Dog)", RawText: "fn use(d: &Dog) { d.shout(); }", CallSites: []core.CallSite{{Callee: "d.shout"}}}
	if !javaHasCall(BuildEdges([]core.SymbolRecord{trait, shout, dog, caller}), caller.ID, shout.ID) {
		t.Fatal("typed Dog receiver must reach Loud's default shout method")
	}
}

func TestRustGenericTraitSupertype(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "traits.rs::Super", FilePath: "traits.rs", Language: "rust", Kind: core.KindTrait, Name: "Super", QualifiedName: "Super", Signature: "trait Super"},
		{ID: "traits.rs::Child", FilePath: "traits.rs", Language: "rust", Kind: core.KindTrait, Name: "Child", QualifiedName: "Child", Signature: "trait Child<T: Bound>: Super {"},
	}
	got := baseClassesFor(newEdgeIndex(syms), "rust", "Child", "")
	if len(got) != 1 || got[0] != "Super" {
		t.Fatalf("Child supertraits = %v, want [Super]", got)
	}
}

func TestRustTraitDefaultBodyIsProvided(t *testing.T) {
	provided := []core.SymbolRecord{{Language: "rust", Kind: core.KindMethod, RawText: "fn shout(&self) { println!(\"hi\"); }"}}
	declared := []core.SymbolRecord{{Language: "rust", Kind: core.KindMethod, RawText: "fn shout(&self);"}}
	seed := map[string]core.SymbolKind{"Loud": core.KindTrait}
	if !contractProvidesBody(provided, seed) {
		t.Fatal("Rust trait method with a body must be DefaultProvided")
	}
	if contractProvidesBody(declared, seed) {
		t.Fatal("Rust trait signature without a body is not provided")
	}
}

func TestRustImplRegexAcceptsGenericAndReferenceReceiver(t *testing.T) {
	for _, source := range []string{"impl<T> Greet for Wrapper<T> {}", "impl Greet for &Dog {}"} {
		if match := rustImplForRe.FindStringSubmatch(source); len(match) != 3 {
			t.Errorf("rustImplForRe did not match %q", source)
		}
	}
}

func TestRustBareImportPinsCallTarget(t *testing.T) {
	caller := core.SymbolRecord{ID: "src/lib.rs::use", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindFunction, Name: "use", QualifiedName: "use", Imports: []string{"use crate::a::helper;"}, RawText: "fn use() { helper(); }", CallSites: []core.CallSite{{Callee: "helper"}}}
	a := core.SymbolRecord{ID: "src/a.rs::helper", FilePath: "src/a.rs", Language: "rust", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper"}
	b := core.SymbolRecord{ID: "src/b.rs::helper", FilePath: "src/b.rs", Language: "rust", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper"}
	edges := BuildEdges([]core.SymbolRecord{caller, a, b})
	if !javaHasCall(edges, caller.ID, a.ID) || javaHasCall(edges, caller.ID, b.ID) {
		t.Fatalf("bare imported helper must pin to a.rs; edges=%+v", edges)
	}
}

func TestRustInlineModuleBareCallStaysInModule(t *testing.T) {
	first := core.SymbolRecord{ID: "lib.rs::first.shared", FilePath: "lib.rs", Language: "rust", Kind: core.KindFunction, Name: "shared", QualifiedName: "first.shared", ParentSymbol: "first"}
	second := core.SymbolRecord{ID: "lib.rs::second.shared", FilePath: "lib.rs", Language: "rust", Kind: core.KindFunction, Name: "shared", QualifiedName: "second.shared", ParentSymbol: "second"}
	caller := core.SymbolRecord{ID: "lib.rs::first.own", FilePath: "lib.rs", Language: "rust", Kind: core.KindFunction, Name: "own", QualifiedName: "first.own", ParentSymbol: "first", CallSites: []core.CallSite{{Callee: "shared"}}}
	edges := BuildEdges([]core.SymbolRecord{first, second, caller})
	if !javaHasCall(edges, caller.ID, first.ID) || javaHasCall(edges, caller.ID, second.ID) {
		t.Fatalf("inline-module bare call was not scoped; edges=%+v", edges)
	}
}
