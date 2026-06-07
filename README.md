# Grove

> **Your codebase's persistent long-term memory — queryable by any AI agent.**

> **Embedded mode (current):** Grove is a Go library at `github.com/provasign/grove/pkg/grove`. Prism, Fuse, and Provasign link it directly and open the on-disk index in-process. There is no `grove serve` daemon, no port (7777/7778), and no `.grove/.token`. The CLI is available for explicit indexing plus read queries (`grove index .`, `grove symbols main`) and stdio MCP (`grove mcp`).

---

Grep answers "does this string appear somewhere?" A language server answers "where is this symbol defined?" Grove answers the harder questions AI agents actually need:

- *What does changing this function break — across the entire codebase?*
- *Which tests cover this method, directly or transitively?*
- *What is the full dependency chain from this file?*
- *What symbols are semantically related to this task description?*

The difference is a graph. Grove indexes your source files into a persistent SQLite graph — 11 languages, 8 edge types, BFS traversal — and keeps it live with delta indexing (files whose content hash hasn't changed are never re-parsed). The graph is queryable through the embedded Go API, CLI, and MCP stdio.

Grove is the foundation all other Provasign tools are built on. Prism uses it to focus context. Fuse uses it to resolve conflicts. Provasign uses it to certify agent output. Without Grove, all three fall back to line-level operations.

Grove also exposes a conservative certification report for unified diffs. This mode is additive: it does not change Provasign behavior unless Provasign explicitly opts into consuming the report. The report labels heuristic evidence, returns `manual_review` for unsupported or unmapped changes, and only returns `allow` when changed code maps cleanly to indexed symbols with required test evidence.

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
│  Native semantic analyzers for Go, Python, Java,       │
│  Rust, C, C++, C#, PHP, JS, and TS                      │
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
       │
       ├───────────────────────┐
       ▼                       ▼
┌────────────┐        ┌────────────────────┐
│ internal/  │        │ pkg/grove          │
│ mcp/       │        │ Embedded Go API    │
│ 8 tools    │        │ Query / impact /   │
│ JSON-RPC   │        │ deps / tests / ICR │
│ stdio      │        └────────────────────┘
└────────────┘
```

---

## Design Decisions

**Single binary, zero runtime dependencies.** SQLite is embedded via `modernc.org/sqlite` — a pure-Go port — which avoids a CGO linker conflict with tree-sitter. Tree-sitter itself (in `internal/parser/`) is the only CGO dependency.

**Delta indexing by content hash.** Grove hashes each file before parsing. If the stored hash matches, the file is skipped entirely. Indexing a 5000-file repo after a one-line change touches one file, not 5000.

**AST-first with native enrichment.** Tree-sitter produces a complete AST even for files with syntax errors, but marks broken subtrees as `ERROR` nodes. When `root.HasError()` is true, Grove runs both the AST extractor and the regex fallback, then merges the results with AST taking precedence. On top of that, language-native analyzers add higher-confidence call, type-use, inheritance, import, and constructor edges when the local toolchain is available. Files that are actively being edited mid-keystroke are still indexed usefully, and the graph gets richer when the repository can be resolved with native tooling.

**Scoped edges prevent false positives.** `calls` and `uses-type` edges are only created between symbols in the same file or in files connected by an `imports` edge. Without this constraint, a function named `parse` in one package would appear to call a `parse` function in an unrelated package, producing roughly 5× the false-positive edges.

**Symbol ID format.** Every symbol has a canonical ID: `{filePath}::{qualifiedName}@{blobSHA}`. The blob SHA component means that if you rename a function, the old symbol ID disappears and a new one is created — stale references in the graph don't survive a reindex.

---

## Language Support

| Language | Extension(s) | Extraction |
|----------|-------------|-----------|
| Go | `.go` | AST walker + native semantic enrichment |
| TypeScript | `.ts` | AST walker + native semantic enrichment |
| TSX | `.tsx` | AST walker + native semantic enrichment |
| JavaScript | `.js .jsx .mjs .cjs` | AST walker + native semantic enrichment |
| Python | `.py` | AST walker + native semantic enrichment |
| Java | `.java` | AST walker + native semantic enrichment |
| Rust | `.rs` | AST walker + native semantic enrichment |
| C | `.c .h` | AST walker + native semantic enrichment |
| C++ | `.cc .cpp .cxx .hh .hpp` | AST walker + native semantic enrichment |
| C# | `.cs` | AST walker + native semantic enrichment |
| PHP | `.php .phtml` | AST walker + native semantic enrichment |

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

Grove is the backend for the Provasign suite. Prism, Fuse, and Provasign consume the embedded Go API directly. Direct AI agent integration is available through MCP stdio.

| Integration | How | Use case |
|-------------|-----|---------|
| Claude Code CLI | `grove mcp .` → MCP stdio | Direct agent integration without Prism |
| Cursor, Windsurf, Zed | `grove mcp .` → MCP stdio | Same |
| VS Code (Copilot Agent) | Prism extension → embedded Grove | Grove-backed context through Prism |
| Prism (all IDEs) | Embedded Go API | Token-optimized context delivery |
| Fuse (git merge) | Embedded Go API | Blast radius + conflict hints |
| Provasign | Embedded Go API | Intent and certification inputs |
| Custom automation | `pkg/grove` | In-process Go integration |

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
VERSION=v0.5.0 curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash
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

# Show persisted index status without refreshing
grove status [dir] [--refresh]

# Symbol search
grove symbols <query> [dir] [--refresh]

# Intent-based semantic query (Model2Vec embeddings + BFS graph ranking)
grove query <intent> [dir] [--refresh]

# Blast radius: what would break if this symbol changed?
grove impact <symbol> [dir] [--refresh]

# Which tests cover a symbol?
grove tests <symbol> [dir] [--refresh]

# Conservative structural certification for a unified diff.
# Exit codes: 0 allow, 2 manual_review, 3 block, 1 runtime error.
grove certify <diff-file-or-> [dir]

# Start MCP stdio server (primary AI agent integration)
grove mcp [dir]

```

## Certification Mode

`grove certify` and `pkg/grove.Engine.CertifyDiff` map unified diff hunks to indexed symbols and emit a JSON report containing changed files, changed symbols, impacted symbols, related tests, unknowns, findings, and a verdict.

Verdicts are intentionally conservative:

| Verdict | Meaning |
|---------|---------|
| `allow` | Grove mapped the diff to indexed symbols and found required evidence. |
| `manual_review` | Grove could not prove enough structurally, for example unsupported files, ignored/sensitive paths, deleted/binary files, unmapped hunks, or missing test evidence. |
| `block` | Grove could not process the diff deterministically, for example malformed diff input. |

Certification mode is not a compiler or language-server resolver. Tree-sitter, astkit, and the native analyzers provide structural facts; the report still stays conservative and falls back to `manual_review` whenever evidence is incomplete.

---

## HTTP API

There is no HTTP or gRPC daemon in the current embedded mode. Use `pkg/grove` for in-process integration, the CLI for local commands, or `grove mcp` for stdio MCP.

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

## Storage

Grove stores everything in `.grove/grove.db` (SQLite, WAL mode). The database is a single file — back it up, copy it, or delete it to force a full reindex. Schema migrations are applied when the store opens.

Key SQLite settings:
- WAL mode for concurrent reads during indexing
- FTS5 virtual table for full-text symbol search
- `busy_timeout = 30s` to handle contention without immediate errors

---

## Security

Grove does not expose a network listener in embedded mode. Indexing skips dependency/build/cache directories, honors `.groveignore` and `.gitignore`, and avoids common secret-bearing filenames and credential/key extensions.

---

## Testing

```bash
make test                                          # all packages
go test ./internal/parser/... -run TestGoExtractor # single extractor
go test ./internal/parser/... -v                   # verbose parser tests
```

Key test areas: language extractors (fixture-based), BFS traversal on known graph topologies, delta indexing, ignore/secret-safe indexing, MCP stdio framing, and FTS5 query ranking.
