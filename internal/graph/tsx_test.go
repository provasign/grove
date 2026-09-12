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

func TestBuildCalls_TSExternalImportedTypeDoesNotBindLocalNamesake(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "server.ts::run@sha", FilePath: "server.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindFunction, Name: "run", QualifiedName: "run",
			RawText:   "import { WebSocket as WsWebSocket } from \"ws\";\nfunction run(socket: WsWebSocket) { socket.close(); }",
			Imports:   []string{core.JSImportAlias("WsWebSocket", "ws#WebSocket")},
			CallSites: []core.CallSite{{Callee: "socket.close", Line: 2}}},
		{ID: "transport.ts::WebSocket@sha", FilePath: "transport.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "WebSocket", QualifiedName: "WebSocket"},
		{ID: "transport.ts::WebSocket.close@sha", FilePath: "transport.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "close", QualifiedName: "WebSocket.close", ParentSymbol: "WebSocket"},
	}, 2)

	if hasEdge(g, core.EdgeCalls, "server.ts::run@sha", "transport.ts::WebSocket.close@sha") {
		t.Fatalf("external imported WsWebSocket must not bind an unrelated local WebSocket.close")
	}
}

func TestBuildCalls_TSForEachCallbackNarrowsByCollectionElement(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "namespace.ts::ParentNamespace@sha", FilePath: "namespace.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "ParentNamespace", QualifiedName: "ParentNamespace",
			RawText: "class ParentNamespace { private children: Set<Namespace> = new Set(); }"},
		{ID: "namespace.ts::ParentNamespace.emit@sha", FilePath: "namespace.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "emit", QualifiedName: "ParentNamespace.emit", ParentSymbol: "ParentNamespace",
			RawText:   "emit(ev) { this.children.forEach((nsp) => { nsp.emit(ev); }); }",
			CallSites: []core.CallSite{{Callee: "nsp.emit", Line: 1}}},
		{ID: "namespace.ts::Namespace.emit@sha", FilePath: "namespace.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "emit", QualifiedName: "Namespace.emit", ParentSymbol: "Namespace"},
		{ID: "namespace.ts::Socket.emit@sha", FilePath: "namespace.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "emit", QualifiedName: "Socket.emit", ParentSymbol: "Socket"},
	}, 2)

	if !hasEdge(g, core.EdgeCalls, "namespace.ts::ParentNamespace.emit@sha", "namespace.ts::Namespace.emit@sha") {
		t.Fatalf("missing narrowed call to collection element method")
	}
	if hasEdge(g, core.EdgeCalls, "namespace.ts::ParentNamespace.emit@sha", "namespace.ts::Socket.emit@sha") {
		t.Fatalf("collection callback must not fan out to unrelated Socket.emit")
	}
}

func TestBuildCalls_TSConstructedReceiverFindsCrossFileInheritedMethod(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "use.ts::run@sha", FilePath: "x/use.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindFunction, Name: "run", QualifiedName: "run",
			RawText:   "import { L1 } from './l1';\nfunction run() { new L1().rootMethod(); }",
			Imports:   []string{"./l1"},
			CallSites: []core.CallSite{{Callee: "L1().rootMethod", Line: 2}}},
		{ID: "l1.ts::L1@sha", FilePath: "x/l1.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "L1", QualifiedName: "L1",
			Signature: "export class L1 extends L0", Imports: []string{"./l0"}},
		{ID: "l0.ts::L0@sha", FilePath: "x/l0.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "L0", QualifiedName: "L0",
			Signature: "export class L0"},
		{ID: "l0.ts::L0.rootMethod@sha", FilePath: "x/l0.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "rootMethod", QualifiedName: "L0.rootMethod", ParentSymbol: "L0"},
	}, 3)

	if !hasEdge(g, core.EdgeCalls, "use.ts::run@sha", "l0.ts::L0.rootMethod@sha") {
		t.Fatal("constructed subclass receiver did not reach inherited method outside direct import scope")
	}
}
