# Grove Roadmap

Grove is the core code intelligence graph used by Prism, Fuse, and Relay. MIT licensed.

---

## v0.1.0 — Parser & Graph Foundation ✅ shipped

- [x] Tree-sitter parser for 11 languages: Go, TypeScript, TSX, JavaScript, Python, Java, Rust, C, C++, C#, PHP
- [x] Symbol extractor per language: functions, classes, methods, types, interfaces, consts, vars
- [x] SQLite schema: `symbols`, `edges`, `files` tables; WAL mode; FTS5 virtual table
- [x] 8 edge types: defines, contains, imports, extends, implements, calls, tests, uses-type
- [x] Delta indexing: skip unchanged files via git blob SHA
- [x] AST-first with regex fallback on parse errors
- [x] Scoped edges (calls/uses-type limited to same-file + imported-files)

---

## v0.2.0 — Query Engine, API & CLI ✅ shipped

- [x] Intent query: BFS traversal from seed symbols, returns ranked candidates (FTS5 + BFS)
- [x] Impact analysis: reverse BFS for blast-radius computation
- [x] Dependency resolution: transitive deps + dependents
- [x] Test mapping: `tests` edge traversal
- [x] ICR (Intent Complexity Rating): symbol count + connected-component decomposition
- [x] CLI: `grove index`, `grove query`, `grove impact`, `grove deps`, `grove tests`, `grove symbols`, `grove status`, `grove serve`, `grove mcp`, `grove grpc`
- [x] MCP server: 8 tools (`grove_index`, `grove_query`, `grove_impact`, `grove_deps`, `grove_tests`, `grove_icr`, `grove_conflicts`, `grove_symbols`) over JSON-RPC 2.0 stdio + HTTP/SSE
- [x] HTTP API: REST endpoints at `:7777` with Bearer token auth
- [x] gRPC API: Protobuf service at `:7778`
- [x] Bearer token at `.grove/.token` (mode 0600, generated from `crypto/rand`)
- [x] `127.0.0.1`-only binding (no LAN exposure)

---

## v0.3.0 — VS Code Integration ✅ shipped (via Prism extension)

Grove's symbol count and graph stats are surfaced in VS Code via the **Prism extension** (left status bar). A standalone Grove extension is not needed — Prism owns the VS Code surface and calls Grove directly.

- [x] Grove symbol count displayed in VS Code status bar (left panel, via Prism extension)
- [x] Auto-index on save (via Prism extension `autoIndex` setting)
- [x] All 8 `grove_*` tools available to Copilot Agent mode (via Prism extension `languageModelTools`)

---

## v0.4.0 — Embeddings & Performance ✅ shipped

- [x] TF-IDF local embeddings (zero external deps, default)
- [x] ONNX Runtime backend: `all-MiniLM-L6-v2` (384-dim) opt-in via `embeddings_backend: onnx`
- [x] Benchmark validated: index 5K files < 5 s · BFS depth-3 on 50K nodes < 30 ms · FTS5 query < 10 ms

---

## v1.0.0 — Production Hardening

- [x] 11-language parser coverage
- [x] Single binary, zero runtime dependencies (pure-Go SQLite via `modernc.org/sqlite`)
- [ ] Homebrew tap: `brew install grove`
- [ ] `curl | sh` installer for Linux
- [ ] API stability guarantee for HTTP/gRPC contracts
- [ ] Published Go module: `github.com/tabladrum/grove-suite/grove`
