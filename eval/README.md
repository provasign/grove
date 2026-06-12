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

## Baseline (2026-06-12, calls edges, Go)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| prism (`ef4eea1`) | 100% | 0.9530 | 0.9935 | 0.9728 |
| gin (`d75fcd4`) | 99.7% | 0.7282 | 0.8571 | 0.7874 |

The gap between the two repos is the finding: gin's interface-heavy render/
binding packages expose the two known weaknesses of name-based resolution —
same-name methods on different types fan out into false positives, and
interface-method calls don't reach concrete implementations (missing
`overrides`-style resolution). Those are the next two accuracy investments,
and this harness is how we'll know they worked.

## Roadmap

- Python oracle (pyright or PyCG) over django/flask/requests corpus pins
- TS/JS oracle (TypeScript compiler API) over express/socket.io pins
- `tests` edge scoring against runtime coverage
  (`go test -coverprofile`, `coverage.py` dynamic contexts, nyc)
- regression gate in CI against the last accepted baseline
