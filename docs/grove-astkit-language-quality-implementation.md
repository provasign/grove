# Grove and astkit Language Quality Implementation Details

## Purpose

This document lists concrete fixes to raise Grove graph precision, recall,
coverage, and false-positive control across every supported language. It covers
two layers:

- `astkit`: extraction correctness: symbols, imports, call sites, qualifiers,
  arity, argument tokens, parent names, spans.
- `grove`: graph correctness: import scope, receiver/type narrowing, native
  enrichment, test evidence, confidence policy, fallback behavior.

This is intentionally an implementation checklist, not a product roadmap.

## Current Review Status

Reviewed against the current Grove workspace and `github.com/provasign/astkit`
`v0.4.15` on 2026-06-13.

This document is still the right quality agenda, but it must be read with these
current-state corrections:

- Java remains the highest-priority open quality risk. Raw wildcard/static
  imports, missing Java call-site forms, incomplete local type inference, and
  aggressive unknown-receiver suppression are still real precision/recall
  issues. Grove has added arity/argument overload narrowing, same-package
  scope, call-result receiver handling, and a native pass that no longer emits
  text-matched calls, so the remaining Java work should be narrower and more
  semantic than the original checklist.
- Rust, C#, PHP, C, and C++ have moved forward. astkit now emits call sites for
  normal Rust, C#, PHP, and C-family calls, and Grove treats those languages as
  AST call-site languages. Items below for those languages are now hardening
  work, not "add call-site extraction from zero." C# call sites also carry
  generic-call evidence that Grove can use during overload narrowing.
- The TSX follow-up from the previous review is fixed. The parser supports
  `tsx`, `astCallSiteLanguages` includes it, and the TS/JS local-type branch in
  `buildCalls` handles it. `internal/graph/tsx_test.go` pins both no-regex
  fallback for empty TSX call-site lists and TSX receiver local-type narrowing.
  Remaining TSX work is JSX component evidence, path alias coverage, and
  native-symbol mapping policy.
- Grove now has local type inference files for Go, Python, JS/TS, Java, Rust,
  C#, PHP, and C/C++. The remaining work is to improve inference quality,
  language-specific scope, and receiver-resolution policy, not to create those
  files from scratch.
- Test detection exists for the major conventions in Go, Python, JS/TS, Java,
  Rust, C#, and PHP, and tests edges are already built from narrowed call
  evidence plus bounded same-test-file helper traversal. The remaining work is
  framework gaps, stricter negative fixtures, and more tests-edge baselines.
- Native analyzers for Java, Rust, C#, PHP, and C/C++ intentionally avoid
  text-matched call edges. They currently contribute project structure,
  imports, type-use, inheritance, and implementation evidence; ordinary call
  edges are owned by astkit call sites plus Grove graph resolution unless a
  future compiler-backed analyzer can resolve them semantically.
- Eval infrastructure is no longer absent. `grove-eval`, snapshot truth data,
  and CI now cover Go, Python, JS/TS, Java, Rust, C#, PHP, and C-family calls,
  plus a Flask tests-edge baseline. The remaining work is feature-specific
  false-positive fixtures, broader tests-edge gates, and impact gates.
- Edge evidence source tagging is done. Native edges set
  `EvidenceSourceNative`, and every graph-built edge now sets `astkit`,
  `heuristic`, or `regex` at construction; the `mergeEdges` `unknown` fallback
  is a safety net guarded by `TestBuildEdges_EverySourceTagged`. The remaining
  open piece is consumer-side confidence policy (test/cert traversal opting out
  of weak edges).

## Fresh Accuracy Sweep: Highest-Leverage Next Work

The current architecture is in a better place than the original checklist:
calls for supported languages are AST-first, native text-matched call edges are
retired outside real compiler analyzers, and source tagging is present. The next
accuracy gains should therefore come from sharper identity, scope, and policy
rather than from more broad matching.

### Accuracy invariants to preserve

- Do not reintroduce text-matched native call edges for Java, Rust, C#, PHP, or
  C/C++. Those analyzers should contribute project structure, import/include
  reachability, type-use, inheritance, and implementation evidence until they
  can resolve calls with a compiler-grade API.
- Do not run regex fallback for languages in `astCallSiteLanguages`. Empty
  `CallSites` is currently authoritative for `go`, `python`, `javascript`,
  `typescript`, `tsx`, `java`, `rust`, `csharp`, `php`, `c`, and `cpp`.
- Keep receiver/type narrowing before broad name matching. A qualifier that is
  a known local, `this`/`self`, a known type, or a call result must get the
  first chance to reduce candidates.
- Treat broad candidate sets as a precision failure unless there is explicit
  dispatch evidence. Interface/trait rescue should stay reduced-confidence and
  capped.
- Keep weak edges out of consumer closure by default. `TestsFor`, impact, and
  certification should traverse only edges whose source/confidence is allowed
  by policy.

### 1. Preserve structured import and namespace identity in Grove

The biggest remaining precision leak is that Grove still projects astkit import
records to `[]string` on `SymbolRecord.Imports`. That loses alias, static,
wildcard, grouped-use, require/include kind, and namespace metadata before
`importedFiles`, `narrowByImport`, and local type narrowing can use it.

Implementation targets:

- Add a Grove-side `ImportRecord` or equivalent parser projection that preserves
  astkit `Raw`, `Path`, `Alias`, `Group`, `Line`, plus Grove-normalized fields:
  `Kind`, `Owner`, `Member`, `Wildcard`, `Static`, `RelativeLevel`, and
  `ResolvedFiles`.
- Keep `SymbolRecord.Imports []string` temporarily for API compatibility, but
  build `edgeIndex.fileImports` from structured records where available.
- Carry namespace/package identity on symbols for languages where short names
  collide routinely:
  - Java package
  - C# namespace and project
  - PHP namespace and Composer prefix
  - C++ namespace
  - Python module path
  - Rust crate/module path
- Use qualified identity during matching before falling back to
  `ParentSymbol + Name` or bare `Name`.

Acceptance:

- Java static/wildcard imports, Rust grouped uses, PHP grouped/function/const
  uses, C# alias/static/global usings, and Python aliases all have fixtures
  proving that unrelated same short names do not enter scope.
- Two classes named `Service` in different PHP/C# namespaces do not collide
  when a call/import names only one namespace.

### 2. Replace repo-wide scopes with project-aware scopes

`importedFiles` still deliberately makes C#, PHP, C, and C++ repo-wide because
string imports do not map cleanly to files. That keeps recall acceptable, but it
is now the largest cross-language false-positive surface. The native analyzers
already collect enough project structure to become scope providers.

Implementation targets:

- C#: build a project graph from `.csproj` files and scope calls to the caller's
  project plus referenced projects. Respect SDK-style default includes,
  explicit `Compile Include/Remove`, linked files, and project references.
- PHP: use Composer PSR-4, `autoload-dev`, classmap, and `autoload.files` as
  the primary cross-file scope. Repo-wide PHP scope should become a fallback
  only when no Composer mapping is available.
- C/C++: use `compile_commands.json` and resolved `#include` edges as call/type
  scope. Calls from a `.c/.cc/.cpp` file should see same file, directly
  included headers, unique implementation/header twins, and explicitly linked
  translation-unit evidence if available.
- Java: replace raw dot-path strings with package/import records. Same-package
  scope should stay, but wildcard/static imports need metadata so they do not
  behave like broad basename matches.
- Rust: expand grouped/glob `use` records and preserve crate/module path
  identity. Crate-wide scope is reasonable, but `pub use` re-export expansion
  should be explicit and fixture-backed.
- Python and JS/TS/TSX: keep exact relative resolution, add path alias/package
  export handling, and avoid generic basename fallback when an exact resolver
  has evidence.

Acceptance:

- Negative same-name fixtures exist for every repo-wide language before and
  after the scope change: C#, PHP, C, C++, Java, Rust, Python, JS/TS/TSX, and
  Go.
- A language-aware resolver can explain why each cross-file candidate is in
  scope: same package, import, project reference, include edge, PSR-4 mapping,
  crate/module path, or native compiler evidence.

### 3. Make receiver resolution a single explainable pipeline

`buildCalls` now contains the right ideas, but the logic is still distributed
across language-specific branches. That makes regressions likely when one
language gets a new narrowing rule.

Implementation targets:

- Introduce a common `CallResolution` flow with ordered stages:
  1. parse qualifier/callee/call-result receiver
  2. resolve self/this/base/super/parent/static
  3. resolve type-qualified calls
  4. resolve inferred local variable type
  5. resolve call-result return type
  6. resolve import/package/module qualifier
  7. apply overload/arity/argument/generic filters
  8. apply dynamic dispatch rescue
  9. only then apply bounded bare-name fallback
- Return a reason code with each edge, such as `receiver-self`,
  `local-type`, `call-result`, `import-qualified`, `overload-argc`,
  `generic-overload`, `constructor`, `dispatch`, or `regex-fallback`.
- Keep the existing language-specific local-type providers, but expose them
  through one interface and require a negative fixture for every new provider
  feature.

Acceptance:

- `TestBuildEdges_EverySourceTagged` is extended to require a non-empty
  reason/policy tag for every graph-built edge once the edge model supports it.
- The same resolver-order tests run across at least Java, TSX, Rust, C#, PHP,
  and C++ with identical expectations for known receiver, unknown receiver,
  call-result receiver, and imported qualifier behavior.

### 4. Use confidence as policy, not just metadata

Edges now carry confidence and source, and `TestsFor` has a minimum traversal
threshold. The next accuracy step is to make policy explicit for all consumers
instead of hard-coding one threshold in one traversal.

Implementation targets:

- Define policy profiles for `tests`, `impact`, `certification`, and
  `diagnostic` traversal.
- Traverse native/compiler and AST/type-narrowed edges by default; require an
  opt-in for regex, broad dynamic dispatch, and name-derived test edges.
- Expose skipped-edge counts and reasons in certification reports so users can
  see when Grove avoided weak evidence.
- Calibrate confidence bands from eval data per language/source instead of
  treating current constants as permanent truth.

Acceptance:

- `TestsFor`, impact, and certification have fixtures where a weak regex or
  ambiguous dispatch edge exists but is not traversed under strict policy.
- Certification output can cite both included and excluded evidence with source,
  confidence, and reason.

### 5. Add targeted recall only after scope is tight

Some recall gaps are still important, but adding call forms before fixing scope
would also add false positives. The order matters.

Highest-value recall additions after scope tightening:

- Java: explicit constructor invocations, method references, static imports,
  lambda/method-reference argument tokens, `null` and varargs compatibility,
  and richer assignment/control-flow local type inference.
- TS/TSX: JSX component usage evidence, path aliases, package exports, overload
  implementation selection, and namespace/import-equals forms.
- Python: relative imports, package `__init__.py` exports, dataclass/attrs/
  pydantic field types, classmethod/staticmethod receiver policy, and fixture
  helper traversal.
- Rust: grouped/glob uses, `<Type as Trait>::method`, more builder/call-result
  returns, and trait default method dispatch fixtures.
- C/C++: include-scoped free functions, C++ namespaces, constructors,
  `ptr->method`, operators where recoverable, and function-pointer calls only
  as weak evidence.
- C#: namespace/project scope, alias/static/global usings, extension methods,
  nullable/generic type erasure, and Roslyn-backed calls when available.
- PHP: namespace-qualified identity, grouped/function/const uses, classmap/
  `autoload.files`, traits, callable arrays, and Pest coverage.

### 6. Build a false-positive corpus, not only recall fixtures

The existing eval baselines are useful, but the next regressions will likely be
specific false positives from broad scope or ambiguous same-name matching.

Implementation targets:

- Add strict negative fixtures for each language with:
  - same short class/function/method name in unrelated package/namespace/module
  - external dependency qualifier that matches an in-repo name
  - ambiguous call-result chain
  - overload set with one valid target
  - test helper with same name outside the test's scope
- Track fanout metrics per call site: number of initial candidates, candidates
  after each narrowing stage, final edges emitted, and cap/drop reason.
- Gate eval on false-positive count and fanout distribution, not only precision
  and recall averages.

Acceptance:

- A change that increases average precision but adds any strict false-positive
  fixture fails CI.
- Large real-repo scorecards report the top false-positive sources by language,
  source, confidence band, and resolver reason.

## Execution Roadmap (sequenced waves)

This is the authoritative execution order for accuracy work. It combines the
Fresh Accuracy Sweep workstreams (1–6 above) with the per-call-site narrowing
levers proven this cycle (PHP fluent-chain resolution F1 .55→.63; C#
generic-overload split F1 .65→.68). It supersedes the older "Implementation
Order" section for accuracy work; that section remains the broader correctness
checklist.

### Routing principle: fix what the board says each language needs

Do not apply one prescription to all languages. Route by the precision/recall
split on the current board, because the levers differ and can fight each other
(tightening scope can cost recall — verified on C/C++ where definitions live in
non-`#include`d `.c` files).

| Bucket | Languages (P / R) | Dominant lever |
|---|---|---|
| Precision-bound (low P) | Java (.68/.84) | structured imports + namespace identity + overload arg-type binding |
| Recall-bound (high P, low R) | C/C++ (.88/.56), Rust (.85/.60), PHP (.77/.54), Python (.85/.61) | dispatch rescue + targeted recall; scope-tighten only where it does NOT cost recall |
| Balanced | C# (.66/.70) | arg-type overload narrowing (precision) + dispatch rescue (recall) |
| Strong, maintain | Go (.94), TS (.90), JS (.73) | regression-guard only |

Invariant from the sweep: **tighten scope/precision before adding recall**, and
**measure every step** (`universe` → implement → score F1 delta on the pin →
raise baseline only on a reproducible gain → gate). Never reintroduce native
text-matched call edges or regex fallback for `astCallSiteLanguages`.

#### Wave 0 measured FP attribution (2026-06-13)

The scorecard now attributes every false positive to its resolver reason
(`FPByReason`). Measured on the pinned repos:

| Pin (lang) | P / R | FPs | Dominant FP reason |
|---|---|---|---|
| commons-lang (Java) | .68/.84 | 1555 | **ast-narrowed 95%** (inheritance 3%, dispatch 1%) |
| Newtonsoft.Json (C#) | .66/.70 | 4471 | **ast-narrowed 94%** (dispatch 5%) |
| ripgrep (Rust) | .85/.60 | 490 | **ast-narrowed 99%** |
| jansson (C/C++) | .88/.56 | 73 | **ast-narrowed 100%** (precision already high; gap is recall) |
| PHP-Parser (PHP) | .77/.54 | 585 | **constructor 79%**, ast-narrowed 20% |

Routing conclusion: the precision leak across Java/C#/Rust/C/C++ is overwhelmingly
the **ast-narrowed** bucket — the AST call site resolves to a candidate set that
still contains the wrong same-name/overload target. That is the single
cross-cutting precision lever (tighter receiver/overload narrowing — Waves 1, 3,
4), not dispatch or fallback. **PHP is the exception**: its leak is constructor
edges (`new X()` the dynamic xdebug oracle under-records in unexecuted
reduce-callback tables) — a distinct, smaller lever, partly an oracle artifact
(hence the static-oracle item below).

### Wave 0 — Guardrails & identity infra (do first; de-risks all later waves)

- **Structured imports + qualified identity** (Sweep #1): Grove-side `ImportRecord`
  projection preserving astkit `Raw/Path/Alias/Group` + normalized
  `Kind/Owner/Member/Wildcard/Static/RelativeLevel/ResolvedFiles`; carry
  namespace/package/crate/module identity on symbols and use it in matching
  before `ParentSymbol+Name`. Foundational for Waves 1–3.
- **False-positive corpus + fanout metrics** (Sweep #6): strict negative
  same-name fixtures per language; per-call-site fanout tracking (candidates in →
  after each stage → edges out → cap/drop reason); gate eval on FP count and
  fanout distribution, not just average P/R. This is the safety net the riskier
  waves require.
- **Edge reason codes** (Sweep #3, start): tag every graph-built edge with a
  resolver reason; extend `TestBuildEdges_EverySourceTagged` to require it.
- **Honest recall measurement** (new): add a static second oracle for the
  dynamic-oracle languages (Python pytest-trace, PHP xdebug-trace) so we separate
  Grove-fault recall from oracle-coverage gaps before chasing their recall.

### Wave 1 — Ready precision levers (cheap; pattern already proven)

- **C# arg-type overload narrowing** — astkit v0.4.15 already emits C# arg tokens;
  wire `narrowOverloadsByArgTypes` for C# to split same-arity *different-type*
  overloads (`SerializeObject(value, Formatting)` vs `(value, settings)`) the
  generic split didn't catch.
- **PHP chain-root propagation** — recover the TPs dropped by the
  ambiguous-fluent-chain rule (`addStmt().getNode`) by threading the chain's root
  type through `return $this` links; lifts PHP recall back up without ceding the
  precision gain.
- **C/C++ hygiene** — exclude generated `build/` dirs from indexing (kills the
  duplicate-header `json_decref` problem) and prefer the `.c` definition over the
  header prototype as call target.

### Wave 2 — Cross-cutting recall: bounded dispatch rescue

Generalize Go's interface-satisfaction dispatch rescue to **PHP, C#, Java, Rust**:
when a receiver resolves to an interface/abstract/trait that declares the method,
emit reduced-confidence, capped edges to in-scope implementations (Sweep #5 +
the "interface/trait rescue stays reduced-confidence and capped" invariant). This
is the single highest-aggregate-F1 lever — it recovers the polymorphic-dispatch
FNs that dominate four languages (PHP `getType`/`getSubNodeNames`, C# virtual
dispatch, Rust trait methods, Java interface dispatch) — and is evidence-gated so
it does not inflate precision loss.

### Wave 3 — Project-aware scope (precision; the big structural one)

Replace repo-wide scope with project-aware scope (Sweep #2), **routed**: apply
where it nets positive, keep repo-wide as fallback where tightening would cost
recall.

- **Java**: package/import records; wildcard/static imports carry metadata so they
  stop behaving like broad basename matches (directly attacks the precision floor).
- **C#**: `.csproj` project graph — scope to caller's project + referenced
  projects.
- **PHP**: Composer PSR-4/`autoload-dev`/classmap as primary scope; repo-wide as
  fallback.
- **C/C++**: `compile_commands.json` + resolved `#include` edges as scope — but
  only after confirming on jansson it does not regress recall (the high-P/low-R
  trap); keep unique header/impl-twin and linked-TU evidence.
- **Rust**: expand grouped/glob `use` + `pub use` re-exports; preserve crate/module
  identity (crate-wide scope stays).

Each change gated on Wave 0's FP corpus + fanout metrics.

### Wave 4 — Consolidation & policy

- **Single explainable `CallResolution` pipeline** (Sweep #3): ordered stages
  (self/this → type → local-type → call-result → import/module → overload/arity/
  generic → dispatch rescue → bounded bare-name), each emitting a reason code.
  Worth doing now — Wave 0's reason-tags + FP corpus give it a measurable payoff
  and a regression net it lacked before. Consolidates the per-language branches.
- **Confidence as policy** (Sweep #4): policy profiles for `tests`/`impact`/
  `certification`/`diagnostic`; default-traverse native/AST-narrowed edges,
  opt-in for regex/broad-dispatch/name-derived; report excluded evidence. Feeds
  RFC #5 (the tests-edge "related ≥0.8 / possibly-related <0.8" tiers).

### Wave 5 — Targeted recall tail

Only after scope is tight (Sweep #5): Java constructors/method-refs/static
imports; TS/TSX JSX usage + path aliases + overload-impl selection; Python
relative imports + dataclass/attrs/pydantic field types + classmethod policy;
Rust `<Type as Trait>::method` + trait-default dispatch; C/C++ include-scoped free
functions + C++ namespaces/constructors/`ptr->method`; PHP traits + callable
arrays + Pest.

### Expected trajectory

Wave 1 nudges C# and PHP a few points each (precision/recall recovery). Wave 2 is
the broad recall lift across PHP/C#/Rust/Java. Wave 3 is the precision lift,
concentrated on Java (the lowest-precision language) and cross-project C#. Waves
4–5 are consolidation and the long tail. Floors to watch: PHP and C# (just
moved), then Rust and C/C++ (recall-bound, partly astkit/macro-limited), with
Java precision as the standing structural target.

## Supported Languages

Grove currently maps these code languages through astkit:

- `go`
- `python`
- `javascript`
- `typescript`
- `tsx`
- `java`
- `rust`
- `c`
- `cpp`
- `csharp`
- `php`

## Cross-Language Fixes

### 1. Keep extraction parity tests for every supported language

Current status:

- Partially implemented. astkit/Grove now have broad extraction tests,
  call-edge scorecards, and CI baselines across the supported language set, but
  the strict feature matrix below is still needed so a supported language cannot
  silently miss imports, call forms, parentage, or negative-scope cases.

Grove targets:

- `internal/parser/engine_test.go`
- `internal/parser/metadata_test.go`
- `internal/graph/edges_test.go`
- `internal/native/native_test.go`

astkit targets:

- `strategies/strategies_test.go`
- `strategies/metadata_test.go`

Implementation:

- Add one fixture per language that asserts:
  - stable symbol count
  - parent-qualified members do not collide
  - imports are normalized and deduplicated
  - call sites include receiver-qualified calls
  - constructor/new calls are captured where the language has them
  - type-use and inheritance relationships can be derived
  - comments and string literals do not create false call edges
- Make every test assert concrete expected names. Remove "note: missing may be
  OK" style tests where the behavior must be production reliable.
- Add a table-driven Grove parser test that extracts the same minimal feature
  matrix for all languages and fails if any supported language silently drops a
  category it claims to support.

Acceptance:

- New extraction tests fail against known missing behaviors and pass after
  astkit fixes.
- Grove graph tests assert exact edge sets for focused fixtures, including
  negative assertions for out-of-scope same-name symbols.

### 2. Normalize import records before Grove scope matching

Current status:

- Partially implemented. astkit `ImportStatement` already carries `Raw`,
  `Path`, `Alias`, `Group`, and `Line`, but Grove currently projects imports
  down to a per-symbol `[]string` of paths. That loses alias/static/wildcard
  metadata before graph resolution.
- Several extractors still emit raw or partially normalized paths: Java strips
  `import`/`static` but leaves wildcard paths as strings, Rust keeps grouped
  `use` declarations opaque, C# keeps alias/static/global `using` syntax in
  `Path`, and PHP stores namespace-use/require text as raw path text.

Grove targets:

- `internal/parser/treesitter.go`: `extractImportsFromAST`
- `internal/parser/engine.go`: `extractImports`
- `internal/graph/edges.go`: `newEdgeIndex`, `importedFiles`,
  `lastImportSegment`

astkit targets:

- `strategies/registry.go`: every `ExtractImports` implementation
- `symbol.go`: consider extending `ImportStatement` if API change is allowed

Implementation:

- Add a small normalization layer in Grove after astkit import extraction:
  - preserve `Raw`
  - expose a normalized `Path`
  - strip quotes, trailing semicolons, language keywords, and unsupported
    wildcard tokens
  - keep enough metadata for alias/static/wildcard behavior where available
- In astkit, normalize at extraction time per language so all downstream
  consumers get stable paths.
- Do not let raw wildcard paths such as `java.util.*`,
  `com.acme.Utils.*`, `use foo::{bar, baz}`, or PHP grouped uses enter Grove's
  import scope as opaque strings.
- Add language-specific import expansion where source syntax names multiple
  import targets in one declaration.

Acceptance:

- `importedFiles` resolves imports deterministically without basename-only
  accidental matches when better path evidence exists.
- Wildcard imports never create broad repo-wide scope without package evidence.
- Aliased imports are represented explicitly enough for call/type narrowing.

### 3. Distinguish AST absence from "no calls"

Current status:

- Partially implemented at the language level. `astCallSiteLanguages` now
  includes `go`, `python`, `javascript`, `typescript`, `tsx`, `java`, `rust`,
  `csharp`, `php`, `c`, and `cpp`, so an empty `CallSites` list skips regex
  fallback for those languages.
- TSX coverage is explicitly pinned by `internal/graph/tsx_test.go`.
- Still open at the symbol level. Grove cannot currently distinguish "body
  visited and no calls" from "strategy supported the language but missed a node
  form inside this symbol." Partial parses can merge regex symbols, but there
  is no per-symbol call-extraction status.

Grove targets:

- `internal/graph/edges.go`: `buildCalls`, `astCallSiteLanguages`
- `internal/parser/treesitter.go`: `projectSymbol`

astkit targets:

- `symbol.go`: add optional extraction status if API change is allowed
- `strategies/metadata.go`: language call-site collectors

Implementation:

- Track whether call-site extraction was actually supported and completed for a
  symbol, not just whether `CallSites` length is zero.
- In Grove, only treat empty call sites as authoritative when astkit can prove
  the callable body was visited successfully.
- If astkit returns no call sites because the grammar missed a node kind, allow
  a constrained fallback for that symbol:
  - scan stripped body once
  - only match candidates in exact scope
  - cap fanout
  - emit lower confidence
  - tag source as heuristic

Acceptance:

- Syntax-error and unsupported-node fixtures recover real calls without
  matching signatures, comments, or strings.
- Empty methods/functions remain empty and do not get fallback self matches.

### 4. Add edge evidence policy that consumers can trust

Current status:

- Implemented for source tagging. `core.Edge` has `Source`; native analyzer
  edges set `EvidenceSourceNative`; and every graph-built edge now sets a
  meaningful `Source` at construction (`EvidenceSourceASTKit` for AST call
  sites / AttrSites and structural defines/contains/imports;
  `EvidenceSourceHeuristic` for constructor/super/inheritance/dispatch and
  name-resolved uses-type/tests/extends/implements; `EvidenceSourceRegex` for
  the body fallback). `mergeEdges` keeps the higher-confidence duplicate and
  carries its source. The `unknown` fallback in `mergeEdges` is now a safety
  net, not the common case — guarded by `TestBuildEdges_EverySourceTagged`.
- Open: confidence-aware policy on the consumer side (test traversal /
  certification opting out of weak edges) is not yet wired.

Grove targets:

- `internal/core/types.go`
- `internal/graph/edges.go`
- `internal/graph/graph.go`
- `internal/native/*.go`

Implementation:

- Ensure every edge has a meaningful `Source`:
  - astkit call-site based: AST source
  - regex/body fallback: heuristic source
  - native analyzer: native source
  - stored/carry-forward edges: preserved original source
- Confidence bands — these are **target** bands; the top tier is only reachable
  today for the two languages with a real semantic analyzer (Go `go/types`, TS
  compiler API). For the other eight languages, native call edges are retired,
  so the AST-narrowed band is the ceiling and the compiler tier stays empty
  until/unless an optional compiler-backed analyzer is added (out of scope —
  see Native Analyzer notes). Bands should ultimately be validated against
  measured per-source precision rather than asserted.
  - `0.98-0.99`: compiler/typechecker resolved (Go/TS native only)
  - `0.93-0.97`: AST exact with import/type narrowing (`astkit` source)
  - `0.80-0.89`: constructor/super/inheritance heuristic with strong scope
  - `0.60-0.79`: fallback or dynamic dispatch, not acceptable for strict tests
  - `<0.60`: diagnostic only, not used for impact/test closure by default
- Update test traversal and certify paths to ignore low-confidence call edges
  unless policy explicitly opts in.

Acceptance:

- `TestsFor`, `Impact`, and certification can explain why an edge exists and
  can exclude weak edges without losing strong native/AST evidence.

### 5. Replace broad basename scope with language-aware resolution

Current status:

- Partially implemented. Grove has exact relative import resolution with index
  files for JS/TS-family imports, Go/Java same-directory package scope, Rust
  crate-wide/workspace crate scope, precise import-qualifier narrowing, and
  repo-wide scopes for C#/PHP/C/C++ where link/assembly/library visibility is
  broader than file imports.
- Still open: generic basename/package fallback remains in `importedFiles`,
  C/C++ call scope is repo-wide rather than include-graph-driven, PHP/C#
  namespace identity is not preserved in symbol IDs, and Java wildcard/static
  imports still lack metadata.

Grove targets:

- `internal/graph/edges.go`: `importedFiles`, `resolveRelativeImport`,
  `lastImportSegment`, `narrowByImport`

Implementation:

- Split import resolution by language family:
  - Go/Rust/Python package/module imports can name directories.
  - JS/TS/TSX relative imports name files or index files.
  - Java package imports are dot paths and same-package files.
  - C/C++ includes are path-based headers.
  - C# namespaces are not file imports; project references matter.
  - PHP uses PSR-4 namespace resolution.
- Remove or heavily downgrade generic basename matching when a language has a
  stronger resolver.
- Keep fallback basename matching only for local files and only when fanout is
  small.

Acceptance:

- Same basename in unrelated packages does not enter scope.
- Relative imports resolve to exact files plus `index` conventions for JS/TS.
- Java package imports do not pull unrelated same-named directories.

### 6. Harden graph accuracy measurement per language

Current status:

- Partially implemented. `eval/cmd/grove-eval` now supports truth/score flows
  for Go, Python, JS/TS, Java, Rust, C#, PHP, and C-family languages. CI gates
  pinned call-edge baselines for gin, flask, socket.io, express, commons-lang,
  ripgrep, Newtonsoft.Json, PHP-Parser, and jansson, and gates a Flask
  tests-edge baseline. Keep building on that infrastructure instead of creating
  a parallel measurement path.

Grove targets:

- `eval/`
- `.github/workflows/eval.yml`
- new fixtures under `testdata/` or `internal/graph/testdata/`

Implementation:

- Keep and extend the existing per-language eval baselines.
- Add a per-edge explanation payload to eval output:
  - source (`native`, `astkit`, `heuristic`, `regex`)
  - confidence
  - resolver reason
  - initial candidate count
  - final candidate count
  - scope reason
- Add strict feature fixtures per language with expected edges:
  - imports
  - calls
  - constructors
  - type-use
  - extends/implements
  - tests edges
  - negative same-name non-scope cases
- Report precision, recall, false-positive count, and fanout distribution per
  language and per evidence source.
- Add "coverage" as feature coverage, not only score coverage:
  - symbol categories covered
  - call forms covered
  - import forms covered
  - test detection covered
- CI should continue to fail on precision/recall regression against baselines,
  any new false-positive in strict fixtures, or a fanout increase that crosses a
  configured language/source budget.

Acceptance:

- Every production language has a reproducible quality score and strict feature
  fixture.
- A change to one language cannot silently regress another.
- Scorecards identify the top false-positive sources by language, resolver
  reason, confidence band, and scope reason.

## Java

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. Java call-site extraction covers
  `method_invocation` and `object_creation_expression`, generic constructor
  names are reduced to the bare type, and call sites carry `Argc` plus simple
  literal/identifier argument tokens.
- Still open: explicit constructor invocation (`super(...)`, `this(...)`),
  method references, parenthesized/chained receiver edge cases, multi-field
  declarations, wildcard/static import metadata, and richer argument tokens.

Targets:

- `strategies/registry.go`: `javaStrategy.ExtractImports`
- `strategies/extractors.go`: `javaFieldDecl`, `javaMethodDecl`,
  `javaTypeDecl`
- `strategies/metadata.go`: `javaCallSites`, `qualifierName`, `argToken`
- `strategies/metadata_test.go`

Fixes:

- Normalize wildcard and static imports:
  - `import java.util.*;` should expose package path `java.util` with wildcard
    metadata.
  - `import static com.acme.Utils.*;` should expose owner `com.acme.Utils`,
    static flag, and wildcard metadata.
  - `import static com.acme.Utils.make;` should expose member `make` and owner
    `com.acme.Utils`.
- Split multi-variable field declarations:
  - `int a, b, c;` must emit fields `a`, `b`, `c`.
  - use every `variable_declarator`, not only the first.
- Add call-site coverage for:
  - `explicit_constructor_invocation`: `super(...)`, `this(...)`
  - `method_reference`: `Type::method`, `expr::method`, `Type::new`
  - parenthesized receivers: `(obj).next()`
  - chained receiver calls where the receiver is a call result
  - enum constant class bodies if tree-sitter exposes methods there
- Improve argument tokens:
  - current support includes strings, numeric literals, booleans, chars, casts,
    field `.length`, and simple call-result markers.
  - `null` -> `#null`
  - arrays -> `#Array:<element>` when simple
  - class literals -> `#Class:<type>`
  - lambdas/method refs -> `#functional`
- Preserve class nesting in `ParentName`, for example `Outer.Inner.method`.

Grove fixes:

Current status:

- Partially implemented. Grove has `javalocaltypes.go`, same-package Java
  scope, arity filtering, argument-type conflict rejection, Java literal
  widening/boxing for overloads, call-result return-type narrowing, base-class
  constructor/super resolution, and native Java evidence for type-use and
  inheritance only.
- Still open: Java import records remain string-only, unknown lowercase
  receivers are hard-dropped rather than confidence-scored, local type inference
  does not cover all assignment/control-flow forms, and overload compatibility
  is still structural rather than compiler-semantic.

Targets:

- `internal/parser/treesitter.go`: `extractImportsFromAST`, `projectSymbol`
- `internal/graph/edges.go`: Java path inside `buildCalls`,
  `narrowByReceiver`, `narrowByLocalType`, `narrowByImport`,
  `constructorTargets`
- `internal/graph/javalocaltypes.go`
- `internal/native/java.go`
- `internal/parser/metadata_test.go`
- `internal/graph/edges_test.go`

Fixes:

- Stop treating raw Java wildcard imports as literal file/package names.
- Add structured import records for:
  - exact type import
  - package wildcard import
  - static member import
  - static wildcard import
  - same-package implicit scope
- Extend current local type inference:
  - current support covers typed parameters, typed locals, class fields,
    enhanced-for-shaped declarations, ancestor fields, and simple return-type
    propagation for call-result receivers.
  - `var x = new Type()`
  - `x = new Type()`
  - `x = other` where `other` is typed
  - `for (Type x : xs)`
  - `catch (Type e)`
  - casts: `x = (Type) y`
  - `this.field` from field declarations
  - constructor parameter assignment: `this.db = db`
- Replace the hard drop for unknown lowercase receivers with confidence-aware
  behavior:
  - if receiver type is known, keep only matching parent type
  - if receiver is unknown but there is one in-scope exact method candidate,
    keep it at reduced confidence
  - if many same-name candidates exist, drop or dispatch only through interface
    evidence
- Complete Java type compatibility for overload narrowing:
  - current support covers arity, simple identifier/call-result argument types,
    literal primitive widening, boxing for literals, generic type variables,
    and common widening supertypes.
  - boxing/unboxing for non-literal identifiers
  - `null` compatibility with reference types
  - simple interface/base-class compatibility
  - generic erasure
  - varargs
- Add `super(...)` and `this(...)` constructor edges.
- Resolve `Type::method` as a type-use edge and, when a target method is in
  scope, as a reduced-confidence call edge.
- Strengthen `native/java.go`:
  - current analyzer is heuristic type-use/inheritance evidence, not a
    compiler-semantic call resolver.
  - add an optional `javac` or JDT-backed path when feasible.
  - at minimum, use project source roots from Maven/Gradle layout to scope
    same-package and imports.
- Remove unused Java regex call helpers if they are no longer used, or wire them
  only as low-confidence fallback with tests.

Tests:

- `import java.util.*`
- `import static com.acme.Utils.*`
- `import static com.acme.Utils.make`
- two packages with same `Service.run`
- overloaded `run(int)`, `run(long)`, `run(String)`, `run(Object)`
- `super(...)`, `this(...)`, `super.close()`
- `String::trim`, `Foo::new`
- `(repo).save()`
- `int a, b, c`

Acceptance:

- Java wildcard/static imports improve recall without adding edges to unrelated
  same-name methods.
- Overload-heavy fixtures reduce false positives.
- Constructor and method-reference fixtures add missing recall.

## Go

### astkit fixes

Current status:

- Mostly implemented for the core call graph. astkit emits receiver-qualified
  Go call sites, generic/type parameter metadata, grouped const/var symbols in
  common cases, and import alias/group metadata.
- Remaining Go astkit work is strict coverage for multi-name grouped
  declarations, generic receiver edge cases, function literals assigned to
  package vars, and more argument-token forms.

Targets:

- `strategies/extractors.go`: `goConstDecl`, `goVarDecl`, `goTypeDecl`,
  `goMethodSym`
- `strategies/metadata.go`: `goCallSites`, `qualifierName`, `argToken`
- `strategies/registry.go`: `goImportSpec`
- `strategies/metadata_test.go`

Fixes:

- Make grouped const/var extraction strict:
  - `const (A, B = 1, 2)` should emit both names or deliberately skip with a
    test if tree-sitter cannot represent it safely.
  - existing "note: missing may be OK" tests should become hard assertions.
- Preserve receiver type for generic receivers:
  - `func (s *Store[T]) Get(...)` -> parent `Store`
  - aliases and pointer/qualified receiver forms should reduce to the bare type.
- Add call-site coverage for:
  - deferred calls
  - goroutine calls
  - function literals assigned to package vars when extracted as symbols
  - selector calls through parenthesized expressions
- Add argument tokens:
  - string/int/float/bool/rune literals already mostly exist; add `nil`,
    composite literals, address-of simple identifiers.
- Preserve import alias metadata:
  - normal alias
  - dot import
  - blank import

Grove fixes:

Current status:

- Mostly implemented for the current measured quality bar. Go has native
  `go/types` call/type-use evidence, same-package scope, local type inference,
  interface satisfaction/dispatch rescue, and CI call-edge baselines.
- Still open: native-vs-heuristic suppression is only duplicate-edge
  confidence merge today; native edges do not globally suppress lower-confidence
  false positives with the same callee name.

Targets:

- `internal/native/go.go`
- `internal/graph/localtypes.go`
- `internal/graph/edges.go`
- `internal/parser/treesitter.go`
- `internal/native/native_test.go`
- `internal/graph/edges_test.go`

Fixes:

- Prefer native `go/types` call edges over heuristic/AST edges when both exist.
- Add conflict resolution in edge merge so a native edge can suppress lower
  confidence false positives for the same caller/callee name in scope.
- Extend local type inference:
  - short declaration from constructor: `x := NewStore()`
  - composite literal: `x := Store{}`
  - address composite literal: `x := &Store{}`
  - assignment propagation: `x = y`
  - map/slice element remains unknown unless indexed type evidence is exact.
- Expand `go/types` handling:
  - interface method dispatch where concrete implementation is visible
  - embedded promoted methods
  - generic method/function instantiation
- Keep regex fallback out of Go when AST/native extraction completed.

Tests:

- same file with two receivers both defining `Close`
- generic receiver method
- embedded method call
- imported package same symbol name in two directories
- `defer f()`, `go f()`
- local constructor assignment and receiver call

Acceptance:

- Go remains the high-confidence reference language.
- Native edges do not coexist with contradictory broad heuristic fanout.

## Python

### astkit fixes

Current status:

- Partially implemented. astkit emits Python call sites and `AttrSites` for
  property reads, and Grove has measured improvements from decorator/property
  handling on Flask.
- Remaining Python astkit work is import alias/relative metadata, nested
  parentage policy, more receiver shapes, decorator semantics tests, and richer
  argument tokens.

Targets:

- `strategies/extractors.go`: `pythonVisitDefinition`
- `strategies/metadata.go`: `pythonCallSites`, `pythonAttrSites`,
  `qualifierName`, `argToken`
- `strategies/registry.go`: `pythonStrategy.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Normalize imports:
  - `import a as b` should record path `a`, alias `b`
  - `import a, b` should emit two imports
  - `from . import x` should preserve relative level and imported name
  - `from pkg import a as b, c` should expose `pkg.a`/`pkg.c` or path plus
    member metadata
- Preserve nested class/function parentage:
  - `Outer.Inner.method`
  - nested functions should either be extracted with a parent or deliberately
    excluded with tests.
- Improve call-site qualifiers:
  - `(obj).method()`
  - `await obj.method()`
  - `factory().method()`
  - `super().method()`
- Add decorator call semantics:
  - decorators should remain annotations and optionally produce type/use edges,
    but should not become calls from the decorated function body.
- Add argument tokens:
  - `None`, list/dict/set literals, keyword argument names.

Grove fixes:

Current status:

- Partially implemented. Grove has `pylocaltypes.go`, property-read edges,
  decorator wrapper edges, super/cls handling, inherited member lookup, Python
  dynamic-call eval baselines, and a Flask tests-edge baseline.
- Remaining Python work is relative/package import precision, richer
  dataclass/attrs/pydantic inference, classmethod/staticmethod edge cases, and
  framework-dispatch ceilings that need explicit policy.

Targets:

- `internal/native/python.go`
- `internal/graph/pylocaltypes.go`
- `internal/graph/edges.go`
- `internal/core/testdetect.go`
- `internal/graph/edges_test.go`

Fixes:

- Improve import scope:
  - use AST import records rather than dotted strings only
  - resolve relative imports against package paths
  - handle package `__init__.py`
  - avoid repo code execution during import resolution; current `PathFinder`
    approach is correct and should be kept.
- Improve local type inference:
  - `x: Type`
  - `x = Type()`
  - `self.x = Type()`
  - dataclass/attrs/pydantic field annotations
  - `typing.Optional`, `typing.Union`, `X | None`, `list[X]` where useful
  - assignment propagation
- Strengthen `self`/`cls` resolution:
  - classmethods should treat first parameter as class holder
  - staticmethods should not treat first parameter as self
  - inherited attribute/property lookup should stay bounded and scoped.
- Python tests:
  - detect `test_*.py`, `*_test.py`, `pytest` test functions, unittest
    methods, async tests, parametrized tests.
  - link test helper calls transitively within the same test module.

Tests:

- `from .service import Service`
- `from pkg import a as alias, b`
- two modules both defining `open`
- dataclass field type driving `self.repo.save()`
- classmethod constructor alternative
- pytest fixture/helper transitive call

Acceptance:

- Python import precision improves for package layouts.
- Property and inherited method edges do not create broad same-name fanout.

## JavaScript

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. JS/TS extraction now covers JSX
  syntax through the JS grammar, CommonJS-style assignment functions
  (`exports.x =`, `app.listen =`, `X.prototype.y =`), class fields, super call
  sites, and receiver-qualified call sites.
- Remaining JS work is import/export completeness, dynamic imports, broader
  CommonJS shapes, object literal methods, optional/tagged-template forms, and
  strict negative fixtures.

Targets:

- `strategies/extractors.go`: `extractJSNodes`, `jsVisitChild`,
  `jsClassDecl`, `jsMethodDef`, `jsArrowDecl`, assignment function extraction
- `strategies/metadata.go`: `jsCallSites`, `qualifierName`, `argToken`
- `strategies/registry.go`: `jsLike.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Import extraction must include:
  - `import x from "m"`
  - `import {x as y} from "m"`
  - `import "side-effect"`
  - `export {x} from "m"`
  - `export * from "m"`
  - dynamic `import("m")` as a lower-confidence dependency
  - CommonJS `require("m")`
- Extract exported variable functions and object methods:
  - `module.exports = function`
  - `exports.x = function`
  - `module.exports.x = function`
  - object literal methods when assigned/exported.
- Call sites:
  - optional chaining: `obj?.method()`
  - parenthesized receiver: `(obj).method()`
  - tagged template calls
  - `await`, `yield`, `new`
  - `super.method()` and `super()`
- Preserve private class fields/methods where grammar supports them.

Grove fixes:

Current status:

- Partially implemented. Grove has exact relative import resolution with index
  convention, TS/JS local type inference, native TypeScript compiler-API import
  and call/type evidence when `node` and a project-local `typescript` package
  are available, and CI baselines for socket.io and express.
- Still open: the native analyzer still requires project-local `typescript`;
  plain JavaScript projects without that package rely on astkit plus graph
  heuristics.

Targets:

- `internal/native/js_ts.go`
- `internal/graph/tslocaltypes.go`
- `internal/graph/edges.go`
- `internal/core/testdetect.go`

Fixes:

- Do not require a project-local `typescript` package for plain JavaScript
  fallback quality. If TypeScript is unavailable, still use astkit plus exact
  relative import scope.
- Native analyzer should resolve:
  - JS with `allowJs`
  - `jsconfig.json`
  - package `exports`/`main` where feasible
  - `.mjs`, `.cjs`, `.jsx`
- Improve local type inference:
  - constructor assignments: `const x = new X()`
  - JSDoc `@type` and `@param`
  - class field initialization
  - `this.x = new X()` in constructors
- Keep dynamic JS fallback confidence low unless exact local evidence exists.

Tests:

- CJS `require`
- `module.exports`
- optional chaining
- side-effect import
- export-from re-export
- `.jsx` component calls

Acceptance:

- JS projects without TypeScript still produce scoped, useful graphs.
- CommonJS does not fall through to broad regex-only matching.

## TypeScript and TSX

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. TS/TSX extraction handles TSX via
  the TSX grammar, abstract classes/method signatures, assignment-style
  functions, class fields, super call sites, and receiver-qualified call sites.
- Remaining TS/TSX work is import/export metadata, path aliases, overload
  signature policy, namespaces/modules, JSX component usage evidence, and
  strict React/hook fixtures.

Targets:

- Same JS/TS files as JavaScript in astkit.

Fixes:

- Import/export coverage:
  - type-only imports
  - namespace imports
  - import equals
  - path aliases should be represented raw for Grove/native resolver.
- Symbol extraction:
  - overloaded function signatures plus implementation
  - abstract/interface method signatures
  - enum members
  - namespaces/modules
  - React function components assigned to const
  - hooks and exported arrow functions
- Call sites:
  - JSX component usage should create type-use or call-like evidence for local
    components, depending on policy.
  - optional chaining, non-null assertions, `as` expressions, parenthesized
    expressions.
- Type arguments and generic signatures should not break name extraction.

Grove fixes:

Current status:

- Partially implemented. Native TS compiler-API enrichment resolves imports,
  calls, and type references when a project config and `typescript` package are
  available. Grove also has TS/JS local type inference for annotations,
  constructor parameter properties, class fields, and simple `new` assignments.
- TSX is now included wherever Grove treats `typescript` and `javascript` as
  AST/typed-local call languages; `internal/graph/tsx_test.go` pins empty
  call-site fallback suppression and receiver local-type narrowing.
- Still open: path aliases/exports need fuller coverage, native symbol mapping
  is still by file plus bare name, and JSX evidence needs explicit policy.

Targets:

- `internal/native/js_ts.go`
- `internal/graph/tslocaltypes.go`
- `internal/graph/edges.go`
- `internal/core/testdetect.go`

Fixes:

- In native TypeScript analyzer:
  - map method symbols by parent-qualified name, not only file plus bare name
  - handle overloaded declarations by preferring implementation declaration
  - resolve property access target symbols for methods and class fields
  - resolve JSX opening elements to component declarations
  - include `ExportDeclaration` and path alias resolution from `tsconfig`
- Grove local type inference:
  - constructor parameters with access modifiers
  - `const x: Type`
  - `const x = new Type()`
  - `this.x = new Type()`
  - return type propagation for simple factory calls when native analyzer is not
    available.
- Tests:
  - `.test.tsx`, `.spec.tsx`
  - React Testing Library component tests
  - Vitest/Jest `describe/it/test`

Tests:

- path alias import `@/service`
- overloaded method call
- JSX `<Button />`
- namespace import `import * as api`
- type-only import should create type-use scope but not runtime call evidence

Acceptance:

- TS/TSX native edges dominate heuristic edges.
- JSX and path aliases improve recall without basename false positives.

## Rust

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. Rust call-site extraction now
  covers normal call expressions, macro invocations, generic-function
  turbofish forms, scoped identifiers, field-expression receiver calls, and
  macro token-tree calls.
- Keep the remaining Rust work focused on import expansion, hard negative
  fixtures, trait/impl edge correctness, argument tokens, and call forms that
  still need explicit coverage.

Targets:

- `strategies/extractors.go`: `extractRustNodes`
- `strategies/metadata.go`: `rustCallSites`, `argToken`
- `strategies/registry.go`: `rustStrategy.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Import extraction:
  - expand grouped uses: `use a::{b, c};`
  - expand nested grouped uses: `use a::{b::{c, d}, e};`
  - handle aliases: `use a::b as c;`
  - handle glob imports: `use a::*;` with wildcard metadata
  - handle `pub use` re-exports.
- Symbol extraction hardening:
  - assert methods inside trait impls parent to the concrete type
  - assert trait methods parent to the trait
  - assert inherent impl methods parent to the type
  - assert inline modules and impl bounds keep stable parentage
  - macros that define functions are out of scope unless expanded, but add tests
    documenting current behavior.
- Call-site hardening:
  - `Type::associated()`
  - `Self::new()`
  - `<Type as Trait>::method()`
  - `receiver.method()`
  - macro invocations
  - closure calls where directly named.
- Argument tokens:
  - string/int/float/bool literals
  - `None`, `Some(...)`, `Ok(...)`, `Err(...)`
  - borrow/address forms.

Grove fixes:

Current status:

- Partially implemented. Grove has Rust crate/workspace scope, module edges for
  `mod`/`pub mod`, `rustlocaltypes.go`, builder-chain/call-result receiver
  handling, static-typing unknown-receiver drops, and a Rust eval baseline
  against ripgrep.
- Native Rust still uses `cargo metadata` and module scanning for project shape
  plus text-derived type-use/implements evidence; it intentionally emits no
  call edges. `rust-analyzer` is currently an eval oracle, not a native
  enrichment path.

Targets:

- `internal/native/rust.go`
- `internal/graph/edges.go`
- `internal/graph/rustlocaltypes.go`
- `internal/core/testdetect.go`

Fixes:

- Replace raw `use` strings in import scope with expanded module paths.
- Expand module resolution:
  - `mod x;`
  - `pub mod x;`
  - `crate::x`, `super::x`, `self::x`
  - `lib.rs`, `main.rs`, `mod.rs`
- Native analyzer:
  - keep the current no-call-edge policy for `cargo metadata`/text-derived
    evidence.
  - add optional `rust-analyzer` or `cargo check` JSON integration later for
    high-confidence call/type edges.
- Extend existing Rust local type inference:
  - current support covers typed lets, constructor-convention lets, function
    return types, builder chains, closure parameters, fields, generic bounds,
    and one-hop field types.
  - `let x = Type { ... }`
  - `let x = Default::default()` only if annotated
  - assignment propagation.
- Test detection:
  - current support recognizes `#[test]`, `#[tokio::test]`,
    `#[async_std::test]`, and directory-based integration tests.
  - add strict fixtures for tests under inline `mod tests` and helper
    traversal edge cases.

Tests:

- grouped `use`
- `Self::new`
- trait impl method parentage
- same method name across two impl blocks
- integration test calling library function

Acceptance:

- Rust import scope no longer treats grouped uses as one opaque path.
- Method edges respect impl parent type or trait evidence.

## C

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. C/C++ function symbols now carry
  `cCallSites` for plain calls, member calls, scoped calls, template calls, and
  `new` expressions where the grammar exposes them.
- Remaining C astkit work is symbol/import metadata hardening: prototypes vs
  definitions, macro policy, typedef/multi-declarator coverage, function
  pointer calls, and local/system include metadata.

Targets:

- `strategies/extractors.go`: `extractCNodes`
- `strategies/metadata.go`: C call-site extraction for `call_expression`
- `strategies/registry.go`: `cStrategy.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Symbol extraction:
  - function definitions and prototypes should be distinguished
  - macros defining constants/functions should either be extracted as macro
    symbols or deliberately excluded with tests
  - typedefs should emit the alias name
  - multi declarators should not emit only the first name if they are globals
- Call sites:
  - keep current `call_expression` extraction covered by strict tests.
  - capture function-pointer calls separately as weak evidence.
- Include extraction:
  - preserve system vs local include metadata.
  - normalize path without quotes/brackets.

Grove fixes:

Current status:

- Partially implemented. Grove treats C and C++ as AST call-site languages,
  has `cfamilylocaltypes.go`, gates regex fallback off for empty AST call-site
  lists, and has a jansson C-family eval baseline.
- Still open: graph call scope for C/C++ is repo-wide today; include graph
  evidence from `compile_commands.json` exists in the native analyzer but is
  not yet the primary call-resolution scope.

Targets:

- `internal/native/cfamily.go`
- `internal/graph/edges.go`

Fixes:

- Current C/C++ native analysis intentionally does not emit text-matched call
  edges. Keep that policy. Use the existing AST call sites with graph-side
  scoped resolution so C calls avoid repo-wide same-name fanout.
- Improve `compile_commands.json` parsing:
  - support response files if simple
  - support `-iquote`, `-idirafter`, `-isystem`, `-Ifoo`, `/Ifoo`
  - handle `arguments` without shell splitting when present
  - avoid naive `strings.Fields` for `command` where quotes matter.
- Scope AST-derived calls through include graph:
  - same `.c` file
  - directly included headers
  - header implementation pairs by basename only when unique
  - no global same-name function fanout.
- Type-use:
  - structs/enums/typedefs from included headers only.

Tests:

- two headers both defining `init`
- local include vs system include
- typedef alias
- prototype plus definition
- function pointer call should not falsely edge to every same-name function

Acceptance:

- C false positives drop by avoiding repo-wide fallback.
- Include graph controls cross-file call/type scope.

## C++

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. C++ shares `cCallSites` with C and
  covers several ordinary function/member/scoped/template call forms. Symbol
  extraction has improved enough for eval, but C++ remains less semantically
  complete than Go/TS/Java/Rust.

Targets:

- `strategies/extractors.go`: C++ paths in `extractCNodes`
- `strategies/metadata.go`: C++ call-site extraction
- `strategies/registry.go`: C++ include extraction
- `strategies/metadata_test.go`

Fixes:

- Symbol extraction:
  - namespace-qualified functions
  - class methods declared inside and outside class body
  - constructors/destructors/operators
  - templates
  - nested classes
  - enum classes
- Call sites:
  - `obj.method()`
  - `ptr->method()`
  - `Type::staticMethod()`
  - constructors: `Type x(...)`, `Type{...}`, `new Type(...)`
  - operator calls where named target is recoverable
- Include extraction should keep local/system metadata.

Grove fixes:

Current status:

- Partially implemented. Grove has C-family local type inference for C++
  receiver variables and fields, C/C++ AST call-site enrollment, and native
  include/type-use evidence. C++ still lacks a dedicated scope/resolution policy
  rich enough for namespaces, templates, overloads, operators, and constructors.

Targets:

- `internal/native/cfamily.go`
- `internal/graph/edges.go`

Fixes:

- Separate C and C++ graph policies:
  - C++ has methods, constructors, namespaces, templates.
  - C does not.
- Add local type inference:
  - `Type x`
  - `Type* x`
  - `auto x = Type{}`
  - `auto x = makeType()` only when return type known
  - member fields from class declarations
- Resolve:
  - `obj.method()` through inferred local type
  - `ptr->method()` through inferred local type
  - `Type::method()` through parent type
  - namespace-qualified free functions.
- Avoid constructor regex matching any uppercase function call as a type.

Tests:

- two classes both with `run`
- `obj.run()` with local type
- `ptr->run()`
- `ns::free_fn()`
- template class method
- constructor forms

Acceptance:

- C++ method calls do not collapse to same-name global functions.
- Constructor/type-use recall improves without uppercase-call false positives.

## C#

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. C# call-site extraction now covers
  `invocation_expression` and `object_creation_expression`, including member
  access receivers, generic names, and a `CallSite.Generic` flag for explicit
  generic method/object-creation syntax.
- The remaining C# work is import metadata, richer symbol forms, property/field
  evidence, extension-method policy, and project/namespace-aware graph scope.

Targets:

- `strategies/extractors.go`: `extractCSharpNodes`
- `strategies/extractors.go`: `csCallSites`
- `strategies/registry.go`: `csharpStrategy.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Import extraction:
  - `using Namespace;`
  - `using Alias = Namespace.Type;`
  - `using static Namespace.Type;`
  - global usings
- Symbol extraction:
  - partial classes
  - records and primary constructors
  - properties, accessors, events, operators
  - extension methods
  - nested classes
  - async methods
- Call-site hardening:
  - `obj.Method()`
  - `Type.Static()`
  - `new Type()`
  - `await Method()`
  - extension method calls as weak candidates unless native analyzer confirms
  - property access should produce property-use evidence, not call evidence.

Grove fixes:

Current status:

- Partially implemented. Grove treats C# call sites as authoritative, uses
  repo-wide assembly-style scope, has `csharplocaltypes.go`, filters overloads
  by arity and generic-call shape, and keeps native C# call text-matching
  retired. `internal/graph/csharp_generic_test.go` pins generic vs
  non-generic same-arity overload narrowing, and the Newtonsoft.Json eval
  baseline is gated in CI.
- Still open: true namespace/project-reference scoping, alias/static/global
  `using` metadata, extension methods, non-generic same-arity overloads with
  identical argument shape, and Roslyn-backed native resolution.

Targets:

- `internal/native/csharp.go`
- `internal/graph/edges.go`
- `internal/graph/csharplocaltypes.go`
- `internal/core/testdetect.go`

Fixes:

- Keep AST call sites authoritative for C# and prevent generic regex fallback
  from reintroducing broad same-name fanout.
- Improve `.csproj` handling:
  - SDK-style default includes
  - `Compile Include/Remove`
  - project references
  - solution-level multi-project layouts
- Add optional Roslyn-backed native analyzer path if available. Without Roslyn,
  keep edges as AST/heuristic confidence, not compiler confidence.
- Extend current type inference:
  - current support covers explicit parameters, typed locals, `var x = new
    Type()`, `foreach`, class fields/properties, and ancestor fields.
  - constructor parameter properties
  - fields and properties assigned in constructor
  - nullable and generic type erasure
- Tests:
  - xUnit `[Fact]`, `[Theory]`
  - NUnit `[Test]`, `[TestCase]`
  - MSTest `[TestMethod]`

Tests:

- alias using
- static using
- partial class in two files
- extension method
- property vs method
- xUnit test calls service method

Acceptance:

- C# no longer depends on broad raw-text call matching for normal method calls.
- Project reference scope prevents same-name cross-project false positives.

## PHP

### astkit fixes

Current status:

- Partially implemented in astkit `v0.4.15`. PHP call-site extraction now
  covers free function calls, member calls, nullsafe member calls, scoped calls,
  and object creation.
- The remaining PHP work is namespace/use normalization, alias metadata,
  namespace-qualified symbol identity, callable-array coverage, and Grove
  resolution through PSR-4 and local type evidence.

Targets:

- `strategies/extractors.go`: `extractPHPNodes`
- `strategies/extractors.go`: `phpCallSites`
- `strategies/registry.go`: `phpStrategy.ExtractImports`
- `strategies/metadata_test.go`

Fixes:

- Import extraction:
  - namespace declarations should not be mixed with imports as raw paths.
  - `use Foo\Bar;`
  - `use Foo\Bar as Baz;`
  - grouped use: `use Foo\{Bar, Baz as Qux};`
  - function imports: `use function Foo\bar;`
  - const imports: `use const Foo\BAZ;`
  - require/include paths should be normalized separately from namespace uses.
- Symbol extraction:
  - class methods and constructors
  - traits
  - enums
  - interfaces
  - promoted constructor properties
  - namespace-qualified functions/classes.
- Call-site hardening:
  - `$obj->method()`
  - `self::method()`, `static::method()`, `parent::method()`
  - `ClassName::method()`
  - `new ClassName()`
  - global functions
  - callable arrays where simple.

Grove fixes:

Current status:

- Partially implemented. Grove treats PHP call sites as authoritative, has
  `phplocaltypes.go`, uses repo-wide library scope, reads Composer PSR-4 plus
  `autoload-dev` in the native analyzer, handles simple fluent call-result
  receiver narrowing through `phpCallResultType`, and keeps native PHP call
  text-matching retired. PHP-Parser is gated in CI.
- Still open: namespace-qualified symbol identity, grouped/function/const use
  metadata, PSR-4/classmap precision beyond the current native hints, trait
  resolution, and callable-array/Pest coverage.

Targets:

- `internal/native/php.go`
- `internal/graph/edges.go`
- `internal/graph/phplocaltypes.go`
- `internal/core/testdetect.go`

Fixes:

- Make PSR-4 resolution the primary cross-file scope mechanism instead of the
  current broad repo-wide PHP graph scope.
- Add namespace-aware symbol identity:
  - same short class name in two namespaces must not collide in matching.
  - Grove should carry namespace in `QualifiedName` or import metadata.
- Improve alias handling:
  - grouped use aliases
  - function/const imports
  - class aliases.
- Improve method resolution:
  - `$this->method()` to current class
  - `parent::method()` to base class
  - `self::method()`/`static::method()` to current class or late-static reduced
    confidence
  - `$service->run()` through constructor/property/local type inference.
- Composer:
  - current native support includes PSR-4 and `autoload-dev` PSR-4.
  - include `autoload.files`
  - include classmap when present
- Tests:
  - PHPUnit test class and `test*` methods
  - attributes `#[Test]`
  - Pest tests if extracting closures is supported, otherwise document gap.

Tests:

- grouped use aliases
- two namespaces with same class short name
- trait method use
- parent/self/static calls
- PSR-4 autoload-dev test target

Acceptance:

- PHP same short names across namespaces do not create false edges.
- `$this`, `self`, `parent`, and PSR-4 aliases drive method resolution.

## Native Analyzer Fixes By Language

### Go

- Keep `go/types` as the highest-confidence source.
- Preserve user environment; do not redirect `HOME` or module caches into the
  repo.
- Add native override behavior beyond duplicate-edge merge so lower-confidence
  AST/heuristic edges do not inflate fanout when native resolution exists.

### Python

- Keep `PathFinder` no-import behavior.
- Add package-root detection from `pyproject.toml`, `setup.cfg`, and editable
  layouts.
- Do not import repo modules during indexing.

### JS/TS/TSX

- Do not run arbitrary project code.
- Current native path loads a project-local `typescript` package via `node` and
  uses the compiler API to resolve imports, calls, and type references.
- JS fallback quality should still work without a project-local `typescript`
  package; today that means astkit plus graph resolution rather than native
  enrichment.

### Java

- Current native Java analyzer is heuristic type-use/inheritance evidence and
  intentionally emits no call edges. Either:
  - implement a real semantic path with JDT/javac, or
  - label it as heuristic and keep confidence below compiler-resolved edges.
- Use Maven/Gradle source roots and dependency layout for scoping.

### Rust

- Current native Rust analyzer uses `cargo metadata` plus module scanning for
  dependency/module shape and emits type-use/implements evidence, but no call
  edges.
- `cargo metadata` is dependency/module metadata, not call resolution.
- Add rust-analyzer integration only behind availability checks and timeouts.
- Keep text-derived call edges below compiler confidence.

### C/C++

- Current C/C++ native analysis should remain no-call-edge unless an optional
  clang-backed path resolves calls semantically. Text-matched C-family calls
  should not come back as high-confidence native edges.
- `compile_commands.json` is the right availability gate for include/type-use
  enrichment and future clang integration.
- Expand include argument support and avoid shell-splitting quoted commands
  incorrectly.
- Keep native C-family type-use/include evidence, but tag it explicitly as
  native and leave ordinary calls to AST call-site resolution.
- Consider clang tooling only as optional high-confidence enrichment.

### C#

- Current native C# analyzer parses `.csproj` files for project-reference
  structure and type-use/inheritance evidence, but emits no call edges.
- Add Roslyn integration if available; otherwise keep C# AST/heuristic edges
  confidence-scoped. The Roslyn code under `eval/cstruth` is currently a truth
  oracle, not a native analyzer.

### PHP

- Current native PHP analyzer reads Composer PSR-4 and `autoload-dev` PSR-4 and
  emits autoload/type-use/inheritance evidence, but no call edges.
- Composer PSR-4/classmap should drive file scope.
- Do not execute PHP code.
- Add classmap and `autoload.files` support; `autoload-dev` PSR-4 already
  exists.

## Graph Construction Fixes

Current status:

- Partially implemented. Receiver/type narrowing now exists across Go,
  Python, TS/JS, Java, Rust, C#, PHP, and C/C++, but it remains distributed
  across language-specific branches in `buildCalls` plus separate local type
  files.
- Edge de-duplication preserves the higher-confidence duplicate, native sources
  are tagged, and graph-built edges now set explicit `astkit`, `heuristic`, or
  `regex` sources. The remaining gap is policy metadata and consumer-side
  confidence handling, not basic source tagging.

Targets:

- `internal/graph/edges.go`
- `internal/graph/localtypes.go`
- `internal/graph/pylocaltypes.go`
- `internal/graph/tslocaltypes.go`
- `internal/graph/javalocaltypes.go`
- `internal/graph/rustlocaltypes.go`
- `internal/graph/csharplocaltypes.go`
- `internal/graph/phplocaltypes.go`
- `internal/graph/cfamilylocaltypes.go`

Implementation:

- Consolidate the existing receiver-resolution branches into a common flow:
  - parse `qualifier.name`
  - identify self/this receiver
  - identify type receiver
  - identify package/module alias
  - identify local variable type
  - identify call-result type
  - only then fall back to in-scope name match.
- Put language-specific local type providers behind one interface:
  - Go: existing plus constructor/assignment improvements
  - Python: existing plus dataclass/classmethod/staticmethod improvements
  - TS/JS: existing plus JSDoc and constructor assignment improvements
  - Java: existing plus assignment/enhanced-for/cast improvements
  - Rust: existing plus typed let and constructor expression inference
  - C/C++: existing simple local variable, parameter, pointer/reference, and
    field inference plus namespace/template/operator improvements
  - C#: existing plus `var`, explicit type, field/property inference
  - PHP: existing plus `$this`, constructor injection, docblock, typed
    properties
- Keep and extend edge de-duplication that preserves the strongest evidence:
  - if native and heuristic edge duplicate, keep native confidence/source.
  - if two heuristic edges duplicate, keep higher confidence and retain the
    strongest source.
- Add negative-scope tests for every language with same symbol names in
  unrelated modules.

Acceptance:

- Receiver-qualified call resolution is the default path.
- Bare name matching is bounded and never the first choice when better evidence
  exists.

## Test Evidence Fixes

Current status:

- Partially implemented. `internal/core/testdetect.go` already recognizes many
  common test file conventions and annotations across Go, Python, JS/TS, Java,
  Rust, C#, and PHP. `buildTests` already prefers narrowed call evidence, walks
  same-test-file helpers with bounded depth, and scopes name-derived test edges
  through imports. The remaining work is framework-specific coverage, strict
  negative fixtures, and more tests-edge CI baselines.

Targets:

- `internal/core/testdetect.go`
- `internal/graph/edges.go`: `buildTests`
- `internal/graph/edges_test.go`

Implementation:

- Extend language test detection where current path/annotation checks are not
  enough:
  - Go: `TestX`, benchmarks/examples as separate evidence type if needed
  - Python: pytest/unittest naming, async tests, class test methods
  - JS/TS/TSX: `*.test.*`, `*.spec.*`, `describe/it/test`, React tests
  - Java: JUnit/TestNG annotations and `*Test.java`
  - Rust: `#[test]`, async test attrs, integration tests
  - C/C++: known test framework macros where AST exposes them, plus file naming
  - C#: xUnit/NUnit/MSTest attrs
  - PHP: PHPUnit/Pest naming and attributes
- Build tests edges from actual call evidence first.
- Name-derived test edges must be scoped to imports/project/package and should
  be lower confidence.
- Same-test-file helper traversal should remain bounded and should not cross
  production helpers unless call evidence exists.

Acceptance:

- Tests do not falsely cover same-named symbols outside scope.
- Languages with common test frameworks get real coverage evidence.

## Removal Or Downgrade List

Remove or downgrade:

- Raw Java wildcard/static import strings as matching keys.
- Repo-wide basename import matching when language-aware resolution exists.
- Hard-coded "AST language with zero call sites means no calls" when extraction
  may have missed node types.
- High-confidence regex/native edges that are not actually compiler-resolved.
- Java/Rust/C#/PHP/C-family `native` call text matching; it has been retired
  and should not be reintroduced without compiler-backed resolution.
- C++ uppercase-call constructor inference that treats ordinary functions as
  type construction.
- PHP namespace declarations treated as imports.
- Tests edges based only on unscoped name matching.

Keep:

- AST-first extraction.
- Native analyzers as optional enrichment.
- Conservative confidence policy.
- Same-package Java and Go scope, but only for those languages.
- No code execution during indexing.

## Implementation Order

1. Preserve structured import/namespace metadata through Grove projection:
   Java wildcard/static, Rust grouped/glob uses, PHP grouped/function/const
   uses, C# alias/static/global usings, Python aliases/relative imports, and
   JS/TS/TSX path aliases/exports.
2. Replace repo-wide scopes with project-aware scopes: C/C++ include graph,
   PHP Composer PSR-4/classmap/files, C# project/reference namespace scope, and
   Java package/import scope.
3. Add strict false-positive fixtures and fanout budgets before adding broad
   recall. Include negative same-name cases for every supported language.
4. Consolidate receiver/type/call-result/import resolution into one ordered,
   explainable pipeline with language-specific providers behind a common
   interface.
5. Wire confidence/source/reason policy into `TestsFor`, impact, and
   certification so strict consumers skip weak edges by default.
6. Java correctness fixes: missing call-site forms, multi-field declarations,
   local type inference, overload narrowing, and unknown-receiver policy.
7. TS/TSX, Python, Rust, C#/PHP, and C/C++ recall hardening on top of scoped
   resolution, keeping native text matching limited to include/type-use/project
   evidence.
8. Broader tests-edge gates, impact gates, and per-language scorecard
   regression gates on top of existing eval baselines.

## Minimum Quality Gates

Current status:

- Call-edge CI baselines currently cover gin, flask, socket.io, express,
  commons-lang, ripgrep, Newtonsoft.Json, PHP-Parser, and jansson.
- Tests-edge CI currently gates Flask only. Impact scoring exists, but impact
  baselines are not yet gated.

Each language should have:

- exact symbol fixture
- exact import fixture
- exact structured import/namespace fixture
- exact call fixture
- exact constructor fixture when applicable
- exact type-use fixture
- exact inheritance fixture when applicable
- exact tests fixture
- at least one negative same-name fixture
- external dependency qualifier fixture where applicable
- ambiguous receiver/call-result fixture where applicable
- overload/generic fixture where applicable
- precision/recall/false-positive score in CI
- fanout budget and top false-positive source report in CI

No language should be marked production-confident until its strict fixture
false-positive count is zero and its measured recall gaps are documented with
explicit unsupported syntax.
