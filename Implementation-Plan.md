# Grove — Implementation Plan

**Project:** Grove  
**CLI:** `grove`  
**Role:** Universal code intelligence graph — shared foundation for Prism, Fuse, and Relay  
**Language:** Go 1.22+  
**Status:** Pre-build — ready to implement  
**Last Updated:** May 26, 2026

---

## Overview

Grove is a persistent, queryable code knowledge graph for any codebase. It ingests source code via Tree-sitter, stores symbols and relationships in SQLite, and exposes the graph through a CLI, an MCP server (for AI agents), and an HTTP/gRPC API (for Prism, Fuse, and Relay). It is the single source of truth that replaces the duplicated graph-building logic previously inside gctx, git-semantic, and the Next-gen CICD CKG service.

### Initial Language Support (Phase 1)

| Language       | Tree-sitter Grammar              | Priority |
|----------------|----------------------------------|----------|
| Go             | `tree-sitter-go`                 | P0       |
| TypeScript     | `tree-sitter-typescript`         | P0       |
| JavaScript     | `tree-sitter-javascript`         | P0       |
| Python         | `tree-sitter-python`             | P0       |
| Java           | `tree-sitter-java`               | P1       |
| Rust           | `tree-sitter-rust`               | P1       |
| Node.js (CJS)  | `tree-sitter-javascript` (reuse) | P1       |

---

## Repository Layout

```
grove/
├── cmd/
│   └── grove/
│       └── main.go                  # Binary entry point
├── internal/
│   ├── config/
│   │   └── config.go                # Workspace config (.grove/config.yaml)
│   ├── parser/
│   │   ├── engine.go                # Parser orchestrator
│   │   ├── languages.go             # Language registry and detection
│   │   └── strategies/
│   │       ├── strategy.go          # LanguageStrategy interface
│   │       ├── go.go                # Go extractor
│   │       ├── typescript.go        # TypeScript extractor
│   │       ├── javascript.go        # JavaScript / Node.js extractor
│   │       ├── python.go            # Python extractor
│   │       ├── java.go              # Java extractor
│   │       └── rust.go              # Rust extractor
│   ├── graph/
│   │   ├── graph.go                 # CodeGraph struct + mutation methods
│   │   ├── build.go                 # Edge construction pipeline
│   │   └── bfs.go                   # BFS traversal algorithms
│   ├── store/
│   │   ├── store.go                 # SQLite connection, migrations, CRUD
│   │   └── schema.sql               # Versioned DDL
│   ├── index/
│   │   └── indexer.go               # Delta-aware indexer (git blob SHA)
│   ├── query/
│   │   ├── query.go                 # Intent→symbols, blast radius, deps, tests
│   │   └── icr.go                   # ICR computation and conflict detection
│   ├── lock/
│   │   └── lock.go                  # In-process lock table (used by Relay)
│   ├── mcp/
│   │   ├── server.go                # MCP server (JSON-RPC 2.0 over stdio/SSE)
│   │   └── tools.go                 # 8 MCP tool handlers
│   ├── api/
│   │   ├── http.go                  # REST HTTP server
│   │   └── grpc.go                  # gRPC server
│   └── cli/
│       └── commands.go              # cobra command tree
├── proto/
│   └── grove.proto                  # gRPC service definition
├── vscode-extension/
│   ├── src/
│   │   ├── extension.ts             # VS Code extension entry
│   │   ├── groveClient.ts           # Child-process client for grove binary
│   │   ├── mcpTools.ts              # vscode.lm.registerTool wrappers
│   │   └── sidebar.ts               # Graph stats webview
│   ├── package.json
│   └── tsconfig.json
├── testdata/
│   └── repos/                       # Fixture repos per language
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

---

## Data Models

### Core Types (`internal/core/types.go`)

```go
type SymbolKind string
const (
    KindFunction    SymbolKind = "function"
    KindMethod      SymbolKind = "method"
    KindClass       SymbolKind = "class"
    KindInterface   SymbolKind = "interface"
    KindType        SymbolKind = "type"
    KindConst       SymbolKind = "const"
    KindEnum        SymbolKind = "enum"
    KindModule      SymbolKind = "module"
    KindVariable    SymbolKind = "variable"
    KindConstructor SymbolKind = "constructor"
    KindField       SymbolKind = "field"
    KindDecorator   SymbolKind = "decorator"
    KindNamespace   SymbolKind = "namespace"
    KindAnnotation  SymbolKind = "annotation"
    KindTrait       SymbolKind = "trait"
    KindStruct      SymbolKind = "struct"
)

type SymbolRecord struct {
    ID             string     // "{filePath}::{qualifiedName}@{blobSHA}"
    FilePath       string
    BlobSHA        string
    Language       string
    Kind           SymbolKind
    Name           string
    QualifiedName  string
    Signature      string
    Docstring      string
    Span           LineRange
    Imports        []string
    Exports        bool
    RawText        string
    ParentSymbol   string
    TokenEstimate  int
    CallSites      []string
    Modifiers      []string   // public/private/static/abstract/async
    TypeParameters []string   // generics
    Annotations    []string   // Java @annotations / Python decorators / TS decorators
}

type LineRange struct {
    Start int // 1-indexed inclusive
    End   int // 1-indexed inclusive
}

type EdgeType string
const (
    EdgeDefines    EdgeType = "defines"
    EdgeImports    EdgeType = "imports"
    EdgeCalls      EdgeType = "calls"
    EdgeExtends    EdgeType = "extends"
    EdgeImplements EdgeType = "implements"
    EdgeUsesType   EdgeType = "uses-type"
    EdgeTests      EdgeType = "tests"
    EdgeContains   EdgeType = "contains"
)

type TKGEdge struct {
    From       string
    To         string
    Type       EdgeType
    Confidence float64 // 0.0–1.0
    // Future: DeltaGitSHA, LockedBy, EditFrequency
}

type IsolatedChangeRegion struct {
    IntentID       string
    Exclusive      []string // symbol node IDs agent will modify
    SharedRead     []string // symbol node IDs agent may read
    Boundary       []string // edge nodes (interface to rest of graph)
    ExclusiveFiles []string // human-readable file paths
    ReadableFiles  []string
    Confidence     float64
    LockKeys       []string // lock identifiers
}
```

---

## Phase 1 — Foundation & Parser Engine

**Goal:** Parse the 7 target languages and extract a rich symbol table.

### 1.1 Project Scaffolding

- `go mod init github.com/your-org/grove`
- Add dependencies:
  - `github.com/smacker/go-tree-sitter` — Tree-sitter Go bindings (CGO)
  - `github.com/smacker/go-tree-sitter/golang` — Go grammar
  - `github.com/smacker/go-tree-sitter/typescript` — TypeScript grammar
  - `github.com/smacker/go-tree-sitter/javascript` — JavaScript grammar
  - `github.com/smacker/go-tree-sitter/python` — Python grammar
  - `github.com/smacker/go-tree-sitter/java` — Java grammar
  - `github.com/smacker/go-tree-sitter/rust` — Rust grammar
  - `github.com/spf13/cobra` — CLI framework
  - `github.com/mattn/go-sqlite3` OR `modernc.org/sqlite` — SQLite driver (prefer modernc for pure-Go, no CGO conflict)
  - `google.golang.org/grpc` + `google.golang.org/protobuf` — gRPC

> **Note on CGO:** go-tree-sitter requires CGO. Use a single CGO compilation unit. If CGO conflicts arise with sqlite3, use `modernc.org/sqlite` (pure Go) for SQLite.

- Makefile targets: `build`, `test`, `lint`, `proto`, `release`

### 1.2 Language Strategy Interface

```go
// internal/parser/strategies/strategy.go
type LanguageStrategy interface {
    Language() string                              // "go", "typescript", etc.
    Extensions() []string                          // [".go"], [".ts", ".tsx"]
    Extract(tree *sitter.Tree, src []byte, filePath string) ([]SymbolRecord, error)
    ExtractImports(tree *sitter.Tree, src []byte) ([]string, error)
    ExtractCallSites(tree *sitter.Tree, src []byte) ([]string, error)
}
```

### 1.3 Per-Language Extractors

#### Go (`strategies/go.go`)

Extract:
- `func` declarations → `KindFunction`
- Methods on named types (`func (r Receiver) Name(...)`) → `KindMethod`, `ParentSymbol = receiver type`
- `type X struct` → `KindStruct`
- `type X interface` → `KindInterface`
- `type X = Y` / `type X Y` → `KindType`
- `const` blocks → `KindConst`
- Package-level `var` → `KindVariable`
- Import paths from `import (...)` blocks

Symbol ID: `filePath::packageName.SymbolName@blobSHA`  
Signature: `func (r ReceiverType) MethodName(param Type) (ReturnType, error)`  
Modifiers: exported (uppercase first letter) = `["exported"]`

#### TypeScript (`strategies/typescript.go`)

Extract:
- `function`, `async function` → `KindFunction`
- Arrow functions assigned to `const`/`let`/`export const` → `KindFunction`
- `class` → `KindClass`; class methods → `KindMethod`
- `interface` → `KindInterface`
- `type X = ...` → `KindType`
- `enum` → `KindEnum`
- `export const`, `export default` → set `Exports = true`
- `namespace` / `module` → `KindNamespace`
- Decorators (`@Injectable`, `@Component`) → captured in `Annotations`
- Generic type parameters → `TypeParameters`
- Import paths from `import ... from '...'`

Signature includes type annotations. Docstring from JSDoc `/** */` above declaration.

#### JavaScript / Node.js (`strategies/javascript.go`)

Reuses `tree-sitter-javascript`. Extract:
- `function`, `async function` → `KindFunction`
- Arrow functions assigned to variables → `KindFunction`
- `class` → `KindClass`; methods → `KindMethod`
- `module.exports = ...` / `exports.X = ...` → set `Exports = true`
- `require('...')` / `import ... from '...'` → imports
- CommonJS patterns: detect `require` call sites

#### Python (`strategies/python.go`)

Extract:
- `def` / `async def` → `KindFunction`
- `class` → `KindClass`; class methods → `KindMethod`
- Decorators (`@staticmethod`, `@classmethod`, `@property`, custom) → `Annotations`
- Module-level assignments that look like constants (ALL_CAPS) → `KindConst`
- `from X import Y` / `import X` → imports
- Type annotations in signatures → captured in `Signature`
- Docstrings (first string literal after `def`/`class`) → `Docstring`

#### Java (`strategies/java.go`)

Extract:
- `class`, `abstract class`, `final class` → `KindClass`
- `interface` → `KindInterface`
- `enum` → `KindEnum`
- `record` (Java 16+) → `KindClass` with modifier `record`
- `@interface` (annotation type) → `KindAnnotation`
- Methods inside classes → `KindMethod`; constructors → `KindConstructor`
- Fields → `KindField`
- Annotations on declarations (`@Override`, `@Bean`, `@Autowired`) → `Annotations`
- `import` statements → imports
- Access modifiers → `Modifiers` (`public`, `private`, `protected`, `static`, `final`, `abstract`)
- Generic type parameters (`<T extends Comparable<T>>`) → `TypeParameters`

QualifiedName: `com.example.package.ClassName#methodName` for methods.

#### Rust (`strategies/rust.go`)

Extract:
- `fn` → `KindFunction`
- `impl Type { fn ... }` → `KindMethod`, `ParentSymbol = impl type`
- `struct` → `KindStruct`
- `enum` → `KindEnum`
- `trait` → `KindTrait`
- `type X = Y` → `KindType`
- `const` / `static` → `KindConst`
- `mod` → `KindModule`
- `use` statements → imports
- Visibility: `pub`, `pub(crate)`, `pub(super)` → `Modifiers`
- Lifetime parameters and generic params → `TypeParameters`
- Derive macros (`#[derive(Debug, Clone)]`) → `Annotations`
- Attribute macros (`#[tokio::main]`, `#[test]`) → `Annotations`

### 1.4 Language Detection

```go
func DetectLanguage(filePath string) (string, bool) {
    ext := filepath.Ext(filePath)
    switch ext {
    case ".go":             return "go", true
    case ".ts", ".tsx":     return "typescript", true
    case ".js", ".jsx", ".mjs", ".cjs": return "javascript", true
    case ".py":             return "python", true
    case ".java":           return "java", true
    case ".rs":             return "rust", true
    }
    return "", false
}
```

### 1.5 Parser Engine

```go
// internal/parser/engine.go
type Engine struct {
    strategies map[string]LanguageStrategy // keyed by language name
    parsers    map[string]*sitter.Parser    // cached per language
}

func (e *Engine) ParseFile(filePath string, src []byte) ([]SymbolRecord, error)
func (e *Engine) ParseDirectory(root string, filter func(string) bool) ([]SymbolRecord, error)
```

- Parse timeout: 2 seconds per file (goroutine with `context.WithTimeout`)
- Graceful degradation: log error, continue with empty symbols for that file
- File size limit: skip files > 10MB

---

## Phase 2 — Storage Layer (SQLite)

**Goal:** Persist symbols and index metadata with delta-aware updates.

### 2.1 Schema (`internal/store/schema.sql`)

```sql
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT
);
-- meta keys: schema_version, grove_root, last_indexed

CREATE TABLE IF NOT EXISTS file_index (
    file_path  TEXT PRIMARY KEY,
    blob_sha   TEXT NOT NULL,
    language   TEXT NOT NULL,
    symbol_count INTEGER NOT NULL DEFAULT 0,
    indexed_at TEXT NOT NULL  -- ISO8601
);

CREATE TABLE IF NOT EXISTS symbols (
    id              TEXT PRIMARY KEY,
    file_path       TEXT NOT NULL,
    blob_sha        TEXT NOT NULL,
    language        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    name            TEXT NOT NULL,
    qualified_name  TEXT NOT NULL,
    signature       TEXT NOT NULL DEFAULT '',
    docstring       TEXT NOT NULL DEFAULT '',
    span_start      INTEGER NOT NULL,
    span_end        INTEGER NOT NULL,
    imports         TEXT NOT NULL DEFAULT '[]',  -- JSON array of file paths
    exports         INTEGER NOT NULL DEFAULT 0,
    raw_text        TEXT NOT NULL DEFAULT '',
    parent_symbol   TEXT,
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    call_sites      TEXT NOT NULL DEFAULT '[]',  -- JSON array of names
    modifiers       TEXT NOT NULL DEFAULT '[]',
    type_parameters TEXT NOT NULL DEFAULT '[]',
    annotations     TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_sym_file     ON symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_sym_name     ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_sym_kind     ON symbols(kind);
CREATE INDEX IF NOT EXISTS idx_sym_lang     ON symbols(language);
CREATE INDEX IF NOT EXISTS idx_sym_qualified ON symbols(qualified_name);

CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
    name, qualified_name, signature, docstring,
    content=symbols, content_rowid=rowid
);

-- Edges stored separately for graph rebuild
CREATE TABLE IF NOT EXISTS edges (
    id         TEXT PRIMARY KEY,  -- "{from}::{type}::{to}"
    from_node  TEXT NOT NULL,
    to_node    TEXT NOT NULL,
    edge_type  TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0
);
CREATE INDEX IF NOT EXISTS idx_edge_from ON edges(from_node);
CREATE INDEX IF NOT EXISTS idx_edge_to   ON edges(to_node);
CREATE INDEX IF NOT EXISTS idx_edge_type ON edges(edge_type);

-- ICR locks (lightweight, no external Redis needed for single-instance)
CREATE TABLE IF NOT EXISTS icr_locks (
    lock_key   TEXT PRIMARY KEY,
    intent_id  TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    expires_at  TEXT NOT NULL
);
```

### 2.2 Store Operations

```go
type Store struct { db *sql.DB }

// Core CRUD
func (s *Store) UpsertSymbols(filePath string, symbols []SymbolRecord) error
func (s *Store) DeleteSymbolsByFile(filePath string) error
func (s *Store) GetSymbolsByFile(filePath string) ([]SymbolRecord, error)
func (s *Store) GetSymbolByID(id string) (*SymbolRecord, error)
func (s *Store) SearchSymbols(query string, limit int) ([]SymbolRecord, error) // FTS5

// File index
func (s *Store) GetFileBlobSHA(filePath string) (string, bool)
func (s *Store) UpsertFileIndex(filePath, blobSHA, language string, symbolCount int) error
func (s *Store) GetStaleFiles(currentSHAs map[string]string) []string

// Edges
func (s *Store) UpsertEdges(edges []TKGEdge) error
func (s *Store) GetEdgesFrom(nodeID string) ([]TKGEdge, error)
func (s *Store) GetEdgesTo(nodeID string) ([]TKGEdge, error)
func (s *Store) RebuildAllEdges(graph *CodeGraph) error
```

Use transactions for batch upserts. Target: index 5000 files in < 5 seconds.

---

## Phase 3 — Graph Engine

**Goal:** Build the in-memory code knowledge graph with 8 edge types and BFS traversal.

### 3.1 Graph Construction (`internal/graph/build.go`)

Edge types built in dependency order:

| Step | Edge Type     | Confidence | Method                                          |
|------|---------------|------------|-------------------------------------------------|
| 1    | `defines`     | 1.0        | Every file → its symbols                        |
| 2    | `contains`    | 1.0        | Every parent symbol → its child symbols         |
| 3    | `imports`     | 0.9        | Each symbol's import list → resolved file node  |
| 4    | `extends`     | 0.85       | Parse signature for "extends X" clause          |
| 5    | `implements`  | 0.85       | Parse signature for "implements Y" clause       |
| 6    | `uses-type`   | 0.5        | Uppercase identifiers in signature, scoped      |
| 7    | `calls`       | 0.85/0.6   | AST callSites preferred; regex fallback         |
| 8    | `tests`       | 0.8        | Test file patterns + directory proximity score  |

**Critical implementation rules:**
- `calls` and `uses-type` edges MUST be scoped to same-file + imported files only. Without scoping, false positives increase ~5×.
- Strip comments and strings before regex-based call matching.
- Import resolution priority: relative path → directory index (`index.*`) → module alias → bare basename.
- Test file patterns: `*_test.go`, `test_*.py`, `*_test.py`, `*.test.ts`, `*.spec.ts`, `*.test.js`, `*.spec.js`, `*Test.java`, `*Spec.java`.

### 3.2 Graph Data Structure

```go
type CodeGraph struct {
    mu sync.RWMutex

    symbols   []SymbolRecord
    edges     []TKGEdge

    // O(1) lookups
    adjacency   map[string][]Neighbor              // undirected neighbors
    outByType   map[string]map[EdgeType][]string   // from → edgeType → []to
    inByType    map[string]map[EdgeType][]string   // to   → edgeType → []from

    // Symbol indexes
    byFile map[string][]*SymbolRecord
    byName map[string][]*SymbolRecord
    byID   map[string]*SymbolRecord

    // Import graph for call-edge scoping
    importedByFile map[string]map[string]struct{}
}
```

### 3.3 BFS Algorithms (`internal/graph/bfs.go`)

```go
// Multi-source BFS; returns map of nodeID → shortest distance
func (g *CodeGraph) Distances(seeds []string, maxDepth int, edgeTypes []EdgeType) map[string]int

// Get all neighbors within depth, optionally filtered by edge type
func (g *CodeGraph) Neighbors(nodeID string, depth int, edgeTypes []EdgeType) []string

// Shortest path length between two nodes (-1 if unreachable)
func (g *CodeGraph) Distance(from, to string, maxDepth int) int

// Impact/blast radius: all nodes reachable from nodeID (outbound)
func (g *CodeGraph) ImpactRadius(nodeID string, maxDepth int) []ImpactNode

// Tests covering a symbol (inbound 'tests' edges + transitive)
func (g *CodeGraph) TestsFor(symbolID string, transitiveDeps bool) []*SymbolRecord

// Callers of a symbol (inbound 'calls' edges)
func (g *CodeGraph) Callers(symbolID string) []*SymbolRecord

// Callees of a symbol (outbound 'calls' edges)
func (g *CodeGraph) Callees(symbolID string) []*SymbolRecord

type ImpactNode struct {
    Symbol   *SymbolRecord
    Distance int
    EdgePath []EdgeType // edge types traversed to reach this node
}
```

**Performance target:** BFS depth-3 on 50K-node graph in < 30ms.

---

## Phase 4 — Query Engine

**Goal:** Translate high-level questions into graph queries.

### 4.1 Intent Query (`internal/query/query.go`)

```go
// grove query "add rate limiting to auth"
// Returns ranked symbols relevant to the intent description
func QueryByIntent(intent string, g *CodeGraph, store *Store, limit int) ([]RankedSymbol, error)
```

Algorithm:
1. Tokenize intent string → keywords
2. FTS5 search on `symbols_fts` for each keyword → candidate symbols
3. Seed the BFS with candidate IDs
4. Score all reachable symbols: `score = 1/(1 + distance)` weighted by confidence
5. Sort descending; return top N with distance and edge path

### 4.2 Impact Analysis (`internal/query/query.go`)

```go
// grove impact src/auth/login.go:45
// Returns all symbols affected if the symbol at that location changes
func ImpactAt(filePath string, line int, g *CodeGraph) ([]ImpactNode, error)
```

Algorithm:
1. Find symbol containing line `line` in file
2. BFS outbound on `calls`, `imports`, `uses-type`, `extends`, `implements` edges (depth 5)
3. For each reached node, compute blast radius score: `1.0 / distance × edge.Confidence`
4. Return sorted by score descending with `ExclusiveFiles` summary

### 4.3 Dependency Chain (`internal/query/query.go`)

```go
// grove deps src/payments/service.go
// Returns full dependency chain for all symbols in a file
func DepsForFile(filePath string, g *CodeGraph) (*DependencyReport, error)
```

Returns: direct imports, transitive imports (depth 3), cycle detection.

### 4.4 Test Selection (`internal/query/query.go`)

```go
// grove tests src/api/handler.go
// Returns minimal test set covering all symbols in a file
func TestsForFile(filePath string, g *CodeGraph) ([]*SymbolRecord, error)
```

Algorithm:
1. Collect all symbols in file
2. For each symbol: find direct test nodes (inbound `tests` edges)
3. Find transitive dependents (inbound `calls`/`uses-type`, depth 3) and their tests
4. Deduplicate; return unique test files sorted by coverage score

---

## Phase 5 — ICR Engine (for Relay)

**Goal:** Compute safe isolated change regions and manage concurrency locks.

### 5.1 ICR Computation (`internal/query/icr.go`)

```go
// grove icr "add OAuth to user service"
func ComputeICR(intent string, g *CodeGraph, store *Store) (*IsolatedChangeRegion, error)
```

Algorithm:
1. FTS5 + keyword search → target symbol candidates
2. If targets found: BFS outward (depth 2, edges: `calls`/`extends`/`implements`/`uses-type`)
3. If no targets (new feature): identify insertion directory from domain/package mapping
4. Classify nodes:
   - **EXCLUSIVE** = target symbols + same-file siblings sharing mutable state
   - **SHARED_READ** = impact radius nodes not in exclusive set
   - **BOUNDARY** = nodes at max BFS depth with inbound edges from outside
5. Compute ICR confidence:
   - Start at 1.0
   - Penalize: high fan-out symbols (> 10 callers), deep inheritance chains (> 3), shared utility modules
6. Generate lock keys: `sha256(sorted(exclusiveFilePaths))`

### 5.2 Conflict Detection (`internal/query/icr.go`)

```go
// grove conflicts icr-a.json icr-b.json
func DetectConflicts(a, b *IsolatedChangeRegion) (*ConflictReport, error)
```

Layers:
1. **Structural**: intersection of `Exclusive` sets → `FILE_OVERLAP` or `SYMBOL_OVERLAP`
2. **Read-Write**: `a.Exclusive ∩ b.SharedRead` → potential write-read conflict
3. **Boundary**: shared boundary nodes → possible interface conflict

```go
type ConflictReport struct {
    HasConflict  bool
    Severity     string // "HIGH", "MEDIUM", "LOW"
    ConflictType string // "FILE_OVERLAP", "SYMBOL_OVERLAP", "READ_WRITE", "BOUNDARY"
    Details      []string
}
```

### 5.3 Lock Management (`internal/lock/lock.go`)

Lightweight in-process lock table backed by the SQLite `icr_locks` table. For Relay's multi-service deployment, locks are managed via Redis (Relay's responsibility); Grove provides the ICR structure only.

```go
func AcquireLock(intentID string, icr *IsolatedChangeRegion, ttlSeconds int) (bool, error)
func ReleaseLock(intentID string, icr *IsolatedChangeRegion) error
func CleanExpiredLocks() error
```

---

## Phase 6 — Interfaces

### 6.1 CLI (`internal/cli/commands.go`)

All commands via cobra. Full command set:

```
grove init                                 Initialize .grove workspace in current directory
grove index [dir]                          Build/update graph (delta-aware; defaults to cwd)
grove status                               Graph stats: symbol count, file count, staleness

grove query <intent>                       Find symbols relevant to an intent string
grove impact <file:line>                   Blast radius of symbol at file:line
grove deps <file>                          Full dependency chain for a file
grove tests <file>                         Minimal test set for a file
grove symbols <name>                       Find all symbols matching name (FTS5)

grove icr <intent>                         Compute isolated change region
grove conflicts <icr-a.json> <icr-b.json>  Detect conflicts between two ICRs
grove lock <icr-id>                        Acquire lock for agent execution
grove unlock <icr-id>                      Release lock

grove serve                                Start MCP server (stdio mode)
grove serve --port 7777                    Start MCP + HTTP server
grove serve --mode http                    HTTP only (for remote/team usage)
grove serve --mode grpc --port 50051       gRPC only
```

Output formats: `--format json` (machine-readable) | default (human-readable table/tree).

### 6.2 MCP Server (`internal/mcp/server.go`)

JSON-RPC 2.0 over stdio. Secondary: HTTP+SSE transport.

Implement handlers: `initialize`, `tools/list`, `tools/call`.

**8 MCP Tools:**

| Tool Name          | Description                               | Used By           |
|--------------------|-------------------------------------------|-------------------|
| `grove_index`      | Index or re-index a repo (delta-aware)    | All               |
| `grove_query`      | Find symbols relevant to an intent        | Prism, Relay      |
| `grove_impact`     | Blast radius of a symbol change           | Fuse, Relay       |
| `grove_deps`       | Full dependency chain for a file          | Prism, Fuse       |
| `grove_tests`      | Minimal test set for a changeset          | Relay             |
| `grove_icr`        | Compute safe isolated change region       | Relay             |
| `grove_conflicts`  | Detect conflicts between two ICRs         | Relay             |
| `grove_symbols`    | Full symbol lookup by name (FTS5)         | Prism, Fuse       |

Each tool: validate input schema → call corresponding query function → return JSON result.

### 6.3 HTTP API (`internal/api/http.go`)

```
GET  /health
POST /index          { "dir": "." }
POST /query          { "intent": "...", "limit": 20 }
POST /impact         { "file": "...", "line": 45 }
POST /deps           { "file": "..." }
POST /tests          { "file": "..." }
POST /icr            { "intent": "...", "intentId": "INT-2026-001" }
POST /conflicts      { "icrA": {...}, "icrB": {...} }
GET  /symbols?q=name
GET  /status
```

Stateless handlers. Graph loaded into memory on start; refreshed every 60 seconds if stale.

### 6.4 gRPC API (`proto/grove.proto`)

```protobuf
syntax = "proto3";
package grove.v1;

service Grove {
    rpc Index(IndexRequest) returns (IndexResult);
    rpc Query(QueryRequest) returns (QueryResult);
    rpc Impact(ImpactRequest) returns (ImpactResult);
    rpc Deps(DepsRequest) returns (DepsResult);
    rpc Tests(TestsRequest) returns (TestsResult);
    rpc ComputeICR(ICRRequest) returns (ICRResult);
    rpc DetectConflicts(ConflictRequest) returns (ConflictResult);
    rpc SearchSymbols(SymbolSearchRequest) returns (SymbolSearchResult);
}
```

---

## Phase 7 — VS Code Extension

**Goal:** Thin TypeScript wrapper that makes Grove available in the editor.

### Stack

- TypeScript, VS Code Extension API
- No bundled graph logic — shells out to `grove` binary

### Features

1. **Auto-index on save** — `vscode.workspace.onDidSaveTextDocument` → `grove index`
2. **MCP tool registration** — `vscode.lm.registerTool` for all 8 Grove MCP tools (enables Copilot Agent mode integration)
3. **Sidebar panel** — Webview showing graph stats (symbol count, files indexed, staleness, language breakdown)
4. **Inline decorations** — On hover over a symbol, show impact score badge (`⚡ 12 affected`)
5. **Commands palette**:
   - `Grove: Index Workspace`
   - `Grove: Query...` (input prompt → results in output channel)
   - `Grove: Show Impact at Cursor`
   - `Grove: Show Dependencies`

### `package.json` capabilities

```json
{
    "contributes": {
        "commands": [...],
        "views": { "explorer": [{ "id": "grove.sidebar", "name": "Grove" }] },
        "configuration": {
            "grove.binaryPath": "grove",
            "grove.autoIndex": true,
            "grove.indexOnSave": true,
            "grove.serverPort": 7777
        }
    },
    "activationEvents": ["onStartupFinished"]
}
```

---

## Phase 8 — Testing Strategy

### Unit Tests

Per-package `_test.go` files. Cover:
- Each language extractor: parse fixture files, assert extracted symbols and edges
- Graph BFS: known graph topologies, assert distances and neighbor sets
- Store: upsert, delta detection, FTS5 search
- ICR: known codebase fixtures, assert exclusive/shared sets and confidence
- Conflict detection: overlapping and non-overlapping ICR pairs

### Integration Tests

- `testdata/repos/` contains minimal fixture repos per language (20–50 files each)
- Full pipeline: `grove index testdata/repos/go-sample` → assert symbol count and spot-check
- MCP server: start in subprocess, send JSON-RPC requests, assert tool responses

### Performance Benchmarks (`*_bench_test.go`)

| Benchmark                   | Target          |
|-----------------------------|-----------------|
| Index 5000 files            | < 5 seconds     |
| BFS depth-3 on 50K nodes    | < 30 ms         |
| FTS5 query                  | < 10 ms         |
| Full edge rebuild (50K sym) | < 3 seconds     |
| ICR computation             | < 100 ms        |
| Memory for 50K-symbol graph | < 600 MB        |

---

## Phase 9 — Distribution & Open Source

### Build & Release

- `goreleaser` — cross-platform binaries (Linux amd64/arm64, macOS amd64/arm64, Windows amd64)
- Install: `go install github.com/your-org/grove/cmd/grove@latest`
- VS Code Extension: packaged with `vsce package`, published to Marketplace

### `.grove` Workspace File

Created by `grove init`:

```yaml
# .grove/config.yaml
version: 1
root: "."
exclude:
  - "vendor/"
  - "node_modules/"
  - ".git/"
  - "dist/"
  - "build/"
languages:
  - go
  - typescript
  - javascript
  - python
  - java
  - rust
db_path: ".grove/index.db"
server:
  port: 7777
  mode: "mcp"  # mcp | http | grpc | all
```

---

## Phased Delivery Schedule

| Phase | Deliverable                                         | Dependencies |
|-------|-----------------------------------------------------|--------------|
| 1     | Parser engine for all 7 languages                   | —            |
| 2     | SQLite storage + delta indexer                      | Phase 1      |
| 3     | Graph engine (8 edge types + BFS)                   | Phase 2      |
| 4     | Query engine (intent, impact, deps, tests)          | Phase 3      |
| 5     | ICR engine + lock table                             | Phase 4      |
| 6a    | CLI (all commands)                                  | Phase 4, 5   |
| 6b    | MCP server                                          | Phase 4, 5   |
| 6c    | HTTP + gRPC API                                     | Phase 4, 5   |
| 7     | VS Code extension                                   | Phase 6b     |
| 8     | Tests + benchmarks                                  | All phases   |
| 9     | Release + open source launch                        | Phase 8      |

**Phases 1–6** form the core deliverable that unblocks Prism, Fuse, and Relay. VS Code extension (Phase 7) can be built in parallel after Phase 6b.

---

## Key Design Constraints (Non-Negotiable)

1. **Single binary** — `grove` is one statically-linked Go binary. No external daemons.
2. **Zero external dependencies at runtime** — SQLite embedded. No Redis, no Postgres, no Kafka.
3. **CGO isolation** — Tree-sitter requires CGO. All CGO usage is isolated to `internal/parser`. The rest of the binary is pure Go.
4. **MIT License** — open source from day one.
5. **Edge scoping for calls** — always scoped to same-file + imported files. Non-negotiable for accuracy.
6. **Delta indexing** — never re-parse a file whose git blob SHA has not changed.
