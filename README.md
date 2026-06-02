# Grove

> **Your codebase's persistent long-term memory — queryable by any AI agent.**

> **Embedded mode (current):** Grove is a Go library at `github.com/provasign/grove/pkg/grove`. Prism, Fuse, and Provasign link it directly and open the on-disk index in-process. There is no `grove serve` daemon, no port (7777/7778), and no `.grove/.token`. The CLI is still available for one-shot queries (`grove index .`, `grove symbols main`) and stdio MCP (`grove mcp`).

---

Grep answers "does this string appear somewhere?" A language server answers "where is this symbol defined?" Grove answers the harder questions AI agents actually need:

- *What does changing this function break — across the entire codebase?*
- *Which tests cover this method, directly or transitively?*
- *What is the full dependency chain from this file?*
- *What symbols are semantically related to this task description?*

The difference is a graph. Grove indexes your source files into a persistent SQLite graph — 11 languages, 8 edge types, BFS traversal — and keeps it live with delta indexing (files whose git blob SHA hasn't changed are never re-parsed). The graph is queryable over CLI, HTTP API, MCP stdio, and gRPC.

Grove is the foundation all other Provasign tools are built on. Prism uses it to focus context. Fuse uses it to resolve conflicts. Provasign uses it to certify agent output. Without Grove, all three fall back to line-level operations.

---

## Architecture

```
Source files
     │
     ▼
┌─────────────────────────────────────────────────────────┐
│  internal/parser/                                       │
│  Tree-sitter AST walkers (11 languages)                 │
│  Regex fallback for syntax-error recovery               │
│  All CGO is isolated to this package                    │
└────────────────────────┬────────────────────────────────┘
                         │ []SymbolRecord
                         ▼
┌─────────────────────────────────────────────────────────┐
│  internal/store/                                        │
│  SQLite WAL + FTS5                                      │
│  Delta indexing by git blob SHA                         │
│  Stale-file pruning                                     │
└────────────────────────┬────────────────────────────────┘
                         │
                         ▼
┌─────────────────────────────────────────────────────────┐
│  internal/graph/                                        │
│  In-memory CodeGraph                                    │
│  8 edge types                                           │
│  BFS traversal                                          │
└──────┬─────────────────┬───────────────────────────────┘
       │                 │
       ▼                 ▼
┌────────────┐   ┌────────────────────┐
│ internal/  │   │ internal/query/    │
│ mcp/       │   │ Intent → symbols   │
│ 8 tools    │   │ FTS5 + BFS         │
│ JSON-RPC   │   │ Blast radius       │
│ stdio      │   │ ICR computation    │
└────────────┘   └────────┬───────────┘
                          │
                          ▼
                 ┌────────────────────┐
                 │ internal/api/      │
                 │ HTTP :7777         │
                 │ gRPC :7778         │
                 └────────────────────┘
```

---

## Design Decisions

**Single binary, zero runtime dependencies.** SQLite is embedded via `modernc.org/sqlite` — a pure-Go port — which avoids a CGO linker conflict with tree-sitter. Tree-sitter itself (in `internal/parser/`) is the only CGO dependency.

**Delta indexing by git blob SHA.** Grove calls `git hash-object` on each file before parsing. If the blob SHA matches what is stored, the file is skipped entirely. Indexing a 5000-file repo after a one-line change touches one file, not 5000.

**AST-first with regex fallback.** Tree-sitter produces a complete AST even for files with syntax errors, but marks broken subtrees as `ERROR` nodes. When `root.HasError()` is true, Grove runs both the AST extractor and the regex fallback, then merges the results with AST taking precedence. Files that are actively being edited mid-keystroke are still indexed usefully.

**Scoped edges prevent false positives.** `calls` and `uses-type` edges are only created between symbols in the same file or in files connected by an `imports` edge. Without this constraint, a function named `parse` in one package would appear to call a `parse` function in an unrelated package, producing roughly 5× the false-positive edges.

**Symbol ID format.** Every symbol has a canonical ID: `{filePath}::{qualifiedName}@{blobSHA}`. The blob SHA component means that if you rename a function, the old symbol ID disappears and a new one is created — stale references in the graph don't survive a reindex.

---

## Language Support

| Language | Extension(s) | Extractor |
|----------|-------------|-----------|
| Go | `.go` | AST walker |
| TypeScript | `.ts` | AST walker |
| TSX | `.tsx` | AST walker (separate JSX grammar) |
| JavaScript | `.js .jsx .mjs .cjs` | AST walker |
| Python | `.py` | AST walker |
| Java | `.java` | AST walker |
| Rust | `.rs` | AST walker |
| C | `.c .h` | AST walker |
| C++ | `.cc .cpp .cxx .hh .hpp` | AST walker |
| C# | `.cs` | AST walker |
| PHP | `.php .phtml` | AST walker |

Non-code files (`.md`, `.yaml`, `.json`, `.xml`, `.sh`, `.toml`, `.proto`, `.sql`, `Makefile`, `Dockerfile`, and more) are indexed as `document` symbols with their content in the FTS5 full-text index. Agents can query them semantically alongside code symbols.

---

## Graph Edge Types

| Edge | Meaning |
|------|---------|
| `defines` | File defines this symbol |
| `contains` | Class/namespace contains this member |
| `imports` | File imports another file |
| `extends` | Class extends/embeds another |
| `implements` | Class implements an interface |
| `calls` | Function calls another function (scoped) |
| `uses-type` | Function/field uses a type (scoped) |
| `tests` | Test function covers a named symbol |

---

## Performance

Benchmarks run on macOS against synthetic Go projects (2026-05-27). Numbers reflect a cold index (no prior SHA cache). Subsequent runs on an unchanged project complete in milliseconds regardless of project size — only modified files are re-parsed.

| Project | Files | Index time | Peak RSS | Query latency |
|---------|------:|----------:|---------:|--------------:|
| Small | 61 | 0.06 s | 30 MB | 6 ms |
| Medium | 801 | 0.85 s | 55 MB | 6 ms |
| Large | 4,501 | 11.6 s | 117 MB | 9 ms |
| Monorepo | 9,901 | 34.0 s | 196 MB | 61 ms |

Query latency is FTS5 full-text search + BFS graph traversal returning ranked results. RSS scales with project size because the in-memory graph is loaded at serve time.

**Targets:** index 5,000 files < 5 s · BFS depth-3 on 50K nodes < 30 ms · FTS5 query < 10 ms

---

## Tool and IDE Integration

Grove is the backend for the entire suite. Direct AI agent integration is via MCP stdio or HTTP/SSE; Prism, Fuse, and Provasign consume the HTTP API.

| Integration | How | Use case |
|-------------|-----|---------|
| Claude Code CLI | `grove mcp .` → MCP stdio | Direct agent integration without Prism |
| Cursor, Windsurf, Zed | `grove mcp .` → MCP stdio | Same |
| VS Code (Copilot Agent) | Prism extension → HTTP API `:7777` | All 8 `grove_*` tools via `#groveIndex`, `#groveQuery`, etc. |
| Prism (all IDEs) | HTTP API `:7777` | Token-optimized context delivery |
| Fuse (git merge) | HTTP API `:7777` | Blast radius + breaking change detection |
| Provasign | HTTP API + gRPC `:7778` | Intent lifecycle and certification |
| Custom automation | HTTP API `:7777` | Any tool that can make HTTP requests |

For most AI agent use cases, running Grove directly is only necessary for custom integrations. The normal path is `prism init` in your project, which starts Grove automatically.

---

## Installation

**Binary install (fastest):**

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/provasign/grove/main/install.ps1 | iex

# Pin a specific version
VERSION=v0.4.0 curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash
```

Installs to `~/bin` by default. Set `INSTALL_DIR=/usr/local/bin` to override.

**Build from source:**

```bash
make build    # compile ./bin/grove
make install  # install to $GOPATH/bin
make test     # run all tests
```

---

## CLI Reference

```bash
# Set up a project (creates .grove/ directory and config)
grove init [dir]

# Index or reindex (skips unchanged files via delta SHA)
grove index [dir]

# Show index status (file count, symbol count, last index time)
grove status [dir]

# Symbol search
grove symbols <query> [dir]

# Intent-based semantic query (Model2Vec embeddings + BFS graph ranking)
grove query <intent> [dir]

# Blast radius: what would break if this symbol changed?
grove impact <symbol> [dir]

# Which tests cover a symbol?
grove tests <symbol> [dir]

# Start HTTP server (binds to 127.0.0.1:7777)
grove serve [--port 7777] [dir]

# Start MCP stdio server (primary AI agent integration)
grove mcp [dir]

# Start gRPC server
grove grpc [--port 7778] [dir]
```

---

## HTTP API

All endpoints require `Authorization: Bearer <token>` (token at `.grove/.token`) except `/health`.

```bash
GET  /health
GET  /status
POST /index     {"dir": string}
POST /symbols   {"query": string}
POST /query     {"intent": string, "limit": int}
POST /impact    {"query": string, "maxDepth": int}
POST /deps      {"file": string}
POST /tests     {"query": string}
POST /icr       {"intent": string}
```

---

## MCP Tools

Grove exposes eight tools over JSON-RPC 2.0 stdio, accessible to any MCP-capable AI agent:

| Tool | Purpose |
|------|---------|
| `grove_index` | Index or reindex a directory |
| `grove_symbols` | Search for symbols by name |
| `grove_query` | Retrieve ranked context for an intent |
| `grove_impact` | Blast radius for a symbol or file |
| `grove_deps` | Dependency tree for a file |
| `grove_tests` | Tests that cover a symbol |
| `grove_icr` | Intent complexity rating |
| `grove_conflicts` | Potential conflict hotspots |

Start the MCP server:

```bash
grove mcp .
```

HTTP/SSE mode (for tools that prefer HTTP over stdio):

```bash
grove serve .
curl http://localhost:7777/mcp/sse
curl -X POST http://localhost:7777/mcp/call \
  -H "Authorization: Bearer $(cat .grove/.token)" \
  -d '{"name":"grove_query","arguments":{"intent":"authentication","limit":10}}'
```

---

## Storage

Grove stores everything in `.grove/grove.db` (SQLite, WAL mode). The database is a single file — back it up, copy it, or delete it to force a full reindex. There is no migration tooling; delete and reindex if the schema changes.

Key SQLite settings:
- WAL mode for concurrent reads during indexing
- FTS5 virtual table for full-text symbol search
- `busy_timeout = 30s` to handle contention without immediate errors

---

## Security

Grove binds to `127.0.0.1` — not `0.0.0.0`. The shared secret token at `.grove/.token` (mode 0600) is required on all non-health requests. The token is 64 hex characters generated from `crypto/rand` on first start and is stable across restarts.

---

## Testing

```bash
make test                                          # all packages
go test ./internal/parser/... -run TestGoExtractor # single extractor
go test ./internal/parser/... -v                   # verbose parser tests
```

Key test areas: language extractors (fixture-based), BFS traversal on known graph topologies, delta indexing (SHA skip), token middleware, FTS5 query ranking.
