// C# generic-overload split (Phase 3). A generic call (Foo<T>(...)) and its
// same-arity non-generic sibling (Foo(...)) collide on name + arity + value-arg
// type; only the call's explicit type arguments distinguish them. astkit now
// emits CallSite.Generic and grove keeps the matching overload. This is what
// moved Newtonsoft.Json precision 0.603 → 0.658 with recall unchanged.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func csOverloadSymbols() []core.SymbolRecord {
	return []core.SymbolRecord{
		{ID: "conv.cs::Conv.Parse#obj@sha", FilePath: "conv.cs", BlobSHA: "sha",
			Language: "csharp", Kind: core.KindMethod, Name: "Parse", QualifiedName: "Conv.Parse", ParentSymbol: "Conv",
			Signature: "public static object Parse(string s)"},
		{ID: "conv.cs::Conv.Parse#T@sha", FilePath: "conv.cs", BlobSHA: "sha",
			Language: "csharp", Kind: core.KindMethod, Name: "Parse", QualifiedName: "Conv.Parse", ParentSymbol: "Conv",
			Signature: "public static T Parse<T>(string s)"},
	}
}

func TestCalls_CSharpGenericCallBindsGenericOverload(t *testing.T) {
	g := New()
	syms := append(csOverloadSymbols(),
		core.SymbolRecord{ID: "app.cs::App.Run@sha", FilePath: "app.cs", BlobSHA: "sha",
			Language: "csharp", Kind: core.KindMethod, Name: "Run", QualifiedName: "App.Run", ParentSymbol: "App",
			Imports:   []string{"conv.cs"},
			CallSites: []core.CallSite{{Callee: "Conv.Parse", Argc: 1, Args: []string{"#String"}, Generic: true}}})
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "app.cs::App.Run@sha", "conv.cs::Conv.Parse#T@sha") {
		t.Fatalf("generic call Conv.Parse<T>() must bind the generic overload")
	}
	if hasEdge(g, core.EdgeCalls, "app.cs::App.Run@sha", "conv.cs::Conv.Parse#obj@sha") {
		t.Fatalf("generic call MUST NOT also bind the non-generic Parse(string) overload")
	}
}

func TestCalls_CSharpPlainCallBindsNonGenericOverload(t *testing.T) {
	g := New()
	syms := append(csOverloadSymbols(),
		core.SymbolRecord{ID: "app.cs::App.Run@sha", FilePath: "app.cs", BlobSHA: "sha",
			Language: "csharp", Kind: core.KindMethod, Name: "Run", QualifiedName: "App.Run", ParentSymbol: "App",
			Imports:   []string{"conv.cs"},
			CallSites: []core.CallSite{{Callee: "Conv.Parse", Argc: 1, Args: []string{"#String"}, Generic: false}}})
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "app.cs::App.Run@sha", "conv.cs::Conv.Parse#obj@sha") {
		t.Fatalf("plain call Conv.Parse() must bind the non-generic overload")
	}
	if hasEdge(g, core.EdgeCalls, "app.cs::App.Run@sha", "conv.cs::Conv.Parse#T@sha") {
		t.Fatalf("plain call MUST NOT bind the generic Parse<T>(string) overload")
	}
}
