# Grove — Final Review: Is the Graph Bulletproof?

Date: 2026-09-11 · Tree: grove `96370154` (includes `89966437`), astkit `3f021ef` (tag `v0.10.0`), both pushed, working trees clean.
Supersedes the narrative in `codereview.md` (rounds 1–4); that file is kept for provenance.

## Post-review remediation (uncommitted, 2026-09-11)

The working tree now fixes forward from this review; it does not revert
`89966437` or astkit `3f021ef`. This section records validation of the current
uncommitted Grove and astkit trees. The original review below is retained as
the evidence that motivated the changes.

### Current verdict

The two release blockers identified below are fixed in the working tree:

- all nine accuracy-oracle corpora pass their committed per-axis gates;
- call, type, hierarchy, interface, impact, missing-implementation, and rename
  resolution are language-family scoped, with ambiguous cross-language API
  queries rejected unless the query is file-qualified.

The fix-forward pass also addresses H1-H5, M1-M5, all 13 round-4
COBOL/JCL findings, and the durability issues in punch-list item 5. The three
gin residual categories in punch-list item 10 remain documented limitations;
they are not release-gate regressions introduced by this fix pass.

### Exact current oracle result

| Corpus | Precision | Recall | F1 | Gate |
|---|---:|---:|---:|---|
| gin | 0.9422 | 0.9505 | 0.9463 | pass |
| flask | 0.8522 | 0.7164 | 0.7784 | pass |
| socket.io | 0.8989 | 0.9972 | 0.9455 | pass |
| express | 0.8400 | 1.0000 | 0.9130 | pass |
| commons-lang | 0.8607 | 0.9182 | 0.8885 | pass |
| ripgrep | 0.8999 | 0.8967 | 0.8983 | pass |
| newtonsoft | 0.8522 | 0.9477 | 0.8974 | pass |
| php-parser | 0.8286 | 0.6010 | 0.6967 | pass |
| jansson | 0.8782 | 0.8662 | 0.8722 | pass |

These scores were produced by a binary built after the final resolver-version
bump, so every corpus was reindexed with the exact current implementation.
The final pass also fixes native TypeScript analysis for solution-style
`tsconfig.json` project references and nested monorepo configs. Each project is
built with its own compiler options, reference traversal is cycle-safe, and
diagnostics distinguish loaded configs, solution configs, and indexed files.
On Socket.IO this activates 993 native call candidates and 1,059 native
type-use candidates while improving the passing oracle result shown above.
Regression tests cover Vite-style references, nested configs, and local
closure names that collide with class methods.

Uncached `go test -count=1 ./...` and `go vet ./...` pass in Grove; the same
checks pass in astkit with workspace mode disabled. `git diff --check` passes
in both repositories. Prism's final closure check is complete for astkit. For
Grove it requests manual review only for `identTokenRe`, `ResolverVersion`, and
`ExtractorVersion`; every reported reference was inspected and no missed
callable site remains.

No commit, push, tag, or release has been made from this remediation tree.

## Verdict

**No — not yet. Two things stand between HEAD and "solid":**

1. **The last fix commit pair regressed the accuracy oracle on 5 of 9 corpora, and the repo's own CI gate has been red on every push of this cycle.** I ran the full `grove-eval` suite locally, A/B'd it against a binary built from the pre-fix commits with identical truth and environment (the old binary reproduces the committed baselines to four decimal places, so environment is not a factor), and bisected. Every regression lands in `89966437` + `3f021ef`. GitHub Actions agrees: the Edge-accuracy workflow at HEAD fails newtonsoft, flask, socket.io/express, jansson and ripgrep jobs. It also failed on the *pre-fix* commits (flask was already 0.008 under its floor), which is how five new failures shipped unnoticed behind an old one.

2. **Call resolution is language-blind.** In any repository containing more than one language, a bare or imported call resolves to same-named functions in *other* languages at certification-grade confidence (0.95 `ast-narrowed`), and `change-impact` / `rename-plan` merge declarations across languages while reporting `Completeness: "closed"`. Most real repositories are polyglot.

Everything else is in far better shape than at round 1: 32 of 33 earlier fixes hold, the graph engine survived 24 hostile-input fixtures with zero crashes or hangs, incremental re-index is byte-identical to `--force` across 9 languages and 28 MCP-driven edit scenarios, and gin (the 0.99-universe Go corpus the toolchain is built around) *improved* over baseline. The open items are specific, reproduced, and mostly small.

---

## 1. The accuracy oracle — every corpus, three binaries

`grove-eval score` against the committed truth in `eval/testdata/` (gin truth regenerated from the Go SSA/VTA oracle exactly as CI does). OLD = grove `592fe8be` + astkit `3163ed8`, the commit before any of this cycle's fixes. `tol` is the per-axis tolerance in `eval/baseline.json`.

| Corpus | Baseline P / R | OLD P / R | **HEAD P / R** | Gate at HEAD | CI job at HEAD |
|---|---|---|---|---|---|
| gin (Go) | 0.9339 / 0.9488 | 0.9406 / 0.9505 | **0.9422 / 0.9505** | pass (+0.008 P) | success |
| flask (Python) | 0.8293 / 0.7039 | 0.8358 / 0.6957 | **0.8638 / 0.6170** | **FAIL R −0.087** | failure |
| socket.io (TS) | 0.8980 / 0.9751 | 0.8969 / 0.9765 | **0.8917 / 0.9917** | **FAIL P −0.006** | failure |
| express (JS) | 0.7500 / 0.7143 | 0.7500 / 0.7143 | **0.8400 / 1.0000** | pass (+0.09 P, +0.29 R) | (same job as socket.io) |
| commons-lang (Java) | 0.8563 / 0.8958 | 0.8478 / 0.9039 | **0.8607 / 0.9182** | pass | success |
| ripgrep (Rust) | 0.8987 / 0.8957 | 0.8987 / 0.8957 | **0.9028 / 0.8715** | **FAIL R −0.024** | failure |
| newtonsoft (C#) | 0.8491 / 0.8158 | 0.8491 / 0.8158 | **0.7896 / 0.9552** | **FAIL P −0.060** (R +0.139) | failure |
| php-parser (PHP) | 0.8254 / 0.5709 | 0.8254 / 0.5709 | **0.8280 / 0.5974** | pass | success |
| jansson (C) | 0.8758 / 0.8674 | 0.8758 / 0.8674 | **0.8851 / 0.8525** | **FAIL R −0.015** | failure |

Environment parity: `cargo` and `dotnet` are absent here — and CI installs neither (the eval workflow sets up Go only), so the baselines were produced under the same conditions. The OLD column matching the baseline exactly on four corpora is the proof. Linkage: workspace mode resolved astkit to the local checkout, tagged `v0.10.0`, so the oracle scored the extractor the binary ships.

### 1.1 Bisect and half-split

| Build | flask R | ripgrep R | newtonsoft P | jansson R | socket.io P |
|---|---|---|---|---|---|
| OLD `592fe8be` + astkit `3163ed8` | 0.6957 | 0.8957 | 0.8491 | 0.8674 | 0.8969 |
| `94168d31` + astkit `3163ed8` | 0.6936 | 0.8957 | **0.8543** ↑ | 0.8674 | 0.8969 |
| `94168d31` + astkit **`3f021ef`** | **0.6335** ↓ | **0.8681** ↓ | 0.8500 | 0.8693 | 0.8958 |
| `89966437` + astkit `3f021ef` (= HEAD) | **0.6170** ↓ | **0.8715** | **0.7896** ↓ | **0.8525** ↓ | **0.8917** ↓ |

- `39582618` and `94168d31` are neutral-to-positive on every corpus.
- **astkit `3f021ef`** carries the flask and ripgrep recall losses (they appear with old grove + new astkit).
- **grove `89966437`** carries the newtonsoft precision collapse and the jansson / socket.io losses.
- The reverse cross-build (new grove + old astkit) does not compile — grove `89966437` depends on the new `CallSite.Write`/`ReferenceOnly` fields — which is why the pair must be bisected as a pair.

### 1.2 Mechanism of each regression (all reproduced on minimal fixtures, old vs new binary)

**flask, R −0.087 — nested functions became orphan symbols.** astkit `3f021ef` now materialises nested `def`s as their own symbols and attributes the calls inside them to the nested symbol. Grove's documented contract (`eval/README.md`: "closures attributed to the enclosing declaration — right for blast radius") was broken **without a `contains` edge to compensate**, so the nested symbol is unreachable from its parent.
```
OLD: login_required -> redirect, url_for
NEW: login_required.wrapped_view -> redirect, url_for      (no contains edge login_required -> wrapped_view)
```
On real flask at HEAD: **535 nested functions, all orphans; 291 call edges originate from them invisibly.** `impact url_for` reaches `login_required` on OLD and **does not** at HEAD. This is a blast-radius regression, not merely an oracle mismatch.

**ripgrep, R −0.024 — calls through an umbrella-crate re-export resolve to nothing.** Keyed the way the oracle matches (file + base name), 166 true edges were lost, 105 of them in `crates/core/flags/hiargs.rs`, which reaches builders via `grep::printer::StandardBuilder::new()` where `grep` is an umbrella crate doing `pub use grep_printer as printer;`. Minimal three-crate repro, identical call sites in both binaries:
```
OLD: via_use -> StandardBuilder.new, .build   via_full_path -> StandardBuilder.new, .build
NEW: (no edges at all)
```
Both `use grep::printer::StandardBuilder; StandardBuilder::new()` and the fully-qualified form drop to zero — a re-exported module path is pinned to a *file* module that does not exist, and nothing falls back. (Part of the churn was healthy: bogus edges to type-named symbols like `defs.rs::str` disappeared, which is why precision rose.)

**newtonsoft, P −0.060 / R +0.139 — dispatch expansion ignores arity.** One call `w.WriteValue(1)` on an abstract receiver with 5 subclasses × 4 overloads:
```
OLD: 0 edges                                   ← a recall bug: abstract-receiver dispatch was missed entirely
NEW: 21 edges = 1 ast-narrowed + 20 dispatch   ← every overload of every implementor; ideal is 6
```
On the corpus: **+8,508 `dispatch` edges** (13,922 added vs 521 removed, 19k → 32k edges); `JsonWriter.WriteValueAsync#9` alone gains 119 dispatch edges; `Read` gains 708. A genuine recall win paired with a precision cost that `filterByArgc`/arg-type narrowing inside the dispatch expansion would remove.

**jansson, R −0.015 (~14 oracle edges) — header prototypes are now second symbols.** A `int json_null(void);` declaration in an included header is now indexed as a `function` in the header, so a cross-file call emits an edge to the prototype as well as to the definition (minimal repro: OLD 1 edge, NEW 2). On the real corpus the exact lost-edge set is small and I could not tie every one to this mechanism; it is the leading candidate, attributed to the grove half by bisect. Flagged as probable, not proven.

**socket.io, P −0.006** — 17 more edges at HEAD (803 vs 786), grove half; within noise of the fan-out change above. Not separately characterised.

---

## 2. New findings from the final adversarial passes

Each item was reproduced by me on the committed HEAD binary unless marked *(agent-verified)*.

### CRITICAL

**C1. Call resolution is language-blind.** `edges.go` `resolveCallees` walks `idx.byName[lower(name)]` with no language check, and `importedFiles` computes scope from directory and basename with explicit `.go/.py/.rs/...` suffix alternation, so a dotted Python import matches a Go file of the same basename.
```
app/svc.py:  from core.models import helper; helper()   →  core/models.go::helper@0.95  AND  core/models.py::helper@0.95
svc.go (no local helper) + tool.py in the same dir     →  Run -> tool.py::helper@0.95
```
Same-file-wins masks it when a same-language candidate is in the preferred tier — which is why a naive repro passes — but in the polyglot fixture *(agent-verified)* `py/svc.py::use` received six cross-language `helper` edges, `cs/core/models.cs::Util.Run → go/core/models.go::User.Close` at 0.95, and 34 of 61 non-structural edges crossed a language boundary. `extends`/`implements`/`overrides` cross languages too: a Python class "implements" a Go interface via case-folded method-set matching.

**C2. Graph APIs merge same-named declarations across languages and report `closed`** *(agent-verified, consistent with C1)*. `canonicalQueryFor` collapses by the `Type.method` string only:
```
change-impact User.Close   → Declarations [cpp, csharp, go], Callers across 3 languages, Completeness "closed"
rename-plan  User.close shutdown → Edits in java/js/php/py/rs/ts — 7 files, Ambiguous null
missing-implementations User.close → contract = 6 declarations in 6 languages
```
No response field indicates the language mix. An agent applying `Edits` renames a Rust method because a Java one was asked for.

### HIGH

**H1. A store with symbols but zero edges is "recovered" with every native analyzer skipped, then frozen.** `indexer.go` falls through on `EdgeCount == 0` but leaves `scope = changedLanguages` (empty), so `AnalyzeChangedFiles` skips every language and `carriedNativeEdges` carries forward from nothing; the next run takes the no-op fast path. Reproduced on gin by emptying the edge table (equivalent to a SIGKILL between symbol persist and edge write, or a concurrent `--force`):
```
fresh --force:  11,662 edges   native=7,756  astkit=3,680  heuristic=226
after recovery:  7,915 edges   native=0      astkit=6,659  heuristic=1,256    'go: skipped: no changed files'
third run:      'skipped: no file changes since last index'                   ← permanently degraded
```
`impact ResponseWriter` 42 → 18 nodes *(agent)*. There is also no cross-process lock, so two concurrent `grove index` runs race for last-writer-wins on this same path.

**H2. MCP `grove_index {"dir": X}` indexes a foreign tree into the server's own store and prunes the workspace** *(agent-verified)*. `dir` is advertised in the schema; after the call `grove_symbols` returns nothing for the original workspace.

**H3. Cross-file inherited-method calls produce no edge in the heuristic path — at any depth.** Not the "depth-5 cliff" the polyglot pass reported: same-file chains resolve at depth 8. What fails is the ancestor that declares the method living in a file the caller does not import:
```
x/use1.ts: import { L1 } from './l1'; new L1().rootMethod()    (rootMethod declared on L0 in x/l0.ts)
--no-native:  xuse0=1  xuse1=0  xuse2=0 … xuse8=0      ← fails from depth 1
native tsc:   all resolve
```
All 8 `extends` edges exist; the candidate is filtered out by import scope. Single-level inheritance across files is the norm, so every TS/JS project without a `tsconfig.json` (and, per the agent's data, the same shape in Python/Go/Java/C#) loses these calls.

**H4. Syntax-error recovery is lossy or phantom-producing in Go, C#, C++** *(agent-verified)*. One unclosed brace: Go drops every declaration after it; C# drops every call site in the file and every method's parent (`change-impact Broken.Good` → "declares no method"); C++ materialises `if` and `while` as functions, and `change-impact if` answers with the phantom. Python, JS, PHP, Rust, TS degrade gracefully.

**H5. The `dead-code` module-level false positive was fixed on an uncommitted tree and never reached the commit.** `gl/dc-ts`: `entry` is called by `entry();` at module scope, yet reports dead on **both** the OLD and HEAD binaries. Round 3 validated the fix against Codex's working tree at the time; no test guards it (`grep` for module-level/top-level in deadcode tests: nothing).

### MEDIUM

- **M1.** Self-recursive calls emit no edge in Go/Java/TS/Python (PHP and Rust keep them); in a polyglot repo the self-skip promotes a foreign-language target *(agent)*.
- **M2.** No form of `change-impact` query reaches a namespaced C++ method — the disambiguation hint it prints (`core::User.Close`) is itself rejected *(agent)*.
- **M3.** `impact` and `symbols` case-fold across languages: a JCL step calling COBOL program `USER` joins `impact User` for a Go type *(agent)*.
- **M4.** `certify --require-tests`: a covered method edit still fails on its enclosing class, because span overlap marks the class and the coverage walk is inbound-only *(agent; Python verified, Java/TS/C# inferred from span shapes)*.
- **M5.** Round-2 PHP namespace fan-out (`Svc.go → src/Other/Widget.php::Repo.find` at 0.95 alongside the imported one) was never fixed.

### Gin residuals (the 0.99-universe corpus) — three patterns, each reproduced with native on

- **FP**: `c.Request.Header.Get(key)` → `Context.Get` at 0.95 (11 of 20 FPs are this shape — a same-file same-name method chosen although the real receiver is an external stdlib type; go/types knows this and the native pass does not override it).
- **FN**: receiver is a call expression — `engine().GET(path, h)` yields only `GET → engine`, never `→ Engine.GET` (9 of 20 FNs; the `ginS` wrapper package).
- **FN**: `w.Write(b)` on an *external* interface parameter (`http.ResponseWriter`) implemented in-repo → no edge (8 of 20 FNs; every `Render` → `responseWriter.Write`). Interface satisfaction covers in-repo interfaces only.

---

## 3. Status of every earlier finding at HEAD

**Rounds 2–3 fixes: 32 of 33 hold.** Re-run on the original fixtures: C++ one-line namespace crash; Rust `implements` (8 edges, `Fish` reported Missing, built from `impl_trait` annotations not RawText); PHP graph-layer inheritance without composer; `rename-plan` interpolated strings in TS/C#/Python; fan-out cliff (holds at N=2000); `Impact` reaching implementations; `uses-type` spray and self-loops; Go generic receivers and bare-call certification; C++ `.h` as C++, guarded headers, `override` declarations, struct members, stack locals, field receivers, visibility flags; Java `@Autowired`, chained calls, enums, records, import shadowing, cross-package rename refusing to guess; Python import scope and both regressions; C# fields, top-level statements, property accessors, `new T().M()`; JS CommonJS, barrels, JSX; `dead-code` under a namespace. The one that did not hold is H5 above.

**Newly fixed since round 3 (not previously claimed):** Java explicit import shadows a same-package class; Rust `<Dog as Greet>::name` pins `Dog.name`; Rust struct-literal bindings resolve; `#[cfg(test)]` functions no longer reported dead; MCP now exposes `grove_change_impact`, `grove_missing_implementations`, `grove_rename_plan`, `grove_dead_code`; store splice invariant is a sha256 fingerprint, no longer `COUNT(*)`; CLI `impact --depth`; certify no longer cites phantom callers.

**Round 4 — mainframe (COBOL/JCL): 13 of 13 still open.** `89966437` touched `graph.go`, `renameplan.go` and `deadcode.go` but not for mainframe kinds: the `impact` allowlist still omits `reads`/`writes`/`redefines`/`binds-dataset`; `buildContains` still gates on struct/class/interface/trait/type (0 `contains` edges across the estate); `rename-plan` still emits 0 edits; `dead-code` considers 0 symbols; every program span is still `2..2` (the stale `&syms[len(syms)-1]` pointer at `cobol.go:194`); `PERFORM A THRU C` still skips the middle; `WRITE … FROM` still classifies its target as a read. The estate produces the same 44 edges as at review time.

---

## 4. What is verified solid — do not re-test

- **Robustness.** Zero crashes, hangs, non-zero exits or empty DBs across 24 hostile fixtures: empty / comment-only / 1-byte / no-trailing-newline files in 10 languages; UTF-8 identifiers, BOM, CRLF, NUL byte mid-file, invalid UTF-8; a 3 MB file with 20,000 functions (exactly 20,000 symbols and edges; Go 6.7 s); 5,000-line bodies; 200 nested closures; 500-file directories; symlink loops; nested `.grove/`; `vendor/`, `node_modules/`, `target/` skipped; adversarial names containing `::`, `@`, `#`, and paths with spaces and `::`.
- **Termination.** Mutual recursion, circular imports, self-extending interfaces/classes, P↔Q mutual extends in 7 languages — all terminate, no self-edges fabricated.
- **Fan-out.** 2,000 same-named methods with a declared receiver → exactly one correct edge in Java, C#, TS, Go, Python.
- **Incremental identity.** 9 languages × edit scenarios (rename, add call, delete symbol, move between files, new file, delete file, rename file, copybook edit) through the real MCP delta path — 28 scenarios on Go/gin/COBOL plus 4–9 per other language — all byte-identical to `--force`.
- **Version stamps.** A store written by an older binary is fully re-extracted by HEAD without `--force` and matches a fresh build.
- **MCP wire shape.** `grove_deps`/`grove_impact`/`grove_symbols` byte-identical to CLI including mainframe kinds and edge types; caps carry an accurate `note`; protocol negotiation and error codes correct.
- **certify.** Change-with-test → allow with the test cited; untested → `manual_review`; renames, deletions, CRLF, unindexed files, staleness (`index_stale` fires from CLI and MCP) all correct; transitive coverage works.
- **Mainframe extraction basics.** Fixed-format columns, comment/debug indicators, `COPY … REPLACING`, dynamic `CALL` via VALUE-clause propagation, concatenated DDs, step-scoped dataset binding — all correct (the mainframe defects are downstream).

---

## 5. Punch list, in order

| # | Item | Size | Evidence |
|---|---|---|---|
| 0 | **Restore the accuracy gate.** Fix or revert the two regression sources, get the Edge-accuracy workflow green, and treat it as blocking — it has been red on every push of this cycle. | — | §1 |
| 1 | Nested-def `contains` edge (flask): when astkit materialises a nested function, emit `contains parent → nested` and let `Impact`/oracle attribution follow it. Fixes both the recall loss and the blast-radius hole. | small | §1.2 |
| 2 | Rust re-export path pinning: a module segment that resolves to a `pub use … as …` alias must map to the target crate/module, with fallback to bare-name resolution instead of zero edges. | small | §1.2 |
| 3 | Dispatch expansion must apply arity/arg-type narrowing per implementor (newtonsoft precision, no recall cost). | small | §1.2 |
| 4 | **Language-scope every resolver**: filter `byName` candidates and import-scope files by the caller's language family; group `change-impact`/`rename-plan` results by language or refuse as ambiguous. | medium | C1, C2 |
| 5 | Zero-edge recovery: set `scope = nil` when `EdgeCount == 0`; add a cross-process lock; reject or re-root MCP `dir != s.root`. | small | H1, H2 |
| 6 | Cross-file inherited-method resolution in the heuristic path: when walking `extends` for a receiver type, admit the ancestor's file into scope. | small | H3 |
| 7 | Mainframe round 4 — start with the one-line pointer bug, then the `impact` allowlist and `buildContains` kinds. | small–medium | §3 |
| 8 | Syntax-error recovery: drop control-keyword phantoms (`if`/`while`) from the C/C++ regex fallback; keep C# parents and call sites on partial parses. | medium | H4 |
| 9 | Re-land the module-level `dead-code` root fix with a test. | small | H5 |
| 10 | Gin residuals: prefer the native receiver type when go/types resolves it to an external package; handle call-expression receivers; dispatch through external interfaces to in-repo implementors. | medium | §2 |

## 6. Process findings

- **A red gate is not a gate.** The eval workflow failed on `592fe8be` (pre-fix) for a 0.008 flask shortfall and on every commit since; five new corpus failures shipped behind it. Either the floor needed lowering with a recorded reason, or the failure needed fixing — not ignoring.
- **A/B against the parent binary catches what `go test` cannot.** Every regression in this cycle (three in round 2, one in round 3, five here) was invisible to the unit suite and found only by diffing edge dumps or oracle scores between binaries. It should be part of the fix workflow.
- **Validate the commit, not the working tree.** The module-level `dead-code` fix existed when round 3 validated it and was gone by the time it was committed.
- **Tests built from hand-written `SymbolRecord` literals can pass on shapes the extractor never produces** (the Rust `implements` bug survived this way). Prefer fixtures through the real extractor.
- **Per-axis gates are right.** newtonsoft's F1 went *up* while precision fell 6 points; an F1 gate would have hidden a real precision problem.

## 7. Limits of this review

- `cargo` and `dotnet` are not installed here: native Rust is untested end to end (the C# native pass is text-based and *was* exercised). Java native reports 0 native call edges by design; not investigated.
- Only gin's truth was regenerated; the other eight corpora used the committed truth snapshots (as CI does).
- No accuracy oracle exists for COBOL/JCL; the mainframe subsystem is validated by fixtures only.
- `certify` was exercised on Go and Python; the class-span finding is inferred for Java/TS/C#.
- The incremental delta path was not exercised on a polyglot tree.
- The jansson regression mechanism is probable, not proven on the corpus (§1.2).
- Prism MCP was unavailable for the entire engagement; all code reading was direct.

Fixtures, scorecards, edge dumps and A/B builds for every claim above are under the session scratchpad (`eval-out*/`, `diff-*`, `t-*`, `k-rg.*`, `v-*`, `hostile/`, `ab-*/`).
