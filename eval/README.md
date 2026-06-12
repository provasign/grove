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

What remains is the hard tier: calls through unknown-typed local variables
(`c.Writer.Header().Set` claiming `Context.Set`). That needs lightweight
local type inference — the next accuracy investment, and this harness is
how we'll know it worked.

## Roadmap

- Python oracle (pyright or PyCG) over django/flask/requests corpus pins
- TS/JS oracle (TypeScript compiler API) over express/socket.io pins
- `tests` edge scoring against runtime coverage
  (`go test -coverprofile`, `coverage.py` dynamic contexts, nyc)
- regression gate in CI against the last accepted baseline
