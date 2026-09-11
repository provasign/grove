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

func TestCalls_PHPStaticReturnTypeNarrowsFluentChain(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Cart.php::Cart.run", FilePath: "Cart.php", Language: "php", Kind: core.KindMethod, Name: "run", QualifiedName: "Cart.run", ParentSymbol: "Cart", RawText: "function run() { self::make()->done(); }", CallSites: []core.CallSite{{Callee: "make().done", Line: 1}}},
		{ID: "Cart.php::Cart.make", FilePath: "Cart.php", Language: "php", Kind: core.KindMethod, Name: "make", QualifiedName: "Cart.make", ParentSymbol: "Cart", Signature: "public static function make(): static", RawText: "public static function make(): static { return new static(); }"},
		{ID: "Cart.php::Cart.done", FilePath: "Cart.php", Language: "php", Kind: core.KindMethod, Name: "done", QualifiedName: "Cart.done", ParentSymbol: "Cart"},
		{ID: "Other.php::Other.done", FilePath: "Other.php", Language: "php", Kind: core.KindMethod, Name: "done", QualifiedName: "Other.done", ParentSymbol: "Other"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "Cart.php::Cart.run", "Cart.php::Cart.done") {
		t.Fatal("static return type did not narrow fluent chain to its declaring class")
	}
}

func TestCalls_PHPUseImportNarrowsSameNamedClass(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/A/User.php::User", FilePath: "src/A/User.php", Language: "php", Kind: core.KindClass, Name: "User", QualifiedName: "User"},
		{ID: "src/A/User.php::User.save", FilePath: "src/A/User.php", Language: "php", Kind: core.KindMethod, Name: "save", QualifiedName: "User.save", ParentSymbol: "User"},
		{ID: "src/B/User.php::User", FilePath: "src/B/User.php", Language: "php", Kind: core.KindClass, Name: "User", QualifiedName: "User"},
		{ID: "src/B/User.php::User.save", FilePath: "src/B/User.php", Language: "php", Kind: core.KindMethod, Name: "save", QualifiedName: "User.save", ParentSymbol: "User"},
		{ID: "src/App/Use.php::run", FilePath: "src/App/Use.php", Language: "php", Kind: core.KindFunction, Name: "run", QualifiedName: "run", Signature: "function run(User $u)", RawText: "function run(User $u) { $u->save(); }", Imports: []string{"A\\User"}, CallSites: []core.CallSite{{Callee: "u.save", Line: 1}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "src/App/Use.php::run", "src/A/User.php::User.save") {
		t.Fatal("imported A\\User.save was not selected")
	}
	if hasEdge(g, core.EdgeCalls, "src/App/Use.php::run", "src/B/User.php::User.save") {
		t.Fatal("use A\\User leaked to B\\User.save")
	}
}

func TestCalls_PHPTraitConflictResolution(t *testing.T) {
	rules := phpTraitRulesFor(`class Service {
use LoggerAware, Cacheable {
    LoggerAware::log insteadof Cacheable;
    Cacheable::log as cacheLog;
}
}`)
	if alias, ok := rules.aliases["cachelog"]; !ok || alias.trait != "cacheable" || alias.method != "log" {
		t.Fatalf("trait alias parse failed: %+v", rules)
	}
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "traits.php::LoggerAware", FilePath: "traits.php", Language: "php", Kind: core.KindTrait, Name: "LoggerAware", QualifiedName: "LoggerAware"},
		{ID: "traits.php::LoggerAware.log", FilePath: "traits.php", Language: "php", Kind: core.KindMethod, Name: "log", QualifiedName: "LoggerAware.log", ParentSymbol: "LoggerAware", Signature: "function log($message)"},
		{ID: "traits.php::Cacheable", FilePath: "traits.php", Language: "php", Kind: core.KindTrait, Name: "Cacheable", QualifiedName: "Cacheable"},
		{ID: "traits.php::Cacheable.log", FilePath: "traits.php", Language: "php", Kind: core.KindMethod, Name: "log", QualifiedName: "Cacheable.log", ParentSymbol: "Cacheable", Signature: "function log($message)"},
		{ID: "svc.php::Service", FilePath: "svc.php", Language: "php", Kind: core.KindClass, Name: "Service", QualifiedName: "Service", RawText: `class Service {
use LoggerAware, Cacheable {
    LoggerAware::log insteadof Cacheable;
    Cacheable::log as cacheLog;
}
}`},
		{ID: "svc.php::Service.run", FilePath: "svc.php", Language: "php", Kind: core.KindMethod, Name: "run", QualifiedName: "Service.run", ParentSymbol: "Service", RawText: "function run() { $this->log('x'); }", CallSites: []core.CallSite{{Callee: "this.log", Argc: 1}}},
		{ID: "svc.php::Service.aliasRun", FilePath: "svc.php", Language: "php", Kind: core.KindMethod, Name: "aliasRun", QualifiedName: "Service.aliasRun", ParentSymbol: "Service", RawText: "function aliasRun() { $this->cacheLog('y'); }", CallSites: []core.CallSite{{Callee: "this.cacheLog", Argc: 1}}},
	}, 1)

	if !hasEdge(g, core.EdgeCalls, "svc.php::Service.run", "traits.php::LoggerAware.log") {
		t.Fatal("insteadof winner LoggerAware::log was not selected")
	}
	if hasEdge(g, core.EdgeCalls, "svc.php::Service.run", "traits.php::Cacheable.log") {
		t.Fatal("insteadof loser Cacheable::log remained a direct target")
	}
	if !hasEdge(g, core.EdgeCalls, "svc.php::Service.aliasRun", "traits.php::Cacheable.log") {
		t.Fatal("trait alias cacheLog did not resolve to Cacheable::log")
	}
	for _, traitID := range []string{"traits.php::LoggerAware", "traits.php::Cacheable"} {
		if !hasEdge(g, core.EdgeImplements, "svc.php::Service", traitID) {
			t.Fatalf("graph fallback did not link composed trait %s", traitID)
		}
	}
}
