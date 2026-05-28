# Grove

Grove is a persistent code knowledge graph. It parses source files, extracts symbols, links them into a graph, and makes that graph queryable — over a CLI, an HTTP API, an MCP stdio server, and a gRPC service.

## Why a Knowledge Graph

Static search (grep, ctags, a language server) answers "where is this symbol defined?" Graph traversal answers harder questions: "what does this function transitively call?", "which tests cover this method?", "what is the full blast radius of changing this interface?" Those are the questions that matter for AI agent task planning and merge conflict resolution.

The graph is persistent (SQLite on disk), delta-aware (files whose git blob SHA hasn't changed are never re-parsed), and scoped (call and type-use edges are constrained to same-file and imported files to suppress false positives).

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

## Design Decisions

**Single binary, zero runtime dependencies.** SQLite is embedded via `modernc.org/sqlite` — a pure-Go port — which avoids a CGO linker conflict with tree-sitter. Tree-sitter itself (in `internal/parser/`) is the only CGO dependency.

**Delta indexing by git blob SHA.** Grove calls `git hash-object` on each file before parsing. If the blob SHA matches what is stored, the file is skipped entirely. Indexing a 5000-file repo after a one-line change touches one file, not 5000.

**AST-first with regex fallback.** Tree-sitter produces a complete AST even for files with syntax errors, but marks broken subtrees as `ERROR` nodes. When `root.HasError()` is true, Grove runs both the AST extractor and the regex fallback, then merges the results with AST taking precedence. Files that are actively being edited mid-keystroke are still indexed usefully.

**Scoped edges prevent false positives.** `calls` and `uses-type` edges are only created between symbols in the same file or in files connected by an `imports` edge. Without this constraint, a function named `parse` in one package would appear to call a `parse` function in an unrelated package, producing roughly 5× the false-positive edges.

**Symbol ID format.** Every symbol has a canonical ID: `{filePath}::{qualifiedName}@{blobSHA}`. The blob SHA component means that if you rename a function, the old symbol ID disappears and a new one is created — stale references in the graph don't survive a reindex.

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

## Installation

```bash
make build    # compile ./bin/grove
make install  # install to $GOPATH/bin
make test     # run all tests
```

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

# Intent-based query (FTS5 + BFS graph ranking)
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

## Storage

Grove stores everything in `.grove/grove.db` (SQLite, WAL mode). The database is a single file — back it up, copy it, or delete it to force a full reindex. There is no migration tooling; delete and reindex if the schema changes.

Key SQLite settings:
- WAL mode for concurrent reads during indexing
- FTS5 virtual table for full-text symbol search
- `busy_timeout = 30s` to handle contention without immediate errors

## Security

Grove binds to `127.0.0.1` — not `0.0.0.0`. The shared secret token at `.grove/.token` (mode 0600) is required on all non-health requests. The token is 64 hex characters generated from `crypto/rand` on first start and is stable across restarts.

## Testing

```bash
make test                                          # all packages
go test ./internal/parser/... -run TestGoExtractor # single extractor
go test ./internal/parser/... -v                   # verbose parser tests
```

Key test areas: language extractors (fixture-based), BFS traversal on known graph topologies, delta indexing (SHA skip), token middleware, FTS5 query ranking.
