# grove-eval — edge accuracy harness

Scores Grove's graph edges against ground truth from typed language
toolchains. The point: every resolution change to Grove gets a number, so
"did this help?" stops being a matter of opinion.

This is a nested Go module so `golang.org/x/tools` stays out of Grove's
runtime dependency set. It imports Grove's internals via the shared
`github.com/provasign/grove/` path prefix.

## Design

- **Oracle**: for Go, the typed SSA callgraph (`cha` seeded, `vta` refined)
  from `golang.org/x/tools`. Only named, non-synthetic, in-repo declarations
  participate; vendor and anonymous functions are excluded.
- **Identity**: declarations are matched between the oracle and Grove by
  repo-relative file + declaration line within the Grove symbol's span +
  base-name agreement. Edge comparison is restricted to this **matched
  universe**, so symbol-extraction differences don't pollute edge accuracy.
- **Self-edges** (direct recursion) are excluded on both sides; they carry
  no blast-radius information.
- **Ground truth is immutable per pinned commit.** Generate once for a
  corpus pin, store it compressed in `provasign/test-fixtures`, and every
  subsequent scoring run is cheap: index + compare.

The oracle is itself an approximation (VTA over-approximates dynamic
dispatch), so treat scores as a consistent yardstick, not absolute truth.
What matters is the trend per pinned commit.

## Usage

```sh
cd eval && go build -o grove-eval ./cmd/grove-eval

# one-shot: generate truth + score
./grove-eval run --repo /path/to/repo --commit <sha> --out-dir out/

# or separately, reusing stored truth
./grove-eval truth --repo /path/to/repo --commit <sha> --out truth.jsonl
./grove-eval score --repo /path/to/repo --truth truth.jsonl --out-dir out/
```

Outputs `scorecard.json` and `scorecard.md` with precision/recall/F1 plus
capped false-positive/false-negative examples for debugging.

## Baseline progression (calls edges, Go)

| Date | Change | gin P | gin R | gin F1 | prism F1 |
|---|---|---|---|---|---|
| 2026-06-12 | initial measurement | 0.7282 | 0.8571 | 0.7874 | 0.9728 |
| 2026-06-12 | receiver-aware narrowing + closure-fair oracle | 0.7632 | 0.8657 | 0.8112 | 0.9876 |
| 2026-06-12 | exact-case CallSite resolution | 0.8522 | 0.8657 | 0.8589 | 0.9907 |
| 2026-06-12 | interface satisfaction → overrides edges + dispatch rescue | 0.8576 | 0.9258 | 0.8904 | 0.9907 |
| 2026-06-12 | astkit v0.4.2 call-site qualifiers + import-qualified narrowing | 0.9034 | 0.9258 | 0.9145 | 0.9969 |
| 2026-06-12 | local type inference (params, declarations, fields) | 0.9259 | 0.9488 | 0.9372 | 0.9984 |

`eval/baseline.json` records the accepted floor; CI
(`.github/workflows/eval.yml`) regenerates gin's ground truth at the corpus
pin and fails any change that drops precision or recall below it.

Day-one findings, all surfaced by the false-positive/negative examples:

1. **Self-receiver fan-out** — `r.WriteContentType(w)` inside `JSON.Render`
   matched every type's `WriteContentType` in the file. Fixed: calls through
   the caller's own receiver (Go receiver var, `self`, `this`) resolve only
   to methods on the caller's `ParentSymbol`.
2. **Case-insensitive AST resolution** — the free function
   `writeContentType` claimed every `WriteContentType` method. Fixed:
   AST-extracted call sites resolve case-exactly (they're exact by
   construction).
3. **Closure attribution** — Grove attributes calls inside closures to the
   enclosing declaration (right for blast radius); the oracle now mirrors
   that instead of dropping anonymous functions.

4. **Capped fan-out hid dynamic dispatch** — `maxCalleeFanout` silently
   dropped gin's ~18 `Render` implementations. Fixed: Grove now derives Go
   interface satisfaction by method-set inclusion (zero → 89 `implements` +
   177 `overrides` edges on gin), and a capped call site whose method an
   in-scope interface declares is rescued as dispatch edges at 0.7
   confidence.
5. **astkit discarded receiver qualifiers** — every call site arrived as a
   bare name ("WriteContentType", not "r.WriteContentType"), so receiver
   narrowing couldn't fire. Fixed in astkit v0.4.2 across all five
   languages; Grove additionally narrows package-qualified calls by import
   (an external import drops candidates; an in-repo import restricts to its
   files, case-exact so a `Session` field isn't confused with an
   `internal/session` package).

6. **Unknown-typed locals** — `ip.String()` matched same-file methods.
   Fixed with shallow local type inference (signature params, var/:=/
   composite-literal declarations, New<Type> constructors resolved against
   indexed types, receiver struct fields): a known type keeps only its own
   methods, an interface type dispatches to implementors (unscoped — DI
   implementations live where the consumer never imports), and a known type
   with no matching candidate drops the edge.

The residual gin gap is mostly oracle-side flow precision (VTA proves which
implementations actually reach a dispatch site; structure alone cannot) —
acceptable territory for a blast-radius tool that says "may affect".

## Python (dynamic oracle)

`pytruth/gen_truth.py` runs a repo's own pytest suite under `sys.setprofile`
and records every executed caller→callee pair between non-test, in-repo
functions (closures attributed to their enclosing def; decorator line
numbers normalized to the `def` line). The oracle is **exact but partial**:
every asserted edge really executed, but untested paths are absent — so
Grove's recall against it is meaningful while precision is a lower bound
(a correct static edge on an untested path scores as a false positive).

```sh
/path/to/venv/bin/python eval/pytruth/gen_truth.py \
  --repo /path/to/repo --commit <sha> --out truth.jsonl
./grove-eval score --repo /path/to/repo --truth truth.jsonl --out-dir out/
```

### Baseline (2026-06-12, calls edges, Python)

| Repo | Universe match | Precision* | Recall | F1 |
|---|---|---|---|---|
| requests (`6f66281a`) | 100% | 0.7887 | 0.6154 | 0.6914 |
| flask (`36e4a824`) | 97.9% | 0.8230 | 0.6066 | 0.6984 |

flask same-day progression: F1 0.4614 → 0.5831 (decorator edges) → 0.6682
(property-read edges) → 0.6984 (annotation-driven local types, super()/cls()
resolution, inherited members through the ancestor chain).

**Python's static ceiling — measured.** The remaining recall gap is
dynamic dispatch a static graph cannot see: registry dispatch
(dispatch_request → view functions), dunder protocols (`g.x` →
`__setattr__`, `with x:` → `__enter__`, descriptors' `__get__`),
werkzeug LocalProxy indirection, and `getattr(module, name)()`. These are
exactly the edges only the dynamic oracle records. Treat ~0.60–0.65 recall
against a dynamic oracle as the honest static bound for idiomatic
framework Python; precision is the lever that still moves (0.66 → 0.82
today via typed narrowing). Tests-edge floors trade the same way:
annotation narrowing took flask edge precision 0.567 → 0.681 while
function hit rate eased 0.336 → 0.315 — fewer, truer suggestions, the
right direction for a review signal.

*lower bound — see the partial-oracle caveat above.

The recall gap decomposes into three buckets (flask FN sample):

1. **Property access** — `request.blueprints` executes `@property` code with
   no call syntax. SOLVED: astkit v0.4.3 emits attribute-access sites
   (`AttrSites`); Grove resolves them against property-annotated methods
   only, so plain field reads never produce edges.
2. **Decorator wrappers** — `@setupmethod`-style wrappers call the wrapped
   function. SOLVED: wrapper→wrapped and caller→wrapper calls edges when
   the decorator resolves to one in-repo function.
3. **Registry dispatch** — `dispatch_request` → view functions through
   Flask's routing table. Fundamentally dynamic; the remaining fair ceiling
   for static structure.

Class instantiation (`Flask(...)` → `Flask.__init__`, ~7% of flask's truth
edges) is already handled: class-named calls route to the constructor.

## TS / JS (compiler-API oracle)

`tstruth/gen_truth.mjs` (node + the `typescript` package) resolves every
call/new expression through the TypeScript checker — `checkJs` covers plain
JS. Overload signatures normalize to their implementation; module-scope
function values take their binding's name (`const f =`, `app.listen =
function()`, `exports.render =`); nested function values are closures and
attribute to the enclosing declaration.

### Baseline (2026-06-12, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| socket.io (`3ad4e1f2`, TS monorepo) | 98.7% | 0.8407 | 0.9406 | 0.8878 |
| express (`dae209ae`, CommonJS) | 90.3% | 0.7500 | 0.7143 | 0.7317 |

The TS ceiling round (same playbook as Python) added: typed-class-field
local types (field symbols already carry "public transport: Transport"
signatures; plain `this.x = param` ctor assignments inherit the param's
annotation), super()/super.method() resolution through extends (astkit
v0.4.6 emits the call sites), inherited members across import scope, and
inherited constructors (`new Server()` walking to the base class ctor —
which also lifted flask). Remaining socket.io gap: twin classes across
monorepo packages (engine.io and engine.io-client both define Transport)
and callback-driven flows — the structural ceiling for a name-based graph.

Day-one findings, fixed same day:

1. **Abstract classes were invisible** — astkit handled only
   `class_declaration`; `abstract_class_declaration` (and abstract method
   signatures, the dispatch points overrides implement) produced no symbols
   at all. Fixed in astkit v0.4.4 (socket.io universe 79% → 98.7%).
2. **CommonJS assignment declarations were invisible** — `app.listen =
   function(){}` / `exports.render =` / `X.prototype.method =` produced no
   symbols (express: 2 of ~30 functions in application.js). Fixed in astkit
   v0.4.5.
3. **`constructor` matched every constructor** — TS constructors' raw text
   starts with `constructor(`, and the fallback path resolved it as a
   callee. `constructor`/`super` are invocation forms, not names; skipped.
4. **Relative imports resolved by basename** — `./socket` pulled every
   socket.ts in the monorepo into scope. Relative imports now resolve
   exactly against the importing file's directory (with index-file
   convention) before any fuzzy matching (socket.io P 0.63 → 0.82).

## Tests edges (runtime coverage oracle)

`gen_truth.py --tests-out` also records which in-repo functions each test
actually executed (transitively). `grove-eval score-tests` compares Grove's
`tests` edges against that. Here precision is fully meaningful — the oracle
saw everything each test touched, so a Grove tests-edge to an untouched
function is a real false signal. The headline metric is the **function hit
rate**: for what share of genuinely covered functions does Grove suggest at
least one truly-covering test. That's the number RFC #5's "related tests"
signal lives or dies by.

Grove's tests edges are built from the fully-narrowed call graph: direct
calls from the test (0.85), through same-test-file helpers/fixtures
(0.75–0.6), and one hop past the entry point into production code (0.55) —
confidence tiers let consumers trade precision for reach.

### Baseline (2026-06-12, tests edges, Python)

| Repo | Edge precision | Function hit rate |
|---|---|---|
| requests | 0.8176 | 0.5060 (85/168) |
| flask | 0.5669 | 0.3356 (99/295) |

Before this round Grove's tests edges were effectively broken for
qualified call sites (26 in-universe edges on flask, 6% hit rate): the
call-site evidence path skipped every dotted callee, which astkit v0.4.2's
qualifiers had made nearly all of them.

## Impact (blast radius) accuracy

`grove-eval score-impact` measures reverse reachability: for every truth
function as a seed, the set of callers within N hops over Grove's calls
edges vs the oracle call graph (per-seed precision/recall, averaged).
`--sweep` tables a path-confidence pruning threshold (the product of edge
confidences along the path must stay above it).

### gin, 2026-06-12

| Depth | Pruning | Mean P | Mean R | Mean F1 | Mean radius (grove/truth) |
|---|---|---|---|---|---|
| 2 | none | 0.9213 | 0.9361 | 0.8784 | 7.1 / 6.7 |
| 2 | ≥0.7 path conf | 0.9249 | 0.7396 | 0.7127 | 4.1 / 6.7 |
| 3 | none | 0.8895 | 0.9263 | 0.8522 | 11.3 / 10.3 |

**Measured decision: do NOT confidence-prune Impact traversal.** The sweep
is flat up to 0.6 (today's resolved edges sit at 0.85–0.95, so products
rarely dip), and beyond 0.7 pruning trades ~20 points of recall for ~0.4
points of precision — the 0.7-confidence dispatch edges carry real impact
paths, and two strong hops (0.85²=0.72) already fall under a 0.75 cut.
Blast radius accuracy is a consequence of edge accuracy, not a separate
knob. This section exists so nobody "optimizes" this without re-running
the sweep.

## Roadmap

- raise the flask tests-edge hit rate: werkzeug test-client indirection
  (`client.get("/")` → WSGI → view) is the dominant unreachable bucket
- impact baseline gate (score-impact is in place; add floors once more
  corpus repos are measured)
- Go tests-edge truth (`go test -coverprofile` per package)
- django pin once flask recall improves (same patterns, 100× the surface)
- tests-edge baseline + CI gate once the metric stabilizes
