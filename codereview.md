# Grove — Language-by-Language Graph Defect Review

Round 1: 2026-09-10 (code-read only, per-language backends)
Round 2: 2026-09-11 (**fixture-verified**, full pipeline astkit → graph APIs),
against HEAD `94168d31` — i.e. *after* the round-1 fixes landed.

---

# Round 4 — COBOL / JCL mainframe subsystem (2026-09-11)

First review of this subsystem; rounds 1-3 never touched it. **13 defects**
(3 CRITICAL, 4 HIGH, 3 MEDIUM, 3 LOW), by owning layer: **7 graph-layer**
(`graph.go`, `edges.go`, `mainframe.go`, `renameplan.go`, `deadcode.go`),
**5 astkit** (`cobol.go`, `jcl.go`), **1 parser** (`languages.go`/`sniff.go`).
Fixtures: the committed `mainframe-estate` plus `fix-cob2`…`fix-cob5`.

**Headline: all three CRITICALs are graph-API — the APIs were never wired to the
four mainframe edge types or the nine mainframe symbol kinds.** Extraction is
comparatively healthy: astkit gets the genuinely hard parts right (fixed-format
column model, comment/debug indicators, continuation, `COPY … REPLACING`,
VALUE-clause constant propagation for dynamic `CALL`). But "extraction is sound"
would overstate it — five real extraction defects remain, including the stale
slice pointer that truncates every program span, and one parser defect (`.copy`).

**Two premises in my round-4 brief were wrong, and the review corrected them:**
- COBOL/JCL tests **do** exist — 811 lines across `graph/mainframe_estate_test.go`,
  `astkit/strategies/mainframe_test.go`, `mainframe_bench_test.go`,
  `parser/sniff_test.go`, all passing. They are extraction/edge-construction
  goldens only; **no test exercises a graph API over a mainframe estate**, which
  is exactly where every defect below lives.
- The two `binds-dataset` edges I flagged as a possible GDG duplicate are
  **correct**: `NIGHTLY.jcl` declares a *concatenated DD* (`//CUSTIN DD DSN=…`
  followed by an unnamed `// DD DSN=…G0001V00`), so one DD name legitimately
  binds two physical datasets and `CUST-FILE` reads both.

### [CRITICAL] `impact` drops all four mainframe edge types — mainframe blast radius is always empty
`internal/graph/graph.go:605-611` — the traversal allowlist omits `EdgeReads`,
`EdgeWrites`, `EdgeRedefines`, `EdgeBinds`. Even if added they would still be
dropped: reads/writes are emitted at `0.7 / ReasonRegexFallbck`
(`mainframe.go:262`) while `PolicyImpact` sets `MinConfidence: 0.6` **and**
`ExcludeReason{ReasonRegexFallbck: true}` (`graph.go:666`). Two independent
gates, both closed. Verified independently:
```
impact CUST-ID           → NULL      impact CUST-FILE         → NULL
impact CUST-REC          → NULL      impact MAIN-PARA         → NULL
impact CUST-SSN          → NULL      impact PROD.CUST.MASTER  → NULL
impact AUDITLOG          → ['CUSTUPD.MAIN-PARA']   ← only `calls` works
```
The driving edges exist in the DB (`writes MAIN-PARA → CUST-SSN 0.7`,
`redefines CUST-ALT → CUST-SSN 1.0`, `binds-dataset CUST-FILE → PROD.CUST.MASTER
0.8`). The flagship blast-radius API is a silent no-op on the exact question a
mainframe estate is indexed to answer. Confidence: high.

### [CRITICAL] Zero `contains` edges for the entire mainframe estate
`internal/graph/edges.go:1463-1467` gates `buildContains` on parent kinds
struct/class/interface/trait/type. Mainframe parents are `program`, `data-item`,
`job`, `step`, `jcl-procedure` — none listed. Verified: **0 contains edges vs 18
symbols carrying a correct `ParentSymbol`.** So `CUST-REC contains CUST-KEY
contains CUST-ID`, `CUSTUPD contains MAIN-PARA` and `NIGHTLY contains UPDATE`
all vanish. This is the second root cause of the `impact` emptiness above
(`EdgeContains` *is* in the allowlist — there simply are no such edges), and it
also disables `deadcode.go:160 enclosingMentions` and any record-layout
navigation. Confidence: high.

### [CRITICAL] `rename-plan` cannot emit a single edit for any mainframe symbol
`internal/graph/renameplan.go:113-114` — the site pattern requires a following
`(`, and the reference pattern requires a `.`/`::` qualifier. COBOL has neither
(`PERFORM INIT-PARA.`, `MOVE ZEROS TO CUST-SSN.`, qualification is `FLD-X OF
GRP-A`). Verified on both a paragraph and a data item:
```
rename-plan INIT-PARA NEW-PARA
  Edits: None   SitesTotal: 2
  Unresolved: ['CUSTUPD.cbl:INIT-PARA','CUSTUPD.cbl:MAIN-PARA']
  Completeness: "callers-only"
```
The sites are found correctly — they just cannot be rendered as edits, and a
populated `SitesTotal` with `Completeness: "callers-only"` reads like a
successful plan. Confidence: high.

### [HIGH] astkit — stale slice pointer: every program span is one line, and PROCEDURE statements outside a paragraph lose all edges
`astkit/strategies/cobol.go:194-196, 301-304, 339-341` — `progSym :=
&syms[len(syms)-1]` takes a pointer into the backing array; every later
`append` reallocates and all writes through it are discarded. Verified: every
`program` symbol has span `2..2` regardless of file length (a 24-line `CUSTUPD.cbl`
→ `2..2`). Worse, a mainline `PERFORM PARA-A THRU PARA-C.` sitting in the
PROCEDURE DIVISION *before* the first paragraph (legal and common) is attributed
to the stale copy, so the program records **zero** call sites and every paragraph
is orphaned. One-line fix: keep an index, not a pointer. Confidence: high.

### [HIGH] astkit — `PERFORM A THRU C` reaches only the endpoints
`cobol.go:306-313` records the two endpoint names as independent call sites, but
COBOL executes A, everything between A and C in source order, and C. `T-TWO`
(between `T-ONE` and `T-THREE`) gets **no inbound edge** — and range members are
precisely the paragraphs with no other caller, so they are exactly the ones that
then look unreachable. Confidence: high.

### [HIGH] edges — the write-verb table misclassifies `WRITE … FROM`, `SET`, and `ADD … GIVING`
`internal/graph/mainframe.go:129` (`reWriteTarget`). Independently reproduced —
3 of 5 data items carry a direction that contradicts the source:
```
WRITE WS-REC FROM WS-SRC        → reads WS-REC            ✗ (WRITE's target is written)
ADD WS-A TO WS-B GIVING WS-C    → writes WS-B             ✗ (GIVING: WS-B is read)
SET WS-IDX TO WS-A              → writes WS-A             ✗ (capture runs past TO)
```
`WRITE` is absent from the alternation entirely — the single most important write
verb in a batch COBOL program is classified as a read. `REWRITE`, `ACCEPT` and
`INSPECT … TALLYING` are likewise missing. Confidence: high.

### [HIGH] edges — a field named on both sides of a statement loses its `reads` edge
`internal/graph/mainframe.go:276-289` builds `writeTargets` as a per-statement
*set of names* and classifies tokens by membership, not position. For the
canonical record-mapping idiom `MOVE FLD-X OF GRP-A TO FLD-X OF GRP-B.`, the
source operand `GRP-A.FLD-X` is recorded as **written and never read** —
direction inverted, read edge lost. Secondary: the `OF GRP-A` qualifier is
present in the statement and discarded, so both `FLD-X` declarations match.
Confidence: high.

### [MEDIUM] `dead-code` considers zero mainframe symbols
`internal/graph/deadcode.go:181-183` restricts candidates to
`KindFunction`/`KindMethod`; no mainframe kind qualifies. On the estate:
`considered: 0`, `dead: []` — a clean bill of health because nothing was
examined. A paragraph with no inbound edge and no textual reference is never
reported. Related and worth fixing together: `identTokenRe` excludes `-`, so
COBOL names tokenize as fragments (`PROCESS-RECORD` → `PROCESS`, `RECORD`);
relaxing the kind filter without fixing the tokenizer would silently break the
name-reference safety net. Note `Exported` **is** set correctly here (programs/
jobs true, paragraphs/data-items false) — none of the C++ "everything exported"
pathology. Confidence: high.

### [MEDIUM] astkit — a PIC clause continued on the next line makes an elementary item look like a group
`cobol.go:270-273` decides group-vs-elementary by whether `rePicture` matches the
remainder of the *same* line. `05 WS-NAME` / `PIC X(20).` on the next line pushes
`WS-NAME` onto the hierarchy stack as a group, so everything after nests one
level too deep. Observed a level-66 `RENAMES` parented to an elementary field
(it is always a sibling of the 01 record); `case 66:` at `cobol.go:226` is a
no-op that never pops the stack. Confidence: high.

### [MEDIUM] astkit — JCL symbolic parameters produce a call site to the literal string `PGM`/`PROC`
`jcl.go:35-36,139-141` — `rePGM` excludes `&`, so `EXEC PGM=&PROG` falls through
to the bare-operand fallback, which matches the keyword itself:
```
PROBEJOB.S1 | [{"callee":"PGM"}]      PROBEJOB.S2 | [{"callee":"PROC"}]
```
A latent spurious edge to any symbol named PGM or PROC. The preceding `SET
PROG=FIXPROG` is matched by `reJCLStmt` but has no `switch` case, so the
resolution that should happen does not. `JCLLIB`/`INCLUDE MEMBER=` are likewise
matched then dropped, so included JCL members contribute no edges.
Confidence: high.

### [LOW] parser — `.copy` copybooks are not indexed
`parser/languages.go:84,134` and `astkit/lang.go:83` list
`.cbl .cob .cobol .cpy .ccp .cpb` — `.copy` is absent, and `DetectLanguageFile`
refuses to sniff a file that has any extension. An `OTHER.copy` record yields
zero symbols and `COPY OTHER.` stays an unresolved `import:OTHER`, silently
breaking the include closure and the copying program's field visibility.
Confidence: high.

### [LOW] astkit — `PERFORM WITH TEST AFTER` emits a spurious call site to `WITH`
`cobol.go:138` takes the next identifier; `reReserved` omits
`WITH`/`TEST`/`THRU`/`FROM`/`BY`/`GIVING`. No edge resulted only because no
paragraph happened to be named `WITH`; same-file PERFORM resolution would emit a
confident 1.0 edge if one were. Confidence: high.

### [LOW] edges — same-file field lineage is rolled up to the program, discarding paragraph granularity
`mainframe.go:250-254`, documented as volume discipline. Defensible, but it
changes answers silently: `MOVE 'N' TO WS-EOF` inside `INIT-PARA` is reported as
`writes CUSTUPD → WS-EOF`, so "which paragraph writes WS-EOF" answers "the
program". Worth surfacing in the result rather than only in a source comment.
Confidence: high (behaviour), low (that it is a defect).

### Verified correct (mainframe — do not re-litigate)
**Fixed-format column model**: cols 73-80 ignored (a `CALL 'XX'` planted there
produced nothing); col-7 `*` comment and `D` debug lines both skipped, with
decoy programs present to catch a false positive. Fixed, indented-free and
column-1-free formats all extract. **`COPY … REPLACING`** resolves at 1.0 and
copied fields become visible to the including program; pseudo-text erasure
prevents harvesting member names from REPLACING arguments. **Dynamic `CALL
WS-RPT-PGM`** resolves via VALUE-clause constant propagation at `0.6 dispatch` —
correctly reduced confidence. **Concatenated-DD binding** (see above) and
**step-scoped dataset binding** (two steps with the same DD name to different
DSNs → only the executing step's edge). **JCL structure**: job/step/dataset
hierarchy, in-stream `PROC`/`PEND` scoping, `//*` comments, comma continuations,
`//SYSIN DD *` in-stream data not parsed as JCL. **`change-impact` is the one API
that works** — `mainframeImpactLocked` traverses reads/writes/redefines:
`CUST-SSN` → `[CUST-ALT (redefines), MAIN-PARA (writes)]`, honestly labelled
`callers-only`. **`REDEFINES`** at 1.0, file-scoped. **`Exported`** sane.
**Incremental identity byte-identical** (`hasMainframeSymbols` forces a full
rebuild — blunt but correct). Existing mainframe tests green.

### Coverage gaps (mainframe)
MCP surface unexercised — `internal/mcp/server.go` references none of the four
mainframe edge types, so whether `grove_impact`/`grove_deps` drop or mis-map the
nine kinds is unknown. `missing-implementations` is not meaningfully applicable
(no interface concept) and errors cleanly. Untested COBOL: `GO TO … DEPENDING`,
`EVALUATE`, nested programs, `CALL … ON EXCEPTION`, `EXEC CICS`/`EXEC SQL`,
`LINKAGE SECTION`/`PROCEDURE DIVISION USING`, `SORT`/`MERGE`, `FD` record areas
vs WORKING-STORAGE collisions, multi-line `COPY`. Untested JCL: `DD DISP=` as a
read/write direction hint (a plausible fix source for the directionality bugs),
`OUTPUT`, `IF/THEN/ELSE`, symbolic override on `EXEC PROC,SYM=val`, `&&TEMP`
temporaries, cataloged-proc expansion from a separate `.prc`.

---

# Round 3 — validation of Codex's fixes (2026-09-11)

Validated against Codex's **uncommitted** working tree (grove +3118 lines / 55
files, astkit +1666 / 9 files). Both build; `go test ./...` green in both repos.
Every check below re-ran the original round-2 fixture and read actual DB rows.

**Verdict: the large majority is genuinely fixed — including every Tier-0/Tier-1
item — but "everything" is not accurate. Seven defects remain, one of which is a
coverage regression against the pre-fix binary.**

### Confirmed fixed (re-tested, not taken on trust)

| # | Defect | Evidence now |
|---|---|---|
| 1 | **C++ one-line nested namespace crash** | Indexes cleanly, 4 symbols, correctly qualified `outer::inner::deep`. Template one-liner also fine |
| 2 | **Rust emitted zero `implements` edges** | 8 implements + 1 extends, incl. generic `Convert<String>`. `missing-impl Greet.name` → `Missing: ['Fish']`, family = 3 impls. **Fixed the right way**: the `raw_text like '%impl % for %'` probe still returns 0, so they built from `impl_trait` annotations rather than hacking RawText |
| 3 | **PHP no graph-layer extends/implements** | 10 hierarchy rows with `--no-native`; `missing-impl Runnable.run` → `Missing: ['Lazy']`; `change-impact Base.run` → Supers + Family populated |
| 4 | **`rename-plan` dropped interpolated-string sites** | Line 6 `` `value=${g.label()}` `` now edited, and only the hole is rewritten |
| 5 | **Fan-out zeroing before receiver narrowing** | N=16/17/20/30 all give exactly 1 correct edge to `R1.Save` — cliff gone |
| 6 | **`Impact` never reached implementations** | `overrides` edges now exist (2); `impact Greeter.greet` includes `Loud.greet`, `Soft.greet` |
| 7 | **`uses-type` spray / self-loops** | C# `Consumer.Load` 8 rows → **1** correct row; zero `from_node == to_node` rows in the TS fixture |
| 8 | **Go generic receivers had empty `ParentName`** | `method\|Run\|Job.Run\|Job`; `change-impact Job.Run` resolves |
| 9 | **Go bare call bound to same-named methods at 0.95** | `useFree` → exactly 1 edge (`Close`); certification path no longer admits fabricated callers |
| 10 | **C++ `.h` parsed as C** | `.h` now indexes as `cpp`, with methods, multi-line spans and an `extends` edge |
| 11 | **C++ preproc conditionals skipped** | `guarded_fn` inside `#ifndef` now extracted |
| 12 | **C++ `override` decl missing → `missing-impl` false positive** | `Missing: []`, `ImplCount: 1` |
| 13 | **C++ stack local `Widget w; w.render()`** | Edge present (verified with `class`) |
| 14 | **Java `@Autowired private Repo repo;`** | `Svc.load → Repo.find` present |
| 15 | **Java chained-call REGRESSION (`39582618`)** | `Caller.run → Leaf.work` restored |
| 16 | **Java enum members / record components** | `Colors.RED/GREEN/pretty/weight`; `Point.x/y` as field *and* accessor; `Point.sum` now has outgoing edges |
| 17 | **Java `rename-plan` rewrote an unrelated package** | Now `Edits: []` with both sites in `Ambiguous` — correctly refuses to guess |
| 18 | **Python import scope by basename** | `alpha/svc.py` → only `alpha/models.py::Record.persist` |
| 19 | **Python REGRESSION: `from .. import X`** | `uses-type → pk/__init__.py::Marker 0.96 native` restored, plus a new import edge |
| 20 | **Python REGRESSION: dict-literal as annotation** | Back to honest `0.7 dispatch` fan-out |
| 21 | **C# fields / top-level `Program.cs`** | `field\|Svc._dog`; `Program.cs` yields `<top-level>` + `TopHelper` |
| 22 | **JS CommonJS `require()` import edge** | `file:src/cjs.js → import:./button` |
| 23 | **`dead-code` in class-based languages** | Java `A.unused`, Python `A._unused`, JS/TS 4 methods — all reported, and the earlier `entry` module-level **false positive is gone** |
| 24 | **Incremental identity** | Still byte-identical to `--force` across Go/Python/Java/C++ fixtures |

### Round 3b — validation of the follow-up fix round (2026-09-11)

Re-validated against the updated uncommitted tree (grove +3277/56 files, astkit
+1750/9). Both build; `go test ./...` green in both. All seven round-3 items
re-tested on their original fixtures.

**Five of seven fixed outright; Codex was correct that two of my findings were
stale. Two genuine defects remain, and one of them is not what I originally
diagnosed.**

| Round-3 item | Verdict | Evidence |
|---|---|---|
| C# `dead-code` under a namespace | **FIXED** | `v-dc/cs` → `dead: ['Widget.DeadOne']` |
| C++ `struct` members (incl. multi-line) | **FIXED** | `SFoo.sInline`, `SMulti.sMultiline` with correct parent and spans 4-6; all four C++ call shapes resolve |
| C# spurious `Other.Move` | **FIXED** | `Svc.Chain → Dog.Move` only |
| C# property accessor call sites | **FIXED** | `Svc.Age → Svc.Compute` |
| JS/TS barrel import edge | **FIXED** | `file:src/index.ts → import:./button` now emitted |
| JSX component usage | **WAS STALE — Codex correct** | `Direct → Button` resolves; my round-3 entry was written against the pre-fix tree |
| C++ field-receiver call | **WAS STALE — Codex correct** | `Owner.go → Widget.render` resolves |

#### Round 3c — both remaining items now FIXED (re-validated 2026-09-11)

Re-tested against the current tree (both repos build, `go test ./...` green):

| Item | Verdict | Evidence |
|---|---|---|
| C++ ignores all visibility specifiers | **FIXED** | `privM`/`protM`/`sPriv`/`fileLocal` → `exports=0`; `pubM`/`sPub`/`globalFn` → `exports=1`. Consequently C++ `dead-code` under a namespace now reports `app::Widget::deadOne` (`considered: 3`) |
| Re-export (barrel) chains not traversed | **FIXED** | Both forms resolve: `viaBarrel` (`export * from`) and `viaNamed` (`export { helper } from`) → `util.ts::helper`; and with JSX, `ViaBarrel → Button` |

**Regression sweep — every earlier-round fix still holds** (this mattered: two
regressions appeared in earlier fix rounds):
```
C++ crash fixture symbols ........ 4     Java impact reaches impls ....... true
Rust implements edges ............ 8     C# uses-type rows (want 1) ...... 1
PHP hierarchy edges .............. 10    Go generic method parent ........ Job
rename-plan interp-string edits .. 3     Java @Autowired + chained ....... 3/3
Python basename scope (want 1) ... 1     mainframe estate edges .......... 44
```

**§ Round 3 and § 3b below are therefore fully closed.** The only open work in
this document is § Round 4 (mainframe), whose 13 findings are untouched — the
estate still produces the same 44 edges as at review time.

#### Originally open after round 3b (both now fixed — see 3c above)

**[HIGH] C++ ignores every visibility specifier, so `dead-code` can never report a C++ method**
This is the real cause of the C++ `dead-code` gap — *not* the namespace symbol,
which was my round-3 diagnosis. Every C++ symbol is extracted with
`Exported: true` and an empty `Modifiers` array:
```
Widget.pubM   exports=1  modifiers=[]
Widget.privM  exports=1  modifiers=[]     ← private:
Widget.protM  exports=1  modifiers=[]     ← protected:
SFoo.sPriv    exports=1  modifiers=[]
fileLocal     exports=1  modifiers=[]     ← static, internal linkage
```
Java and C# get this right (`Widget.used`/`Widget.deadOne` → `exports=0`).
Because exported symbols are dead-code *roots*, `rootCount` equals the symbol
count for every C++ file, `dead` is always empty, and genuinely dead private
methods are demoted to the softer `exportedUnreferenced` bucket. Fix belongs in
astkit's C++ extraction: honour `private:`/`protected:` access-specifier regions
and `static` at file scope.

**[MEDIUM] Re-export (barrel) chains are not followed for call resolution — general, not JSX-specific**
The barrel's own import edge is now emitted (that part is fixed), but a consumer
importing *through* the barrel never gets scope to the re-exported symbol's
file. Reproduced with plain functions, no JSX involved:
```
direct.ts    import { helper } from './util'   → direct   → util.ts::helper   ✓
viabarrel.ts import { helper } from './index'  → viaBarrel → (no edge)        ✗   export * from
vianamed.ts  import { helper } from './named'  → viaNamed  → (no edge)        ✗   export { helper } from

imports: viabarrel.ts→./index, index.ts→./util   ← the chain exists, it is just not traversed
```
Both re-export forms fail. `index.ts` barrels are ubiquitous in TS/JS, so any
call through one degrades to an unresolved or fanned-out target. Fix: resolve
import scope transitively through re-export edges (one hop covers the common
case).

#### Correction to my own round-3 report
The two "stale" findings were real errors on my part: I wrote the JSX and
C++ field-receiver entries from the earlier fixture run and did not re-test them
against the tree as it stood when I filed round 3. Codex's challenge was right.
The related barrel finding survives, but only because the failure is in
re-export *chain traversal*, which is a different mechanism than the missing
import edge I originally described.

### Still open after the fix round (round 3 — superseded by 3b above)

**[HIGH] `dead-code` remains broken whenever a namespace symbol exists (C#, C++)**
`internal/graph/deadcode.go:148-169` — `enclosingMentions` walks only **direct**
`contains` parents. A C# method's direct parent is its class, but
`namespace App`'s `RawText` is the whole file and it is a *grand*parent, so its
mention is never discounted. Isolated A/B:
```
C# with    namespace → dead: []                  ← wrong
C# without namespace → dead: ['Widget.DeadOne']  ← correct
C++ with   namespace → dead: []                  ← wrong
Java (no namespace symbol) → dead: ['Widget.deadOne']
```
Fix: walk the transitive `contains` chain, or skip container kinds
(namespace/class/struct) when building the token index.

**[HIGH] C++ `struct` members are never extracted — and a multi-line struct method is now LOST vs the pre-fix binary**
`astkit/strategies/extractors.go:1644` routes `struct_specifier` to
`cTaggedTypeSym` (type symbol only) while `:1655` routes `class_specifier` to
`cppClassSym` (extracts methods). In C++ the two differ only in default access.
A/B against the old binary:
```
              OLD                          NEW
SFoo.sInline    (absent)                    (absent)
CFoo.cInline    method|CFoo.cInline         method|CFoo.cInline
SMulti.sMultiline  function|sMultiline ←phantom   (absent)   ← coverage REGRESSION
```
The phantom-twin fix removed the mis-parented symbol without adding the correct
one, so for multi-line `struct` methods NEW has strictly less than OLD. Every
method of every C++ `struct` is invisible, and calls through them yield nothing.

**[MEDIUM] C# `new Dog().Move()` still emits a spurious edge**
Partially fixed — the correct target was added, the wrong one retained:
```
Svc.Chain → Dog.Move    0.95 ast-narrowed   ← correct (new)
Svc.Chain → Other.Move  0.95 ast-narrowed   ← still spurious; Other is unrelated
```

**[MEDIUM] C# property accessors still carry no call sites**
`public int Age => Compute();` → no `calls` edge from `Svc.Age`.

**[MEDIUM] JS/TS barrel re-export still produces no import edge**
`export * from './button'` in `src/index.ts` → no outgoing `imports` row (only
the consumer's `app.tsx → import:./index` and the CJS one appear).

**[MEDIUM] JSX component usage still produces no edge**
`<Button />` inside `App` → no `calls`/`uses-type` edge to `Button`.

**[MEDIUM] C++ field-receiver call produces no edge**
`class Owner { Widget field; void go() { field.render(); } }` → no edge. Not a
regression (the old binary also missed it, and emitted fewer edges overall), but
still open.

### Process note
The three regressions flagged in round 2 are all fixed, and this round found one
new coverage regression (C++ `struct`) — again invisible to `go test ./...`,
again caught only by A/B-ing against the parent binary. That A/B step keeps
paying for itself; it should be part of the fix workflow, not the review.

---

## How to read this file

- **§ Round 2 — verified** sections are empirical: every claim was reproduced by
  building a fixture, indexing it with a HEAD binary, and reading the actual
  `edges`/`symbols` rows or CLI output. Round-1 items are given explicit
  verdicts (FIXED / PARTIALLY FIXED / NOT FIXED / REGRESSION).
- **§ Round 1 — superseded** sections are the original code-read findings,
  retained only so the round-2 verdict tables have something to refer to. Every
  language now has a fixture-verified pass above. Do not work from round 1.
- Scope of round 2 is wider than round 1: it includes **astkit extraction**,
  the **parser projection**, the **native passes**, **edge construction**, and
  the **graph APIs** (`change-impact`, `missing-implementations`, `rename-plan`,
  `dead-code`, `impact`, `deps`) plus **incremental re-index** correctness.

Round-2 status: Go · Python · Java · C# · Rust · JS/TS · PHP · C/C++ · shared
graph/API/store layer — all fixture-verified.
**~130 defects; 12 CRITICAL.** `go test ./...` is green for every one of them.

Round 4 (2026-09-11) closed the last coverage gap: **the mainframe subsystem —
COBOL and JCL — which rounds 1-3 never reviewed** (`graph/mainframe.go` was
explicitly marked out of scope by the shared-layer pass). **13 defects**
(3 CRITICAL, 4 HIGH, 3 MEDIUM, 3 LOW), split 7 graph-layer / 5 astkit / 1 parser.
All three CRITICALs are graph-API. See § Round 4.

**Regression warning — three regressions from the round-1 fix commits, none
caught by the test suite**, all found by A/B-ing HEAD against a binary built
from the parent commit:

| Regression | From | Effect |
|---|---|---|
| Java chained calls through a lowercase receiver | `39582618` | `chain.leaf().work()` — the dominant fluent/builder idiom — lost its downstream edge entirely |
| `from .. import X` where `X` lives in the package `__init__.py` | `94168d31` | Native `uses-type` edge lost; the file now produces zero Python edges |
| `pyAnnAssignRe` reads a dict-literal entry as an annotation | `94168d31` | Turns an honest 0.7 fan-out into one confident, wrong 0.95 edge |

Recommend A/B-ing every future fix commit the same way: build the parent-commit
binary, index the same fixture, `diff` the edge dumps. `go test ./...` was green
for all three.

**Tooling note:** Prism MCP could not connect for either round
(`ENOENT: /Users/tapabratapal/bin/prism`), so both rounds used direct file
reads rather than `prism_change_impact`/`prism_search` as CLAUDE.md prescribes.

---

## Cross-cutting pattern

Six of eight languages share **one root defect**: `internal/graph/tslocaltypes.go`'s
`tsBaseClasses`/`baseClassesFor` — the single shared "walk a type's ancestors"
primitive — only recognizes the literal substring `"extends "` and returns
**one** base name. Every language routed through it inherits its blind spots:

| Language | Symptom |
|---|---|
| **C/C++** | Not wired to it at all (`baseClassesFor` has no `cpp` case) → virtual dispatch, inherited methods/constructors, multiple inheritance are all completely inert for C++, despite `edges.go` explicitly special-casing C++ throughout. Highest-severity finding of the whole review. |
| **Java** | Wired, but Java classes use `implements`, and interfaces can multiply-extend — never captured. Interface default-method dispatch is structurally unreachable. |
| **JS/TS** | Filters candidates to `Kind == KindClass` only, so **interface** `extends` chains (TS) never resolve, and comma-separated multi-`extends` is rejected outright by `tsBareType`. |
| **PHP** | Separate regex, same disease — `extends`/`implements` list regexes capture only the first name when interfaces multiply-extend. |
| **C#** | Own regex, but breaks on same-line generic `where` constraints and primary-constructor/positional-record syntax — silently drops the whole base list. |
| **Rust** | Its own path (`impl Trait for Type`), but generic trait args (`impl From<X> for Y`) aren't tolerated, and — separately — `classLanguage`/interface-satisfaction machinery excludes Rust entirely, so `dyn Trait`/generic-bound dispatch never expands to concrete implementors. |

This is the single highest-leverage fix across the whole codebase: fixing
multi-base + `implements`-keyword support in the shared walker, and wiring
C++ and Rust into it, would close the largest share of findings below.

**Round-2 note:** `tslocaltypes.go` was rewritten in `94168d31` (+232 lines) to
address this. The C# and PHP re-verification confirms the *native* layer now
resolves modern declaration forms correctly, but shows the fix did **not** reach
the graph-layer fallbacks (`edges.go` `csharpBaseListRe`, `changeimpact.go`,
`missingimpl.go`), so `--no-native` indexes and projects without a build file
still hit the original bug. See the per-language round-2 sections.

---

# Round 2 — verified

## Shared graph layer, graph APIs, store, presentation — round 2 (fixture-verified)

16 defects: 3 CRITICAL, 4 HIGH, 6 MEDIUM, 3 LOW. `go test
./internal/{graph,store,index,mcp,cli}/...` is **green** — every finding here is
a gap the suite does not cover. The healthiest parts of the system are
incremental indexing and the store: 16 edit scenarios across Go and Python were
byte-identical to `--force`, through the genuine delta path.

### [CRITICAL] `rename-plan` silently drops every call site inside an interpolated string / template literal / f-string
`internal/graph/renameplan.go:109-112` + `internal/graph/edges.go:1104-1122`
```go
strippedN := len(pat.FindAllStringIndex(stripCommentsAndStrings(before), -1))
if strippedN == 0 {
    return // only comment/string-literal occurrences on this line
}
```
`stripCommentsAndStrings` deletes the whole literal without honouring
interpolation holes, which are executable code. Reproduced identically in
TypeScript, C# **and** Python:
```
calls edges:  show → Greeter.label   0.98
rename-plan Greeter.label → caption
  Edits:       2 (declaration), 10 ('return g.label();')
  Unresolved: ['a.ts:show']        # line 6:  return `value=${g.label()}`;
```
An agent applying `Edits` renames the declaration and every other caller and
leaves `g.label()` behind — a guaranteed compile break. The call edge for that
exact site exists at 0.98, so the plan contradicts the graph it came from.
(Round 1 filed this as LOW from a code read; reproduction shows CRITICAL.)
Confidence: high.

### [CRITICAL] Fan-out zeroing runs *before* receiver-type narrowing — a precisely typed call loses its edge once ≥17 same-named methods exist
`internal/graph/edges.go:1784-1794`
```go
if capped && symbol.Language != "java" && symbol.Language != "rust" && symbol.Language != "python" {
    cands = nil          // ← C#, PHP, C/C++, Go, TS/JS
}
// ... :1919-1955 — the C#/PHP receiver-type narrowing that would pin it exactly
```
The comment claims only Java and Rust have "real evidence" narrowing, but C#/PHP
have arity (`:1804`), generic (`:1814`), arg-type (`:1822`) and
declared-receiver-type (`:1931`) narrowing sitting immediately below — and never
get the chance. With `R1 r = new R1(); r.Save(1);` and N classes each declaring
`Save(int)`:
```
n=16 classes: 1 calls edge   (correct, 0.95 ast-narrowed)
n=17 classes: 0 calls edges
n=20 classes: 0 calls edges
```
The receiver type is written in the source. The dispatch rescue at `:2220` only
fires when an *interface* declares the name. On any real monorepo `Save`,
`Execute`, `Handle`, `Dispose`, `Process`, `Get` exceed 16 trivially — a silent,
repo-size-dependent recall cliff on the most common method names. Confidence: high.

### [CRITICAL] `dead-code` is unusable for class-based languages, and reports false positives on module-level code
`internal/graph/deadcode.go:125-145,158,171`
Measured `length(raw_text)` from the DB confirms the mechanism:
```
Java : class Widget raw_text = 350 chars (whole body incl. every method)
       → tokenSymbols["deadMethod"] = 2 → outsideMentions = 1 → never Dead
C#   : namespace App = 343, class Widget = 321 → 3 mentions → never Dead
Go   : struct Widget = 25 chars (decl only) → 1 mention → correctly Dead
```
```
Go     dead: [Widget.deadMethod, deadFreeFunc]     ← correct
Python dead: [_main]  expUnref: [dead_free_func]   ← dead_method in NEITHER bucket
Java   dead: null (considered 4)                   ← deadMethod, deadStatic missed
C#     dead: null (considered 3)                   ← DeadMethod missed
TS     dead: [deadFreeFunc, entry]                 ← deadMethod missed; `entry` is a FALSE POSITIVE
```
Two defects: **(a)** 100% false-negative rate on private methods in
Java/C#/TS/Python/PHP/C++ (only Go escapes); **(b)** a false positive — `entry`
is called by `entry();` at TS module scope, and module-level statements belong to
no symbol, so they are invisible to both forward reachability and the token
index. Deleting `entry` breaks the program. Same exposure for Python/JS
script-style entry points. Note the "inbound override edges keep implementations
alive" rescue at `:109-115` is a **no-op for every nominal language** — see the
`Impact` finding. Confidence: high.

### [HIGH] Go bare calls bind to same-named *methods*, at certification-grade confidence
`internal/graph/edges.go:1466-1497` (`resolveCallees` accepts `KindMethod` for a
bare name), `:1795-1841` (arity filtering applies to C#/PHP/C/C++/Java only —
never Go/TS/Python/Rust).
```go
func (a Alpha) Close() error { ... }
func (b Beta) Close() error  { ... }
func Close(n int) error      { ... }
func useFree() error         { return Close(3) }   // bare call, 1 arg
```
```
calls useFree → Alpha.Close  0.95 astkit ast-narrowed   ← spurious
calls useFree → Beta.Close   0.95 astkit ast-narrowed   ← spurious
calls useFree → Close        0.95 astkit ast-narrowed   ← correct
```
`Alpha.Close()` takes 0 args and the call passes 1, so arity alone would have
killed it — but Go is not arity-filtered, and `filterByArgc` returns the
*unfiltered* set when filtering empties it anyway. 0.95 with reason
`ast-narrowed` survives `PolicyCertification` (floor 0.9, `ReasonASTNarrowed` not
excluded), so a certification-grade blast radius contains fabricated callers.
`change-impact 'Alpha.Close'` reports `Callers = ['useAlpha', 'useFree']`.
Composes with the previous finding into total loss: 20 types with `Close()` plus
one free `Close` → **zero** edges from `useFree` (`--no-native`); only the Go
native pass rescues it. Confidence: high.

### [HIGH] `uses-type` targets constructors and namespace symbols
`internal/graph/edges.go:1410-1440` — **`buildUsesType`, not `resolveTypeEdges`**
(the latter has exactly the `Kind` filter this loop is missing, at `:2917-2921`):
```go
for _, target := range idx.byName[strings.ToLower(candidateName)] {
    if target.Language == "python" && target.Kind == core.KindField { continue }
    if _, inScope := scope[target.FilePath]; !inScope { continue }
    // ← no Kind filter: constructors, namespaces, anything named alike
```
One C# signature `public Fix.User Load(Fix.User u)` → 8 `uses-type` rows: 5 to
the `namespace Fix` symbol (one per file), 2 to `User` constructor overloads, 1
correct. Reproduced in Java too (class + both ctors). `usesTypeIdent` also
tokenizes a dotted qualifier into segments, which is what reaches the namespace
symbols. Inflates `Impact` (which follows `uses-type` inbound) super-linearly in
overload count. Confidence: high.

### [HIGH] `Impact` never reaches the implementations of a contract method
`internal/graph/graph.go:604-607`
```go
if edge.Type != core.EdgeCalls &&
    edge.Type != core.EdgeContains && edge.Type != core.EdgeImplements &&
    edge.Type != core.EdgeExtends && edge.Type != core.EdgeUsesType {
    continue      // ← core.EdgeOverrides is not in the set
}
```
Compounded by `internal/graph/interfaces.go:309-319`, which skips structural edge
emission for every `nominalInterfaceLang` — so for Java/C#/TS/JS/Python/PHP/C++/
Rust **no method-level `overrides` or `implements` edge exists at all**.
```
grove impact 'Greeter.greet'        → ['Client', 'Client.run', 'Greeter', 'Loud', 'Soft']
grove change-impact 'Greeter.greet' → Family = ['Loud.greet', 'Soft.greet']
```
`Loud.greet` and `Soft.greet` — the two methods that actually have to change —
are absent from the blast radius; `Loud`/`Soft` appear only via the class-level
`implements` edge. `impact` and `change-impact` disagree about the same question.
Confidence: high.

### [HIGH] `missing-implementations`: `DefaultProvided` false for expression-bodied members, unreachable for C# interface defaults
`internal/graph/missingimpl.go:356-361`
```go
for _, k := range seedKinds {
    if k == core.KindClass || k == core.KindStruct {
        if strings.Contains(c.RawText, "{") || c.Language == "python" { return true }
```
```
Shape.Area      (=> 0)               DefaultProvided=False  Missing=['Square']  ← wrong
Shape.Describe  ({ return "..."; })  DefaultProvided=True   Missing=['Square']
Greeter.greet   (java default)       DefaultProvided=True   Missing=['Impl']    ← Java OK
```
Two problems: the brace test misreads `=> expr` as body-less, reporting
compiling code as broken; and the `seedKinds` gate has no `KindInterface` case,
so a **C# 8 interface default member can never set `DefaultProvided`**. Java is
fine (caught by the `hasModifier("default")` branch at `:341`); Python bypasses
the brace test; TS/PHP bodies always carry `{`. Confidence: high.

### [MEDIUM] CLI `impact` has no depth control and silently discards unknown flags
`internal/cli/commands.go:268-286` — `codeGraph.Impact(query, 3)`, depth
hard-coded; `--depth 1` is accepted without complaint and ignored. The MCP
surface *does* honour `maxDepth` (`mcp/server.go:145`), so CLI and MCP give
different answers to the same question. `Impact`'s own depth semantics are
correct — only the CLI wiring is missing. Confidence: high.

### [MEDIUM] `ComputeICR` confidence is inverted with respect to precision
`internal/graph/graph.go:686,786-797` — `confidenceForSeeds` rises monotonically
with `len(Search(intent, 20))`:
```
icr 'Alpha.Close'  conf=0.65  nExclusive=1   # exact, single symbol
icr 'e'            conf=0.80  nExclusive=8   # one letter, substring noise
```
A precise intent is reported as *less* trustworthy than a vague one.
Confidence: high.

### [MEDIUM] A parameter-qualified bare query cannot disambiguate overloads across types
`internal/graph/changeimpact.go:904-969` — `head` is truncated at `(` before
candidate collection and `queryParams` are never used to narrow. `change-impact
'Close(int)'` → *ambiguous, 3 candidates*, though only the free `Close` takes an
`int`. Usability gap rather than a wrong answer — the resolver correctly refuses
to guess. Confidence: high.

### [MEDIUM] Store — the post-splice invariant is `COUNT(*)` only
`internal/index/indexer.go:116-126`, `internal/store/store.go:713-781`.
`SpliceEdges` point-deletes owner rows and upserts the write-set; the only
verification is `EdgeCount(ctx) != len(edges)`. A write-set enumeration that both
leaves one stale row and misses one new row keeps the count equal and the
self-heal never fires. Not reproduced (16 scenarios all matched `--force`), but
the invariant cannot detect the class of bug it exists to catch. A checksum over
`(id, confidence, source, reason)` would. Confidence: medium (design observation).

### [MEDIUM] `Deps` cannot answer "who imports this file"
`internal/graph/graph.go:430-445` matches `From == "file:"+path` or
`From`/`To` prefixed `path+"::"`. `imports` edges are emitted as
`file:<importer> → import:<path>`, and `"import:"+path` matches neither pattern.
`grove deps src/Greeter.java` returns 9 edges, none of them an `imports` row.
Confidence: high.

### [MEDIUM] MCP exposes none of the type-resolved APIs
`internal/mcp/server.go:117-174` implements `grove_index|symbols|deps|impact|
icr|conflicts|certify`. `ChangeImpact`, `MissingImplementations`, `RenamePlan`
and `DeadCode` — the operations that give *correct* answers where `impact` does
not — are CLI-only, so an MCP agent is routed to the weaker primitive. The MCP
layer is otherwise sound (`grove_impact` caps at 50 with an accurate `note`
carrying the true count). Confidence: high.

### [LOW] `declParamCount` counts a Go method's receiver as a parameter
`internal/graph/edges.go:2460-2472` — `tsDeclParams(src)` takes the first
balanced paren group, which for `func (a Alpha) Close() error` is the
**receiver**, yielding `n=1` for a zero-parameter method. Latent (Go is not
arity-filtered today). `paramTypesOf` routes Go through `goDeclParamsOK` — the
fix pattern this is missing. Confidence: high (code read).

### [LOW] `stripCommentsAndStrings` truncates the rest of its input on three common constructs
`internal/graph/edges.go:1081-1127` — a single quote opens a "string" that runs
to the next matching quote or EOF, with `\` escape-skipping applied
unconditionally: **Rust lifetimes** (`fn f<'a>(x: &'a str)` — `'a>(x: &'` eaten
as a char literal); **C# verbatim / Python raw strings ending in a backslash**
(`@"C:\dir\"` — the escape-skip eats the closing quote); **JS/TS regex literals
with an unpaired quote** (`s.replace(/["']/g, '')`). Consumers are
`interfaceMethodNames`/`interfaceMemberSignatures`, `renameplan.editLine` and
`renamesite.chainFromLine`, so the failure mode is silently missing interface
members and rename sites. Code-read. Confidence: medium.

### [LOW] On equal confidence, a heuristic base edge beats the native one
`internal/graph/graph.go:160-174` — `add` replaces only on
`edge.Confidence > existing.Confidence`, and base edges are added first, so a
native edge that ties keeps `source=heuristic` and the weaker `reason`. Those two
fields are exactly what policy filters and agents read. Confidence: high.

### Verified correct (shared layer — do not re-test)
**Incremental identity**: 16 scenarios × 2 languages through the genuine delta
path (`BuildEdgesDeltaMeta` + `spliceEdgeWrite`, confirmed by the
`edge construction: incremental` marker) — rename across files, add call, delete
symbol, move symbol between files, new file, file delete, file rename, plus the
Python equivalents including package-relative imports. All byte-identical to
`--force`. *Note for future reviewers: a one-shot `grove index` never exercises
the delta path (`cli/commands.go:535` passes an empty resident graph) — drive it
through `grove mcp` or `pkg/grove`.*
**Resolver-version stamp**: indexing with the pre-fix binary then re-indexing
with HEAD *without* `--force` re-extracted every file and matched `--force`;
poisoning `meta.resolver-version` does the same. No stale/mixed index.
**Fan-out dispatch rescue**: 20 TS implementations of one interface → all 20
targets at `0.7 heuristic dispatch`. The cap does not lose real interface
dispatch when an interface declares the name.
**Loose-query collisions**: candidates are collapsed to distinct canonical
queries and the API **errors with the candidate list** rather than silently
picking one. No silent wrong pick found.
**`filterByArgc`/`filterByGeneric` never zero a set** — failed narrowing falls
back to unfiltered.
**`Search` ranking**: case-insensitive, exact-name-first, deterministic tie-break
by path then span.
**`tsLocalTypes` consumption** is correctly layered: it feeds `edges.go` and
`renamesite.go` (hence `rename-plan`), while `changeimpact.go`/`missingimpl.go`
deliberately consume only the signature-level helpers and need no local types.
Verified end-to-end: `rename-plan Greeter.label` over `mixed(g: Greeter, o: Other)`
edits `g.label()` and correctly excludes `o.label()`.
**Store edge ID scheme**: `From::type::To` used consistently by `ReplaceEdges`,
`SpliceEdges` and `dedupeWriteSet`, matching `mergeEdges`' in-memory key.

### Coverage gaps (shared layer)
`externalContract`/`externalRootedImpact`/`resolveFileLineLocked`/
`freeFunctionImpactLocked` are code-read only. `mainframe.go`, `spring.go`,
`decorators.go` untouched. `DetectConflicts` read, not fixture-tested.
`cert.CertifyDiffWithStaleness` not exercised — note it consumes
`PolicyCertification`, which the Go bare-call finding shows is admitting
fabricated 0.95 edges. Two items flagged for the language reviewers: the
`94168d31` diff adds `cpp`/`rust` to `nominalInterfaceLang`, which **removes**
structural `implements`/`overrides` emission for them (regression vs. intended
precision gain — needs a C++/Rust oracle to judge); and the `graphCallableSymbol`
widening in the same diff makes TS/JS **fields** legal `calls` targets when their
text contains `=>` or `function`, so `private items = arr.map(x => x*2)` matches.

---

## C# — round 2 (fixture-verified)

Fixtures: `fix-csharp` (12 files + `Fix.csproj`), `fix-csharp-nn`
(`--no-native`), `fix-csharp2`, `fix-csharp3`. Of 13 round-1 findings: **1
fixed, 3 partially fixed, 9 not fixed**. No regressions. 9 new defects.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | Primary-ctor / positional-record base lists → no extends/implements | **PARTIALLY FIXED** | Native now emits `extends Circle→Shape 0.96 native`; `change-impact Shape.Area` family = Circle+Square. Graph fallback untouched (`edges.go` `csharpBaseListRe`, `changeimpact.go:710`, `missingimpl.go:325`): `--no-native` still family = Square only |
| 2 | C# fields never extracted | **NOT FIXED** | `extractors.go:1846-1871` unchanged; no `field` rows for `_tracker`, `_logger`, `_items`, … |
| 3 | Property accessors produce no call sites | **NOT FIXED** | `csPropertyDecl` still has no `CallSites`; `User.Age`/`User.Label` `call_sites=[]` |
| 4 | Extension methods with receiver syntax don't resolve | **NOT FIXED** | `user.Shout()` → no edge to `Ext.Shout` |
| 5 | Extends-vs-implements by `I`-prefix | **PARTIALLY FIXED** | Native picks by target kind now (`missing-impl Contract.Run` → `Missing: null`); heuristic rows still wrong-typed, and `--no-native` → `Missing: [Sub]` false positive |
| 6 | `: this(...)` / `: base(...)` produce no call site | **NOT FIXED** | `Child.Child(int) : this()` → `call_sites: []` |
| 7 | `new T().Method()` receiver dropped → fan-out | **NOT FIXED** — now shown to give a *wrong* edge | see new finding #1 |
| 8 | `using Alias = Ns.Type;` never resolved | **NOT FIXED** | `a.Touch()` → no edge; import stored as `import:Alias = Fix.Models.User` |
| 9 | `global using` / `using static` stored with keywords | **NOT FIXED** | `import:global using System.IO`, `import:static System.Math` |
| 10 | `record struct` → class; delegates/events/operators/indexers absent | **NOT FIXED** | `class\|Point` for `record struct Point` |
| 11 | `uses-type` sprayed to namespace symbols | **NOT FIXED** | 12 rows, one per file — promoted to a cross-language finding above |
| 12 | Partial classes: only first fragment seen | **PARTIALLY FIXED** | Same-directory fragments now merge; cross-directory still broken (new finding #7) |
| 13 | Same-line `where` on non-generic base emitted nothing natively | **FIXED** | `implements Cache→ICache 0.96 native` |

### New findings

**[CRITICAL] astkit/edges — `new T().M()` binds to an unrelated same-file method**
`astkit/strategies/extractors.go:1757` (`csCallSites` qualifier node types omit
`object_creation_expression`) + `internal/graph/edges.go:1472-1496`
(`resolveCallees` same-file-first).
```go
qual := qualifierName(fn.ChildByFieldName("expression"), src,
    []string{"identifier", "this", "base", "this_expression", "base_expression"}, ...
```
Fixture `fix-csharp2/Members.cs`: `new Dog().Move();` with an unrelated
`class Other { public void Move() {} }` in the same file:
```
calls|Members.cs::Service.Chain|Members.cs::Other.Move|0.95|astkit|ast-narrowed
change-impact Other.Move → Callers: [Service.Chain]
change-impact Dog.Move   → Callers: null
```
Expected edges to `Dog.Move`/`Cat.Move`, none to `Other.Move`. The receiver is
dropped, so the bare name resolves same-file-first to the wrong class. This is a
wrong answer from a graph API for a mainstream idiom. Confidence: high.

**[HIGH] native + graph — positional record ending in `;` loses its whole base list**
`internal/native/csharp.go:258-276` (`csharpBaseList` terminators are `{`, `\n`,
`\r`, ` where ` — not `;`), same shape in `internal/graph/csharplocaltypes.go`,
and `edges.go:2828` requires `where|{|$`.
```go
case '{', '\n', '\r':
    if depth == 0 { end = i; i = len(tail) }
```
`public record Person(string Name) : IPerson;` → no `implements` row in either
layer (captured base text is `IPerson;`); `record Blob(string Id) : IVec { }`
works. `missing-implementations IVec.Len` → `Missing: null, ImplementedCount 3`
with `Plain` silently omitted. Confidence: high.

**[HIGH] astkit — top-level statements (`Program.cs`) yield no symbols at all**
`astkit/strategies/extractors.go:1592-1645` — `csVisit` has no case for
`global_statement` / `local_function_statement`.
```
select count(*) from symbols where file_path='Program.cs'  →  0
grove deps Program.cs → {"edges": null}
```
No symbols, no edges, no imports for the file. Expected at least `TopHelper` and
a synthetic entry symbol carrying the top-level call sites. Confidence: high.

**[MEDIUM] localtypes — `is`-pattern variables not inferred, dropping their calls**
`internal/graph/csharplocaltypes.go` declaration regexes + drop policy at
`edges.go:1919-1926`. `if (o is Service s2) { s2.Tag(); }` → `qual="s2"`,
type unknown, candidates dropped → no edge. The switch-expression arm
`Service s => s.Tag()` *does* resolve, so only the `is` form is missing.
Confidence: high.

**[MEDIUM] graph API — interface property contracts are `field` kind and unqueryable**
`astkit/strategies/extractors.go:1824-1844` (`csPropertyDecl` → `KindField`);
`missingimpl.go`/`changeimpact.go` accept only method/function kinds.
`interface IShape { double Area { get; } }` → `missing-implementations
IShape.Area` errors with `type "IShape" declares no member "Area"`, so a class
missing an interface *property* is never reported. Confidence: high.

**[MEDIUM] localtypes — partial fragments in different directories are not merged**
`internal/graph/csharplocaltypes.go:csharpTypeFragments` — `candidates :=
preferred` discards other-directory fragments when any same-directory fragment
exists. `Gen/Host.g.cs` declares `private Engine _engine;`, `Src/Host.cs` calls
`_engine.Run()` → no edge (the generated-partial layout is the norm for EF Core
and source generators). Confidence: high.

Round-1 items **2, 3, 4, 6, 8, 9, 10** remain open as written above.

### Verified correct (C#, HEAD — do not re-test)
Overload narrowing (generic vs non-generic vs arity); `base.Method()`; explicit
interface implementation; override family/supers; abstract-through-record
chains; nested-class qualifiers; `#if`/`#else` blanking (spans preserved, one
edge only); multi-line and attribute-prefixed base lists; `where` on the next
line; virtual dispatch rescue at 0.7; `yield return`, switch expressions,
`with { }`, `?.`/`??`, `using static` + bare call, interpolated-string calls;
same-directory partial field merging; `new Cache<Tracker>()` uses-type;
**incremental re-index byte-identical to `--force`** (424 rows, empty diff).

### Coverage gaps (C#)
No `dotnet`/Roslyn on this machine — only the text-based native pass was
exercised. Not tested: cross-`.csproj` ProjectReference edges, enums,
generic-arity overloads across files, LINQ attribution audit, event `+=` method
groups, MCP presentation.

---

## PHP — round 2 (fixture-verified)

Fixtures: `fix-php-fable` (A), `-b` (B), `-c` (C). All four round-1 HIGH items
are fixed at the regex level; one is only half-fixed downstream. No regressions.
10 new defects.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `parent::`/`static::` not narrowed | **FIXED** | `Child.run → Base.describe` / `→ Base.create`, trace `super bases=[Base Runnable Stoppable] matched=1`; works `--no-native` too |
| 2 | `phpTraitUsePattern` needs `;` right after one name | **FIXED** | Block form and `use A, B;` both emit `implements` rows; closure `use ($x) {` emits nothing spurious |
| 3 | `phpUsePattern` misses `as` alias and group use | **PARTIALLY FIXED** | Import edge works (`imports … 0.94 native`), but alias *resolution* is still dead — see new finding #4 |
| 4 | `phpExtendsPattern` single-name only | **FIXED** | `interface Both extends Pausable, Haltable` → both rows |

### New findings

**[CRITICAL] edges — PHP has no graph-layer extends/implements at all**
`internal/graph/edges.go:1265-1380` — `resolveTypeEdges`'s caller switch has
cases for ts/js/java, csharp, cpp, python, rust, go, and **no `case "php"`**.
Every PHP inheritance edge therefore comes from the native pass, which requires
`composer.json`. With `--no-native` (or in any tree without composer):
```
extends/implements rows: 0        (with native: 2 extends + 4 implements)
missing-implementations 'Runnable.run' → "Missing": null   ← Lazy implements it without run()
change-impact 'Base.run' → "Supers": null, "Family": null
```
So a WordPress/Drupal/legacy tree gets confidently wrong "closed" answers. The
same file already documents fixing exactly this shape of bug for C#
("a bare source tree with no .csproj got zero extends/implements edges"), and
`tsBaseClasses` already parses PHP signatures. Confidence: high.

**[HIGH] edges — `static`/`self` return types resolve to nothing, killing factory and fluent chains**
`internal/graph/phplocaltypes.go:140-161` (`phpReturnType`), `:223-239`
(`phpBareType` maps `self`/`static` → `""`).
```go
case "int", ..., "self", "static", "parent":
    return ""
```
Fixture B (`Model::create(): static`, `make(): self`, `save(): static { return
$this; }`, `class Post extends Model`):
```
callee="p.title"      qual="p"        cands=1 [Post.title]  → no edge
callee="create().id"  qual="create()" cands=1 [Model.id]    → no edge
callee="save().title" qual="save()"   cands=1 [Post.title]  → no edge
```
Every ActiveRecord / builder / enum-factory idiom loses its downstream edges.
Confidence: high.

**[HIGH] edges — same class name in two namespaces: `use` import not consulted**
`internal/graph/edges.go:1919-1935` (typed-receiver narrowing filters by *bare*
parent name) and `:720` (`importedFiles` is the whole repo for PHP).
`use App\Infra\Repo;` + `__construct(private Repo $repo)` + `$this->repo->find(1)`:
```
calls|Svc.go|src/Infra/Repo.php::Repo.find |0.95   ← correct
calls|Svc.go|src/Other/Widget.php::Repo.find|0.95   ← spurious
uses-type|Svc.__construct|src/Other/Widget.php::Repo|0.5  ← spurious
```
`change-impact Repo.find` lists `Svc.go` as a caller of **both** declarations.
`phpNarrowNewByNamespace` does the right thing for `new X()`; nothing equivalent
runs for typed receivers/properties. Confidence: high.

**[HIGH] native — `phpUseAliases` reads file-level `use` from symbol bodies, so it is always empty**
`internal/native/php.go:238-252`
```go
for _, symbol := range symbols { … phpUseRefs(symbol.RawText) … }
```
Symbols are class/function records; the `use … as DataRepo;` line sits above
them at file scope, so the alias map only ever captures trait-`use` lines inside
class bodies. Consequence: `new DataRepo()` gets no `uses-type` edge (while
`new Post()` does), and `$this->repo->find()` with a `DataRepo`-typed promoted
property resolves to nothing. Fix: build the map from file content as
`phpReferencedClasses` already does. Confidence: high.

**[MEDIUM] native — inheritance regexes run over the whole class body, so an anonymous class inside a method makes the outer class extend its base**
`internal/native/php.go:311-318`. `class Svc { … $x = new class extends Model {…}; }`
→ `extends|Svc|Model|0.95|native`. Pre-existing, not a regression.
Confidence: high.

**[MEDIUM] astkit — group `use A\{B, C as D}` emitted as one unsplit import with no alias**
`astkit/strategies/registry.go:462-486` walks for `namespace_use_clause`, but
group-use children are `namespace_use_group_clause`, so the fallback stores the
whole clause verbatim: `path="B\{C, D as E}" alias=""`. Downstream
`phpNarrowNewByNamespace` can never match a group-imported class.
Confidence: high.

**[MEDIUM] astkit/edges — `new static()` / `new self()` produce a callee of `static`/`self`, never a constructor edge**
`astkit/strategies/extractors.go:2063-2075` (`phpNewClassName` returns the
literal keyword). `Base.create` → `call_sites: [{"callee":"static"}]`, no
`Base.create → Base.__construct` edge. Confidence: high.

**[LOW] astkit — methods of a top-level anonymous class become free functions**
`extractors.go:1928-1956`: `$x = new class extends Foo { public function extra() }`
at file scope → `SYM function extra parent=""`. Inside a method body they are
not materialized at all. Confidence: high.

**[LOW] astkit — first-class callable `$this->g(...)` reports `argc=1`**
`metadata.go:124-130` counts the `variadic_placeholder`, so `filterByArgc`
would prefer a 1-param same-named candidate over the real 0-param one.
Confidence: medium.

**[LOW] edges — trait conflict resolution ignored**
`LoggerAware::log insteadof Cacheable` is not honoured: `$this->log('x')` edges
to **both** trait methods (one is a false positive), and the aliased
`$this->cacheLog('a')` resolves to nothing. Confidence: high.

### Verified correct (PHP, HEAD — do not re-test)
`parent::`/`self::`/`static::` narrowing with and without native; multi-trait
and block-form trait use; `implements A, \Fq\B`; `interface X extends A, B`;
constructor promotion + nullsafe `?->`; closures/arrow fns attributing call
sites to the enclosing method; `use function`/`use const` correctly excluded;
`\Fq\fn()` and `Ns\fn()` free functions; `__construct` → KindConstructor; enums
with methods and static factory calls; HTML-mixed `index.php` and `.phtml`;
**incremental re-index byte-identical to `--force`**; `rename-plan
Base.describe` enumerating all 5 real sites.

### Coverage gaps (PHP)
`__invoke`/`__call`/`__get` dispatch (confirmed no edges, expected limitation);
attribute spans; regex-fallback merge on syntax-error files; `require_once` →
`file:` resolution; constants/enum cases as symbols; MCP presentation.

---

## Go — round 2 (fixture-verified)

Fixtures: `fix-go`, `fix-go2`, `fix-go3`, `fix-go4`. 10 defects (1 critical, 3
high, 3 medium, 3 low). Worst stage is **astkit extraction**. No regressions
from `94168d31`; incremental identity held in every test.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `filesByBase` keyed by dir basename → type-use leaks between same-basename packages | **PARTIALLY FIXED** | `filesByBase` is gone, scope is now a `goDirMatchesImport` suffix match — but a caller importing both `foo/models` and `bar/models` still gets `uses-type` to **both** `Thing`s, and `goCallSiteEdges` keeps a `lastPathSegment == qualifier` fallback (`go.go:757-761`) |
| 2 | Generics skipped in `go_interfaces.go` | **PARTIALLY FIXED** | `implements Task→Runner`, `Box→Container` now emitted at 0.99. But no `overrides` edge for any generic method, and `missing-implementations Runner.Run` still answers `Missing: [Task], ImplementedCount: 0` |
| 3 | Explicit type-arg calls unhandled in `goResolveCall` | **PARTIALLY FIXED** | Native resolves them now; astkit still emits **no call site** for the single-type-arg form `Identity[int](3)`, so `--no-native` / non-module repos lose the edge |
| 4 | Interface satisfaction limited to analyzed package + direct imports | **FIXED (in effect)** | `impl.Worker` implements `iface.Doer` without importing it → `implements`/`overrides`/`dispatch` all present at 0.75 via the repo-wide heuristic layer |
| 5 | `goImportedPackageForQualifier` ignores source aliases | **PARTIALLY FIXED — new false positive** | Aliases are parsed now, but the parser emits *both* `@go-alias:mm=…/foo/models` **and** the bare path, so the basename branch still matches `models.` to the aliased package; result is order-dependent mis-binding (see below) |

### New findings

**[CRITICAL] astkit → all APIs — methods on generic types get no receiver, so they are indexed as free functions**
`astkit/strategies/extractors.go:88-113` (`goReceiverTypeName`)
```go
switch typeNode.Type() {
case "pointer_type":
    for j := 0; j < int(typeNode.ChildCount()); j++ {
        if c := typeNode.Child(j); c != nil && c.Type() == "type_identifier" { return c.Content(src) }
    }
case "type_identifier":
    return typeNode.Content(src)
}                                    // generic_type ( Task[T] ) → ""
```
Observed symbols:
```
kind    name      qualified_name  parent_symbol  signature
method  Run       Run             (empty)        func (t Task[T]) Run() error
method  Reset     Reset           (empty)        func (t *Task[T]) Reset()
method  Get       Get             (empty)        func (b Box[T]) Get() T
```
Every graph API then fails or lies:
```
$ grove change-impact 'Task.Run'            → type "Task" declares no method "Run"
$ grove rename-plan 'Task.Reset' Clear      → type "Task" declares no method "Reset"
$ grove missing-implementations 'Container.Get'  → Missing:[Box] ImplementedCount:0 Completeness:"closed"
$ grove missing-implementations 'Runner.Run'     → Missing:[Task] ImplementedCount:0
```
Plus a spurious call edge — a test that only calls the package function `g.Run()`:
```
calls  g/g_x_test.go::TestRun  g/g.go::Run    0.99 native   (correct)
calls  g/g_x_test.go::TestRun  g/g.go::Run#2  0.95 astkit   (WRONG — that is Job[T].Run)
```
`interfaces.go:~190` (`if s.ParentSymbol == "" { continue }`) drops these from
every method set, so the heuristic layer cannot compensate either. The native
go/types layer partly papers over it by still emitting `implements` edges, which
makes the failures *silent and confident*: `missing-implementations` prints
`"Completeness": "closed"` while listing implementing types as missing.
Confidence: high.

**[HIGH] native + graph — package-qualified calls fan out to every same-basename package; alias imports mis-bind, order-dependent**
`internal/native/go.go:794-807` + `:754-762` fallback; `internal/parser/treesitter.go:190-205`
emits the alias **and** the bare path. Two packages named `models`, caller
imports foo as `mm` and bar plainly, and calls only `models.Shared()`:
```
calls  user/user.go::Use  pkg/bar/models::Shared  0.95 astkit   (correct)
calls  user/user.go::Use  pkg/foo/models::Shared  0.99 native   (WRONG — foo is only reachable as mm)
-- same code, imports swapped --
calls  user3::Use3  pkg/bar/models::Shared  0.99 native  (correct)
calls  user3::Use3  pkg/foo/models::Shared  0.95 astkit   (WRONG)
```
Import order decides which spurious edge carries the higher confidence.
`--no-native` emits both targets too. Confidence: high.

**[HIGH] native — lexical type-use scope emits uses-type to same-named types in every imported package**
`internal/native/go.go:819-884` (`goImportScope` + `goTypeUseEdges`): the scope
is a file set, then bare identifier tokens are matched against `typesByName`,
so an ambiguous type name hits every in-scope package. `func Use(t mm.Thing)`:
```
uses-type  user/user.go::Use  pkg/foo/models::Thing  0.98 native   (correct)
uses-type  user/user.go::Use  pkg/bar/models::Thing  0.98 native   (WRONG)
```
The qualifier is present in the token stream (`mm.Thing`) but discarded by
`goIdentRe`. Confidence: high.

**[HIGH] astkit + native — method values and method expressions produce no reference; rename-plan emits a build-breaking plan**
`astkit/strategies/metadata.go:286-311` (`goCallSites` walks only
`call_expression`); `internal/native/go.go:510-544` (native `ast.Inspect` handles
only `*ast.CallExpr`).
```go
fn := h.Only      // method value
expr := H.Only    // method expression
return take(fn) + expr(h)
```
`call_sites` = `[{"callee":"take"},{"callee":"expr"}]` — no reference to
`H.Only` in either mode. Therefore:
```
$ grove rename-plan 'H.Only' Renamed
  Line 5: func (h H) Only() … → func (h H) Renamed() …    [the only edit produced]
```
Applying the plan does not compile. This is the `mux.HandleFunc("/x", s.Handle)`
idiom. Confidence: high.

**[MEDIUM] astkit — single explicit type-argument call is dropped (`Identity[int](3)`)**
`astkit/strategies/metadata.go:295-305`: the callee switch handles `identifier`
and `selector_expression` only. `Identity[int](3)` parses with an
`index_expression` function node, while the two-arg `Map[int,string](…)` parses
as `identifier` + `type_arguments` — which is why one works and the other does
not. Visible only without the native pass. Confidence: high.

**[MEDIUM] graph — generic embedded types produce no extends edge**
`internal/graph/edges.go:2850-2877` (`goEmbeddedTypes`): `embeddedRe` requires
the line to end right after the identifier, so `Base[T]` never matches.
`type Derived[T any] struct { Base[T]; Extra int }` → no `extends Derived→Base`
(the non-generic `Wrapper→Base` does get one at 0.7), so `impact` on
`Base.Describe` never reaches `Derived`. Confidence: high.

**[MEDIUM] graph — heuristic layer misses promoted-method calls through an embedded struct value**
`edges.go:1373` records embedding as `extends`, but the no-native call resolver
does not walk it for a local/parameter receiver. `func UseWrapper(w Wrapper) {
w.Bump(); w.Hello() }`: both edges present with native on, **neither** with
`--no-native`. Confidence: high.

**[LOW] astkit — generic type declarations carry no TypeParameters**
`extractors.go:118-152` (`goTypeDecl`) never calls `goTypeParameters`, although
that helper has a `type_spec` branch for exactly this. `Task` →
`type_parameters: []` while the generic *function* `Map` → `["T","U"]`. Also
removes the obvious input for fixing the CRITICAL above. Confidence: high.

**[LOW] native — `goSymbolForFunc` receiver-less fallback can bind a method call to a same-named package function**
`internal/native/go.go:600-613`: when a method symbol is not indexed under its
receiver (exactly the generic case), resolution silently falls back to the
receiver-less key in the same directory. In `fix-go3` package `g` has both
`func Run()` and `func (j Job[T]) Run()` and they only stay distinguishable
because `ensureUniqueIDs` appended `#2`. Code-read; masked in the fixture.
Confidence: medium.

**[LOW] native — `goDirMatchesImport` suffix match can bind a foreign module's import path to a local dir**
`internal/native/go.go:812-817`: `strings.HasSuffix(imp, "/"+dir)` — an import
of `github.com/other/project/recv` matches a local dir `recv`. Same class as
round-1 #1, narrowed but not closed. Not reproduced (needs a dependency whose
path tail equals a local dir). Confidence: medium.

### Verified correct (Go, HEAD — do not re-test)
Embedded struct promotion, value **and** pointer receivers, interface embedding;
interface dispatch fan-out plus `#member` contract anchors; `rename-plan
Base.Hello Greet` complete and correct across interface spec, declaration and
three call sites; `change-impact Base.Hello` declaration + supers + callers;
aliased import used as `mm.FooOnly()` and **dot-imports**; `init()`, `go`,
`defer`, closures capturing a receiver; `_test.go` → implementation edges for
both in-package and external test packages; cross-package structural
satisfaction without an import. **Incremental identity verified twice** (adding
an implementation; cross-package rename + method deletion) — byte-identical to
`--force`.

Note: no `tests` edge type is produced any more — `graph.go:108-118`
deliberately filters legacy persisted `tests` rows and `deadcode.go:102`
documents that it added no reachability. Treated as intentional.

### Coverage gaps (Go)
Build tags, cgo, vendored/multi-module (`go.work`) layouts, type aliases
(`type A = B`), constraint interfaces (`~int | ~string`), `types.Alias` method
sets. The `--no-native` path on a repo without `go.mod` from a cold cache was
not exercised. The two LOW code-read items were not reproduced. MCP presentation
and `store.SpliceEdges` dedupe not reviewed for Go specifically.

---

## Python — round 2 (fixture-verified)

Fixtures: `fix-pyrev` … `fix-pyrev8`, `fix-pyinc`. 14 defects, **2 of them
regressions introduced by `94168d31`**. Worst stage is **edge construction**, not
astkit or native. The Python parts of `94168d31` are otherwise good: C3 MRO is
correct (including on an inconsistent hierarchy), bare annotations and multi-line
`with (...)` genuinely work, incremental indexing is byte-identical to `--force`.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `from . import x` → package `__init__.py`, no import edge | **PARTIALLY FIXED + REGRESSION** | `from . import helpers` now emits a correct `imports … 0.94 native` edge; but `from .. import Marker` where `Marker` lives in `pk/__init__.py` now emits **nothing**, where the pre-fix binary emitted `uses-type … pk/__init__.py::Marker 0.96 native` |
| 2 | `imported_names` only for `ast.ImportFrom` | **FIXED** (two holes) | `import pkg.core as pc` + `Optional[pc.Engine]` → `uses-type … 0.96 native`. Holes: quoted annotations, and `import a.b`/`import a.c` key collision (below) |
| 3 | `pyClassAttrTypes` skips the same-dir tie-break | **PARTIALLY FIXED** | Class-body lookup is preferDir-pinned now; the `__init__` lookup is not — a different same-named class supplies `self.*` types |
| 4 | Bare `x: Type` not handled for function locals / `self.x` | **FIXED** | `a: Engine` (no `=`) and `self.store: Store` both resolve at 0.95 |
| 5 | Multi-line parenthesized `with (...)` missed | **FIXED** (one gap) | Multi-line form works; the one-liner `with lk: pass` is still missed |
| 6 | MRO is BFS-by-name, not C3 | **FIXED** | `D(B,C)` → `super().who()` = `B.who` **only**; inconsistent `Bad(P1(X,Y), P2(Y,X))` terminates deterministically, no hang |
| 7 | Unused `self_name` cleanup | **FIXED** | Removed |

### New findings

**[CRITICAL] edges — Python import scope is resolved by module *basename* across the whole repo**
`internal/graph/edges.go:848-975` (the generic tail of `importedFiles`; there is
no Python branch)
```go
for _, c := range idx.baseToFiles[segLower] {          // :957
    if base == segLower || strings.HasSuffix(lower, "/"+segLower) ||
        strings.HasSuffix(lower, "/"+segLower+".py") ||    // :965
        ... { out[c] = struct{}{} }
```
`lastImportSegment(".models")` → `"models"`, so **every** `models.py` in the repo
enters scope. Contrast `computeImportFilesForQualifierForSymbol`
(`edges.go:2663-2682`), which *does* call `pyModuleFiles` for Python.

`alpha/models.py` and `beta/models.py` each define `Record.persist`;
`alpha/svc.py` does `from .models import Record; r = Record(); r.persist()`:
```
calls|alpha/svc.py::save|alpha/models.py::Record.persist|0.95|astkit|ast-narrowed
calls|alpha/svc.py::save|beta/models.py::Record.persist |0.95|astkit|ast-narrowed  ← WRONG
imports|file:alpha/svc.py|file:alpha/models.py|0.94|native                          ← native got it right
```
The same shape also fabricates a *constructor* edge to a class the caller never
sees. User-visible consequence:
```
$ grove rename-plan 'Engine.go' run2 .
  Edits: app/engine.py:9, app/use.py:6,
         lib/engine.py:10      ← unrelated class in another package
         lib/sub/deep2.py:12   ← its caller
  Ambiguous: null  Unresolved: null  SitesTotal: 4  Completeness: "closed"
```
An apply-able rename plan that breaks code, reported as closed and unambiguous.
`models.py`/`utils.py`/`views.py` duplication is near-universal in real Python
repos, so this is systematic. Confidence: high.

**[HIGH] edges/localtypes — receiver narrowing keys on the class *name* only; no file or preferDir tie-break**
`internal/graph/localtypes.go:362`, and `""` preferDir at `:376`, `:402`
```go
byType := filterByParent(cands, typ)                                  // name-only
for _, m := range subclassOverrides(idx, lang, typ, calleeName, "") {  // preferDir dropped
bases := baseClassesFor(idx, lang, typ, "")                           // preferDir dropped
```
The second half of the CRITICAL above: once two `Engine`/`Record` symbols are in
scope, `localTypes["r"] == "Record"` matches both and both are emitted at 0.95.
`pyResolveClass`/`pyBaseClasses`/`pyClassAttrTypes` all accept a `preferDir`; the
main call resolver does not. Confidence: high.

**[HIGH] native — REGRESSION: `from .. import Name` where `Name` lives in the package's `__init__.py` now yields no edge at all**
`internal/native/python.go:170-177`
```python
else:                                        # node.module is None
    for alias in node.names:
        target_file = local_module_file(rel, alias.name, node.level)
        if target_file:                      # looks for pk/Marker.py only
            imported_modules[...] = target_file
```
The new branch resolves `alias.name` as a **submodule** and never falls back to
the package `__init__.py`, and it populates only `imported_modules`, never
`imported_names`. The old code computed `local_module_file(rel, None, level)` →
`pk/__init__.py`.
```
HEAD:      imports|file:pk/sub/leaf.py|import:..|0.9|astkit    (and nothing else)
           diagnostic: "python: no edges produced"
pre-fix:   uses-type|pk/sub/leaf.py::takes|pk/__init__.py::Marker|0.96|native   ← LOST
```
Compounding graph-layer bug in the same fixture: `lastImportSegment("..") == ""`
(`edges.go:1019-1031`) so `importedFiles` skips it, and `bindPySubmoduleImports`
drops `..#Marker` — so `pk/__init__.py` is never in `leaf.py`'s call scope and
`m.mark()` gets no edge either, in HEAD *or* pre-fix. Confidence: high (A/B
verified against the pre-fix binary).

**[HIGH] native — quoted / forward-reference qualified annotations bind to the wrong homonym at 0.96**
`internal/native/python.go:96-109` (`type_qualifiers`) vs `:86-92` (`type_names`).
`type_names` re-parses string annotations (`ast.parse(..., mode="eval")`);
`type_qualifiers` has no `ast.Constant` branch, so the `pc.`/`lc.` qualifier is
lost for every quoted annotation and `add_types` falls through to the caller's
own file:
```
uses-type|app/shadow.py::takes |app/shadow.py::Conf|0.96|native     ← WRONG (local homonym)
uses-type|app/shadow.py::takes |lib/conf.py::Conf  |0.5 |heuristic  ← real target, demoted
uses-type|app/shadow.py::takes2|lib/conf.py::Conf  |0.96|native     ← unquoted: correct
```
`if TYPE_CHECKING:` + quoted annotations is the standard cycle-avoidance idiom,
so this hits real code hard. Confidence: high.

**[HIGH] edges — `pyClassAttrTypes` picks `__init__` by parent *name* across all files**
`internal/graph/pylocaltypes.go:671-682`
```go
for _, cand := range idx.byName["__init__"] {
    if cand.ParentSymbol != className { continue }   // name-only; no FilePath check
    if init == nil || dirOf(cand.FilePath) == preferDir { init = cand }
```
The class symbol is preferDir-pinned but `init` is chosen independently, with no
`init.FilePath == class.FilePath` requirement. `app/engine.py::Engine` has no
`__init__` and no `part` attribute, yet `self.part.tick()` binds to
`lib/engine.py::LibWidget.tick` at 0.95 via the *other* `Engine`'s `__init__`.
Confidence: high. (Remaining half of round-1 #3.)

**[MEDIUM] interfaces — `typing.Protocol` structural implementors are never linked**
`internal/graph/interfaces.go:164-170` lists `python` as **nominal**, which is
right for ABCs and wrong for `Protocol` — the one Python construct that is
structural. `class FileReader` satisfying `class Reader(Protocol)` gets no
`implements` edge and no dispatch edge; `missing-implementations 'Reader.read'`
answers `ImplementedCount: 0, Missing: null, Completeness: "closed"` for a
Protocol that *is* implemented. The ABC path works correctly by contrast.
Confidence: high.

**[MEDIUM] edges — `__call__`, `__getattr__`, `__getitem__` implicit dunders never emitted**
`internal/graph/edges.go:1630-1643` handles only `pySetattrTargets` and
`pyWithTargets`. `t(1)`, `t.missing_thing`, `t[0]` on a class defining all three
produce no edges to them. Callable objects, `__getattr__` proxies and container
types are mainstream Python; their bodies are invisible to `impact` and
`dead-code`. No false positives — purely missing. Confidence: high.

**[MEDIUM] edges — `with X() as y:` never types `y`, so `y.m()` fans out to base methods the override shadows**
`internal/graph/pylocaltypes.go:19-40` has no `with … as` rule, although
`pyWithTargets` (`:960-998`) already computes the item's class for the
`__enter__`/`__exit__` edges and then discards it:
```
calls|consumer.py::ctx|pkg/core.py::Engine.run|0.7|heuristic|dispatch
calls|consumer.py::ctx|pkg/core.py::Base.run  |0.7|heuristic|dispatch   ← spurious
```
Confidence: high.

**[MEDIUM] native — `import a.b` + `import a.c` collide in `imported_modules` (keyed by first segment)**
`internal/native/python.go:153-159`
```python
imported_modules[alias.asname or alias.name.split(".")[0]] = target_file
```
`import lib.engine` and `import lib.conf` both key on `"lib"`; last wins. Result
for `def f(a: lib.engine.Engine, b: lib.conf.Conf)`: `Conf` at 0.96 native,
`Engine` demoted to 0.5 heuristic *and* a wrong homonym joins. Related, same
function: `local_module_file` returns `None` whenever `len(matches) != 1`
(`python.go:58-59`), so an ambiguous `import pkg.mod` is silently dropped rather
than reported. Confidence: high.

**[MEDIUM] edges — one-line `with lk: pass` never matched**
`internal/graph/pylocaltypes.go:39` — `pyWithRe`'s `[ \t]*(?:\n|$)` tail requires
the colon to end the line, so the one-liner produces zero edges while the
single-line-with-newline and multi-line forms both work. Same regex now carries a
code-read hazard: with `(?ms)`, a colon not at EOL lets the lazy `.+?` scan
forward across lines. Not reproducible as a wrong edge (comments are stripped and
`pyBareType` rejects the garbage). Confidence: high for the miss, low for the
over-match.

**[LOW/MEDIUM] edges — a read-only property access also emits an edge to the `@x.setter`**
`internal/graph/edges.go:1590-1627` / `resolvePropertyTargets` (`:2370`). A pure
read `return p.val` emits edges to both `Prop.val` (getter) and `Prop.val#2`
(setter body, which never runs). `AttrSites` are deduped by access text and carry
no read/write flag, so the fix needs that flag at `astkit/strategies/metadata.go:375-418`.
Inflates `impact`, `change-impact` and `rename-plan` for every property pair.
Confidence: high.

**[LOW] astkit — nested functions and nested classes are never materialized**
`astkit/strategies/extractors.go:559-587` — the `function_definition` case never
recurses into `body`. Calls made inside a nested function are attributed to the
enclosing one (right for blast radius) and `declaresLocalFunction` correctly
suppresses fabricated edges *to* them, so this is coverage loss, not wrong
answers — but decorator factories, closures and `functools.wraps` wrappers are
un-queryable. Confidence: high (deliberate per the `:641-645` comment).

**[LOW] edges — module-level calls produce no edges**
`TOPLEVEL = Temp()` / `TOPLEVEL(3)` / `pkg.helpers.helper_two()` at module scope
emit nothing. Import-time side effects — registry population,
`app = Flask(__name__)`, singleton construction — are invisible to `dead-code`
and `impact`. Confidence: high.

**[LOW] edges — REGRESSION: `pyAnnAssignRe`'s new `(?:=|$)` reads a dict-literal entry as a local annotation**
`internal/graph/pylocaltypes.go:22`
```go
pyAnnAssignRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*([^=\n]+?)\s*(?:=|$)`)
```
A dict literal's last bare-identifier-keyed entry (`table = {\n thing: Gear\n }`)
is read as `thing: Gear`:
```
HEAD:     calls|o.py::dict_fp|o.py::Gear.spin |0.95 ast-narrowed   ← confident and wrong
pre-fix:  calls|o.py::dict_fp|o.py::Gear.spin |0.7  dispatch       ← honest fan-out
          calls|o.py::dict_fp|o.py::Wheel.spin|0.7  dispatch
```
A trailing comma saves it. Narrow, but a strict confidence regression.
Confidence: high (A/B verified).

**[LOW] parser — `import lib.engine` fabricates a second binding Python does not create**
`internal/parser/treesitter.go:222-227` adds `engine=lib.engine` alongside
`lib=lib.engine`, so a bare `engine.Foo()` in that file resolves even though
Python binds only `lib`. The comment justifies it as compensating for astkit's
last-receiver-segment retention; it should be gated to the case where the call
site actually lost the prefix. Confidence: medium.

**[LOW] missingimpl — `@abc.abstractmethod def m(...): ...` counted as a provided default**
`internal/graph/missingimpl.go:163` (`contractProvidesBody`) treats the `...`
Ellipsis body of an abstract stub as a real implementation, so
`DefaultProvided: true` for a method that raises `TypeError`. Confidence: medium.

### Verified correct (Python, HEAD — do not re-test)
**C3 linearization** (`pyMRO`/`pyLinearize`): `D(B,C)` → `B.who` only;
inconsistent `Bad(P1(X,Y), P2(Y,X))` terminates deterministically with no hang,
no dropped ancestors, no duplicates; `narrowBySuper` honours it. **Incremental
identity verified twice** (add method+call; delete a class cross-file) —
byte-identical to `--force`. **native only ever adds edges or upgrades
`0.5 heuristic` → `0.96 native`** — nothing dropped, no id/source mismatch, all
`calls` edges identical with and without native. Bare annotations (locals,
`self.x: T`, dataclass fields); multi-line `with (...)`; star-import resolution;
`super().run()`; `cls.make()` from a classmethod; all decorator forms incl.
stacked, with spans starting at `def`; `async def`/`await`; conditional and
`try/except ImportError` imports; `if TYPE_CHECKING` bodies; getter/setter
`ensureUniqueIDs` disambiguation; `missing-implementations` on a real ABC.

### Coverage gaps (Python)
Metaclasses/`__init_subclass__`, `__slots__`, `singledispatch`/`@overload`,
`Generic[T]`/`TypeVar` bases, namespace packages (no `__init__.py`), `.pyi`
stubs, `__all__` re-exports, `async with`/`async for`, walrus and match-statement
bindings, fan-out caps on a large repo, MCP surface, `icr`/`certify`/`conflicts`,
the `eval/` Python oracle, `SpliceEdges` under concurrent writers.

---

## Java — round 2 (fixture-verified)

Fixtures: `fixj`, `fixj2`, `fixj3`, `fixj4`, `fixj6`. `javac`/`java`/`mvn`
present; the native pass runs with a minimal `pom.xml`. 14 defects, **1 a
regression from `39582618`**. All four round-1 HIGH items are genuinely fixed.
Worst stages: **astkit extraction** (two whole language constructs missing) and
the **graph APIs**.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | Shared `tsBaseClasses` followed only `extends`, never `implements` → interface default-method dispatch unreachable | **FIXED** | `Square.report → Shape.describe 0.85 inheritance` and `Shape.describe → Square.area 0.7 dispatch`, with and without native |
| 2 | `extends` regex captured one name only | **FIXED** | `interface AB extends A, B` → both edges |
| 3 | `implements` regex couldn't tolerate a generic arg mid-list | **FIXED** | `Multi implements Comparator<Multi>, Serializable, AB` → `implements Multi→AB` (third position, past the generic) |
| 4 | `javaNewPattern` didn't tolerate `<…>` before `(` | **FIXED** | `new Gen<>()` → `GenUse.use → Gen.get 0.95`; raw `new Gen()` too |
| 5 | `javaBestType` drops ambiguous cross-package names | **NOT FIXED — worse than documented** | It does not drop, it *fans out*: an explicit `import com.ex.a.Leaf` still binds to **both** `a/Leaf.work` and `b/Leaf.work` at 0.95 |
| — | `dead-code` never reports an unreferenced private method | **CONFIRMED** | `Square.unusedHelper` private + unreferenced → `considered: 51, dead: null` |
| — | `missing-implementations` brace test vs Java `default` methods | **Not a Java defect** | `contractProvidesBody` checks `hasModifier("default")` first; `Iface.findOr` → `Missing: null` |

### New findings

**[CRITICAL] edges — REGRESSION in `39582618`: chained calls through a lowercase receiver lost their downstream edge**
`internal/graph/javalocaltypes.go:539` (`javaCallResultOwners`)
```go
owners := map[string]bool{symbol.ParentSymbol: true}
...  // + up to 4 levels of base classes
if own := javaMethodsOwnedBy(candidates, owners); len(own) > 0 { return own }
...
for _, match := range javaQualifiedCallRe.FindAllStringSubmatch(lines[off], -1) { // \b([A-Z]\w*)\s*\.\s*(\w+)\s*\(
```
Built from `39582618~1`:
```
Caller.run         → Chain.leaf   0.95
Caller.run         → Leaf.work    0.95   ← present
Caller.viaFactory  → Chain2.make  0.95
Caller.viaFactory  → Leaf.work    0.95
```
HEAD:
```
Caller.run         → Chain.leaf   0.95
Caller.viaFactory  → Chain2.make  0.95
Caller.viaFactory  → Leaf.work    0.95   ← only the Type.m() form survives
```
The owner set is the *caller's* class hierarchy, but in `x.foo().bar()` the owner
of `foo` is the type of `x` — essentially never the caller's own class. The
fallback regex `([A-Z]\w*)\s*\.` cannot match a variable/field receiver, which is
lowercase by Java convention. So `chain.leaf().work()` — the dominant fluent/
builder idiom — silently lost its edge, and `rename-plan Leaf.work` now omits
`Caller.java:6`. The fix's own test (`java_callresult_test.go`) exercises only
the same-class and `Type.m()` shapes. Confidence: high.

**[CRITICAL] astkit — enum members are never extracted**
`astkit/strategies/extractors.go:744-776` (`javaVisit`) inspects only **direct**
children of the body; tree-sitter-java nests enum members under
`enum_body → enum_body_declarations → method_declaration`, one level deeper.
An enum with `pretty()`, `weight()` and a constant-specific body on `RED`
produces exactly one row — the enum type itself:
```
enum  Colors  Colors  (no parent)  core/Colors.java  3  10
$ grove change-impact 'Colors.pretty' → type "Colors" declares no method "pretty"
```
No methods, no constants, no constant-specific override. Every call to an enum
method anywhere in a repo dangles. Confidence: high.

**[CRITICAL] astkit — record components are not extracted (no fields, no accessors)**
`astkit/strategies/extractors.go:751` routes `record_declaration` to
`javaTypeDecl`, which reads only `ChildByFieldName("body")`; the
`formal_parameters` component list is ignored. `record Point(int x, int y)`
yields `Point`, `Point.sum`, `Point.origin` — but no `x`/`y` as fields *or* as
the implicit accessors, so `Point.sum`'s body `x() + y()` produces **zero**
outgoing edges and `change-impact 'Point.x'` errors. Records are also reported
with `kind: class`. Confidence: high.

**[CRITICAL] graph APIs — `rename-plan` rewrites a same-named method in an unrelated package, with `Ambiguous: null`**
`com.ex.a.Leaf.work` and `com.ex.b.Leaf.work` are unrelated classes:
```
$ grove rename-plan 'Leaf.work' newWork
  a/Caller.java:9   Chain2.make().work() → newWork()
  a/Leaf.java:4     public String work() → newWork()
  b/Leaf.java:4     public String work() → newWork()   ← WRONG CLASS
  "Ambiguous": null
```
A silently uncompilable multi-package rename — and, because of the regression
above, it simultaneously *misses* `Caller.java:6`. Over- and under-inclusive at
once. Confidence: high.

**[HIGH] edges — a field with a same-line annotation loses its type, killing every call through it**
`internal/graph/javalocaltypes.go:24-26`
```go
javaFieldRe = regexp.MustCompile(`(?m)^\s+(?:(?:public|private|protected|static|final|transient|volatile)\s+)*([A-Z]\w*)...`)
```
A leading `@Annotation` on the same line is neither whitespace nor in the
modifier alternation, so the line never matches:
```
private Repo repo;                      → Plain.load → Repo.find 0.95
@Deprecated \n private Repo repo;       → AnnotatedOwnLine.load → Repo.find 0.95
@Deprecated private Repo repo;          → (no row)                        ← lost
```
Verified with the real idiom: `@Autowired private Repo repo;` produces **no**
`Svc.load → Repo.find` edge. This silences the entire Spring/JPA/Mockito
injected-dependency call graph — `@Autowired`, `@Inject`, `@Value`, `@Mock`,
`@Resource` are all routinely written on the field's own line. Confidence: high.

**[HIGH] astkit — method references (`Foo::bar`, `Leaf::new`) produce no call sites**
`astkit/strategies/metadata.go:420-424` — `javaCallSites`' `nodeTypes` is
`{method_invocation, object_creation_expression, explicit_constructor_invocation}`;
`method_reference` is absent. `xs.stream().map(Refs::len)` and
`Supplier<Leaf> s = Leaf::new;` produce no edges, so `Refs.len` looks
unreferenced to every API. Confidence: high.

**[HIGH] edges — spurious `uses-type` edges from the case-insensitive name index: self-loops and wrong-kind targets**
`internal/graph/edges.go` `resolveTypeEdges` looks up
`idx.byName[strings.ToLower(name)]` with no case-equality re-check:
```
uses-type  C1.ov       → C1.ov            ← field points at itself
uses-type  C1.ov       → C2.ov            ← field of another class
uses-type  Chain2.make → Chain.leaf       ← a method as a "type"
uses-type  Ctors       → BaseC.BaseC
```
Type token `Ov` lowercases to `ov`, matching the *field* `ov`; `Leaf` matches the
*method* `leaf`. Both `Impact` and `dead-code` traverse `EdgeUsesType`, so this
inflates reachability — part of why `reachableCount` exceeds anything real.
Confidence: high.

**[HIGH] astkit — Lombok-synthesized accessors collide with hand-written methods**
`astkit/strategies/extractors.go:826-881` (`synthesizeLombokAccessors`) has no
check for an existing method of that name. A `@Data` class with a hand-written
`getName()` gets two `Entity.getName` symbols, and one call site yields two
edges (`…getName` and `…getName#2`). `dead-code` then reports the synthesized
symbols as user-actionable findings (`"public setLoanId(...) [lombok, from field
loanId]"`). Also: `@Builder`/`@AllArgsConstructor`/`@NoArgsConstructor`/
`@EqualsAndHashCode` are unhandled, and the boolean test
`strings.Contains(f.Signature,"boolean ")` misses `Boolean` and matches a field
typed `boolean[]`. Confidence: high.

**[MEDIUM] edges — `this(...)` constructor delegation produces no edge; `super(...)` is not argc-filtered**
`astkit/strategies/metadata.go:436-445` emits the callee as the literal
`"this()"`/`"super()"`, discarding the arguments — so downstream narrowing is
impossible even in principle, despite `Argc`/`Args` being carried on the call
site:
```
Ctors.Ctors#2 → BaseC.BaseC    0.85   // BaseC(String) — correct
Ctors.Ctors#2 → BaseC.BaseC#2  0.85   // BaseC() — spurious, argc 1 vs 0
(no row for Ctors.Ctors → Ctors.Ctors#2)   // this(5) lost entirely
```
Confidence: high.

**[MEDIUM] edges — an explicit `import` does not shadow a same-package class of the same simple name**
`com/ex/b/UseB.java` with `import com.ex.a.Leaf;`:
```
UseB.go → a/Leaf.work  0.95   // correct
UseB.go → b/Leaf.work  0.95   // WRONG — shadowed by the import (JLS §6.4.1)
```
A 2-way fan-out at full confidence with no reason label distinguishing them.
This is the practical shape of round-1 #5 — not "dropped" but "silently
doubled". Confidence: high.

**[MEDIUM] astkit — only the first declarator of a multi-declarator field is extracted**
`astkit/strategies/extractors.go:918` — `FindChildByType(n, "variable_declarator")`
returns one node, but a `field_declaration` may carry several. `int a, b, c;`
yields only `a`. Confidence: high.

**[MEDIUM] astkit — anonymous-class and lambda bodies are not materialized**
`javaVisit` descends only into class/interface/enum/annotation bodies; an
`object_creation_expression` carrying a `class_body` is never visited. Mitigating:
`javaCallSites` walks the whole method body, so calls inside the anonymous class
are still attributed to the enclosing method and reachability survives — but the
anonymous override itself is invisible to `change-impact` and
`missing-implementations` on `Runnable.run`. Confidence: high.

**[MEDIUM] edges — no `overrides` edges are ever produced for Java**
`internal/graph/interfaces.go:314` is the only `EdgeOverrides` emitter and it
links method→*interface type* via method-set satisfaction only.
`select count(*) … where edge_type='overrides'` → **0** in all four fixtures,
despite `@Override` methods throughout. Consequences: `deadcode.go:111`
(inbound-override keeps implementors alive) never fires for Java, and
`changeimpact.go:834` (requires `EdgeOverrides` + `EvidenceSourceNative`) is dead
code for Java. `ChangeImpact.Family` is correct anyway because it is recomputed
by name, so this is latent rather than currently visible. Confidence: high.

**[LOW] astkit — static and wildcard imports are flattened into the same shape as a type import**
`astkit/strategies/registry.go:221-222` strips `import` then `static`, so
`import static com.ex.core.Overloads.name;` is recorded as
`com.ex.core.Overloads.name` and `import java.util.*;` as `java.util.*`, with
nothing marking which is which. The static-import call still resolved correctly
in practice. Confidence: medium.

### Verified correct (Java, HEAD — do not re-test)
**The Go generic-receiver bug has no Java analogue** — `class Box<T extends
Comparable<T>>`, generic method `<R> R map(...)`, nested `static class Inner<U>`
all get correct `parent_symbol` and populated `TypeParameters`. Nested-class
QualifiedName projection (`Box.Inner.val`); overload ID collisions
(`Overloads.f`, `#2`, `#3`, `#4`, plus two constructors) without collapsing;
overload argc/arg-type narrowing verified from separate caller classes;
interface default-method dispatch in both modes; interface-typed field/param
dispatch; `super.log()` and bare `log()` both → `Base.log 0.85 inheritance`;
`missing-implementations` correct for `default`, abstract, and a member
annotated `@SuppressWarnings({"a","b"})`; **Spring `spring.go` with no false
positives** (`findByName` → `User.getName`, `findByNonexistentField` → nothing,
Thymeleaf `${user.name}` → 0.6); **incremental identity byte-identical** (129
rows, empty diff, including span shifts).

### Coverage gaps (Java)
Every fixture reported `"java: resolved 0 native call edge(s)"` — not determined
whether that is correct-by-design (Java native doing types/inheritance only) or a
silent failure. `javaReceiverQualifier` cast/array/class-literal shapes read but
not tested. `Impact`/`Deps`/`ComputeICR` not exercised for Java (expect inflated
fan-out given the spurious `uses-type` edges). Not tested: sealed/permits,
varargs argc, `Outer.this.m()`, switch pattern matching, multi-module Maven
reactors, `package-info.java`.

---

## JavaScript / TypeScript / TSX — round 2 (fixture-verified)

Fixtures: `fix-jsts` (17 files, native + `--no-native` twins), `fix-jsts2` …
`fix-jsts8`. Native tsc **was** available, so both layers were exercised.
17 defects. Worst stage: the graph/edges layer (call-target narrowing and
interface satisfaction), with astkit's JS import/field extraction close behind.
**No regression found in the rewritten `tslocaltypes.go`** — deliberately probed
with index signatures, union/object-literal annotations, arrow-typed fields,
generic constraints and multi-segment `new`.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | Interface `extends` chains never walked | **PARTIALLY FIXED** | Field-type hops through an ancestor interface now work; a *call to an inherited interface member* still yields zero edges (see below) |
| 2 | CommonJS `require()` / dynamic `import()` → no import edge | **PARTIALLY FIXED (native only)** | With tsc: `imports file:cjs/b.js → file:cjs/a.js 0.97 native`. With `--no-native`: **no import edge and no call edge at all** out of that file |
| 3 | Arrow class fields excluded as call targets | **PARTIALLY FIXED** | `graphCallableSymbol` now accepts JS/TS `KindField` containing `=>`, so `Widget.fire → Widget.handleClick 0.95` works. Calls made *inside* the arrow field are still lost |
| 4 | `tsBareType` comma rejection lost `extends A, B` | **FIXED** | `interface Multi extends Mid, Other` → both edges, `--no-native` too |
| 5 | Overload resolution reported `declarations[0]` | **FIXED** | `declInfo` now picks `declarations.find(d => d.body)`; lands on the implementation |
| 6 | `tsNewAssignRe` one namespace segment | **FIXED** | `new Deep.Inner.MysqlDeep()` resolves at 0.95 |

### New findings

**[CRITICAL] edges — a same-file subclass override shadows the typed target; `impact` loses the caller**
`internal/graph/edges.go:1758-1768`
```go
if (symbol.Language == "java" || symbol.Language == "rust" || symbol.Language == "csharp") &&
    qualifier != "" && qualifier != "this" && ... {
    if _, isSelf := selfVars[qualifier]; !isSelf { sameFileWins = false }
}
```
The "a declared receiver type beats the same-file preference" rule is applied to
Java/Rust/C# only — never TS/JS. With `class B extends A` in the *caller's* file
and a field typed `a: A`:
```
UserSameFileSub.plainOverridden    → callers.ts::B.m  0.7  dispatch      ← A.m dropped
UserSameFileSub.plainNotOverridden → base.ts::A.n     0.95 ast-narrowed  (no override exists)
UserNoSub.plain                    → A.m 0.95 + B.m 0.7                  (different file: correct)

grove impact 'A.m' → A, B, UserNoSub, UserNoSub.plain, UserNoSub.opt
```
`UserSameFileSub.plainOverridden` — a real caller of `A.m` — is absent from the
blast radius. `change-impact` survives only because its override-family expansion
re-adds the caller. TS/JS is where this bites hardest: `export class X extends Y`
in a consumer file is ubiquitous (React HOCs, NestJS, TypeORM). Confidence: high.

**[HIGH] interfaces — calls to a method inherited from an ancestor interface produce no edge**
`internal/graph/interfaces.go:239` (`interfaceMethodNames` returns the
interface's *own* members) and `tslocaltypes.go:712` (`tsTypeOwnMember` searches
`ParentSymbol == typ` with no ancestor walk; TS interface members are not indexed
symbols at all). With `interface DriverEx extends Driver` where `escape` is
declared on `Driver`:
```
Repo.twoHopOwn       → MysqlDriver.quote  0.7 dispatch
DirectRepo.oneHopOwn → MysqlDriver.quote  0.7 dispatch
(zero rows for oneHopInherited / twoHopInherited)
```
`sat.implementors[DriverEx]` has no `escape` key, and the
`declaringIfaces["escape"] → Driver` rescue is dropped by `dispatchTargets`'
requirement that the *implementor's* file be in the caller's import scope
(`interfaces.go:340-358`) — precisely the DI case interfaces exist for. The
`94168d31` fix taught `tsBaseClasses` to walk interfaces for *field types* only,
not for member lookup or interface-satisfaction member sets. Confidence: high.

**[HIGH] astkit — `require()`, dynamic `import()` and `export … from` are not imports**
`astkit/strategies/registry.go:332-348`
```go
internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
    if n.Type() != "import_statement" { return }
```
With `--no-native`, a CommonJS file (`const a = require('./a')`, `await
import('./a')`) yields **no import edge and no call edge whatsoever**; a barrel
of `export * from` / `export { X as Y } from` yields zero import rows in *both*
runs. Expected at least an unresolved `import:./a` placeholder so import-scope
narrowing can work. Confidence: high.

**[HIGH] native — files with no symbols are never handed to the analyzer, so barrel files contribute no import edges**
`internal/native/native.go:142` (`files := filesByLanguage(symbols)`) +
`js_ts.go:229` (`!fileScope[from]`). A pure re-export barrel has zero symbols, so
it never enters `req.Files`:
```
select count(*) from edges where from_node='file:src/index.ts';  → 0
```
The identical node script run by hand resolves all three of its re-exports. A
consumer importing `{ Driver }` from the barrel therefore loses scope to
`driver.ts`, and `d.escape('q')` degrades from a pinned target to a 3-way 0.7
dispatch fan-out. `grove deps src/index.ts` returns nothing. Confidence: high.

**[MEDIUM-HIGH] astkit + native — arrow-function class fields: outgoing calls lost, or attributed to the class**
`astkit/strategies/extractors.go:425-444` (`jsFieldDef` sets no `CallSites`);
`internal/native/js_ts.go:124-134` (`currentName` knows FunctionDeclaration /
Class / Method / VariableDeclaration — not PropertyDeclaration):
```
Widget.handleClick  call_sites: []          ← body is `this.render(e)`
calls|src/arrowfields.ts::Widget|…Widget.render|0.98|native   ← from = the CLASS
calls|src/tricky.ts::Tricky|src/types.ts::Leaf.ping|0.98|native  ← `fn = (x) => x.ping()`
```
Missing entirely without native; mis-attributed to the class with it, which also
makes the class look like a caller in `impact`. Confidence: high.

**[MEDIUM-HIGH] parser — `import Default, { named } from 'm'` loses the default alias**
`internal/parser/treesitter.go:303` — `jsDefaultImportRE` requires the identifier
to be followed directly by `from`:
```
import Renamed from './mod'              → ["./mod","@js-alias:Renamed=./mod#default"]  ✓
import Renamed2, { named } from './mod'  → ["./mod"]                                    ✗
```
The mixed form is the single most common JS import shape
(`import React, { useState } from 'react'`). Confidence: high.

**[MEDIUM] edges — `buildUsesType` self-loops and wrong-kind targets (independently confirms the Java finding)**
`internal/graph/edges.go:1416` — case-insensitive `idx.byName` lookup with no
`target.Name != candidateName` check, no kind filter, and no `target.ID !=
symbol.ID` check (all three of which `resolveTypeEdges:2907-2921` has):
```
uses-type|Tricky.leaf|Tricky.leaf|0.5|type-ref   ← self-loop ("Leaf" ≈ field "leaf")
uses-type|Tricky.map |Tricky.map |0.5|type-ref   ← "Map" ≈ field "map"
uses-type|Tricky.cfg |Tricky.leaf|0.5|type-ref
```
Self-loops make a field trivially "referenced" in reachability walks. The same
failure mode was fixed for Python fields (see the comment at `edges.go:1399-1407`)
but TS/JS fields are still indexed and scanned. Confidence: high.

**[MEDIUM] astkit — object-literal methods are never extracted**
`astkit/strategies/extractors.go:503-537` — `jsArrowDecl` accepts only
`arrow_function|function|function_expression` as the declarator value. `export
const objLit = { method() {…}, arrow: () => helper() }` produces no symbols at
all. Vue options API, exported service objects and Express route tables are
invisible. Confidence: high.

**[MEDIUM] graph API — `missing-implementations` refuses inherited interface members**
```
$ grove missing-implementations 'Mid.escape'     → (empty)
$ grove missing-implementations 'Multi.escape'   → type "Multi" declares no member "escape"
$ grove missing-implementations 'Base.escape'    → Missing: [Partial2]   ✓
```
`Mid extends Base` should resolve `escape` through `extends` and report
`Partial2`. Same `extends`-blindness as the interface finding above.
Confidence: high.

**[MEDIUM] graph API — `rename-plan` is unusable for module-level functions, and reports an incomplete plan as `closed`**
```
$ grove rename-plan 'parse' parseNum
change-impact: query must be Type.method or Type.method(Params), got "parse"
```
Module-level functions are the dominant JS/TS unit. And `rename-plan
'MysqlDriver.escape' escapeIdent` edits the `Base.escape` interface declaration
but **not** `PgDriver.escape`, the other implementor of that same member, while
reporting `"Completeness": "closed"`; a real override `Sub.escape` is demoted to
`Ambiguous`. Applying it leaves the tree non-compiling. Related: TS overload
signatures are not indexed at all, so their lines would never be renamed.
Confidence: high.

**[MEDIUM] astkit — JSX component usage produces no edge**
`jsCallSites` (`metadata.go:312`) handles `call_expression`/`new_expression`
only; `jsx_element` / `jsx_self_closing_element` are neither. `<Button label="go"
onPress={fire} />` inside `App` yields no call site and no edge for `Button`.
Every React component's only real use site is invisible to the graph — `Button`
escapes `dead-code` solely via the lexical token rescue. Confidence: high.

**[LOW-MEDIUM] astkit — `jsAssignFunc` marks every `obj.prop = function(){}` as exported**
`astkit/strategies/extractors.go:361` sets `Exported: true` unconditionally,
whether the target is `exports.x`, `X.prototype.m`, or a private local object.
Exported symbols become `dead-code` roots, so this suppresses dead-code detection
across CommonJS files. Confidence: medium.

**[LOW] astkit — a call chained onto a `new` expression loses its receiver**
`new mod.PgDriver().escape('z')` → `[{"callee":"escape"},{"callee":"mod.PgDriver"}]`.
`qualifierName` maps `call_expression → function` but not `new_expression`, so
`escape` resolves only because native rescued it; nothing in the `--no-native`
run. (Same root cause as the C# `new T().M()` CRITICAL.) Confidence: high.

**[LOW] parser — `export { x }` does not set `Exported`**
`function notDefault()` + `export { notDefault };` → `exports=0`. (The suspected
`jsDefaultExportAt` false positive was checked and did **not** occur — that scan
is sound.) Confidence: high.

**[LOW] decorators — class decorators emit no edge; the decorator line is outside the symbol span**
`@Log()` on a method yields `calls|Log→SvcA.doWork|0.7|decorator`, but
`@Injectable()` on the class yields nothing. Spans: class `SvcA` 5-8 with
`@Injectable()` on line 4 — so a diff touching only the decorator maps to no
symbol. Confidence: high.

### Verified correct (JS/TS, HEAD — do not re-test)
Multi-base inheritance clauses (`interface Multi extends Mid, Other`;
`class X implements A, B`); generic bases and parameter stripping, including
`class Weird<T extends Repository<Entity>> extends Repository<Entity>`;
field-type hops through an ancestor interface's property; `this.x = new A.B.C()`;
constructor parameter properties; arrow class field as a call *target*;
`super.escape()`; static method via `Widget.make()`; getters/setters and
`#private` fields; optional chaining `this.impl?.escape()` / `?.quote?.()`;
extensionless `./util` from a `.js` file resolving to `util.ts`; `import type`;
multi-line braces; namespace import; TS enums; nested `namespace` qualified
names; abstract classes; `export default class/function`;
`missing-implementations` true positives including a member whose RawText
contains `{` (the brace heuristic did **not** misfire for TS); native overload
selection landing on the implementation; **incremental identity byte-identical**.

### Coverage gaps (JS/TS)
`GROVE_UNTRUSTED=1`, `jsconfig.json`, monorepo `paths`/project references,
`.d.ts`, `.mjs`/`.cjs`/`.jsx`, top-level `await`, `experimentalDecorators` under
native tsc, `export =`/`import x = require()`. `ComputeICR`, `DetectConflicts`,
`certify` and the MCP layer not probed for JS/TS. `native/semantic.go:170
callableKind` excluding arrow fields is code-read only.

---

## Rust — round 2 (fixture-verified)

Fixture: `fix-rustx` (Cargo.toml + 11 source files). 15 defects. **`cargo` is not
installed on this machine**, so the native pass was skipped on every run
(`rust: skipped: cargo executable not found`) — native findings are code-read
plus a standalone regex harness. The graph-edge stage is by far the worst, and
**one root cause dominates everything**.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `impl Trait<Args> for Type` never matched | **PARTIALLY FIXED — and moot** | The regexes accept `From<Feet> for Meters` now, but they run over struct `RawText` that never contains an impl block → 0 implements edges either way. The graph variant still rejects **all** `impl<T> …` |
| 2 | Rust absent from `classLanguage`/`baseClassesFor`/subclass index | **PARTIALLY FIXED** | `rust` added to `classLanguage`, `nominalInterfaceLang`, plus a new `rustBaseClasses`; trait→supertrait works (`extends Loud→Greet` ✅). Struct→trait is still unresolvable, so `dyn_call(g: &dyn Greet)` edges only to `Greet.name`, never `Dog.name` |
| 3 | `pub(crate) mod` not recognized | **FIXED** (code-read; cargo absent) | `rustModuleNames` peels `pub(...)`. The same one-line bug survives in the graph layer for `use` — see below |
| 4 | Default trait-method dispatch has no supertrait walk | **PARTIALLY FIXED** | 4-level walk added at `edges.go:1996-2010`, works for `self.x()` inside an impl. A *typed receiver* still fails entirely |
| 5 | `&'a mut Foo` missed by `rustSignatureTypePattern` | **FIXED** | `fn f(x: &'a mut Input) -> &'a Output` → `[Input Output]` |
| 6 | No `_test.go` for `rustlocaltypes.go` | **NOT FIXED** | Still absent — every other language has one |

**The Go `goReceiverTypeName` analogue does NOT exist in Rust.** `rustImplItem`
strips at the first `<` (`extractors.go:1117-1120`), so `impl<T> Wrapper<T>` →
`ParentName=Wrapper` and `impl<T: Greet> Greet for Wrapper<T>` → `Wrapper.name`.
Tuple structs and enums attribute correctly too.

### New findings

**[CRITICAL] edges + native — Rust `implements` edges can never be produced; trait families, `missing-implementations` and `dyn` dispatch all collapse**
`internal/graph/edges.go:1352-1363`, `internal/native/rust.go:184-194`
```go
if symbol.Kind != core.KindStruct && symbol.Kind != core.KindEnum { continue }
body := symbol.RawText                               // ← struct decl only
matches := rustImplForRe.FindAllStringSubmatch(body, -1)
```
Both layers detect `impl Trait for Type` by regexing the **struct/enum symbol's
`RawText`** — but astkit emits `impl` blocks as *separate* items, so no symbol's
RawText ever contains an `impl … for …` header:
```
select count(*) from symbols where raw_text like '%impl % for %';   →  0
```
With `impl Greet for Dog`, `impl Loud for Dog`, `impl Greet for Cat`,
`impl Greet for Fish {}` (empty), `impl Convert<String> for Meters`:
```
edges where edge_type in ('implements','extends','overrides'):
  extends  Loud  Greet  heuristic  type-ref          ← the ONLY hierarchy edge

$ grove change-impact 'Greet.name'
  Declarations ['Greet.name']   Supers: None   Family: None
$ grove missing-implementations 'Greet.name'
  {'Missing': None, 'ImplementedCount': 0, 'Completeness': 'closed'}
```
`Fish` implements nothing and is not reported; three real implementors are
invisible; the answer is confidently `"closed"`. The round-1 fix that widened
these regexes for `impl From<X> for Y` is therefore **dead code on the real
pipeline**.

The data needed is already present: astkit stamps each impl method with
`impl_trait:<Trait>` (`Dog.name.annotations = ["impl_trait:Greet"]`), so the edge
can be built from methods, or by emitting a symbol for the impl block.

**Why the test suite is green:** `edges_test.go:65-74` and `:77-90` both
fabricate `RawText: "struct Point { x: i32 }\nimpl Display for Point …"` — a
shape the extractor cannot produce. The tests assert against synthetic input that
does not occur in practice. Confidence: high.

**[HIGH] graph APIs — Rust unit tests are neither roots nor excluded: `dead-code` is wrong in both directions**
`internal/graph/deadcode.go:73,154,194-201` — `isTestFilePath` is path-based
(`_test.`, `/tests/`, `test_`), but Rust puts tests *inside* the production file
via `#[cfg(test)] mod tests`:
```
$ grove dead-code
dead: ['t_dead_one']     ← false positive: a #[test] entry point reported as dead production code
# dead_one absent from both lists  → false negative: kept alive by its own test
```
astkit already records the attribute (`t_dead_one.annotations = ["test"]`), so
the filter is a one-liner. Confidence: high.

**[HIGH] edges — `pub(crate) use` / `pub(super) use` silently drops every call edge for the imported name**
`internal/graph/edges.go:384` (and the twin at `:423`)
```go
imp = strings.TrimSpace(strings.TrimPrefix(imp, "pub "))   // "pub(crate) use …" survives
head = head[:i]   // → "pub(crate) use crate" → unknown crate → treated as external → cands = nil
```
Two files differing only in the visibility of the `use`:
```
use crate::a::helper;            fn z1() { helper() }  →  z1 → helper@a.rs, z1 → helper@b.rs
pub(crate) use crate::a::helper; fn z2() { helper() }  →  (no rows at all)
```
Confidence: high.

**[HIGH] rustlocaltypes — struct-literal bindings are untyped, so the most common Rust construction pattern loses its method calls**
`internal/graph/rustlocaltypes.go:22-32` covers `let x: T`, `T::new()`, `f()` and
builder chains — but not `let x = T { … }`:
```
let t = Thing { id: 1 };    t.bump()   →  NO EDGE
let t: Thing = Thing{id:2}; t.bump()   →  calls Thing.bump
let t = Thing::make();      t.bump()   →  calls Thing.bump
```
The unresolvable lowercase qualifier is then dropped entirely by
`edges.go:2012-2034`. The same mechanism kills calls through an aliased import
(`use crate::traits::{Dog as Doggo}`) — Rust has no alias encoding, unlike
Python/JS. Confidence: high.

**[HIGH] edges — an `impl` block in a different file from its type gets no `contains` edge**
`internal/graph/edges.go:1193-1202`
```go
if parent.FilePath != symbol.FilePath {
    if symbol.Language != "go" || parent.Language != "go" || path.Dir(...) != path.Dir(...) { continue }
}
```
The cross-file fallback is Go-only and directory-scoped. `struct Thing` in
`src/a.rs` with `impl Thing { pub fn bump(&self) }` in `src/b.rs` — idiomatic and
legal anywhere in the crate — leaves `Thing.bump` orphaned for `impact`,
`FileSymbols` and declaring-type traversal. Rust needs a crate-scoped fallback.
Confidence: high.

**[MEDIUM] edges — a trait default method invoked on a concrete typed receiver resolves to nothing**
`internal/graph/edges.go:1992-2011` — the supertrait/default rescue is gated on
`isSelf`; a typed receiver takes the `typed == true` path, `filterByParent(cands,
"Dog")` is empty, and nothing rescues it. `loud_default(d: &Dog) { d.shout() }`
where `Dog` impls `Loud` and `shout` has a default body → no edge.
Confidence: high.

**[MEDIUM] astkit — inline `mod x { … }` emits no module symbol and does not qualify its items**
`astkit/strategies/extractors.go:1039-1049` — with a body, `rustVisit` descends
but emits nothing for the module; only body-less `mod x;` becomes a symbol. Two
inline modules each defining `shared_name`:
```
symbols: shared_name (no parent) ×2
calls: only_here → shared_name@lib.rs:9  AND  → shared_name@lib.rs:14   ← 1 spurious
calls: top → only_here          (top → other_inline::shared_name MISSING)
```
The module-qualifier branch at `edges.go:2021-2033` understands *file* modules
only (`base == qualifier`). Confidence: high.

**[MEDIUM] astkit — `<T as Trait>::method()` loses its qualifier and fans out**
`astkit/strategies/metadata.go:637-644` (`rustPathQualifier`): for a
`scoped_identifier` whose path is `<Dog as Greet>`, `strings.IndexByte(p,'<') == 0`
→ qualifier `""` → the callee arrives bare:
```
<Dog as Greet>::name(d)  →  Cat.name, Dog.name, Greet.name
```
The most explicit dispatch form in the language becomes the least precise.
Confidence: high.

**[MEDIUM] missingimpl — `DefaultProvided` is always false for Rust traits**
`internal/graph/missingimpl.go:356-362` gates on `KindClass || KindStruct`; a
Rust seed is `KindTrait`, so the loop never fires. `Loud.shout` (which has a
default body) reports `DefaultProvided: False`. Once implements edges exist this
becomes false `Missing` rows for every implementor legitimately omitting a
defaulted method. The `Contains(RawText,"{")` heuristic itself is *sound* for
Rust. Confidence: high.

**[MEDIUM] edges — the graph-layer `rustImplForRe` cannot match any generic impl (diverges from the native regex)**
`internal/graph/edges.go:1231` — `\bimpl\s+(?:<[^>]+>\s+)?…` puts `\s+` *before*
the optional generic list, so `impl<T>` (no space) fails:
```
"impl<T> Greet for Wrapper<T> {}"   native=[Greet Wrapper]  graph=[]
"impl<'a> Greet for Ref<'a> {}"     native=[Greet Ref]      graph=[]
```
Latent today (the CRITICAL means it is never fed real text) but it will silently
halve any fix. Confidence: high.

**[MEDIUM] edges — the import path is not used to pin a Rust call target**
`use crate::a::helper;` still yields edges to both `helper@a.rs` and
`helper@b.rs` — crate-wide scope admits every file and nothing prefers the
explicitly imported one. Confidence: high.

**[LOW] Both impl regexes reject nested generics and reference receivers**
`impl<T: Into<String>> Convert<String> for Holder<T>` → `native=[] graph=[]`;
`impl Greet for &Dog` → both empty; `impl Greet for Box<Dog>` → type extracted as
`Box`. `impl<T: Bound<X>>` is idiomatic. Confidence: high.

**[LOW] astkit — `pub use` import paths keep their visibility prefix**
`astkit/strategies/registry.go:272` — `TrimPrefix(raw, "use ")` misses when the
statement starts with `pub`, yielding import nodes like
`import:pub use crate::conv::Meters`. Graph consumers defensively strip `"pub "`
but not `"pub(crate) "`. Confidence: high.

**[LOW] astkit — nested items inside function bodies, and tuple-struct fields, are never extracted**
`extractors.go:960-1052` descends only into `mod`/`trait`/`impl` bodies;
`:1076-1109` reads only `field_declaration_list`. `fn outer() { fn helper() {…} }`
yields no symbol, and `struct Pair(pub i32, pub i32)` yields no fields — so
`self.0` can never be typed. Confidence: high.

**[LOW] edges — supertrait parsing breaks on generic traits**
`internal/graph/tslocaltypes.go:119-136` (`rustBaseClasses`) takes
`strings.IndexByte(Signature, ':')` — for `trait Foo<T: Bound>: Super` the first
colon is inside the generic list, so the supertrait is lost. Confidence: medium.

### Verified correct (Rust, HEAD — do not re-test)
Generic-impl method attribution (no Go-style receiver bug); call-site extraction
for `Self::new()`, `Type::assoc()`, turbofish, closures, `?`, `async fn`,
`println!`/`assert_eq!`/custom `macro_rules!`; supertrait `extends` and
default-method dispatch *inside* a trait; `use` inside modules/functions recorded
as imports; `extern_crate`; the synthetic `crate` module; **incremental identity
byte-identical**; the same method name on an inherent impl *and* a trait impl
disambiguated by `ensureUniqueIDs` with both surviving into `change-impact`;
`#[derive(...)]` captured as an annotation. **The cross-language `dead-code`
enclosing-type bug does NOT affect Rust** — precisely because impl blocks are
separate from struct declarations, `Pair.never_used` is correctly reported.

### Coverage gaps (Rust)
**The entire native Rust pass was never executed** (no `cargo`) — `rustSemanticEdges`,
`rustModuleEdges`, the `pub(crate) mod` fix and native/graph edge-id dedupe are
code-read only. Anyone with rustup should re-run `fix-rustx` with native on; the
CRITICAL predicts `resolved 0 native implements edge(s)` there too. Not
exercised: multi-crate workspaces, `src/<mod>/mod.rs` directory modules,
`#[async_trait]`, blanket impls, associated types/consts, macro-generated items,
`Deref`-based method resolution. `rename-plan` spot-checked only.

---

## C / C++ — round 2 (fixture-verified)

Fixtures: `fix-cfam` … `fix-cfam8`, `fix-crash`. 13 defects, 4 CRITICAL. **The
parser stage is by far the worst** — two of its bugs make the pipeline unusable
for ordinary C/C++ regardless of how good the downstream graph is. The good news:
the round-1 inheritance findings really were fixed.

### Round-1 verdicts

| # | Claim | Verdict | Evidence |
|---|---|---|---|
| 1 | `baseClassesFor` had no `cpp` case → dispatch/inherited calls inert | **FIXED** | `tslocaltypes.go:310` lists `cpp`; `extends Fancy→Widget 0.85` + `byNew → render#2 0.7 dispatch`. Dead for `.h` headers (below) |
| 2 | `tsBaseClasses` understood only `extends`/`implements`, ≤1 base | **FIXED** | `cppBaseClasses` at `tslocaltypes.go:207`; `class Circle : public Shape, protected Loggable` → **two** extends edges |
| 3 | `cpp` absent from `nominalInterfaceLang` → structural false implements | **FIXED** | `interfaces.go:166` includes `cpp`; `Widget.tag` vs unrelated `Unrelated.tag` → no edge. Caveat: C++ now emits **no** `overrides` edges at all |
| 4 | Type-use used `stripQuotedText` (strings only, not comments) | **FIXED** | A type named only in a comment → zero `uses-type` edges |
| 5 | No namespace tracking — `net::Handle` vs `fs::Handle` collide | **PARTIALLY FIXED** | Symbols are distinct now and the *call* resolves correctly, but `uses-type` still fans out to both |
| 6 | Dead qualified-call index | **COULD NOT VERIFY** | `constructorTargets` is live at `edges.go:2189`; could not isolate the dead map |
| 7 | `std::shared_ptr<Foo> foo;` unmatched by local-type regexes | **PARTIALLY FIXED** | Works as a **field**; as a **local** still produces nothing — subsumed by the stack-local CRITICAL below |

### New findings

**[CRITICAL] parser — a one-line nested namespace crashes `grove index` and destroys the index for the whole repository**
`internal/parser/engine.go:536-564` (`enrichCppNamespaces`)
```go
if parent.Span.Start <= child.Span.Start && parent.Span.End >= child.Span.End {
    width := parent.Span.End - parent.Span.Start
    if width < bestWidth { bestWidth = width; scopes[i].parent = j }
}
...
scopePath = func(i int) string { ... name = scopePath(scopes[i].parent) + "::" + name ... }
```
Two namespace symbols on the **same line** have identical spans, so containment
holds in *both* directions and `width < bestWidth` never breaks the tie: each
becomes the other's parent. `scopePath` has no cycle guard and recurses forever.

Independently reproduced — one line of entirely valid C++, plus an unrelated
`.c` file in the same directory:
```
$ cat x.cpp
namespace outer { namespace inner { void deep() {} } }
$ grove index . --no-native
runtime: goroutine stack exceeds 1000000000-byte limit
fatal error: stack overflow
  parser.enrichCppNamespaces.func1  internal/parser/engine.go:554
  parser.enrichCppNamespaces.func1  internal/parser/engine.go:560   (×∞)
$ sqlite3 .grove/grove.db "select count(*) from symbols"
0
```
The multi-line form of the same code indexes correctly (3 symbols). Also
triggered by `namespace std { template<class T> class unique_ptr { … }; }` on one
line. Note the process aborts **after** creating the database, so the result is
an empty index for every file in the repo, not a partial one. Fix: require strict
containment (a parent must be strictly wider) and add a cycle guard / visited set
to `scopePath`. Confidence: high (reproduced twice, independently).

**[CRITICAL] astkit/registry — `.h` is parsed as C, so C++ headers lose all classes and inheritance**
`astkit/strategies/registry.go:361`
```go
func NewC() *cStrategy { return &cStrategy{lang: astkit.LangC, exts: []string{".c", ".h"}} }
```
The same file, once as `shapes.h` and once as `shapes.hpp`:
```
shapes.h   (language c):
  namespace|geo|…|7|7|c          class|Shape|…|9|9|c     ← regex-only, 1-line spans
  function|Loggable|…|11|11|c    ← a CLASS indexed as a function
  → no extends edges at all; Shape::area, Shape::describe, Circle::area,
    operator+=, Box::get do not exist as symbols; geo:: qualification lost

shapes.hpp (language cpp), identical text:
  class|Shape|…|9-18|cpp   method|area|Shape.area|Shape|13-13
  extends Circle → Shape [0.85]     extends Circle → Loggable [0.85]
```
`.h` for C++ is the convention in LLVM, Chromium and most of the ecosystem, so
every such project loses its entire type hierarchy. Fix: sniff `.h` content for
C++ constructs (`class`/`namespace`/`template`/`extern "C"`), or unify the
strategies. Confidence: high.

**[CRITICAL] astkit — `extractCNodes` never descends into preprocessor conditionals, so `#ifndef`-guarded headers are invisible**
`astkit/strategies/extractors.go:1190-1232` switches on `function_definition`,
`declaration`, `struct_specifier`, … and never on `preproc_if`/`preproc_ifdef`/
`preproc_else`, so anything tree-sitter nests under a conditional is unreachable.
```
g.h  (#ifndef G_H guarded):  struct Pt  → regex fallback only;  guarded_fn ABSENT
p.h  (unguarded):            struct Pt2, plain_fn  → both present
w.c  (#ifdef WIN32):         win_fn ABSENT;  tail_fn present
```
Also kills `extern "C"` blocks (`#ifdef __cplusplus / extern "C" {` → **zero
symbols**) and every branch of `#if/#elif/#else` chains. Crucially,
`blankPreprocessorBranches` (`treesitter.go:385`) is invoked **only when the parse
already has errors** (`treesitter.go:132`), so it never runs on a clean parse and
does not rescue this. Header guards are universal in C. Confidence: high.

**[CRITICAL] edges — C++ method calls on a plain stack local resolve to nothing**
`internal/graph/cfamilylocaltypes.go:77-83` — `cppLocalDeclRe` (line 20) is
applied only to **class bodies for fields** (line 57), never to the function
body, so `Widget w;` yields no local type and `edges.go:1919-1945` then *drops*
the candidate as "neither a known indexed type nor an inferable local":
```
trace: callee="w.render" qual="w" cands=1 first=[m.cpp::render]    → dropped
trace: callee="p.render" qual="p" cands=1 first=[m.cpp::render]    → dropped
resulting calls edges out of use(): 0        (expected 3)
```
Controls confirm it: `byNew` (`new Widget()`), `byParam` (`Widget* q`) and a
field receiver all get their edges — **only the stack local gets none**. Same for
`std::unique_ptr<Widget> q;` as a local and for `using WAlias = Widget;`.
`Widget w; w.render();` is the most ordinary line in C++. Confidence: high.

**[HIGH] parser — in-class declarations with `override` / `noexcept` / `final` produce no symbol, causing a `missing-implementations` false positive**
`internal/parser/engine.go:1017` (`cppMemberPattern`) allows only optional
`const` and `= 0` after `)`; `override`, `final`, `noexcept`, `= default`,
`= delete` and trailing return types all fail. astkit cannot cover for it —
`cppClassSym` (`extractors.go:1538`) looks only at `function_definition`
children, never `field_declaration`:
```cpp
class FileSink : public Sink { public: void write(int b) override; };
```
```
iface.hpp|class|FileSink|7-10        ← FileSink::write MISSING
$ grove missing-implementations 'Sink.write'
  "Missing": [ FileSink ]           ← false positive; FileSink DOES implement it
```
(`Sink.flush`, genuinely unimplemented, is correctly reported — so the API looks
trustworthy while being wrong.) `override` is the single most common modern-C++
idiom. Confidence: high.

**[HIGH] parser — inline in-class method definitions get a phantom free-function twin**
`internal/parser/engine.go:759` — the dedupe key includes `ParentSymbol`, so the
AST's `(inline_m, parent=Base)` never masks the regex extractor's
`(inline_m, parent="")`:
```
method  |inline_m|Base.inline_m|par=Base|a.hpp|5-5
function|inline_m|inline_m     |par=    |a.hpp|5-5   ← phantom
```
The phantom then shows up as a decoy candidate (`callee="w.draw" cands=2
first=[Widget.draw  draw]`), inflating `dead-code`, `rename-plan` and call
fan-out. Confidence: high.

**[HIGH] astkit/parser — out-of-line *template* member definitions become parentless free functions**
`astkit/strategies/extractors.go:1573-1576` — `cppTemplateDecl` always passes
`parentClass=""`, and the grove-side rescue `cppQualifiedCallableRe`
(`engine.go:522`) has no `<…>` alternative so `Holder<T>::put` never matches it
either:
```
template <typename T> void Holder<T>::put(T v) { … }
  → function|put|put|par=|a.hpp|21-21       (expected method|put|Holder.put|par=Holder)
```
**This is the exact C++ analogue of the Go `goReceiverTypeName` CRITICAL**, for
the out-of-line case. The in-class case is fine, so `template<typename T> class
Foo { void bar(); }` attributes correctly while `template<typename T> void
Foo<T>::bar()` does not. In one fixture the out-of-line `void Box<T>::reset()`
produced **no symbol at all**. Confidence: high.

**[HIGH] graph API — `rename-plan` omits the override family and the declaration site**
With `Widget::render` virtual, `Fancy::render() override`, and four callers:
```
$ grove rename-plan 'Widget.render' 'paint'
  m.cpp 10 | void Widget::render() {}      → paint
  m.cpp 15 | a->render();                  → paint
  m.cpp 18 | q->render();                  → paint
  m.cpp 19 | f.render();                   → paint
  m.cpp 29 | field.render();               → paint
```
Missing: line 3 `virtual void render();` (**the declaration**), line 12
`void Fancy::render()` (**the override**), line 22 `w.render();` (the stack-local
caller). `change-impact` on the same symbol gets the family right
(`family ['Fancy.render']`), so the two APIs disagree. Applying this plan yields
code that does not compile *and* silently breaks virtual dispatch.
Confidence: high.

**[MEDIUM] parser/astkit — out-of-line destructors and `operator` overloads are dropped**
`engine.go:1032` drops destructor *declarations* (`if strings.HasPrefix(name,
"~") { continue }`), and `cppQualifiedCallableRe` captures `([~A-Za-z_]\w*)` so
it cannot match `operator+=`, `operator[]`, `operator()`. `Circle&
Circle::operator+=(const Circle& o)` → no symbol; `Base& operator+(const Base&)`
and `~Base();` in a header → no symbols. Operator-heavy C++ loses its entire
operator surface from headers. Confidence: high.

**[MEDIUM] graph API — `dead-code` never reports unreferenced C++ methods**
`Widget::tag` and `Unrelated::tag` are defined and never called:
```
$ grove dead-code
dead []      expUnref ['byNew','byParam','byLocal','byField']
```
Confirms the cross-language enclosing-class-RawText mechanism for C++.
Confidence: medium-high.

**[MEDIUM] edges — `uses-type` still collides on bare class names across namespaces**
`net::Handle` and `fs::Handle`, each with its own `open`:
```
uses-type ns.cpp::open   → Handle      uses-type ns.cpp::open   → Handle#2   ← wrong ns
uses-type ns.cpp::open#2 → Handle      uses-type ns.cpp::open#2 → Handle#2   ← wrong ns
```
Symbols and `calls` are namespace-correct; only the type-use pass ignores the
qualified name. Half the `uses-type` edges here are spurious. Confidence: high.

**[MEDIUM] parser — every regex-derived C/C++ symbol has a 1-line span and 1-line RawText**
`internal/parser/engine.go:297-299` — `extractBody` has no `c`/`cpp` case, so
`extractBraceBody` is never used for them. Since the regex extractor is **always**
merged for C/C++, any symbol the AST missed gets `span_end == span_start`:
`namespace|geo|shapes.h|7|7` (actually spans 7-30), `class|Shape|9|9` (spans
9-18). Everything span-based downstream — call attribution, `FileSymbols`, ICR,
diff — is wrong for those symbols. Confidence: high.

**[LOW] native — the C/C++ native pass resolves zero call edges even with `compile_commands.json`**
```
"c-cpp: compile_commands loaded 3 command(s)",
"c-cpp: resolved 4 native include edge(s)",
"c-cpp: resolved 0 native call edge(s)",     ← never non-zero in any fixture
"c-cpp: resolved 13 native type-use edge(s)"
```
Include and type-use edges land and are high quality (0.91), and the pass skips
cleanly without `compile_commands.json`. But the native layer never rescues any
of the call edges lost above. Confidence: high that it is 0; low on whether that
is by design.

### Verified correct (C/C++, HEAD — do not re-test)
Multiple and access-specified inheritance (`: public Shape, protected Loggable` →
two edges); virtual dispatch fan-out (`0.95 ast-narrowed` + `0.7 dispatch`);
`change-impact` on a virtual method (declaration + family + three callers);
**call-site extraction quality is good** — `obj.m()`, `ptr->m()` (normalised),
`Ns::f()`, `new T()`, member-init-list calls and calls inside lambdas are all
present, while `sizeof(T)`, casts and `if (x)` are correctly **not** mistaken for
calls (the failures above are all in *resolution*, not extraction); **prototype
dedup (astkit `4abcd3e`) verified working**; two TUs each defining `static void
helper()` stay distinct with distinct callers; comment-only type mentions produce
no edge; multi-line anonymous namespaces; `#include` file graph correct in both
layers; **incremental identity byte-identical**.

### Coverage gaps (C/C++)
`native/cfamily.go`'s `+134` diff was black-box tested only (its call pass emits
nothing, so its call-side logic is untested by any fixture). The `94168d31`
engine.go `+216` diff was reviewed as code-on-disk, not as a diff. `impact`,
`Neighbors`, `Deps` direction, `ComputeICR`, `DetectConflicts` only lightly
touched. Not tested: C++20 modules, `friend` functions, nested classes, scoped
enumerators, `constexpr`, structured bindings, variadic templates, partial
specializations (the fixture for that hit the crash and was not re-run),
`co_await`, `T{}` brace-construction call sites.
`astkit/internalast/helpers.go` not reviewed.

---

# Round 1 — superseded, kept for provenance

Everything below is the original **code-read-only** review of 2026-09-10. Every
language has since had a fixture-verified round-2 pass above, which supersedes
it. Kept only so the round-2 verdict tables have something to refer to.

Calibration note, now that both rounds are done: of ~45 round-1 findings,
roughly a third were **fixed**, a third **partially fixed** (the native layer
fixed, the graph fallback missed — a recurring shape), and a third **not fixed**.
Several were materially wrong as written: the Rust trait-regex finding was
*moot* (the regex runs on text that never contains an impl block), the C#
`new T<>()` finding was *compensated* by another pass, and the `rename-plan`
interpolated-string finding was filed LOW but is CRITICAL in practice. Read-only
review found real bugs but mis-ranked them; fixtures were needed to sort them.

## Go

Files: `internal/native/go.go`, `go_importer.go`, `go_interfaces.go`,
`internal/core/goimports.go`

1. **HIGH** — `internal/native/go.go:833-844` (`goImportScope`) keys type-use
   scope via `idx.filesByBase`, which indexes files by directory *basename*
   only, repo-wide. Two unrelated packages sharing a basename (e.g.
   `pkg/foo/models` and `pkg/bar/models`) cross-contaminate `EdgeUsesType` —
   a caller importing only `pkg/foo/models` gets both packages' types in
   scope. **False positive.** The sibling call-edge resolver
   (`goImportedPackageForQualifier`, go.go:809-821) was already hardened
   against this exact bug with a regression test
   (`TestGoCallSiteEdgesPreferExactImportedPackage`); type-use edges never
   got the equivalent fix.
2. **HIGH** — `internal/native/go_interfaces.go:82-84` and `:54-56` skip any
   type/interface with `TypeParams().Len() > 0` (i.e. any generic) outright.
   No `EdgeImplements`/`EdgeOverrides`/dispatch edges are ever produced for
   generic types or generic interfaces — a total gap for a common modern-Go
   pattern (`type Task[T any] struct{...}` implementing `Runner`).
3. **HIGH** — `internal/native/go.go:582-601` (`goResolveCall`) only handles
   `*ast.Ident` and `*ast.SelectorExpr`. Explicit-type-argument generic calls
   (`Map[int, string](xs, f)`) use `*ast.IndexExpr`/`*ast.IndexListExpr` and
   fall through to `return false` — no call edge. Same gap in
   `go_interfaces.go:97-101` and `:156-160` for interface-dispatch call
   targets.
4. MEDIUM — interface satisfaction (`go_interfaces.go:37-71`) only checks
   candidate types from the exact package being analyzed against interfaces
   declared in that package or its *direct* imports — a type satisfying an
   interface structurally without importing its package never gets an edge.
   Possibly an intentional cost/coverage tradeoff.
5. LOW — `goImportedPackageForQualifier` (go.go:809-821) matches call-site
   qualifiers only against the import's default package name, not a
   source-level alias (`import myauth "..."` then `myauth.Login()`). Only
   affects the supplementary lexical fallback layer; the primary
   `go/types`-based pass resolves aliases correctly.

No concurrency bugs found in the parallel per-package worker pool — checked
specifically; `sealed` is set before the goroutine pool starts and shared
maps are read-only afterward.

---

## Python

Files: `internal/native/python.go`, `internal/graph/pylocaltypes.go`,
`internal/core/pythonimports.go`

1. **HIGH** — `internal/native/python.go:43-59` (`local_module_file`): for
   `from . import x` (`module` empty, `level=1`), `module_path` is `""`, so
   the only candidates produced are the package's own `__init__.py` — never
   `x.py`. Every alias in `from . import x, y, z` collapses to the same
   wrong target, and since `node.module` is falsy, no import edge is emitted
   at all for the statement (python.go:131-140).
2. **HIGH** — `imported_names` (python.go:131-140) is populated only inside
   the `ast.ImportFrom` branch, never for `ast.Import`. `import pkg.mod as m`
   followed by a type hint `m.Thing` resolves via the fallback as if `Thing`
   were declared in the current file — wrong-symbol resolution.
3. MEDIUM — `pyClassAttrTypes` (`internal/graph/pylocaltypes.go:590-624`)
   skips the same-directory tie-break that every sibling resolver
   (`pyResolveClass`, `pyBaseClasses`) uses. On a duplicate class name across
   files, attribute types can be inferred from the wrong file's class body.
4. MEDIUM — bare annotation without `=` (`x: Type`, no assignment) is
   handled for class bodies (`pyClassAnnRe`, pylocaltypes.go:32) but not for
   function-local vars (`pyAnnAssignRe`, :22) or `self.x` annotations
   (`pySelfAnnRe`, :30) — both require a trailing `=`.
5. LOW/MEDIUM — `pyWithRe` (pylocaltypes.go:39) is single-line-anchored;
   Python 3.10+ parenthesized multi-line `with (...)` statements are never
   matched, missing `__enter__`/`__exit__` edges.
6. LOW — MRO walk (`pyDunderTargets`, `narrowBySuper`) is BFS-by-name, not
   true C3 linearization — a same-level diamond override in `class D(B, C)`
   returns candidates from both branches instead of preferring `B`.
7. Cleanup note (not a bug) — `self_name` in python.go's
   `visit_FunctionDef` is computed and never used; suggests abandoned
   self/cls dispatch logic.

---

## Java

Files: `internal/native/java.go`, `internal/graph/javalocaltypes.go`

(Note: Java call edges are deliberately not emitted by this text-matching
layer — they come from an external `astkit` pass. Findings below concern
the edges this layer does own: extends/implements, uses-type, and the
shared hierarchy-walk helpers Java feeds into.)

1. **HIGH** — `internal/graph/tslocaltypes.go:106-116` dispatches Java into
   `tsBaseClasses`, which only follows `extends`, never `implements` — see
   Cross-cutting pattern above. Concretely: `class Impl implements Service`
   where `Service` declares a default method — `impl.run()` can never
   resolve, because the ancestor walk from `Impl` never reaches `Service`.
2. **HIGH** — `internal/native/java.go:147` — the `extends` regex captures
   only one name, but **interfaces** can extend multiple interfaces
   (`interface UserRepo extends CrudRepository<User,Long>, QueryByExampleExecutor<User>`)
   — only the first is captured; the rest silently dropped.
3. **HIGH** — `internal/native/java.go:150` — `implements`/`extends`
   name-list regex can't tolerate generic type args mid-list. For
   `implements Comparable<Foo>, Serializable`, the match stops at
   `Comparable`; `Serializable` is dropped entirely (only when the generic
   interface isn't the last in the list).
4. **HIGH** — `internal/native/java.go:192` (`javaNewPattern`) doesn't
   tolerate `<...>` before `(` — `new ArrayList<>()`, `new HashMap<String,Integer>()`,
   `new Foo<Bar>()` are all invisible to constructed-type detection (the
   diamond operator is the standard idiom since Java 7). Inconsistent with
   `javaLocalDeclRe`/`javaTypedLocalRe` in the sibling file, which do
   tolerate one level of generic nesting.
5. MEDIUM (deliberate tradeoff, not clearly a bug) — `javaBestType`
   (java.go:246-252) drops the edge entirely when a simple name resolves to
   multiple candidates outside the local file/package, to avoid
   confidently-wrong edges.

Lower-confidence / out of scope for these files: anonymous classes, lambdas,
and method references (`Foo::bar`) producing no edges — plausible, but
whether such constructs are even materialized as symbols is decided
upstream in `astkit`, not in these files.

---

## C#

Files: `internal/native/csharp.go`, `internal/graph/csharplocaltypes.go`

1. **HIGH** — `internal/native/csharp.go:217`
   (`csharpDeclInheritancePattern`): a same-line generic `where` constraint
   corrupts the base-list capture. `class Repository<T> : IRepository where T : class`
   captures `"IRepository where T "`, which never resolves — **zero**
   inheritance edges for a very common generic-repository idiom. The sibling
   function `csBaseClasses` (csharplocaltypes.go:719-722) already handles
   this correctly by truncating at `" where "` — the two "parse the base
   list" paths disagree, and the one driving Extends/Implements edges is the
   broken one.
2. **HIGH** — same pattern has no allowance for a primary-constructor
   parameter list between name/generics and the colon. `record Person(string Name) : IPerson`
   and C# 12 `class Point(int x, int y) : Shape(x, y)` fail to match at all
   — no inheritance edge for any positional record or primary-constructor
   class, the dominant modern record syntax since C# 9.
3. **HIGH** — partial classes: `csharpLocalTypes`, `csharpArgTypes`, and
   `csBaseClasses` all `break` on the first matching class fragment found by
   name — fields and base lists declared in a sibling partial-class file
   (a routine EF Core / source-generator / designer-file pattern) are
   invisible.
4. MEDIUM — `csharpNewPattern` (csharp.go:254) doesn't tolerate `<...>`
   before `(` — `new Repository<User>()` misses `EdgeUsesType` (same class
   of bug as Java finding #4).
5. LOW/MEDIUM — Extends-vs-Implements is decided by whether the first
   base-list name starts with `"I"` (csharp.go:227-230), not by resolving
   the symbol's actual kind — a base class named `ItemBase` or an interface
   breaking the `IXxx` convention gets the wrong edge type. Cheap to fix
   since the type index is already available at that point.

Checked and found sound: extension-method `this` parameter is dropped
consistently on both call-site and declaration-side arg counting; nested
qualifier scoping is consistent with Java's; base-chain walk depth caps at 4
levels by design (matches Java).

---

## Rust

Files: `internal/native/rust.go`, `internal/graph/rustlocaltypes.go`

(No `_test.go` exists for `rustlocaltypes.go` at all, unlike Go/Java/TS/
Python — findings 2 and 4 below have no regression coverage.)

1. **HIGH** — `internal/native/rust.go:227` and `internal/graph/edges.go:1209`
   (`rustImplForPattern`/`rustImplForRe`) only tolerate a generic parameter
   list *before* the trait name (`impl<T> Trait for Type`), not generic
   arguments *on* the trait. `impl From<ParseError> for MyError`,
   `impl PartialEq<&str> for Token`, `impl TryFrom<u8> for Flag` all fail to
   match — implementation edges for `From`, `TryFrom`, `PartialEq<Rhs>`,
   operator traits (`Add`/`Sub`/`Mul`/`Div`), `AsRef<T>`, `Borrow<T>` are
   missing wholesale, in both the native and graph-layer copies of the
   regex.
2. **HIGH** — Rust is entirely absent from `classLanguage`
   (`internal/graph/edges.go:2209-2211`), from the `baseClassesFor` switch,
   and from `nominalInterfaceLang`'s equivalent machinery. A local/param
   typed to a trait object (`&dyn Matcher`) or a generic bound (`M: Matcher`)
   can hit the trait's own default-method symbol via `narrowByLocalType`,
   but never expands to each concrete `impl`'s override — dynamic dispatch
   never surfaces the real implementations in the blast radius.
3. **HIGH** — `internal/native/rust.go:127-142` only strips a bare `pub `
   prefix before checking for `mod ...;`. `pub(crate) mod foo;`,
   `pub(super) mod bar;`, `pub(self) mod baz;` (common restricted-visibility
   declarations) fail the prefix check and are dropped — the module's
   file-level import edge is never emitted.
4. MEDIUM — `internal/graph/edges.go:1933-1941` default-trait-method
   dispatch (`self.is_match()` inside `impl Matcher for X`) only checks the
   one trait named on the enclosing `impl` block — no walk up to a
   supertrait (`trait Matcher: Sink`) where the actual default method lives.
5. LOW — `rustSignatureTypePattern` (rust.go:240) tolerates only a single
   bare `&`; `&'a mut Foo` (lifetime before `mut`) fails to match, dropping
   that parameter/return type from the native `EdgeUsesType` pass (a
   documented supplementary signal, not the primary call-resolution graph).

Verified correct: `Self::new()` associated-function calls resolve properly
via `rustLocalTypes`'s explicit `out["Self"] = symbol.ParentSymbol`; wrapper
type peeling (`Box`/`Rc`/`Arc`/`RefCell`/`Cell`, `&`, `dyn`, `impl`,
lifetimes) is thorough and consistent between struct/enum handling.

---

## JS / TypeScript

Files: `internal/native/js_ts.go`, `internal/graph/tslocaltypes.go`

1. **HIGH** — `internal/graph/tslocaltypes.go:66-67` (`tsBaseClasses`)
   filters candidates to `Kind == core.KindClass` only. When `className`
   names a TS **interface**, no candidate ever matches, so interface
   `extends` chains are never walked — even though `tsLocalTypes` explicitly
   says "interfaces participate too" and walks up to 4 ancestor levels for
   both. A receiver chain through an interface-only ancestor
   (`interface QueryRunner extends BaseQueryRunner`) silently fails to
   resolve past the point the ancestor's fields would be needed. Not
   covered by the existing e2e test (single-level interface, no `extends`).
2. **HIGH** — `internal/native/js_ts.go:145-159` only inspects
   `ImportDeclaration`/`ExportDeclaration`/`ImportEqualsDeclaration` at
   statement level. Plain CommonJS `const foo = require('./foo')` and
   dynamic `import('./foo')` (which can appear anywhere, not just as a
   top-level statement) never produce a file-level `EdgeImports` edge — a
   real gap since `Available()` explicitly supports plain-JS/`jsconfig.json`
   projects where CommonJS is the norm.
3. MEDIUM-HIGH — `tsTypeOwnMember` (tslocaltypes.go:519) and
   `callableKind` (semantic.go:64-66) only treat `KindMethod`/`KindFunction`/
   `KindConstructor` as call targets. Arrow-function class fields
   (`handleClick = () => {...}` — a very common JS/TS/React idiom) may be
   extracted as `KindField`, in which case calls into them via `this.x()`
   or lexical matching are never recorded. Inferred from the strict Kind
   filtering; not directly confirmed against the (external) astkit
   extractor.
4. MEDIUM — `tsBareType` (tslocaltypes.go:44) rejects any annotation
   containing a comma outright, so even if finding #1 were fixed,
   `interface Foo extends A, B` would still only ever recover the first
   base — compounds finding #1.
5. LOW-MEDIUM — `declInfo` (js_ts.go:100-110) always reports the call-site
   line from `declarations[0]`, which for TS overloads is typically an
   overload *signature*, not the implementation — likely a wrong call-site
   line, not a wrong-symbol edge.
6. LOW — `tsNewAssignRe` (tslocaltypes.go:22) only supports one namespace
   segment before the class name — `this.driver = new db.drivers.MysqlDriver()`
   isn't matched.

No solid findings on default-export handling, named/aliased imports, or
single-inheritance class `extends`/`implements` — covered by existing
passing e2e tests.

---

## PHP

Files: `internal/native/php.go`, `internal/graph/phplocaltypes.go`

1. **HIGH** — `parent::`/`static::` get no special resolution, unlike
   `self`. `internal/graph/edges.go:2222-2233` (`callerSelfQualifiers`) only
   returns `{"self", "this", "cls"}`; the ancestor-walk trigger
   (`edges.go:1969`) only recognizes `super`/`base`, never PHP's `parent`;
   and `narrowByReceiver`'s exemption list (edges.go:1862-1863) exempts
   `parent`/`static` from the "drop unresolvable receiver" rule but then
   never narrows them — `filterByParent` searches for a class literally
   named `parent`/`static`, finds none, and returns candidates
   **unfiltered**. Both `parent::foo()` (the standard override-then-call-
   parent idiom) and `static::foo()` (late static binding) can produce
   spurious cross-class call edges or miss the intended ancestor. Untested.
2. **HIGH** — `internal/native/php.go:263` (`phpTraitUsePattern`) requires
   `;` immediately after the trait identifier — `use LoggerAware, Cacheable;`
   (multi-trait) and `use A, B { A::foo insteadof B; }` (conflict-resolution
   block) never match. Any class composing more than one trait in the
   idiomatic single-statement form gets zero trait edges for all traits in
   that statement.
3. **HIGH** — `internal/native/php.go:93` (`phpUsePattern`) has the same
   shape of bug: `use Foo\Bar as Baz;` and group-use `use Foo\{Bar, Baz};`
   never match. The composer-autoload import edge is dropped, and — since
   `phpUseAliases` (php.go:240-254) builds its alias map from the same
   regex — the alias `Baz` is never registered, so later `new Baz()` either
   fails to resolve or coincidentally matches an unrelated class actually
   named `Baz` elsewhere in the index.
4. **HIGH** — `internal/native/php.go:261` (`phpExtendsPattern`) captures a
   single identifier with no comma-list support, unlike the sibling
   `implements` pattern. `interface Foo extends Bar, Baz, Qux {}` captures
   only `Bar` — same disease as Java/C# above.

Lower-confidence notes: trait-use is modeled as `EdgeImplements` rather than
a dedicated edge type (a modeling simplification, not obviously a bug);
`resolvePHPClass`/`phpBestType` don't track a file's own `namespace X;`
declaration for bare same-namespace references (degrades gracefully, not a
wrong edge); magic methods (`__call`, `__get`, etc.) are unhandled, likely
an accepted limitation of static analysis generally.

---

## C / C++

Files: `internal/native/cfamily.go`, `internal/graph/cfamilylocaltypes.go`

1. **HIGH** — `internal/graph/tslocaltypes.go:106-116` (`baseClassesFor`)
   has no `"c"`/`"cpp"` case at all — falls through to `return nil`. Yet
   `edges.go` explicitly special-cases C/C++ throughout and depends on this
   returning real bases:
   - `edges.go:1860-1885` — inherited-method call resolution for C/C++
     explicitly walks up to 4 base levels via this function; always empty,
     so a call to a base-declared method (`d->foo()` where `foo` is on
     `Base`) drops the call edge entirely if not declared on the receiver's
     static type.
   - `pylocaltypes.go:949-989` (`subclassOverrides`) builds the
     subclass/override index from the same function for every language,
     including `cpp` (via `classLanguage`) — always empty for C++, so **no
     virtual-dispatch fan-out edges are ever produced for C++** despite
     being nominally supported.
   - `edges.go:2419-2455` (`constructorTargets`) — instantiating a class
     that only inherits its constructor from a base produces no call edge.
   - `localtypes.go:404-417` — a receiver typed as a derived class calling
     a base-declared method resolves to nothing.
2. **HIGH** (root cause of #1) — even if wired in, `tsBaseClasses` only
   understands `extends`/`implements` syntax — never C++'s `: public Base`
   clause — and returns at most one base, so multiple inheritance
   (`class D : public A, public B {}`) would still only ever surface one
   parent. No C++ inheritance test exists in the test suite at all.
3. MEDIUM-HIGH — C++ is absent from `nominalInterfaceLang`
   (`internal/graph/interfaces.go:164-170`), so C++ classes fall into
   *structural* (Go-style duck-typed) interface matching — two unrelated
   classes that happen to both define `void close()` can get a spurious
   implements/override edge synthesized from name matching alone, while
   genuine polymorphic overrides (blocked by findings #1/#2) go unrecorded.
4. MEDIUM — `cFamilyContainsType`/`containsTypeToken`
   (`internal/native/semantic.go:138-179`) runs its regex over
   `stripQuotedText`, which strips only quoted literals — **not**
   `//`/`/* */` comments. A type name mentioned only in a comment
   (`// old code used Foo here`) produces a spurious `EdgeUsesType`. The
   sibling local-types pass (`cfamilylocaltypes.go:73`) already uses the
   comment-stripping variant (`stripCommentsAndStrings`) — one file over.
5. MEDIUM — no namespace tracking anywhere in the C-family symbol model;
   `typesByName`/`methods`/`functions` are keyed by bare name only. Two
   classes with the same name in different namespaces (`net::Handle` vs
   `fs::Handle`) can collide via `cFamilyBestType`'s file/directory
   tie-break or an arbitrary `candidates[0]` pick. `using namespace`
   aliasing is entirely unhandled.
6. LOW — `cFamilyIndex.methods`/`.functions`/`.ctors`/`.methodsByCls` and
   helpers `cFamilyContainsCallable`/`cFamilyQualifiedCalls` are defined but
   never referenced anywhere (dead code) — evidence a qualified-call
   resolution path was built and abandoned.
7. LOW — `cppLocalDeclRe`/`cFamilyParamTypes` regexes don't match
   template-typed declarations (`std::shared_ptr<Foo> foo;`) — the `<`
   character breaks the type-name match, so the field/parameter gets no
   inferred type. Expected limitation of a regex-based approach, but
   smart pointers are pervasive in modern C++.

---

## Suggested priority order — ROUND 2 (verified; supersedes the round-1 list below)

> **Mostly done — see § Round 3 at the top of this file.** Codex's fix round
> closed every Tier-0 and Tier-1 item plus most of Tiers 2–4, all re-verified
> against fixtures. What is still open after that round is the seven-item list
> in § Round 3 ("Still open after the fix round"); the two that matter are
> `dead-code` under a namespace symbol, and C++ `struct` members. Read this
> list for the reproductions and rationale, not as an open work queue.

Ordered by blast radius: things that destroy or falsify an answer first, then
things that silently lose edges. Every item below has a reproducing fixture.

### Tier 0 — data loss / crash

1. **C++ one-line nested namespace aborts `grove index` and leaves an empty
   database for the entire repo.** `internal/parser/engine.go:544-550`: equal
   spans satisfy containment in both directions, `width < bestWidth` never
   breaks the tie, so two namespaces become each other's parent and `scopePath`
   (no cycle guard) recurses to stack overflow. Reproduced twice, independently.
   Fix: require *strict* containment, and add a visited-set guard to `scopePath`.
   One line of valid C++ (`namespace a { namespace b { void f() {} } }`) is
   enough. This is the only finding in the review that loses data rather than
   degrading an answer.

### Tier 1 — APIs that emit confidently wrong answers

2. **`rename-plan` drops call sites inside interpolated strings / template
   literals / f-strings** (`renameplan.go:109`) — reproduced in TS, C# and
   Python. The `Edits` list alone is build-breaking and contradicts a 0.98 edge
   in the same graph. C++ additionally omits the declaration *and* the override
   family; Java rewrites a same-named method in an unrelated package with
   `Ambiguous: null`; JS/TS refuses module-level functions outright. Anything
   that applies `rename-plan` output unattended will break a build.
3. **`dead-code`** — 100% false negatives on private methods in every
   class-based language (the enclosing class's `RawText` re-mentions every
   method; only Go and Rust escape, both by accident of layout), *plus* false
   positives: a TS module-level entry point and a Rust `#[test]` function are
   both reported dead. Deleting either breaks the program.
4. **Fan-out zeroing runs before receiver-type narrowing** (`edges.go:1784`) —
   a call on an explicitly declared receiver drops to **zero edges** once 17
   same-named methods exist. Silent, repo-size-dependent recall cliff on
   `Save`/`Get`/`Handle`/`Dispose`-class names.
5. **Go bare calls bind to same-named methods at 0.95 `ast-narrowed`**, which
   passes `PolicyCertification` — so `grove certify` admits fabricated callers.
6. **`Impact` never reaches implementations** — `EdgeOverrides` is absent from
   the traversal set (`graph.go:604`), and no method-level `overrides` edge is
   emitted for nominal languages at all. `impact` and `change-impact` disagree
   about the same question, and MCP exposes only the wrong one.

### Tier 2 — whole features that are inert

7. **Rust emits zero `implements` edges, ever.** Both layers regex the
   *struct's* `RawText` for `impl … for …`, but astkit emits impl blocks as
   separate symbols, so the pattern can never match (`… where raw_text like
   '%impl % for %'` → 0). Trait families, `missing-implementations` and `dyn`
   dispatch all collapse; `missing-implementations` answers
   `ImplementedCount: 0, Completeness: "closed"` for a trait with three
   implementors. **The two Rust tests in `edges_test.go` fabricate a `RawText`
   shape the extractor cannot produce, which is why this is green.** The data
   needed is already there: methods carry `impl_trait:<Trait>` annotations.
8. **PHP has no graph-layer `extends`/`implements`** (`resolveTypeEdges` has no
   `case "php"`), so any tree without `composer.json` gets zero inheritance
   edges and silently wrong "closed" answers. One missing switch case.
9. **C++ `.h` files are parsed as C** — LLVM/Chromium/most of the ecosystem —
   so classes, methods and inheritance vanish. And **`extractCNodes` never
   descends into `preproc_if*`**, so every symbol inside a `#ifndef` header
   guard or an `extern "C"` block is invisible. (`blankPreprocessorBranches`
   runs only on an already-failed parse, so it does not rescue this.)
10. **Python import scope resolves by module basename repo-wide**
    (`importedFiles` has no Python branch, though its sibling
    `computeImportFilesForQualifierForSymbol` does). `from .models import
    Record` pulls in every `models.py` in the repo, and `rename-plan` then edits
    unrelated packages while reporting `Completeness: "closed"`.

### Tier 3 — extraction gaps that silence whole idioms

11. **Go: methods on generic types get an empty `ParentName`**
    (`goReceiverTypeName` has no `generic_type` case) → indexed as free
    functions; `change-impact`/`rename-plan` fail outright on them.
    **C++ has the same bug for out-of-line template members**
    (`Holder<T>::put` → parentless free function).
12. **Java `@Autowired private Repo repo;`** — a same-line annotation makes
    `javaFieldRe` miss the field, silencing the entire injected-dependency call
    graph for Spring/JPA/Mockito. One regex alternation.
13. **C++ `Widget w; w.render();`** — `cppLocalDeclRe` is never applied to
    function bodies, so the most ordinary line in C++ yields no call edge.
14. **Whole constructs missing from astkit**: Java enum members and record
    components; C# fields, property accessors and top-level `Program.cs`; JS/TS
    object-literal methods and JSX component usage; Java/JS method references
    (`Foo::bar`); Rust inline `mod x { … }`.
15. **`new T().m()` loses its receiver** in C# and JS/TS — in C# this produces a
    *wrong* edge to an unrelated same-file class, not just a missing one.

### Tier 4 — precision

16. **`buildUsesType` needs the `Kind`/exact-case/self-edge filters that
    `resolveTypeEdges` already has** (`edges.go:1416` vs `:2907-2921`).
    Independently reproduced in C#, Java and TS/JS: fields point at themselves,
    methods are used as "types", namespace symbols collect an edge per file.
    Both `Impact` and `dead-code` traverse `uses-type`. Small fix, in-repo model.
17. **Fix the three regressions** at the top of this file — Java chained calls
    (`39582618`), and the two Python ones (`94168d31`).
18. **`missing-implementations` brace test** — expression-bodied members
    (`=> x`) read as body-less; C# interface defaults and Rust traits can never
    set `DefaultProvided` at all.

### Process recommendations

- **A/B every fix commit against its parent binary.** All three regressions were
  invisible to `go test ./...` and took a fixture diff to find.
- **Audit tests that construct `SymbolRecord` literals by hand.** The Rust
  `implements` bug survived because its tests fabricate a `RawText` shape the
  extractor never produces. Prefer fixtures that run the real extractor.
- **`rustlocaltypes.go` still has no `_test.go`** — the only language resolver
  without one.
- Several fixes landed in the native layer but not the graph fallback
  (C# base lists, PHP aliases, Rust regexes). Any fix to a `native/<lang>.go`
  resolver should ask whether `edges.go` needs the same change, since
  `--no-native` and build-file-less projects take the other path.

## Suggested priority order — round 1 (historical, code-read only)

1. Fix `tsBaseClasses`/`baseClassesFor`: support `implements`, multi-base
   lists, and add a `cpp` case — the single biggest lever, touching Java,
   JS/TS, PHP, and unblocking C++ entirely.
2. Wire C++ into that fixed walker and into `nominalInterfaceLang` — closes
   the largest false-positive/false-negative pair in the review.
3. Go's package-basename collision (type-use false positives) and
   generic-type exclusion from interface dispatch — both are silent
   correctness bugs in the language the toolchain's 0.99 accuracy baseline
   is measured against.
4. Rust's generic-trait-impl regex and Rust's total exclusion from
   `classLanguage`/dispatch — closes a large share of idiomatic Rust
   (`From`, `TryFrom`, trait objects).
5. Remaining per-language regex gaps (PHP `parent::`/`static::`, C#
   generic-`where`/primary-constructors, Python relative imports) — each
   narrower but individually high-confidence and easy to fix in isolation.
