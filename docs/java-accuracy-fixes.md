# Java Accuracy Fix Documentation

## Goal

Raise Java call-graph quality for production confidence: better precision, better recall, lower false positives, and stronger coverage.

Current Java baseline (`eval/baseline.json`) is:
- Precision: `0.6798`
- Recall: `0.8413`

This is behind Gin/TS/Go targets and below best-in-class requirements.

## Fix Plan (Ordered by Impact)

### 1) Recover Java calls when AST call-site extraction is missing (High)

**Problem:** For Java, if a symbol has AST support but zero `CallSites`, `buildCalls` skips regex fallback entirely because Java is in `astCallSiteLanguages`. Those symbols get no call edges.

- Anchor: `internal/graph/edges.go:985-987`

**Fix:**
- Keep the AST path as primary for Java.
- If `len(CallSites) == 0`, run a narrow fallback scan using symbol scope and the same candidate gating used by regex-call language paths.
- Only emit fallback edges when candidates resolve confidently within scope.

**Expected impact:** Recall lift on malformed/incomplete files and generated code.

### 2) Expand Java import extraction (High)

**Problem:** Java imports are currently extracted with one narrow regex only, missing wildcard and static import forms.

- Anchor: `internal/parser/engine.go:169-172`

**Fix:**
- Extend import patterns for:
  - wildcard imports (`import x.y.*;`)
  - static imports (`import static x.y.*;`)
  - comments/spacing variants used by real projects.
- Preserve package-token normalization for Java (`.`-separated path handling).

**Expected impact:** Better in-scope candidate resolution; fewer missed valid imports.

### 3) Strengthen Java local type inference (High)

**Problem:** `javaLocalTypes` is intentionally shallow: params + typed locals + field declarations only.

- Anchor: `internal/graph/javalocaltypes.go:342-414`

**Fix:**
- Add assignment-aware propagation:
  - `x = new Foo()` => `x: Foo`
  - `x = y` and `x = someCall()` when return type is known.
- Add lightweight flow through simple branch merges and enhanced-for constructs.
- Add `final` / cast / null-guard typed assignment patterns.

**Expected impact:** Fewer unknown-typed receiver drops and better overload narrowing.

### 4) Replace hard drop on unknown lowercase Java receivers (High)

**Problem:** Unknown-lowercase receiver types are often zeroed to avoid FP, which suppresses real edges.

- Anchor: `internal/graph/edges.go:848-867`

**Fix:**
- Replace hard `cands=nil` for this path with confidence-aware fallback:
  - retain strongly narrowed candidates where call-site signal is clear,
  - emit reduced-confidence edges where ambiguity remains,
  - use true hard-drop only for unresolved, high-noise cases.

**Expected impact:** Recall increases while precision stays controlled with confidence tiers.

### 5) Improve import/qualifier narrowing logic (Medium)

**Problem:** Unknown uppercase qualifiers can be dropped too aggressively when import/type presence is not obvious.

- Anchor: `internal/graph/edges.go:1280-1289`

**Fix:**
- Before dropping, re-check for:
  - in-scope type symbols matching qualifier,
  - same-package/relative path hints,
  - exact package alias matches.
- Keep the drop when no in-scope evidence exists.

**Expected impact:** Fewer false negatives from conservative pruning.

### 6) Add richer Java overload narrowing with Java typing rules (High)

**Problem:** Overload filtering is string-token based and cannot model widening/subtyping/erasure safely.

- Anchor: `internal/graph/javalocaltypes.go:107-159`, `internal/graph/javalocaltypes.go:281-323`

**Fix:**
- Add a bounded Java type relation helper:
  - primitive widening/boxing compatibility,
  - `null` compatibility,
  - simple subtype checks (`String` vs `CharSequence`, raw/generic erasure handling).
- Keep neutral behavior when metadata is missing.

**Expected impact:** Precision improvement on overload-heavy call sites.

### 7) Add constructor and chain-call handling improvements (Medium)

**Problem:** Constructor targets and call-result-based chaining are partially supported and can still miss valid class-construction paths.

- Anchor: `internal/graph/edges.go:938-964`, `internal/graph/javalocaltypes.go:176-214`

**Fix:**
- Improve handling of constructor-style edges for `ClassName(...)` and return-type propagation through single-level chains.
- Add fallback path for mixed/weak return-type information with lowered confidence.

**Expected impact:** Recall gains in builder/factory-heavy Java code.

### 8) Explicit evidence tagging for Java call edges (Medium)

**Problem:** Java call edges often fall back to default evidence when source is unknown, which hurts explainability and triage.

- Anchors: `internal/core/types.go:86-87`, `internal/graph/graph.go:113-115`

**Fix:**
- Emit `EvidenceSourceASTKit` for AST-derived qualified call-site edges.
- Emit `EvidenceSourceHeuristic` for qualifier/import/type-inference-based resolutions.
- Keep source tags stable in merge logic.

**Expected impact:** Easier debugging and tunable precision policy downstream.

### 9) Add Java coverage/measurement breadth (High)

**Problem:** Java oracle coverage is effectively one pinned corpus and no Java tests-edge score path.

- Anchors: `.github/workflows/eval.yml:120-142`, `eval/README.md:203-269`

**Fix:**
- Add at least one second Java benchmark pin with distinct language patterns (modern generics/lambdas).
- Add Java regression fixtures for overload-heavy and wildcard import scenarios.
- Include regular Java scorecard diff reporting in CI artifacts.

**Expected impact:** Safer rollout, fewer metric regressions from one-repo overfitting.

### 10) Add Java-specific regression test matrix (High)

**Problem:** Current tests do not cover many of these pathologies together.

- Anchors: `internal/graph/edges_test.go` (call tests), `internal/native/native_test.go`, `eval/README.md` baseline methodology.

**Fix:**
- Add focused tests for:
  - fallback call extraction when AST call-sites are absent,
  - static/wildcard imports,
  - unknown-typed receiver confidence downgrades,
  - overload conflict/non-conflict cases,
  - constructor/call-result chain resolution.

**Expected impact:** Prevents silent regressions and tracks each fix numerically.

## Rollout and Acceptance

1. Implement Phases 1 → 2 → 3:
   - Phase 1: import + call-site fallback + local typing + unknown receiver policy.
   - Phase 2: overload/type-relation and constructor-chain refinements.
   - Phase 3: evidence tagging + measurement expansion.
2. Target outcome after Phase 1: recall improves without large precision drop.
3. Target outcome after Phase 2: precision improves on commons-lang-style overload clusters.
4. Target outcome after Phase 3: improved precision/recall trend plus traceable evidence in scorecards.

## Notes

This doc is not yet implemented in code; it is the proposed fix set for a Java quality milestone.

## Astkit Layer Fixes (v0.4.10)

The same precision/recall issues above are also constrained by the upstream
`astkit` Java extractor. I probed AST output directly (`go run` harness + `dumpTree`) and found
high-confidence misses in v0.4.10:

### A1) Split multi-variable field declarations (High)

- **Where:** `github.com/provasign/astkit/strategies/extractors.go`
  in `javaFieldDecl`
- **Current behavior:** only the first `variable_declarator` child is emitted.
  Example: `int a, b, c;` produces one field symbol (`a`) and drops `b,c`.
- **Fix:** iterate all `variable_declarator` children and emit one `astkit.Symbol`
  per name.

### A2) Capture explicit constructor-invocation nodes (High)

- **Where:** `github.com/provasign/astkit/strategies/metadata.go`
  in `javaCallSites` (node set list)
- **Current behavior:** Java constructor calls inside constructors are parsed as
  `explicit_constructor_invocation` (`super(...);`, `this(...);`) and are currently
  absent from CallSites.
- **Fix:** include `explicit_constructor_invocation` in `nodeTypes` and map it to
  canonical callee strings (`super()` and `this()`). `super()` aligns with current
  `grove` constructor-resolution logic in `internal/graph/edges.go`.

### A3) Preserve method reference usage (Medium)

- **Where:** `metadata.go` (`javaCallSites`)
- **Current behavior:** `method_reference` nodes (e.g. `String::length`) are not visited.
- **Fix:** visit `method_reference` nodes and emit a stable callee shape such as
  `<type_or_expr>::<name>` or at least `<name>` for a conservative signal.
  This improves recall for functional style Java APIs and stream pipelines.

### A4) Recover receivers wrapped in parentheses (Medium)

- **Where:** `metadata.go` (`qualifierName` + `javaCallSites`)
- **Current behavior:** parenthesized receivers are dropped (`(obj).next()` currently
  yields callee `next` instead of `obj.next`).
- **Fix:** unwrap `parenthesized_expression` in `qualifierName` and continue recursively.

### A5) Treat static import/wildcard forms more usefully (Medium)

- **Where:** `strategies/registry.go` (`javaStrategy.ExtractImports`)
- **Current behavior:** wildcard imports are emitted as literal `*.`-suffixed strings
  (`java.util.*`, `import static java.util.Collections.*`). That shape does not
  participate well in Grove's package/folder import matching.
- **Fix (astkit side):** normalize wildcard/static import paths (drop trailing `.*`
  for package import context and/or surface canonicalized tokens) so downstream can
  reason over imports deterministically.

### A6) Add targeted Java AST regression tests (High)

- **Where:** `github.com/provasign/astkit/strategies` tests
- **Current behavior:** existing Java tests validate basic symbols/import/calls, but
  do not lock coverage for the call-shape variants above.
- **Fix:** add tests for each case:
  - `int a,b,c;` field extraction cardinality
  - `super()` and `this(...)` constructor-invocation callsites
  - `String::length` / method references
  - `(obj).next()` qualifier handling
  - wildcard/static import parsing

### Integration note

These are intentionally AST-layer changes; they should be applied in astkit (or a
vendored equivalent) rather than patched ad-hoc in Grove, to keep behavior stable
for all downstream consumers.
