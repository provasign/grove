# Grove v0.5.0 — Technical Assessment & Roadmap

> **Status update (2026-06-11, same-day fix pass):** all Critical findings
> (C1–C3), all High findings (H1–H7), M1–M3, M5, M6, and the L-series
> (L1–L6) are **fixed and verified** — see the Unreleased section of
> `CHANGELOG.md` for the change list and regression tests. Measured after
> the pass: no-change reindex 3.7 s → 35 ms; BuildEdges 10K worst case
> 39.7 s → 0.5 s; `.grove` footprint on this repo 727 MB → 6 MB; same-named
> symbols index 2/2 instead of 1/2. Still open from the roadmap: M4 (nested
> .gitignore/`**`), M7 (incremental embeddings), M8 (FTS5 decision —
> documented as-is for now), GraphDiff (§6), parallel parsing, and the
> bench-corpus work. L7/L8 are documented behavior.

**Date:** 2026-06-11 · **Scope:** full source review of `cmd/`, `pkg/grove`, all
`internal/` packages, plus the astkit extraction layer Grove depends on
(`provasign/astkit` v0.4.0). Every Critical/High finding below was either
reproduced empirically against a built binary or verified in benchmarks; the
reproduction is noted inline. Build and full test suite pass (`go build ./...`,
`go test ./...` exit 0).

**Context:** Prism (`internal/grove/client.go`) calls `Index`, `Query`,
`Symbols`, `Deps`, `Impact`, `Semantic`, `Tests`. Fuse calls `Deps`, `Impact`,
`Symbols`, `Index`, and `EnsureIndexed` (which triggers a full `Index` on every
cold engine). The Fuse NEXT_STEPS stale-context loop additionally requires a
**graph-diff** capability Grove does not yet have. Grove's accuracy and
incremental-index latency are therefore directly load-bearing for the whole
family.

---

## 1. Strengths (what is genuinely good)

- **Architecture.** Clean layering (parser → store → graph → pkg/grove/MCP/CLI),
  CGO isolated to the astkit bridge, pure-Go SQLite (`modernc.org/sqlite`)
  avoiding the tree-sitter linker conflict. `pkg/grove` is a small, honest
  embedded API with re-exported core types — exactly the right shape for Prism/Fuse.
- **Resilient extraction.** AST-first with regex fallback, and the merge path
  for files with syntax errors (AST + regex union) means mid-keystroke files
  stay indexed. This is a real differentiator vs. LSP-based tools.
- **Native enrichment design.** Analyzer interface with availability probing,
  per-analyzer timeout, evidence `Source` and `Confidence` on every edge, and
  graceful diagnostics instead of failures. `go/types`-based call edges at 0.99
  confidence are the right ambition.
- **Embedded Model2Vec.** potion-base-8M via `go:embed`, pure-Go inference,
  process-level caching, TF-IDF fallback. Offline semantic search with zero
  runtime deps is rare and valuable. Measured: 0.13 s end-to-end CLI query on a
  915-symbol index, with sensible top results.
- **Conservative certification posture.** `CertifyDiff` defaults to
  `manual_review` on anything unprovable; the three issues documented in
  `docs/certify-diff-issues.md` are already fixed in code (b43c372) — that doc
  is now stale and should be archived.
- **Hygiene.** Deep-copied snapshots to prevent aliasing corruption, documented
  lock-ordering for the semantic engine, WAL + busy_timeout + single writer
  conn, FTS5 sync triggers, idempotent ALTER migrations, secret-aware indexing
  (`.env`, key material, `secret`/`credential` names), `.groveignore`/`.gitignore`
  support, MCP framing that accepts both NDJSON and Content-Length and
  negotiates protocol versions.
- **Test culture.** ~8 k test LOC against ~8.5 k source LOC; fixture-based
  extractor tests, 50 k-node graph benchmarks, MCP framing tests, lock tests.

---

## 2. Critical bugs

### C1 — Symbol-ID collisions silently drop same-named symbols in a file
`ID = {filePath}::{qualifiedName}@{blobSHA}`, but astkit sets
`QualifiedName = bare name` for **every** language (methods included; the
receiver/class goes only to `ParentName`). Two same-named members in one file
produce identical IDs; `store.UpsertFile` dedups by ID and the graph map keys
by ID, so all but one are **silently dropped**.

*Reproduced:* a Go file with `func (a *A) Close()` + `func (b *B) Close()`
indexes **1** `Close` symbol; a Python file with two classes each defining
`save` and `__init__` indexes **1** of each. This pattern (multiple types in
one file with same-named methods — `String()`, `Close()`, `__init__`,
constructors) is ubiquitous.

*Impact:* wrong blast radius, wrong test mapping, wrong CertifyDiff symbol
attribution (edits to the dropped method attribute to the surviving one, or
fall to `hunk_unmapped`), wrong context delivered through Prism. This is the
single biggest accuracy defect in Grove.

*Fix:* qualify names in astkit (`Receiver.Method`, `Class.method`, nested
paths) or disambiguate IDs in `projectSymbol` (include `ParentSymbol` and/or
span). Add a regression test asserting N same-named members ⇒ N symbols.
Note the regex fallback (`extractGoSymbols` etc.) has the same flattening and
needs the same fix.

### C2 — `ComputeICR` returns arbitrary files at confidence 0.9 when the intent matches nothing
`ComputeICR` seeds from `Search(intent, 20)`; on zero matches it falls back to
`Search("", 20)` — the first 20 symbols **alphabetically by path** — and then
reports confidence via `confidenceForSeeds(len(seeds))`, which yields **0.9
for 20 seeds**.

*Reproduced:* `grove icr "implement quantum flux capacitor zorbtran"` on this
repo returns confidence 0.9 with lock keys for `.claude/settings.local.json`,
`.github/workflows/ci.yml`, etc.

*Impact:* ICR feeds `DetectConflicts` and `grove lock`. Two unrelated
no-match intents will lock the same arbitrary alphabetically-first files and
"conflict" with each other at high confidence — exactly the multi-agent
coordination path Fuse depends on.

*Fix:* on zero seeds return an empty region with confidence ≤ 0.2 and no lock
keys (and consider seeding from `SemanticSearch` before giving up).

### C3 — Go native analyzer hijacks `HOME`, writing a per-repo Go module cache inside `.grove/`
`goAnalyzerEnv` sets `HOME=.grove/home` and `GOCACHE=.grove/go-build` for
`go list`. With `HOME` redirected, Go derives `GOMODCACHE` under it and
re-downloads the entire module graph **into the repository**.

*Observed:* this very repo carries **727 MB** in `.grove/` (module cache +
build cache), duplicated per repo, with read-only file modes that break naive
`rm -rf`/backup/sync tooling. Redirecting `HOME` also breaks `GOPRIVATE`,
`.netrc`, and toolchain auth, so `go list` can fail or stall on private
modules (against a 5 s default timeout).

*Fix:* keep the user environment; isolate with `GOFLAGS=-mod=readonly`
(already passed) and at most a scratch `GOCACHE` under `os.TempDir()`. Never
override `HOME`/`GOMODCACHE`. Add cleanup for caches already written.

---

## 3. High-severity bugs

### H1 — CertifyDiff hunk ranges include context lines (and `extendHunkRange` is dead logic)
`startHunk` initialises `NewRange` from the `@@ +start,count @@` header — the
**entire post-image hunk including context lines** — so `extendHunkRange`
(which narrows to actual `+` lines) can never have an effect. Symbols that
only overlap context lines are reported as **ChangedSymbols**, inflating
changed/impacted/test sets in every certification report. For the
Shale-evidence use case this misattributes changes; with default 3-line
context, small functions adjacent to an edit are routinely misreported.
*Fix:* initialise `NewRange` empty and let `extendHunkRange` build it from
`+` lines; treat deletion-only hunks explicitly (map via old-range adjacency
or emit a dedicated finding).

### H2 — No staleness check between diff and index
`DiffInput.BaseRef/HeadRef` are echoed into the report but never validated.
If the index predates the diff's base (or the worktree moved), hunk line
numbers are mapped against stale symbol spans → silently wrong
`ChangedSymbols` with verdict `allow`. *Fix:* compare each changed file's
indexed `blob_sha` against the actual file content hash (it's already in
`file_index`) and emit `index_stale` → `manual_review` on mismatch. This is
cheap and turns CertifyDiff from "trust me" into evidence.

### H3 — `tests` edges are unscoped name matches across the whole repo
`buildTests` links `TestFoo`/`test_foo`/`FooTest` to **every** symbol named
`Foo` in the repository (`idx.byName`, no import/package scoping — unlike
`calls`/`uses-type`, which are scoped). `TestOpen` in `store_test.go` gets a
0.8-confidence `tests` edge to `store.Open`, `pkg/grove.Open`, and any other
`Open`. This inflates test-coverage evidence, which `CertifyDiff` (under
`RequireTestsForCode`) accepts as proof — over-claiming the one thing the
cert mode must not over-claim. *Fix:* scope test targets with
`importedFiles()` exactly like calls edges; prefer CallSites from the test
body when present.

### H4 — Quadratic regex fallback in `buildCalls` (39.7 s for 10 k symbols)
When symbols lack AST `CallSites`, `buildCalls` runs every callable's
compiled regex over every other callable's body within scope. Benchmarks in
this repo: `BenchmarkBuildEdges10K` (1 000 same-package Go files, no call
sites) = **39.7 s**, vs. 164 ms for the favorable 50 k-symbol benchmark. Any
large same-directory Go package, or any language whose extraction lands on
the regex path (syntax-error merge path included), can hit this. And because
edge building runs on **every** `Index()` (see H5), the cost recurs. *Fix:*
prefilter with a single multi-pattern pass (e.g. one `strings.Contains` scan
or Aho–Corasick over the stripped body) before regex confirmation; cap
worst-case work per file.

### H5 — "Delta" indexing only skips parsing; everything downstream is rebuilt every time
On a no-change `grove index`: every file is re-hashed, then `AllSymbols` is
loaded, **all native analyzers re-run** (`go list` + full `go/types` check +
`goTypeUseEdges`), **all edges rebuilt**, and the entire `edges` table is
deleted and re-inserted row-by-row (no prepared statement, one tx).

*Measured:* no-change reindex of Grove itself (80 files, 915 symbols, 4 419
edges) = **3.7 s**. README claims "subsequent runs … complete in milliseconds".
On a 5 000-file repo this is tens of seconds — and Fuse's `EnsureIndexed`
triggers it, so every Fuse cold start pays it. *Fix (staged):* (1) short-circuit
when `FilesUpdated == 0 && FilesPruned == 0` — reuse stored edges; (2) batch
edge writes with a prepared statement and only rewrite when changed; (3)
longer term, maintain edges incrementally per changed file.

### H6 — `goTypeUseEdges` compiles a regex per caller×type pair, outside the analyzer timeout
`goContainsType` calls `regexp.MustCompile` inside an O(callables × types)
loop, and this loop runs **after** the `go list` context is cancelled — the
native timeout does not bound it. On a large Go repo this alone can dominate
indexing. *Fix:* precompile per type name once; check `ctx.Err()` in analyzer
loops; consider dropping the lexical pass where `go/types` succeeded.

### H7 — MCP tool schemas are empty, so agents can't discover parameters
`tools/list` returns `{"type":"object","additionalProperties":true}` with the
description `"Grove code graph tool: grove_impact"` for all eight tools. No
parameter names, no types, no docs. Agents calling Grove over MCP must guess
`query` vs `file` vs `intent` vs `dir`. For a tool whose pitch is
"queryable by any AI agent," this is the front door and it's unlabeled. Also:
`grove certify` is not exposed over MCP at all, although Fuse's plan treats
CertifyDiff as the evidence generator. *Fix:* real JSON schemas + per-tool
descriptions; add `grove_certify`.

---

## 4. Medium / low findings

| # | Severity | Finding |
|---|----------|---------|
| M1 | Medium | `Search()` ranks by file path alphabetically, not relevance; exact-name matches can fall outside the limit on big repos. Affects Prism `SearchSymbols` and `Impact` seed selection. Rank exact > prefix > substring, name > path. |
| M2 | Medium | `Impact()`'s substring fallback can over-seed (any symbol whose ID/name/path contains the needle becomes a seed), silently inflating blast radius for fuzzy queries. Return a "matched seeds" list so callers can see what was traversed. |
| M3 | Medium | `goImportedPackageForQualifier` returns the import's **last segment** but compares it to `packageDir(filePath)` (full relative dir), so qualified cross-package call-site edges almost never resolve for nested packages — the 0.99-confidence path silently degrades to lower-confidence fallbacks. |
| M4 | Medium | Ignore handling reads only root-level `.gitignore`/`.groveignore`; nested `.gitignore` files (standard in monorepos) and `**` patterns are unsupported (`path.Match` has no `**`). |
| M5 | Medium | Python native analyzer runs `importlib.util.find_spec` with repo root on `sys.path`; for dotted modules this **imports (executes) parent packages' `__init__.py`** from the repo being indexed. JS analyzer `require`s the repo's local `typescript`. Indexing an untrusted checkout can execute its code. Document the trust model; in Python prefer `importlib.machinery.PathFinder.find_spec` (no import) per segment. |
| M6 | Medium | `isTestFile`/`buildTests` cover Go/Python/JS/TS/Java naming only. No Rust (`#[test]` is same-file, which `buildTests` skips), no C/C++/C#/PHP, no `.test.tsx`/`.spec.tsx`. Test evidence for half the supported languages is structurally absent — and `RequireTestsForCode` will flag those changes forever. |
| M7 | Medium | Model2Vec/TF-IDF index is rebuilt from scratch on the **first query after every `Replace()`** — full re-embed of all symbols. Fine at 1 k symbols, but at 50 k it's `Benchmark50K_Semantic` ≈ 22 ms *per query* plus a multi-second first-query rebuild after each index. Consider embedding incrementally or persisting vectors. |
| M8 | Medium | `SearchFTS5` is dead code: nothing calls it, yet the FTS5 table + three triggers are maintained on every symbol write (write amplification). README's performance section describes the query path as "FTS5 full-text search," which is not the actual path (in-memory substring + embeddings). Use it or drop it. |
| L1 | Low | `store` LIKE patterns don't escape `%`/`_` — `a_b.go::%` also matches `axb.go::…`, over-deleting edges in `UpsertFile`/`DeleteFilesNotIn` (self-healing today only because `ReplaceEdges` rewrites everything). |
| L2 | Low | Traditional `diff -u` headers (`+++ b/file\t2026-…` timestamp suffix) keep the tab+timestamp in the path → `file_not_indexed`. Strip at first tab. |
| L3 | Low | `grove init` still writes `server: port: 7777` config; `config.DefaultPort` survives — vestiges of the deleted daemon mode. |
| L4 | Low | `pkg/grove.Open` ignores `AllSymbols`/`AllEdges` errors (silent empty graph) and runs `Migrate` twice (once in `store.Open`, again in `Open`). |
| L5 | Low | `decodeICR` tries base64 first; JSON that is also valid base64 mis-decodes. Require a `base64:` prefix or try JSON first. |
| L6 | Low | Two concurrent `Engine.Index` calls on one process aren't serialized (interleaved store writes; last graph swap wins). Add a mutex around `Index`. |
| L7 | Low | `extractBraceBody` caps bodies at 500 lines (200 for Python) — longer functions get truncated spans; symbols below a truncation point inside the body can be re-extracted as top-level. Documented limitation at minimum. |
| L8 | Low | README claims symbol IDs use "git blob SHA"; code uses plain SHA-1 of content (function even named `FileBlobSHA`). Behaviorally fine; fix the docs/name. |

---

## 5. Performance summary (measured, Apple M5 Pro)

| Operation | Measured | Verdict |
|---|---|---|
| BFS depth-3, 50 k nodes | 4.5 ms | ✅ within 30 ms target (despite O(V·E) scan — adjacency map would future-proof it) |
| Substring search, 50 k | 15 ms | ⚠️ above the "<10 ms" target; fine in practice |
| Semantic query, 50 k | 22 ms | ✅ |
| BuildEdges, 50 k favorable | 164 ms | ✅ |
| BuildEdges, 10 k regex-fallback | **39.7 s** | ❌ H4 |
| No-change reindex (80 files) | **3.7 s** | ❌ H5 (README: "milliseconds") |
| Cold semantic CLI query (915 symbols) | 0.13 s | ✅ |

Impact/TestsFor/cert traversals scan the full edge list per BFS node
(O(V·E)). Acceptable today; build a `map[node][]edgeIdx` adjacency index once
per `Replace()` to make it O(V+E) before repos get big.

---

## 6. Gap analysis vs. the four-piece story

The stale-context loop in Fuse's NEXT_STEPS §1 needs Grove to **diff the code
graph before/after a merge**. Grove has no graph-diff API: symbol IDs embed
the blob SHA, so a one-line edit changes every ID in the file and a naive
set-diff reports the whole file as churn. What's needed:

1. `GraphDiff(before, after Snapshot) → {added, removed, changed symbols; changed edges}`
   keyed by *stable* identity (`filePath + qualifiedName`, post-C1) with
   span/signature/body-hash comparison — not ID equality.
2. An exported-surface diff (signature changes of `Exports=true` symbols) —
   this is also what Fuse's "breaking changes" audit wants.
3. Cheap snapshot retention (the store already holds one generation; keep the
   previous symbols table or serialize snapshots) so the diff doesn't require
   two live checkouts.

Without C1 fixed first, graph-diff output would be noise — another reason the
ID-collision fix is the keystone.

---

## 7. Roadmap

**P0 — accuracy keystones (do first; everything downstream inherits them)**
1. Fix symbol-ID collisions (C1) in astkit qualified names + Grove regex
   fallback; add multi-same-name regression fixtures for all 11 languages.
2. Fix ICR no-match fallback (C2).
3. Stop hijacking `HOME`/module cache (C3); add `.grove` cache cleanup.
4. CertifyDiff: narrow hunk ranges to `+` lines (H1) and add the
   `index_stale` blob-SHA check (H2).
5. Scope `tests` edges like calls edges (H3).

**P1 — make "fast" true (Prism/Fuse latency budget)**
6. Short-circuit no-change `Index()`; reuse stored edges; batch/prepare edge
   writes (H5).
7. Kill the quadratic fallbacks: multi-pattern prefilter in `buildCalls`
   (H4); precompiled patterns + ctx checks in `goTypeUseEdges` (H6).
8. Adjacency index for BFS traversals (graph + cert).
9. Parallel file parsing (the walk is single-threaded; parse is
   embarrassingly parallel — biggest cold-index win).

**P2 — agent-facing surface (the "best tool" bar)**
10. Real MCP tool schemas + descriptions; expose `grove_certify` (H7).
11. Relevance-ranked `Search` (M1) and explicit seed reporting in `Impact` (M2).
12. `GraphDiff` + exported-surface diff (§6) — unlocks the stale-context loop,
    the family's flagship differentiator.
13. Test-evidence coverage for Rust/C/C++/C#/PHP (M6).

**P3 — robustness & hygiene**
14. Nested `.gitignore` + `**` support (M4); document/contain native-analyzer
    code-execution trust model (M5).
15. Decide FTS5: wire `SearchFTS5` into `Symbols()` for large indexes or drop
    the table+triggers (M8).
16. Docs refresh: README performance claims (H5), FTS5 query-path claim,
    "git blob SHA" wording, archive `certify-diff-issues.md` (fixed in
    b43c372); remove port-7777 vestiges (L3).
17. The L-series small fixes (LIKE escaping, diff timestamp paths, Open error
    handling, Index mutex, decodeICR).

---

## 8. Bottom line

The architecture, posture, and test culture are right, and most of the graph
heuristics are thoughtfully scoped. But today Grove silently drops same-named
symbols (C1), can hand Fuse confident nonsense regions (C2), writes 700 MB of
module cache into user repos (C3), and re-does ~all graph work on every index
(H5). Fix the five P0 items and the two quadratic paths, and Grove's outputs
become trustworthy and fast enough to be the consistency checker the
four-piece story needs; add GraphDiff and real MCP schemas and it starts
earning "best in the world."
