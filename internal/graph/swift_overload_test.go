package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// SwiftyJSON's constructor set: nine `init`s that all take exactly one
// argument. Arity cannot split them; the labels — part of a Swift
// function's identity — can. Measured: precision 0.11 → 0.95 on that repo.
func swiftJSONInits() []core.SymbolRecord {
	return []core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::JSON.init#object@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "JSON", QualifiedName: "JSON", ParentSymbol: "JSON",
			Signature: "public init(_ object: Any)"},
		{ID: "J.swift::JSON.init#parseJSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "JSON", QualifiedName: "JSON", ParentSymbol: "JSON",
			Signature: "public init(parseJSON jsonString: String)"},
		{ID: "J.swift::JSON.init#jsonObject@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "JSON", QualifiedName: "JSON", ParentSymbol: "JSON",
			Signature: "fileprivate init(jsonObject: Any)"},
		{ID: "J.swift::JSON.init#data@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "JSON", QualifiedName: "JSON", ParentSymbol: "JSON",
			Signature: "public init(data: Data, options opt: JSONSerialization.ReadingOptions = []) throws"},
	}
}

func swiftCaller(callee string, args ...string) core.SymbolRecord {
	return core.SymbolRecord{ID: "J.swift::JSON.caller@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
		Kind: core.KindMethod, Name: "caller", QualifiedName: "caller", ParentSymbol: "JSON",
		Signature: "func caller()",
		CallSites: []core.CallSite{{Callee: callee, Line: 1, Argc: len(args), Args: args}}}
}

func TestSwiftDelegatingInitBindsByLabel(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"labeled", []string{"jsonObject:object"}, "J.swift::JSON.init#jsonObject@sha"},
		{"unlabeled", []string{"_:object"}, "J.swift::JSON.init#object@sha"},
		{"defaulted param omitted", []string{"data:d"}, "J.swift::JSON.init#data@sha"},
		{"defaulted param given", []string{"data:d", "options:o"}, "J.swift::JSON.init#data@sha"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := New()
			g.Replace(append(swiftJSONInits(), swiftCaller("self.init", tc.args...)), 1)
			for _, ctor := range swiftJSONInits()[1:] {
				has := hasEdge(g, core.EdgeCalls, "J.swift::JSON.caller@sha", ctor.ID)
				if has != (ctor.ID == tc.want) {
					t.Errorf("edge to %s = %v (args %v)", ctor.ID, has, tc.args)
				}
			}
		})
	}
}

func TestSwiftDirectConstructionBindsByLabel(t *testing.T) {
	g := New()
	g.Replace(append(swiftJSONInits(), swiftCaller("JSON", "parseJSON:#String")), 1)
	for _, ctor := range swiftJSONInits()[1:] {
		has := hasEdge(g, core.EdgeCalls, "J.swift::JSON.caller@sha", ctor.ID)
		if has != (ctor.ID == "J.swift::JSON.init#parseJSON@sha") {
			t.Errorf("edge to %s = %v", ctor.ID, has)
		}
	}
}

func TestSwiftLabelMismatchDropsLoneSibling(t *testing.T) {
	// merge(with:typecheck:) calling merge(with:, typecheck:) is
	// recursion; its only same-name sibling merge(with:) has different
	// labels and must not be bound just because it is the only candidate.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::JSON.merge1@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "merge", QualifiedName: "merge", ParentSymbol: "JSON",
			Signature: "public mutating func merge(with other: JSON) throws"},
		{ID: "J.swift::JSON.merge2@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "merge", QualifiedName: "merge", ParentSymbol: "JSON",
			Signature: "fileprivate mutating func merge(with other: JSON, typecheck: Bool) throws",
			CallSites: []core.CallSite{{Callee: "merge", Line: 1, Argc: 2, Args: []string{"with:o", "typecheck:#boolean"}}}},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "J.swift::JSON.merge2@sha", "J.swift::JSON.merge1@sha") {
		t.Fatalf("merge(with:typecheck:) must not bind merge(with:) — the labels differ")
	}
	if !hasEdge(g, core.EdgeCalls, "J.swift::JSON.merge2@sha", "J.swift::JSON.merge2@sha") {
		t.Fatalf("the recursive call must still be recorded")
	}
}

func TestSwiftTrailingClosureSatisfiesLastParam(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "U.swift::each@sha", FilePath: "U.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindFunction, Name: "each", QualifiedName: "each",
			Signature: "func each(_ items: [Int], _ body: (Int) -> Void)"},
		{ID: "U.swift::run@sha", FilePath: "U.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindFunction, Name: "run", QualifiedName: "run", Signature: "func run()",
			// `each(xs) { ... }`: the trailing closure is not in the
			// argument list astkit records.
			CallSites: []core.CallSite{{Callee: "each", Line: 1, Argc: 1, Args: []string{"_:xs"}}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "U.swift::run@sha", "U.swift::each@sha") {
		t.Fatalf("a trailing closure must satisfy the last function-typed parameter")
	}
}

func TestSwiftUnknownReceiverDropsCandidates(t *testing.T) {
	// `rawArray[idx]` on an untyped stored property: Swift is statically
	// typed, so an unknown receiver is not "any subscript in the repo".
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::JSON.subscript@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: [JSONSubscriptType]) -> JSON"},
		{ID: "J.swift::JSON.count@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "count", QualifiedName: "count", ParentSymbol: "JSON",
			Signature: "func count() -> Int",
			CallSites: []core.CallSite{
				{Callee: "rawArray.subscript", Line: 1, Argc: 1, Args: []string{"_:idx"}},
				{Callee: "self.subscript", Line: 2, Argc: 1, Args: []string{"_:path"}},
			}},
	}, 1)
	// Exactly one edge: the self subscript, not the untyped-field one.
	edges := 0
	for _, e := range g.edges {
		if e.Type == core.EdgeCalls && e.From == "J.swift::JSON.count@sha" && e.To == "J.swift::JSON.subscript@sha" {
			edges++
		}
	}
	if edges != 1 {
		t.Fatalf("count -> subscript edges = %d, want exactly 1 (self only)", edges)
	}
}

func TestSwiftArgShapePicksExactOverload(t *testing.T) {
	// Inside `subscript(path: JSONSubscriptType...)`, `self[path]` passes an
	// array (a variadic parameter is one in its body): the `[T]` overload,
	// not `subscript(position: Index)`, both unlabeled and one-arg.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::sub.position@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript (position: Index) -> (String, JSON)"},
		{ID: "J.swift::sub.array@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: [JSONSubscriptType]) -> JSON"},
		{ID: "J.swift::sub.variadic@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: JSONSubscriptType...) -> JSON",
			CallSites: []core.CallSite{{Callee: "self.subscript", Line: 1, Argc: 1, Args: []string{"_:path"}}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "J.swift::sub.variadic@sha", "J.swift::sub.array@sha") {
		t.Fatalf("self[path] with path: T... must bind subscript(path: [T])")
	}
	if hasEdge(g, core.EdgeCalls, "J.swift::sub.variadic@sha", "J.swift::sub.position@sha") {
		t.Fatalf("self[path] must not bind subscript(position: Index)")
	}
}

func TestSwiftRecursiveExactMatchExcludesSiblings(t *testing.T) {
	// Inside `subscript(path: [JSONSubscriptType])`, `self[path]` is
	// recursion: the caller is the one exact type match, so the inexact
	// siblings (Index, variadic) are not candidates at all.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::sub.position@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript (position: Index) -> (String, JSON)"},
		{ID: "J.swift::sub.variadic@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: JSONSubscriptType...) -> JSON"},
		{ID: "J.swift::sub.array@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: [JSONSubscriptType]) -> JSON",
			CallSites: []core.CallSite{{Callee: "self.subscript", Line: 1, Argc: 1, Args: []string{"_:path"}}}},
	}, 1)
	for _, id := range []string{"J.swift::sub.position@sha", "J.swift::sub.variadic@sha"} {
		if hasEdge(g, core.EdgeCalls, "J.swift::sub.array@sha", id) {
			t.Errorf("recursive self[path] must not bind %s", id)
		}
	}
	if !hasEdge(g, core.EdgeCalls, "J.swift::sub.array@sha", "J.swift::sub.array@sha") {
		t.Errorf("the recursive call itself must be recorded")
	}
}

func TestSwiftSelfAliasReceiverIsTyped(t *testing.T) {
	// `var merged = self; merged.merge(with:typecheck:)` — the alias is the
	// enclosing type, not an unknown receiver to drop.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::JSON.merge@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "merge", QualifiedName: "merge", ParentSymbol: "JSON",
			Signature: "fileprivate mutating func merge(with other: JSON, typecheck: Bool) throws"},
		{ID: "J.swift::JSON.merged@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "merged", QualifiedName: "merged", ParentSymbol: "JSON",
			Signature: "public func merged(with other: JSON) throws -> JSON",
			RawText:   "public func merged(with other: JSON) throws -> JSON {\n var merged = self\n try merged.merge(with: other, typecheck: true)\n return merged\n}",
			CallSites: []core.CallSite{{Callee: "merged.merge", Line: 3, Argc: 2, Args: []string{"with:other", "typecheck:#boolean"}}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "J.swift::JSON.merged@sha", "J.swift::JSON.merge@sha") {
		t.Fatalf("merged.merge(...) on a `var merged = self` alias must resolve")
	}
}

func TestSwiftArrayReceiverIsExternal(t *testing.T) {
	// `path[0]` on `path: [JSONSubscriptType]` subscripts an Array — the
	// standard library's — not the in-repo type named by the element.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "J.swift::JSONSubscriptType@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindInterface, Name: "JSONSubscriptType", QualifiedName: "JSONSubscriptType", Signature: "protocol JSONSubscriptType"},
		{ID: "J.swift::JSON@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON", Signature: "struct JSON"},
		{ID: "J.swift::JSON.subscript@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "subscript", QualifiedName: "JSON.subscript", ParentSymbol: "JSON",
			Signature: "public subscript(path: JSONSubscriptType...) -> JSON"},
		{ID: "J.swift::JSON.walk@sha", FilePath: "J.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindMethod, Name: "walk", QualifiedName: "walk", ParentSymbol: "JSON",
			Signature: "func walk(path: [JSONSubscriptType])",
			CallSites: []core.CallSite{{Callee: "path.subscript", Line: 1, Argc: 1, Args: []string{"_:#int"}}}},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "J.swift::JSON.walk@sha", "J.swift::JSON.subscript@sha") {
		t.Fatalf("an Array receiver must not resolve to the element type's subscript")
	}
}

func TestSwiftSuperInitResolvesToSuperclassConstructor(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "A.swift::Animal@sha", FilePath: "A.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindClass, Name: "Animal", QualifiedName: "Animal", Signature: "class Animal"},
		{ID: "A.swift::Animal.init@sha", FilePath: "A.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "Animal", QualifiedName: "Animal", ParentSymbol: "Animal",
			Signature: "init(name: String)"},
		{ID: "D.swift::Dog@sha", FilePath: "D.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindClass, Name: "Dog", QualifiedName: "Dog", Signature: "class Dog: Animal"},
		{ID: "D.swift::Dog.init@sha", FilePath: "D.swift", BlobSHA: "sha", Language: "swift",
			Kind: core.KindConstructor, Name: "Dog", QualifiedName: "Dog", ParentSymbol: "Dog",
			Signature: "init(name: String)",
			CallSites: []core.CallSite{{Callee: "super.init", Line: 1, Argc: 1, Args: []string{"name:name"}}}},
	}, 2)
	if !hasEdge(g, core.EdgeCalls, "D.swift::Dog.init@sha", "A.swift::Animal.init@sha") {
		t.Fatalf("super.init(name:) must resolve to Animal's constructor")
	}
	if hasEdge(g, core.EdgeCalls, "D.swift::Dog.init@sha", "D.swift::Dog.init@sha") {
		t.Fatalf("super.init must not bind the caller's own constructor")
	}
}
