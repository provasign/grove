// Hand-built table tests for Swift and Kotlin edge resolution, mirroring
// the compiler-free style used for every other language (see
// csharp_generic_test.go, rust_round2_test.go): construct SymbolRecord/
// CallSite literals directly and assert against the edges BuildEdges (via
// CodeGraph.Replace) produces. No astkit/tree-sitter parsing involved here —
// that side is covered by astkit's own strategies_test.go.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestSwiftCalls_CrossFileWholeModuleScope(t *testing.T) {
	// Swift's `import Module` names a whole module, not a file — so two
	// files in the same module resolve to each other with NO import at
	// all. This asserts the new "swift" branch in importedFiles.
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.swift::Person@sha", FilePath: "Person.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "class Person"},
		{ID: "Person.swift::Person.greet@sha", FilePath: "Person.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindMethod, Name: "greet", QualifiedName: "greet",
			ParentSymbol: "Person", Signature: "func greet()"},
		{ID: "App.swift::App.run@sha", FilePath: "App.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindMethod, Name: "run", QualifiedName: "run",
			ParentSymbol: "App", Signature: "func run()",
			RawText:   "func run() { let p = Person(); p.greet() }",
			CallSites: []core.CallSite{{Callee: "p.greet", Line: 1}},
		},
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "App.swift::App.run@sha", "Person.swift::Person.greet@sha") {
		t.Fatalf("p.greet() must resolve to Person.greet via whole-module scope + constructor-call local typing")
	}
}

func TestSwiftExtendsImplements_ClassAndProtocol(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Animal.swift::Animal@sha", FilePath: "Animal.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindClass, Name: "Animal", QualifiedName: "Animal", Signature: "class Animal"},
		{ID: "Greeter.swift::Greeter@sha", FilePath: "Greeter.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindInterface, Name: "Greeter", QualifiedName: "Greeter", Signature: "protocol Greeter"},
		{ID: "Dog.swift::Dog@sha", FilePath: "Dog.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindClass, Name: "Dog", QualifiedName: "Dog",
			Signature: "class Dog: Animal, Greeter"},
	}
	g.Replace(syms, 3)

	if !hasEdge(g, core.EdgeExtends, "Dog.swift::Dog@sha", "Animal.swift::Animal@sha") {
		t.Fatalf("Dog must extend Animal (first base name resolves to a class)")
	}
	if !hasEdge(g, core.EdgeImplements, "Dog.swift::Dog@sha", "Greeter.swift::Greeter@sha") {
		t.Fatalf("Dog must implement Greeter")
	}
	if hasEdge(g, core.EdgeImplements, "Dog.swift::Dog@sha", "Animal.swift::Animal@sha") {
		t.Fatalf("Animal must not ALSO be recorded as an implemented protocol")
	}
}

func TestSwiftExtendsImplements_StructCanOnlyConformNotExtend(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Codable.swift::Codable@sha", FilePath: "Codable.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindInterface, Name: "Codable", QualifiedName: "Codable", Signature: "protocol Codable"},
		{ID: "Point.swift::Point@sha", FilePath: "Point.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindStruct, Name: "Point", QualifiedName: "Point",
			Signature: "struct Point: Codable"},
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeImplements, "Point.swift::Point@sha", "Codable.swift::Codable@sha") {
		t.Fatalf("Point must conform to Codable")
	}
	if hasEdge(g, core.EdgeExtends, "Point.swift::Point@sha", "Codable.swift::Codable@sha") {
		t.Fatalf("a struct conformance must never be recorded as EdgeExtends")
	}
}

func TestSwiftCalls_ImplicitSelfResolvesWithinClass(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.swift::Person@sha", FilePath: "Person.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindClass, Name: "Person", QualifiedName: "Person", Signature: "class Person"},
		{ID: "Person.swift::Person.helper@sha", FilePath: "Person.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindMethod, Name: "helper", QualifiedName: "helper",
			ParentSymbol: "Person", Signature: "func helper()"},
		{ID: "Person.swift::Person.greet@sha", FilePath: "Person.swift", BlobSHA: "sha",
			Language: "swift", Kind: core.KindMethod, Name: "greet", QualifiedName: "greet",
			ParentSymbol: "Person", Signature: "func greet()",
			CallSites: []core.CallSite{{Callee: "self.helper", Line: 1}},
		},
	}
	g.Replace(syms, 1)

	if !hasEdge(g, core.EdgeCalls, "Person.swift::Person.greet@sha", "Person.swift::Person.helper@sha") {
		t.Fatalf("self.helper() must resolve to Person.helper")
	}
}

func TestKotlinCalls_SamePackageScope(t *testing.T) {
	// Kotlin (like Java) treats same-package files as mutually visible with
	// no import — this asserts the extended "go"||"java"||"kotlin" branch.
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.kt::Person@sha", FilePath: "src/Person.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "class Person"},
		{ID: "Person.kt::Person.greet@sha", FilePath: "src/Person.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindMethod, Name: "greet", QualifiedName: "greet",
			ParentSymbol: "Person", Signature: "fun greet()"},
		{ID: "App.kt::App.run@sha", FilePath: "src/App.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindMethod, Name: "run", QualifiedName: "run",
			ParentSymbol: "App", Signature: "fun run()",
			RawText:   "fun run() { val p = Person(); p.greet() }",
			CallSites: []core.CallSite{{Callee: "p.greet", Line: 1}},
		},
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "App.kt::App.run@sha", "Person.kt::Person.greet@sha") {
		t.Fatalf("p.greet() must resolve to Person.greet via same-package scope + constructor-call local typing")
	}
}

func TestKotlinExtendsImplements_ConstructorCallMarksSuperclass(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Base.kt::Base@sha", FilePath: "Base.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindClass, Name: "Base", QualifiedName: "Base", Signature: "open class Base"},
		{ID: "IThing.kt::IThing@sha", FilePath: "IThing.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindInterface, Name: "IThing", QualifiedName: "IThing", Signature: "interface IThing"},
		{ID: "Foo.kt::Foo@sha", FilePath: "Foo.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindClass, Name: "Foo", QualifiedName: "Foo",
			Signature: "class Foo : Base(), IThing"},
	}
	g.Replace(syms, 3)

	if !hasEdge(g, core.EdgeExtends, "Foo.kt::Foo@sha", "Base.kt::Base@sha") {
		t.Fatalf("Foo must extend Base (the constructor-call entry)")
	}
	if !hasEdge(g, core.EdgeImplements, "Foo.kt::Foo@sha", "IThing.kt::IThing@sha") {
		t.Fatalf("Foo must implement IThing (the plain entry)")
	}
	if hasEdge(g, core.EdgeImplements, "Foo.kt::Foo@sha", "Base.kt::Base@sha") {
		t.Fatalf("Base must not ALSO be recorded as an implemented interface")
	}
}

func TestKotlinCalls_ImplicitThisResolvesWithinClass(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.kt::Person@sha", FilePath: "Person.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindClass, Name: "Person", QualifiedName: "Person", Signature: "class Person"},
		{ID: "Person.kt::Person.helper@sha", FilePath: "Person.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindMethod, Name: "helper", QualifiedName: "helper",
			ParentSymbol: "Person", Signature: "fun helper()"},
		{ID: "Person.kt::Person.greet@sha", FilePath: "Person.kt", BlobSHA: "sha",
			Language: "kotlin", Kind: core.KindMethod, Name: "greet", QualifiedName: "greet",
			ParentSymbol: "Person", Signature: "fun greet()",
			CallSites: []core.CallSite{{Callee: "helper", Line: 1}},
		},
	}
	g.Replace(syms, 1)

	if !hasEdge(g, core.EdgeCalls, "Person.kt::Person.greet@sha", "Person.kt::Person.helper@sha") {
		t.Fatalf("bare helper() must implicitly resolve to Person.helper")
	}
}
