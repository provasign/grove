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
| requests (`6f66281a`) | 100% | 0.7653 | 0.5971 | 0.6708 |
| flask (`36e4a824`) | 97.9% | 0.6693 | 0.3520 | 0.4614 |

*lower bound — see the partial-oracle caveat above.

The recall gap decomposes into three buckets (flask FN sample):

1. **Property access** — `request.blueprints` executes `@property` code with
   no call syntax, so no CallSite exists. Needs attribute-access modeling.
2. **Decorator wrappers** — `@setupmethod`-style wrappers call the wrapped
   function; Grove sees the decoration (in `Annotations`) but emits no
   wrapper→wrapped edge yet.
3. **Registry dispatch** — `dispatch_request` → view functions through
   Flask's routing table. Fundamentally dynamic; a fair ceiling for static
   structure.

Class instantiation (`Flask(...)` → `Flask.__init__`, ~7% of flask's truth
edges) is already handled: class-named calls route to the constructor.

## Roadmap

- decorator wrapper→wrapped call edges (flask bucket 2; `Annotations` has
  the data already)
- property-access edges (flask bucket 1; needs astkit attribute sites)
- TS/JS oracle (TypeScript compiler API) over express/socket.io pins
- `tests` edge scoring against runtime coverage — `gen_truth.py` is the
  natural base: it already traces test→function execution
- django pin once flask recall improves (same patterns, 100× the surface)
