# Mainframe support — careful build plan

Companion to the requirements spec (grove-mainframe-spec, maintained
separately). This document is the engineering sequence and the safety
contract: what lands where, in what order, and what proves at each step
that the existing engine did not move.

## Ground rules (the safety contract)

1. **Risky refactors land first, alone, as provable no-ops.** Each one
   ships with a before/after check on the existing corpus: identical
   symbol/edge counts, identical test results, benchmarks within noise.
   No mainframe feature rides in the same change.
2. **Existing gates are tripwires, extended never weakened.** astkit
   `go test`, grove `go test`, prism `go test`, prism ci_invariants
   (jackson/django/typeorm/guava/grafana cells + index determinism).
   A PR that moves an existing cell fails, regardless of what it adds.
   Baselines re-confirmed green 2026-08-28 before this plan was written.
3. **Mainframe code is reachable only behind content detection.** No
   normalizer work, no confidence checks, no hierarchy code on the
   modern-language path. The pass-through must be structural (early
   return), not conditional-per-line.
4. **Early abort is a feature.** If a phase-0 refactor cannot be done as
   a clean no-op, stop and reassess the module boundary before any
   grammar work. That discovery is cheap now and expensive later.

## Where the work lands

| Layer | Owns | Mainframe additions |
|---|---|---|
| astkit | detection, tree-sitter dispatch, per-language Strategy → Symbol | content-based detection hook; text-strategy capability (front-ends without a sitter.Tree); normalizer stage; mainframe extractors |
| grove | graph store, incremental indexing, query | include-closure invalidation; new symbol kinds/edge types; provenance+confidence (optional field); data-hierarchy subsystem |
| prism | MCP/CLI surface, steering, completeness reporting | new incompleteness reasons; lineage via change_impact on field symbols (NO new tool unless routing measurably fails); config: search paths, dialect, format override |
| research | ground truth, invariants | mainframe conformance corpus + invariant cells (include-resolution rate, dispatch classification counts, layout correctness, determinism) |

## Findings the plan is grounded in (verified 2026-08-28)

- `astkit.Strategy` hard-codes tree-sitter: `Extract(tree *sitter.Tree,
  src []byte)`. Line-structured front-ends (JCL, DATA DIVISION, sort
  cards) need a parallel capability, not a signature change.
- Detection is extension-based (`Strategy.Extensions()`); the spec
  requires content sniffing with extension as hint only (estates reuse
  suffixes freely).
- Grove's incremental indexing is content-keyed per file
  (docs/INCREMENTAL_INDEXING.md); include expansion breaks that
  assumption — R-7.1 in the spec.

## Phase 0 — no-op refactors (astkit + grove core)

- **0.1 Text-strategy capability (astkit).** Optional interface checked
  by type assertion at dispatch; existing strategies untouched (their
  code does not change and the tree-sitter path is byte-identical).
  Proof: astkit suite + grove/prism suites + symbol-count diff on the
  eval corpus = zero.
- **0.2 Content-detection hook (astkit).** `DetectLanguage` gains an
  optional content-sniff pass that no current language registers;
  extension path unchanged. Proof: same as 0.1.
- **0.3 Normalizer stage (astkit).** Pre-parse hook, structural
  pass-through for every current language (early return on "no
  normalizer registered"). Carries the source-map contract (normalized
  position → original file/line) used later by fixed-format artifacts.
  Proof: same as 0.1 + parse-latency benchmark within noise.
- **0.4 Closure-aware invalidation, generic (grove).** Invalidation
  accepts a dependency closure; for artifact types with no include
  edges (all current languages) the closure is the file itself —
  behavior identical by construction. Proof: incremental-indexing tests
  + ci_invariants determinism cell.

Phase 0 exit: all four landed, all gates green, zero cell movement.
This is the go/no-go point for the module boundary.

## Phase 1 — decisions on paper (before any grammar)

- **1.1 Field identity.** One symbol per (member, field); per-unit
  expansions are edges carrying the substituted name. Settles lineage
  aggregation, storage growth under N-fold inclusion, and what
  "declaring member" means in acceptance criteria.
- **1.2 Dataset identity.** Base-name + resolution rules for GDG
  relative generations, temp (&&) datasets, DISP step chains.
- **1.3 Confidence/provenance schema.** Optional edge fields, absent =
  certain; modern path never writes them. Reuse prism's completeness
  vocabulary (closed / qualified + reason), do not invent a parallel one.
- **1.4 Conformance corpus intake.** Real estate source from the
  sql-research recon (copybooks + programs + JCL already in hand);
  ground truth seeded from that recon's characterized failures
  (the 19x column-handling swing, the VSAM group-key case, COPY
  REPLACING renames, unresolved dynamic calls). Committed to research/
  with per-construct coverage tracking (spec §6 list).

## Phase 2+ — the spine (per spec §8, amended)

JCL + dataset identity → normalizer in anger (fixed-format) →
DATA DIVISION line-structured extractor + include resolver + hierarchy
subsystem (level tree, byte offset/length, REDEFINES overlays) →
field lineage queries via change_impact → PROCEDURE DIVISION grammar
(direction: reads vs writes) → dynamic-call resolution with bounded
fan-out → embedded layers (CICS/SQL) by corpus evidence.

Promise boundary to state everywhere until the grammar phase: early
phases deliver "who can see this field" (closure + hierarchy +
overlap); "who reads vs writes it" arrives only with PROCEDURE
DIVISION parsing. Do not let reachability be presented as lineage.

## Standing decisions

- No standalone engine. Module boundary inside astkit/grove; product
  packaging may differ later. Revisit only if phase 0 fails or existing
  cells break repeatedly despite the boundary.
- Assembler and no-grammar artifact types: text-level indexing floor,
  clearly distinguished from graph-derived results.
- Tiers 1–3 artifacts start at the text floor and are promoted to
  grammars only on corpus evidence that their edges are load-bearing.
