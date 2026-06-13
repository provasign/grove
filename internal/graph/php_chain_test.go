// PHP fluent-chain receiver resolution (Phase 3 precision lever). A call-result
// receiver ($builder->make()->method()) is emitted by astkit as a flat "()"
// qualifier. When the result type is inferable the chain narrows to that class;
// when an ambiguous self-returning builder method spans several classes it must
// drop rather than fan out to every same-named downstream method. This is what
// moved php-parser precision 0.53 → 0.77.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestCalls_PHPFactoryChainNarrowsToResultType(t *testing.T) {
	// $this->factory()->checkout(): factory() returns `new Cart`, so checkout
	// resolves to Cart.checkout, never the same-named Wishlist.checkout.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "shop.php::ShopTest.buy@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "buy", QualifiedName: "ShopTest.buy", ParentSymbol: "ShopTest",
			RawText:   "function buy() { $this->factory()->checkout(); }",
			CallSites: []core.CallSite{{Callee: "factory().checkout", Line: 1}}},
		{ID: "shop.php::ShopTest.factory@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "factory", QualifiedName: "ShopTest.factory", ParentSymbol: "ShopTest",
			RawText: "function factory() { return new Cart(); }"},
		{ID: "shop.php::Cart.checkout@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "checkout", QualifiedName: "Cart.checkout", ParentSymbol: "Cart"},
		{ID: "shop.php::Wishlist.checkout@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "checkout", QualifiedName: "Wishlist.checkout", ParentSymbol: "Wishlist"},
	}, 1)

	if !hasEdge(g, core.EdgeCalls, "shop.php::ShopTest.buy@sha", "shop.php::Cart.checkout@sha") {
		t.Fatalf("missing narrowed edge buy→Cart.checkout (factory() returns Cart)")
	}
	if hasEdge(g, core.EdgeCalls, "shop.php::ShopTest.buy@sha", "shop.php::Wishlist.checkout@sha") {
		t.Fatalf("edge MUST NOT reach Wishlist.checkout — factory() result is Cart")
	}
}

func TestCalls_PHPAmbiguousFluentChainDrops(t *testing.T) {
	// add() returns $this on BOTH Cart and Wishlist, so the result type of
	// add() is ambiguous: the downstream done() must drop, not fan out to every
	// done(). This is the precision win over the prior all-candidates behavior.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "shop.php::ShopTest.run@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "run", QualifiedName: "ShopTest.run", ParentSymbol: "ShopTest",
			RawText:   "function run() { $c->add()->done(); }",
			CallSites: []core.CallSite{{Callee: "add().done", Line: 1}}},
		{ID: "shop.php::Cart.add@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "add", QualifiedName: "Cart.add", ParentSymbol: "Cart",
			RawText: "function add() { return $this; }"},
		{ID: "shop.php::Cart.done@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "done", QualifiedName: "Cart.done", ParentSymbol: "Cart"},
		{ID: "shop.php::Wishlist.add@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "add", QualifiedName: "Wishlist.add", ParentSymbol: "Wishlist",
			RawText: "function add() { return $this; }"},
		{ID: "shop.php::Wishlist.done@sha", FilePath: "shop.php", BlobSHA: "sha",
			Language: "php", Kind: core.KindMethod, Name: "done", QualifiedName: "Wishlist.done", ParentSymbol: "Wishlist"},
	}, 1)

	for _, to := range []string{"shop.php::Cart.done@sha", "shop.php::Wishlist.done@sha"} {
		if hasEdge(g, core.EdgeCalls, "shop.php::ShopTest.run@sha", to) {
			t.Fatalf("ambiguous fluent chain must not edge to %s", to)
		}
	}
}
