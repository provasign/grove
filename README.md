# Grove

Grove is a code intelligence graph exposed through a CLI and HTTP API. This repository now contains a runnable foundation: language detection, symbol extraction, SQLite persistence, delta-aware indexing, an in-memory graph, CLI commands, and HTTP endpoints matching the suite contract.

## Build

```bash
make build
make test
make lint
make install
```

## CLI

```bash
grove init .
grove index .
grove status .
grove symbols AuthService .
grove query "authentication" .
grove impact Login .
grove tests Login .
grove serve --port 7777 .
grove mcp .
grove grpc --port 7778 .
```

## HTTP

```bash
curl http://localhost:7777/health
curl http://localhost:7777/status
curl -X POST http://localhost:7777/index -d '{"dir":"."}'
curl -X POST http://localhost:7777/symbols -d '{"query":"Auth"}'
curl -X POST http://localhost:7777/query -d '{"intent":"auth","limit":10}'
curl -X POST http://localhost:7777/impact -d '{"query":"Login","maxDepth":3}'
curl -X POST http://localhost:7777/tests -d '{"query":"login"}'
curl -X POST http://localhost:7777/icr -d '{"intent":"Login"}'
curl http://localhost:7777/mcp/sse
curl -X POST http://localhost:7777/mcp/call -d '{"name":"grove_query","arguments":{"intent":"Login"}}'
```

## MCP

Grove exposes the 8 planned MCP tools over JSON-RPC stdio:

```bash
grove mcp .
```

Tools: `grove_index`, `grove_query`, `grove_impact`, `grove_deps`, `grove_tests`, `grove_icr`, `grove_conflicts`, `grove_symbols`.

HTTP/SSE mode is available through the regular server:

```bash
grove serve --port 7777 .
curl http://localhost:7777/mcp/sse
curl -X POST http://localhost:7777/mcp/call -d '{"name":"grove_symbols","arguments":{"query":"Auth"}}'
```

## gRPC

The gRPC contract is defined in [proto/grove.proto](proto/grove.proto). The current server is available with:

```bash
grove grpc --port 7778 .
```

The implementation registers `grove.v1.GroveService` with grpc-go and uses a JSON codec internally so the service is buildable without generated code during this pre-1.0 phase.

## Current Scope

Implemented now:

- `.grove/grove.db` SQLite store using WAL mode
- Delta-aware indexing by file content SHA
- Stale-file pruning when indexed files are deleted
- Tree-sitter parse validation for Go, TypeScript, JavaScript, Python, Java, and Rust before extraction
- Symbol search and intent query over the current graph
- Dependency edges for file definitions and imports
- Test edges for common test naming patterns such as `TestLogin` → `Login`
- Impact traversal over reverse graph edges
- ICR computation, conflict detection, and SQLite-backed lock/unlock
- MCP stdio and HTTP/SSE tool surfaces
- gRPC service surface and checked-in proto contract

Still intentionally next:

- Replace conservative regex symbol extraction with full Tree-sitter AST walkers while keeping package/API boundaries stable
- Add full SQLite FTS5 ranking and BFS query package
