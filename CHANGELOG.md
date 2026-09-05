# Changelog

## v0.43.0 - 2026-09-05

Engine-ceiling program, measured against compiler/runtime oracles
(`eval/baseline.json` floors raised; P/R before -> after):
commons-lang .697/.891 -> .856/.896, newtonsoft .672/.702 -> .849/.816,
flask .830/.646 -> .829/.704, socket.io .849/.963 -> .898/.975,
ripgrep .851/.597 -> .899/.896, php-parser .770/.536 -> .825/.571,
jansson .879/.564 -> .876/.867.

- Java: overload resolution by argument types — primitive arrays never
  bind `T[]`/`Object[]`, exact-type overloads beat boxing/wildcard
  siblings per declaring type, varargs element binding, shape rules
  (array vs Collection, lambda vs primitive), boxing pairs, class field
  types and a JDK return-type table as argument evidence; declaration
  parsing skips leading annotations/Javadoc; cast, array-element and
  `X.class` receivers are qualified; `super.` receivers reach the call.
- C#: base-class parsing (previously absent), `base.`/`this.` receivers
  reach the call, argument-type overload narrowing with BCL alias
  folding, `params`, extension-method receivers, subtype assignability,
  literal conversion ranking, enum-member typing, nested-`new` markers,
  preprocessor-split files re-parsed with `#else` branches blanked.
- Python: `with` items -> `__enter__`/`__exit__`, return-annotation
  typing, from-import submodule binding, imported module globals,
  template-method dispatch, class-attr types only through `self.`.
- Rust: re-export-only facade files and `mod x;` exist to the graph,
  inline crate paths and grouped/nested `use` join scope, builder chains
  survive one unparseable return type, same-file-wins no longer shadows
  typed type paths, same-named cross-crate types pinned by path/import.
- PHP: namespace-aware `new`, `$x = Class::m()` locals, interface
  member parsing.
- C: a regex-found callable the AST already declared is not a second
  function (jansson do_dump lost all 64 calls to a 1-line twin).
- TypeScript: multi-line generic field types keep their head, declared
  non-class receivers drop, inherited self-calls prefer in-scope ancestors.
- All class languages: class-hierarchy dispatch through typed receivers
  (`reason=dispatch`, capped); `new X(...)` sites bind constructors only;
  bare calls to functions declared in the caller's own body emit nothing;
  candidate lists sorted for determinism.
- change-impact: constructors count as members (`GlobSetBuilder.new`
  reported "declares no method new").
- eval: deterministic declaration claiming when two oracle decls land on
  one symbol; `GROVE_EVAL_MAX_EXAMPLES`; dispatch edges are neither TP
  nor FP under declaration-binding oracles (`ignoredDispatch`).
- `GROVE_TRACE_CALLS=1` prints every call site's candidates, arguments
  and narrowed set, plus the Rust scope walk.
- Requires astkit v0.9.0.

## v0.42.0 - 2026-09-02

- change-impact: `file=` scoping disambiguates same-named types in
  different packages (`Engine.ChangeImpactScoped`); unscoped behavior
  unchanged.
- graph: Java package scope spans Maven/Gradle source roots —
  same-package test callers (src/test/java mirroring src/main/java, no
  import needed) join call resolution and change-impact sets.

## v0.41.0 - 2026-08-30

- graph: Python class attributes join the graph as fields, with
  uses-type/fan-out guards.

## v0.40.0 - 2026-08-30

- change-impact: bare TYPE-NAME queries answer with the type-level
  dependent set instead of dead-ending on the constructor.

## v0.39.0 - 2026-08-29

- framework edges generalize past Java (Angular templates, Flask routes);
  field-anchor change-impact.

## v0.38.0 / v0.38.1 - 2026-08-29

- framework edges: JPA derived queries, template expression language;
  honest completeness reporting (heuristic-refs as a structured field).

## v0.37.0 - 2026-08-28

- index: extractor-version stamp; `--force` actually re-extracts.

## v0.33.0 - v0.36.3 - 2026-08-28

- Mainframe estate: COBOL/JCL parsing conformance corpus, data-flow
  lineage (directional reads/writes, REDEFINES, dataset binding),
  change-impact anchors for mainframe kinds, extensionless-member
  content sniffing, uppercase-extension detection (32% -> 99% include
  resolution).

## v0.30.0 - v0.32.0 - 2026-08-10 - 2026-08-20

- Receiver resolution: multi-hop chains, generic-constraint extends,
  signature-aware interface satisfaction, per-site receiver
  classification for rename plans, Go local types for conversions/call
  results/closure params.

## Earlier removals worth knowing (v0.26-v0.27, 2026-08)

- v0.26.0 REMOVED heuristic test-coverage (`tests`) edges: measured
  4-12% recall against real per-test runtime coverage — an unreliable
  signal shipped as a guarantee is worse than none. Covering-test
  questions answer over resolved `calls` edges (a test that exercises
  code calls it); dead-code reachability verified byte-identical
  before/after.
- v0.27.0 REMOVED embedding-based semantic search (Model2Vec): measured
  2026-08-01 on 15 hand-verified concept queries across 5 corpora, an
  agent guessing one keyword through lexical search beat or tied the
  embedding fallback in 12/15 cases. The graph is calls/types, queried
  lexically.


## v0.29.1 - 2026-08-09

- **Windows:** the v0.29.0 permission tightening is now Unix-only —
  `Chmod(0o700)` on Windows cleared the read-only attribute on a
  deliberately read-only `.grove`, defeating the read-only diagnostics.
  Windows has no POSIX permission bits to tighten.
- eval module: astkit pin caught up with the main module (v0.4.23).

## v0.29.0 - 2026-08-09

Robustness fixes from the 2026-08-09 cross-repo audit.

- **BREAKING — store errors surface instead of returning empty results:**
  `Engine.ICR`, `FileSymbols`, `SnapshotSymbols`, `SnapshotGraph`, and
  `DiffSince` now return an error; every graph-backed method propagates
  rehydration/database failures instead of printing to stderr and answering
  with an empty graph (corruption was indistinguishable from "no matches").
- **Indexer never prunes what it could not see:** a nonexistent/unreadable
  root aborts the run with an error, and files under an unreadable subtree
  are shielded from the deletion pass (a transient FS error could previously
  empty a valid index).
- **Parse failures invalidate stale symbols:** a changed file that fails to
  parse now has its previously stored symbols pruned instead of serving the
  last successfully parsed version.
- **Private index permissions:** `.grove` is created (and tightened) to
  0700 and `grove.db`/WAL/SHM to 0600 — the database stores full source
  bodies.

## v0.6.3 - 2026-06-12

Two precision fixes found by Prism's grafana-scale benchmark
(prism/docs/AB-Test-Payflow-2026-06-12.md):

- **TestsFor traversal confidence gate:** the "tests for X" closure no
  longer follows low-confidence fallback edges (ambiguous cross-file
  bare-name call matches at 0.6, type-use guesses at 0.5), which connected
  unrelated subsystems on monorepos. Direct `tests` edges are unaffected.
  Known limitation: residual cross-subsystem noise can still arrive over
  high-confidence edges when a bare callee name resolves to ≤16 candidates
  (all get 0.95 edges); the durable fix is type-aware callee resolution.
- **GraphDiff rename pairing for common and partially-renamed names:**
  body normalization now blanks only standalone identifier occurrences of
  the symbol's own name (a substring ReplaceAll mangled "Get" inside
  "GetKeys" and broke pairing for short names), and a bounded pairwise
  second pass blanks BOTH names on both sides so mechanical renames that
  leave the old name in the doc comment ("// Get an item…") still pair.
  Both cases previously fell back to removed+addition — breaking flag
  correct, continuity signal lost.

## v0.6.2 - 2026-06-12

Real-repo validation pass (prometheus / django / grafana) plus token
discipline for the MCP surface.

- **Scoped native analyzers:** only languages whose files changed re-run;
  skipped analyzers' stored edges are carried forward. A one-file Go edit
  on a 19k-file polyglot monorepo no longer re-runs the TypeScript
  program check.
- **O(import-depth) import resolution:** package-import matching used a
  per-import scan over every directory (~0.5B string comparisons on
  grafana); slash-suffix lookups produce a bit-identical graph.
- **Diff-based edge persistence:** the edge table is synced by difference
  (batched multi-row writes) instead of delete-everything-reinsert.
- Net effect on grafana (18,979 files / 98.5k symbols / 1.16M edges):
  one-file change 78.3s → 18.7s; cold index 87.4s → 56.6s. README
  publishes the measured table.
- **MCP token discipline:** symbol payloads no longer carry full bodies
  (one grove_query response was ~10.7k tokens; now ~1.1k);
  grove_impact caps at 50 minimal refs with an exact count; all
  responses are compact JSON.

## v0.6.1 - 2026-06-11

- Added `Engine.FileSymbols(ctx, relPath)`: the indexed symbols for one
  file, without paying for a whole-graph snapshot. Supports working-set
  drift checks in Prism.

## v0.6.0 - 2026-06-11

- **GraphDiff rename detection:** a removed symbol whose body matches an
  added one (modulo its own name) is reported as `renamed` instead of an
  unrelated removal + addition. Only unambiguous 1:1 body matches pair;
  trivial bodies never pair. An exported rename is a breaking change
  (callers of the old name break); a pure file move is not.

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
