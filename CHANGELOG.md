# Changelog

## Unreleased

Accuracy, performance, and trust fixes from the 2026-06-11 assessment
(`docs/grove-assessment-2026-06-11.md`).

### Fixed — correctness
- **Symbol-ID collisions (critical):** same-named members in one file (two
  receivers' `Close()`, two classes' `__init__`) collapsed into a single
  stored symbol. Qualified names now include the parent
  (`Service.Login`); residual collisions get deterministic ID suffixes.
- **ICR no-match fallback (critical):** an intent matching no symbol
  returned the first 20 symbols alphabetically at confidence 0.9 with real
  lock keys. It now returns an empty region at confidence 0.2 with no locks.
- **Go analyzer environment (critical):** `go list` ran with
  `HOME=<repo>/.grove`, downloading a full per-repo module cache (hundreds
  of MB of read-only files) and breaking GOPRIVATE/.netrc auth. The user
  environment is preserved; legacy `.grove/home` and `.grove/go-build`
  caches are cleaned up on the next index.
- **CertifyDiff hunk mapping:** changed-symbol ranges now cover only the
  lines a hunk actually adds/deletes; context lines no longer mark adjacent
  untouched symbols as changed. Deletion-only hunks map to their enclosing
  symbol.
- **CertifyDiff staleness gate:** changed files whose indexed content no
  longer matches the working tree produce an `index_stale` unknown and
  escalate to `manual_review` instead of silently certifying outdated spans.
- **Test-edge scoping:** `tests` edges are now scoped through the import
  graph (TestOpen no longer "covers" every `Open` in the repo) and gain
  call-site evidence; Rust `#[test]` / JUnit `@Test` / xUnit `[Fact]`
  annotated tests and `tests/`-dir conventions are recognised.
- **Qualified cross-package Go call edges** resolved against the wrong
  package-dir comparison and silently never matched for nested packages.
- Diff paths with traditional `+++ file\t<timestamp>` suffixes parse
  correctly; SQLite LIKE wildcards in file paths are escaped; ICR JSON
  arguments are no longer mis-decoded as base64; engine `Open` surfaces
  rehydration errors; concurrent `Engine.Index` calls are serialized.
- **Python native analyzer** no longer executes repository code at index
  time (`find_spec` imported parent packages' `__init__.py`; resolution is
  now pure-filesystem via `PathFinder`).

### Changed — performance
- No-change reindex short-circuits: persisted edges are reused instead of
  re-running native analyzers and edge construction (~3.7 s → ~35 ms on an
  80-file repo). `grove index --force` re-runs everything.
- Call-edge fallback extracts callees in a single pass instead of matching
  every callable's regex against every body (synthetic 10K-symbol corpus:
  39.7 s → 0.5 s); ambiguous callee names (> 16 cross-file candidates) emit
  no edges instead of fanning out to all of them.
- BFS traversals (Impact, TestsFor, certification) use a per-node inbound
  edge index instead of scanning the whole edge list per visited node.
- Go type-use analysis tokenizes each body once and honours the analyzer
  timeout; per-pair regex compilation removed across analyzers.
- Edge and symbol writes use prepared statements.

### Added
- `PreviewFileSymbols` / `DiffAgainstFileContent` (`pkg/grove`): parse
  in-memory content as if it lived at a path and diff it against a
  snapshot — for callers whose result is not on disk yet, like a git merge
  driver (git writes `%A` to the worktree only after the driver exits).
- **GraphDiff API** (`pkg/grove`: `SnapshotSymbols`, `Diff`, `DiffSince`):
  structural delta between two snapshots matched by stable identity
  (file path + qualified name + kind), with `BreakingChanges` for exported
  symbols removed or re-signatured. Line shifts and content-SHA churn do
  not register — only symbols whose signature or body changed appear. This
  is the primitive for cross-agent drift notification (the Fuse
  stale-context loop).
- Nested `.gitignore`/`.groveignore` files now apply relative to their own
  directory with last-match-wins override, and `**` globs are supported.
- `grove_certify` MCP tool; all MCP tools now publish full JSON schemas
  with per-parameter descriptions.
- Ranked symbol search (exact name > prefix > substring) replacing
  alphabetical-by-path ordering; tighter Impact seed fallback.
- `grove index --force` and `force` argument on `grove_index`.

### Changed — performance (second batch)
- Changed files are parsed on a worker pool (tree-sitter parsing dominates
  cold indexing; astkit engines are concurrency-safe); store writes remain
  serial and ordered, so results are deterministic.
- Embedding vectors are cached by symbol ID across index rebuilds: the
  first query after a delta reindex re-embeds only changed files' symbols
  instead of the whole corpus.

### Removed
- The unused FTS5 mirror (`symbols_fts` + sync triggers): no retrieval
  path ever queried it, while its triggers doubled the cost of every
  symbol write. Existing databases are migrated (table and triggers
  dropped) on next open.
- Vestigial daemon-mode config (`server.port: 7777`) from `grove init`.

## v0.5.0 - 2026-06-07

- Added native semantic analyzers for Go, Python, Java, Rust, C, C++, C#, PHP, JavaScript, and TypeScript.
- Persisted native edge source so graph consumers can distinguish AST, heuristic, and native evidence.
- Fixed symlink-root normalization so `/tmp` and `/private/tmp` resolve consistently during indexing.
- Tightened Go fallback resolution and C++ member extraction to reduce false positives and symbol loss.
- Updated documentation to describe the native enrichment architecture and current release surface.
