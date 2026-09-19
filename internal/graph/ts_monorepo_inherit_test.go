package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// socket.io's monorepo declares `class Transport` in both engine.io and
// engine.io-client. `this.onError()` inside the client's Fetch transport
// inherits from the Transport its module imports, not the server's
// same-named class (P 0.8984 → 0.9029 on socket.io).
func TestTSInheritance_ResolvesBaseClassThroughImports(t *testing.T) {
	g := New()
	ts := func(id, file string, kind core.SymbolKind, name, parent, sig string, imports []string, sites ...core.CallSite) core.SymbolRecord {
		return core.SymbolRecord{
			ID: id, FilePath: file, BlobSHA: "sha", Language: "typescript", Kind: kind,
			Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig,
			Imports: imports, CallSites: sites,
		}
	}
	syms := []core.SymbolRecord{
		ts("packages/engine.io/lib/transport.ts::Transport@sha", "packages/engine.io/lib/transport.ts", core.KindClass, "Transport", "", "export abstract class Transport extends EventEmitter", nil),
		ts("packages/engine.io/lib/transport.ts::Transport.onError@sha", "packages/engine.io/lib/transport.ts", core.KindMethod, "onError", "Transport", "onError(msg: string, desc?: string)", nil),
		ts("packages/engine.io-client/lib/transport.ts::Transport@sha", "packages/engine.io-client/lib/transport.ts", core.KindClass, "Transport", "", "export abstract class Transport extends Emitter", nil),
		ts("packages/engine.io-client/lib/transport.ts::Transport.onError@sha", "packages/engine.io-client/lib/transport.ts", core.KindMethod, "onError", "Transport", "protected onError(reason: string, description: any, context?: any)", nil),
		ts("packages/engine.io-client/lib/transports/polling.ts::Polling@sha", "packages/engine.io-client/lib/transports/polling.ts", core.KindClass, "Polling", "", "export abstract class Polling extends Transport", []string{"../transport.js"}),
		ts("packages/engine.io-client/lib/transports/polling-fetch.ts::Fetch@sha", "packages/engine.io-client/lib/transports/polling-fetch.ts", core.KindClass, "Fetch", "", "export class Fetch extends Polling", []string{"./polling.js"}),
		ts("packages/engine.io-client/lib/transports/polling-fetch.ts::Fetch.doPoll@sha", "packages/engine.io-client/lib/transports/polling-fetch.ts", core.KindMethod, "doPoll", "Fetch", "doPoll()", []string{"./polling.js"},
			core.CallSite{Callee: "this.onError", Line: 1, Argc: 2, Args: []string{"#String", "res"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "packages/engine.io-client/lib/transports/polling-fetch.ts::Fetch.doPoll@sha", "packages/engine.io-client/lib/transport.ts::Transport.onError@sha") {
		t.Fatalf("this.onError() must resolve to the client package's Transport.onError")
	}
	if hasEdge(g, core.EdgeCalls, "packages/engine.io-client/lib/transports/polling-fetch.ts::Fetch.doPoll@sha", "packages/engine.io/lib/transport.ts::Transport.onError@sha") {
		t.Fatalf("this.onError() must not resolve to the server package's same-named Transport")
	}
}
