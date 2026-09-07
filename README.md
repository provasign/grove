# Grove

**A local, multilingual code graph for impact analysis.** Grove parses a repository, stores symbols and typed relationships in SQLite, and answers deterministic questions about callers, implementations, dependencies, tests, and change impact.

Grove is the graph engine embedded by [Prism](https://github.com/provasign/prism). Tool builders can also use it directly through the CLI, MCP server, or Go package.

## What it provides

- Tree-sitter extraction across ten languages, with native semantic enrichment when the local toolchain is available
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
VERSION=v0.43.2 curl -fsSL https://raw.githubusercontent.com/provasign/grove/main/install.sh | bash
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

Common non-code files are indexed as document symbols for lexical retrieval. Native analyzers can enrich the graph when the relevant language tooling is available.

## Accuracy and testing

Grove's graph operations are scored against independent or typed-toolchain ground truth on pinned repositories. CI runs unit tests and the committed accuracy gates. Method-level impact results used by Prism are also evaluated in [provasign/research](https://github.com/provasign/research), including exact task definitions, oracles, and raw agent transcripts.

```sh
make test
go test ./internal/parser/... -v
```

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
