# Grove Roadmap

Grove is the core code intelligence graph used by Prism, Fuse, and Relay. It is open source (MIT).

## v0.1.0 — Parser & Graph Foundation
_Target: Phase 1–3 of Implementation Plan_

- [ ] Tree-sitter parser for 7 languages: Go, TypeScript, JavaScript, Python, Java, Rust, plus Node.js module resolution
- [ ] Symbol extractor per language: functions, classes, methods, types, interfaces, consts
- [ ] SQLite schema: `symbols`, `edges`, `files`, `file_index` tables; WAL mode; FTS5 virtual table
- [ ] 8 edge types: defines, contains, imports, extends, implements, calls, tests, uses-type
- [ ] Delta indexing: skip unchanged files via git blob SHA

## v0.2.0 — Query Engine & CLI
_Target: Phase 4–6 of Implementation Plan_

- [ ] Intent query: BFS traversal from seed symbols, returns ranked candidates
- [ ] Impact analysis: reverse BFS for blast-radius computation
- [ ] Dependency resolution: transitive deps + dependents
- [ ] Test mapping: `tests` edge traversal
- [ ] CLI: `grove index`, `grove query`, `grove impact`, `grove deps`, `grove tests`, `grove symbols`, `grove status`, `grove serve`
- [ ] MCP server: 8 tools (`grove_index`, `grove_query`, `grove_impact`, `grove_deps`, `grove_tests`, `grove_icr`, `grove_conflicts`, `grove_symbols`) over stdio + HTTP+SSE
- [ ] HTTP API: REST endpoints at `:7777`
- [ ] gRPC API: Protobuf service at `:7778`

## v0.3.0 — ICR Engine & VS Code Extension
_Target: Phase 5, 7 of Implementation Plan_

- [ ] ICR (Intent-Conflict-Resolution) engine: symbol-level lock table, conflict detection
- [ ] VS Code extension: auto-index on save, sidebar stats, inline impact decorations, `vscode.lm.registerTool` for all 8 tools
- [ ] Published to VS Code Marketplace

## v0.4.0 — Embeddings & Performance
_Target: Phase 8–9 of Implementation Plan_

- [ ] ONNX Runtime integration: `all-MiniLM-L6-v2` (384-dim) for semantic queries
- [ ] TF-IDF fallback (zero external deps, default)
- [ ] Benchmark suite: index 10K files < 30 s; `grove_query` < 100 ms; symbol lookup < 10 ms

## v1.0.0 — Production Ready

- [ ] 897 tests passing (unit + integration + e2e)
- [ ] Single binary distribution: `brew install grove`, `curl | sh`, GitHub Releases
- [ ] Go module published: `github.com/org/grove`
- [ ] API stability guarantee for v0.2.0 HTTP/gRPC contracts
