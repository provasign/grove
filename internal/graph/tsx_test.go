// TSX is a first-class astkit call-site language: .tsx files carry Language
// "tsx" (see internal/parser/languages.go). These tests pin the two places
// edges.go must treat "tsx" exactly like "typescript": the astCallSiteLanguages
// allowlist (empty CallSites is authoritative, no regex fallback) and the
// local-type branch in buildCalls (receiver narrowing via tsLocalTypes).
// Both regressed silently before enrollment because no eval pin is a .tsx repo.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestBuildCalls_TSXEmptyCallSitesSkipsRegexFallback(t *testing.T) {
	// A .tsx symbol whose astkit extraction found no call sites must NOT fall
	// through to the regex fallback and fabricate an edge from the body text.
	// Before "tsx" was in astCallSiteLanguages this produced a bogus edge.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "App.tsx::Component@sha", FilePath: "App.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindFunction, Name: "Component", QualifiedName: "Component",
			RawText: "function Component() { return helper(); }"},
		{ID: "App.tsx::helper@sha", FilePath: "App.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper",
			RawText: "function helper() {}"},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "App.tsx::Component@sha", "App.tsx::helper@sha") {
		t.Fatalf("tsx empty-CallSites must be authoritative; got fabricated regex-fallback edge")
	}
}

func TestBuildCalls_TSXReceiverNarrowsByLocalType(t *testing.T) {
	// A typed local (`const c: Cart = ...`) plus a receiver-qualified call site
	// (`c.checkout`) must narrow to Cart.checkout, not the same-named method on
	// an unrelated class. Without "tsx" in the local-type switch, localTypes is
	// nil and both candidates survive as fanout.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "cart.tsx::buy@sha", FilePath: "cart.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindFunction, Name: "buy", QualifiedName: "buy",
			RawText:   "function buy() { const c: Cart = new Cart(); c.checkout(); }",
			CallSites: []core.CallSite{{Callee: "c.checkout", Line: 1}}},
		{ID: "cart.tsx::Cart@sha", FilePath: "cart.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindClass, Name: "Cart", QualifiedName: "Cart"},
		{ID: "cart.tsx::Cart.checkout@sha", FilePath: "cart.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindMethod, Name: "checkout", QualifiedName: "Cart.checkout", ParentSymbol: "Cart"},
		{ID: "cart.tsx::Wishlist@sha", FilePath: "cart.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindClass, Name: "Wishlist", QualifiedName: "Wishlist"},
		{ID: "cart.tsx::Wishlist.checkout@sha", FilePath: "cart.tsx", BlobSHA: "sha",
			Language: "tsx", Kind: core.KindMethod, Name: "checkout", QualifiedName: "Wishlist.checkout", ParentSymbol: "Wishlist"},
	}, 1)

	if !hasEdge(g, core.EdgeCalls, "cart.tsx::buy@sha", "cart.tsx::Cart.checkout@sha") {
		t.Fatalf("missing narrowed calls edge buy→Cart.checkout (local type Cart)")
	}
	if hasEdge(g, core.EdgeCalls, "cart.tsx::buy@sha", "cart.tsx::Wishlist.checkout@sha") {
		t.Fatalf("calls edge MUST NOT reach Wishlist.checkout — receiver is typed Cart")
	}
}
