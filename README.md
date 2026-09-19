# Grove

**A local, multilingual code graph for impact analysis.** Grove parses a repository, stores symbols and typed relationships in SQLite, and answers deterministic questions about callers, implementations, dependencies, tests, and change impact.

Grove is the graph engine embedded by [Prism](https://github.com/provasign/prism). Tool builders can also use it directly through the CLI, MCP server, or Go package.

## What it provides

- Tree-sitter extraction across sixteen languages, with native semantic enrichment when the local toolchain is available
- Type-resolved calls, imports, inheritance, implementation, type-use, and test relationships
- Incremental indexing by content hash
- Deterministic change-impact, rename, missing-implementation, and dead-code operations
- A local SQLite store with no network service or account
- An embeddable Go API and stdio MCP server

Grove reports evidence and capability tiers. Language support means the file can be parsed and indexed; it does not imply identical semantic precision in every language or repository configuration.

## Install

```sh
# macOS or Linux
curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash

# Windows PowerShell
irm https://raw.githubusercontent.com/provasign/grove/main/install.ps1 | iex

# Pin the current release
VERSION=v0.45.0 curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash
```

The installer writes to `~/bin` by default. Set `INSTALL_DIR` to choose another location.

Build from source:

```sh
make build
make test
make install
```

## Quick start

```sh
grove init .
grove index .
grove status .

grove symbols QueryData .
grove change-impact 'QueryDataHandler.QueryData' .
grove deps internal/service/query.go .
```

Grove stores the index in `.grove/grove.db`. Re-running `grove index` hashes the working tree and updates changed files and affected packages. See [incremental indexing](docs/INCREMENTAL_INDEXING.md) for the invariants and verification strategy.

### Watching an index run

`grove status` reads the database only, so it is safe to poll while `grove index` runs in another process. Besides the three counts it reports the run state the indexer records as it goes:

```json
{
  "filesIndexed": 25849, "symbolCount": 353362, "edgeCount": 0,
  "phase": "resolving",
  "progress": "118272/353362 symbols resolved (calls)",
  "indexStarted": "2026-09-19T17:02:11Z",
  "native": ["csharp: skipped: no .csproj file", "go: resolved 1 native call edge(s)"]
}
```

- `phase` moves through `walking`, `parsing`, `persisting`, `native`, `resolving`, `writing-edges`, then `complete` (or `failed: <error>`). `filesIndexed`/`symbolCount` only move during `persisting` and `edgeCount` only at the end, so on a large repository the counts freeze for the whole `resolving` phase.
- `progress` is a monotonic counter within the phase, refreshed at least every second while it moves — the number to watch to tell a busy index from a wedged one. Do not use a "no change in N minutes" kill on the counts alone.
- `indexStarted`/`indexFinished` bracket the current or last run (`indexFinished` is empty while a run is in flight).
- `native` is the per-analyzer verdict of the run that produced the stored edges, persisted in the database, so a consumer reading `grove.db` later knows which tier built it. A no-change re-index keeps the previous verdict.

### What the native tier needs on disk

The grammar tier always runs. The native tier runs a language's own toolchain and only when the repository carries its project files and the tool is installed; otherwise it is skipped and says so in the `native` verdict. A fresh clone without a dependency install is the common way to lose it:

| Analyzer | Project file(s) required | Tool required |
|---|---|---|
| go | `go.mod` or `go.work` | `go` |
| js-ts | `package.json`, `tsconfig.json` or `jsconfig.json` | `node`, and `typescript` resolvable from the project (i.e. installed in `node_modules`) |
| java | `pom.xml` or Gradle build/settings files | `jdtls`, `mvn`, `gradle` or `javac` |
| csharp | a `.csproj` | none |
| c-cpp | `compile_commands.json` | none |
| php | `composer.json` | none |
| python | `pyproject.toml`, `setup.py`, `setup.cfg` or `requirements.txt` | `python3` |
| rust | `Cargo.toml` | `cargo` |

`GROVE_NATIVE=false`, `GROVE_NATIVE_LANGUAGES` and `GROVE_NATIVE_DISABLED_LANGUAGES` select analyzers; unset means all of them.

### Sizing

Indexing holds every symbol (with its source text) and every edge in memory while edges are resolved, then writes them in one transaction. Before v0.53.0, files of whole-repo-scope languages (C#, PHP, C/C++, Swift, Objective-C) and Rust crates each held their own copy of the visible-file set — O(files²): a 25,849-file C# monorepo (353k symbols, 3.9M edges) peaked at 19.5 GB RSS, and `GOMEMLIMIT` could not lower it (live data, not garbage). Those sets are now shared, and the resolve phase no longer copies class bodies per call site. Measured on a synthetic 4,800-file / 848k-edge C# corpus: peak heap 3.99 GB → 1.3 GB, max RSS 4.45 → 2.24 GB, wall 78 → 57 s, identical output. What remains is roughly 300 KB of peak heap per file (the symbol load plus three transient copies of the edge set), so expect a few GB on a 10k-file repository. `grove.db` is about 5× the working tree on C# (the `edges` table and its three indexes are the bulk, not the stored source text); `cmd/memprobe` reproduces these measurements on any repository. Plan one `grove index` at a time per host, and expect `edges` to hold millions of rows on large repositories — read it with a streaming cursor, not `SELECT *` into memory. Edges are already unique per (from, type, to) with the best-confidence copy kept.

Two `grove index` flags trade graph breadth for size:

- `--vacuum` compacts the database after the write (a re-index leaves deleted rows' pages behind; VACUUM rewrites the file, so it needs that much free disk and takes minutes on a multi-GB index).
- `--min-confidence=0.6` drops every edge below that confidence from the graph and the store and persists the floor for later runs (`--min-confidence=0` clears it). The heuristic tiers — dispatch fan-out at 0.7, type-use at 0.5 — are the bulk of a monorepo's rows; a consumer that only reads resolved calls (0.85–0.95) can roughly halve the database. Impact and dead-code answers change accordingly, so treat it as a per-deployment setting, not a default.

## Task-shaped operations

| Question | Command |
|---|---|
| Which symbols match this name? | `grove symbols <query>` |
| What depends on this file or symbol? | `grove impact <query>` |
| What must change with this method signature? | `grove change-impact 'Type.method(Params)'` |
| Which types lack an interface member? | `grove missing-implementations 'Type.method'` |
| Which exact lines participate in a rename? | `grove rename-plan 'Type.method' NewName` |
| Which affected code lacks test evidence? | `grove untested-surface 'Type.method'` |
| Which production symbols appear unreachable? | `grove dead-code` |
| Can a structural diff be certified? | `grove certify <diff-file-or->` |

`change-impact` returns the declaration, override and implementation family, super-declarations, and resolved callers. For Go, it can identify local methods satisfying a compatible external interface method even when that interface is declared in a dependency.

These operations are conservative about uncertainty. Runtime dispatch, reflection, dependency injection, generated code, and missing toolchains can limit static evidence; callers should inspect the returned completeness and caveat fields.

Run `grove --help` for the current command and flag list.

## MCP

Start the stdio server in a repository:

```sh
grove mcp .
```

The MCP surface exposes indexing, symbol search, semantic query, impact, dependency, isolated-change-region, conflict, and diff-certification operations. [Prism](https://github.com/provasign/prism) is the recommended agent-facing layer when you want token-budgeted source delivery, complete method change sets, and diff verification in a focused six-tool surface.

## Go API

```go
import "github.com/provasign/grove/pkg/grove"

eng, err := grove.Open(ctx, grove.Config{RepoRoot: "/path/to/repo"})
if err != nil {
    return err
}
defer eng.Close()

if _, err := eng.Index(ctx, ""); err != nil {
    return err
}

result, err := eng.ChangeImpact(ctx, "QueryDataHandler.QueryData")
```

The package also exposes snapshots and structural diffs for integrations that need to detect graph changes across edits or merges.

## Language support

| Language | Extensions |
|---|---|
| Go | `.go` |
| TypeScript / TSX | `.ts`, `.tsx` |
| JavaScript / JSX | `.js`, `.jsx`, `.mjs`, `.cjs` |
| Python | `.py` |
| Java | `.java` |
| Rust | `.rs` |
| C / C++ | `.c`, `.h`, `.cc`, `.cpp`, `.cxx`, `.hh`, `.hpp` |
| C# | `.cs` |
| PHP | `.php`, `.phtml` |
| Swift | `.swift` |
| Kotlin | `.kt`, `.kts` |
| Objective-C | `.m`, `.mm`, `.h` (content-sniffed) |
| COBOL | `.cbl`, `.cob`, `.cpy` and copybook variants |
| JCL | `.jcl`, `.prc` |

Common non-code files are indexed as document symbols for lexical retrieval. Native analyzers can enrich the graph when the relevant language tooling is available. `grove capabilities` reports each language's resolution tier and known limitations.

## Accuracy and testing

Grove's graph operations are scored against independent or typed-toolchain ground truth on pinned repositories. CI runs unit tests and the committed accuracy gates. Method-level impact results used by Prism are also evaluated in [provasign/research](https://github.com/provasign/research), including exact task definitions, oracles, and raw agent transcripts.

```sh
make test
go test ./internal/parser/... -v
```

Current call-edge accuracy against each language's compiler or runtime oracle
(pinned corpora, CI-gated floors in `eval/baseline.json`; 2026-09-19):

| Language | Corpus | Oracle | Precision | Recall |
|---|---|---|---|---|
| Go | gin | SSA + VTA | 0.952 | 0.951 |
| Java | commons-lang | javac + javap | 0.936 | 0.920 |
| C# | Newtonsoft.Json | Roslyn | 0.901 | 0.948 |
| TypeScript | socket.io | TypeScript checker | 0.903 | 0.992 |
| JavaScript | express | TypeScript checker (`checkJs`) | 0.840 | 1.000 |
| Rust | ripgrep | rust-analyzer SCIP | 0.936 | 0.905 |
| C | jansson | clang AST | 0.999 | 0.925 |
| Swift | SwiftyJSON | SourceKit index | 0.936 | 1.000 |
| Kotlin | turtle | kotlinc + javap | 1.000 | 0.988 |
| Objective-C | SBJson | clang AST | 1.000 | 0.991 |
| Python | flask | pytest trace (dynamic) | 0.852 | 0.716 |
| PHP | PHP-Parser | Xdebug trace (dynamic) | 0.914 | 0.647 |

The dynamic oracles record only paths the test suites execute and count
reflection-driven dispatch a static graph cannot name, so their recall is a
floor rather than a ceiling.

See [eval/README.md](eval/README.md) for Grove's engine evaluation and progression history.

## Storage and security

The SQLite database uses WAL mode for concurrent reads. Indexing honors `.gitignore` and `.groveignore`, skips dependency, build, and cache directories, and excludes common credential and key files. Grove does not open a network listener.

See [SECURITY.md](SECURITY.md) and [THREAT_MODEL.md](THREAT_MODEL.md) for reporting and trust boundaries.

## Related projects

- [Prism](https://github.com/provasign/prism) — agent-facing context and change verification
- [Shale](https://github.com/provasign/shale) — local agent-session evidence for pull requests
- [Research](https://github.com/provasign/research) — reproducible evaluations and raw results

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [GOVERNANCE.md](GOVERNANCE.md), and [SUPPORT.md](SUPPORT.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
