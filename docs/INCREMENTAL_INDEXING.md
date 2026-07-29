# Incremental edge construction

Since v0.24.0, delta reindexes recompute only the edges an edit can affect,
instead of rebuilding all edges from the full symbol set. On
kubernetes-scale repos (25k files, 160k symbols, 6.3M edges) a single-file
edit in a resident session (MCP / watch) dropped from ~95s to ~33s; small
and mid-size repos were already fast and are unchanged.

**On by default.** `GROVE_INCREMENTAL=0` opts out (full rebuild every
delta). One-shot CLI runs have no resident baseline and always use the full
path.

## How it stays correct

The invariant is absolute: an incremental delta must produce the exact edge
set a full rebuild would. The design never relies on the scoping being
tight — only on it being sound — and every layer is verified, not assumed:

- **Semantic identity remap.** Symbol IDs embed the file blob SHA, so every
  edit turns over every ID in the file. Symbols whose resolution-relevant
  content is unchanged keep their edges via an oldID→newID rewrite; only
  semantically added/removed/changed symbols re-resolve callers (via import
  scope and name-delta rules). A comment-level edit re-resolves almost
  nothing.
- **Owner-scoped recomputation** for the two expensive passes (calls,
  tests); the cheap passes run globally, unchanged — identical output by
  construction.
- **Automatic full-rebuild fallback** when the affected set degenerates
  (>30% of symbols — e.g. edits to very common names).
- **Splice store writes** persist the known write-set instead of diffing
  the whole table; a COUNT(*) invariant self-heals through the full diff on
  any mismatch.
- **Canonical install order** makes the in-memory graph a pure function of
  the edge set — full and incremental paths are byte-identical on every
  query surface, including order-sensitive ones.

## Verification gates (all in-tree)

- `internal/graph/equivalence_test.go` — randomized edit sequences over a
  synthetic corpus (modeling blob-SHA ID turnover) hash-compared against
  the full-rebuild oracle after every step; instrumented so a vacuous gate
  (silent fallback) fails the test.
- `pkg/grove/incremental_test.go` — engine-level: stored edge dumps AND raw
  order-sensitive query outputs byte-compared between a full-rebuild engine
  and an incremental engine, with native analyzers active.
- Real-corpus shadows (jackson-databind, kubernetes): incremental-indexed
  store byte-identical to a from-scratch rebuild of the same tree.
- `eval/` scorecards (CI-gated): edge precision/recall unchanged.

## Known limits / next levers

- ~28s of the kubernetes delta is edge-SET materialization (merge, sort,
  adjacency install) that scales with total edges, not edit size. The next
  optimization (kept-edge overlay merge, projected ~15s or below) requires
  per-pass invalidation rules for the cheap passes — their name-bucket
  dependencies (contains: parent names; extends: supertype names;
  interface-satisfaction: member-name sets) are NOT covered by the current
  affected rules, so keeping their previous edges without global recompute
  risks stale edges. Do not attempt without extending the equivalence
  harness first.
- Build reproducibility: release binaries bake the CI toolchain's GOROOT;
  `ensureGOROOT` repairs it at runtime so released and locally-built
  binaries produce identical native edges (fixed in v0.24.0).
