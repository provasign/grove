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
capped false-positive/false-negative examples for debugging
(`GROVE_EVAL_MAX_EXAMPLES=100000` lists every one; `GROVE_TRACE_CALLS=1`
traces candidate narrowing per call site to stderr).

## Current scores (2026-09-19, calls edges)

| Corpus (pin) | Language | Oracle (`--lang`) | Universe | P | R | F1 |
|---|---|---|---|---|---|---|
| gin (`d75fcd4`) | Go | SSA + VTA (default) | 99.7% | 0.9522 | 0.9505 | 0.9513 |
| commons-lang (`44298fe`) | Java | javac + javap (`java`) | 97.0% | 0.9381 | 0.9517 | 0.9449 |
| newtonsoft (`0a2e291`) | C# | Roslyn (`csharp`) | 99.7% | 0.9346 | 0.9470 | 0.9408 |
| socket.io (`3ad4e1f2`) | TypeScript | tsc checker (`tstruth/gen_truth.mjs`) | 98.3% | 0.9029 | 0.9917 | 0.9452 |
| express (`dae209ae`) | JavaScript | tsc `checkJs` (`tstruth/gen_truth.mjs`) | 90.3% | 0.8400 | 1.0000 | 0.9130 |
| ripgrep (`82313cf`) | Rust | rust-analyzer SCIP (`rust`) | 100% | 0.9364 | 0.9045 | 0.9202 |
| jansson (`684e18c`) | C | clang AST (`clang`) | 97.7% | 0.9991 | 0.9247 | 0.9605 |
| SwiftyJSON (`3d25441`) | Swift | SourceKit index (`swift`) | 100% | 0.9355 | 1.0000 | 0.9667 |
| turtle (`3cfc963`) | Kotlin | kotlinc + javap (`kotlin`) | 76.8% | 1.0000 | 0.9881 | 0.9940 |
| json-framework (`93e4ca5`) | Objective-C | clang AST (`objc`) | 98.7% | 1.0000 | 0.9914 | 0.9957 |
| flask (`36e4a824`) | Python | pytest trace (`pytruth`, dynamic) | 97.9% | 0.8522 | 0.7164 | 0.7784 |
| php-parser (`8eea230`) | PHP | Xdebug trace (`php`, dynamic) | 100% | 0.9176 | 0.6471 | 0.7590 |

Every row is gated in `baseline.json`; the per-language sections below hold
the progression that produced each number and what remains.

### Second corpus per language (2026-09-19)

Every rule above was tuned against one repository per language. A second
pin per language is the overfitting check: the same binary, a repository
the rules never saw. All are gated in `baseline.json`; the snapshot files
sit beside the first corpus's under `testdata/`.

| Corpus (pin) | Language | Oracle | Universe | P | R | First corpus P / R |
|---|---|---|---|---|---|---|
| cobra (`adbc881`) | Go | SSA + VTA | 100% | 0.9828 | 0.9518 | 0.9522 / 0.9505 |
| commons-io (`8ad9867d`) | Java | javac + javap | 94.5% | 0.8756 | 0.9128 | 0.9381 / 0.9517 |
| cJSON (`6d9f244`) | C | clang AST | 100% | 1.0000 | 0.6462 | 0.9991 / 0.9247 |
| fd (`5bbfa3e`) | Rust | rust-analyzer SCIP | 100% | 0.9516 | 0.9130 | 0.9364 / 0.9045 |
| p-queue (`180ab9e`) | TypeScript | tsc checker | 100% | 0.9592 | 1.0000 | 0.9029 / 0.9917 |
| Files (`e85f2b4`) | Swift | SourceKit index | 97.0% | 0.9881 | 0.9222 | 0.9355 / 1.0000 |
| CocoaLumberjack (`f54de25f`) | Objective-C | clang AST | 79.2% | 0.9557 | 0.7989 | 1.0000 / 0.9914 |
| csv (`89ac08c`) | PHP | Xdebug trace (dynamic) | 100% | 0.8892 | 0.7016 | 0.9176 / 0.6471 |

What the second corpora found, all fixed the same day and re-gated on both
corpora of each language:

- **The Java oracle dropped every method with a `throws` clause.**
  `javapMethodRe` required the header line to end in `);`; a throws clause
  ends it in `IOException;`, the header went unrecognized, and that method's
  invokes were attributed to the previous method. commons-io declares throws
  on most of its API, so its truth had 1,687 edges and Grove's recall read
  0.649 — against the corrected oracle the truth has 2,707 edges and the same
  build scores R 0.913. The **commons-lang snapshot was re-pinned** with the
  corrected oracle too: R 0.9202 → 0.9517 on an unchanged Grove build (its
  gate moves up accordingly).
- **Implicit super-constructor calls** (Java, C#): a constructor with no
  `super(...)`/`this(...)` calls the superclass's parameterless constructor,
  which javac emits and the oracle records — 89 of commons-io's misses. The
  base class is resolved from the constructor's own file first, because
  nested helper types (`Builder`, `AbstractBuilder`) reuse simple names
  across a package.
- **Cargo integration tests are crates** (Rust): files under `tests/`,
  `benches/` and `examples/` had no crate root, so `TestEnv::new` from
  `tests/tests.rs` scoped to its own file — 93 of fd's 138 misses.
- **Swift**: a `typealias` constructs the aliased type (`throw
  LocationError(..)` → `FilesError.init`); properties of the enclosing type
  or protocol (`var storage: Storage<Self> { get }`) type receivers, generic
  arguments dropped; an in-repo extension of an external type
  (`extension String`) is reachable from a `String` receiver. Files.swift
  R 0.533 → 0.922.
- **Objective-C oracle**: SwiftPM/CocoaPods lay headers out as
  `include/<Module>/X.h` and sources write `#import <Module/X.h>`; the
  include-path heuristic now adds the parent of each header directory.
  CocoaLumberjack parsed 17 functions before, 321 after.

What they left open:

- **C preprocessor** (cJSON R 0.646): the Unity test framework is entirely
  macros (`RUN_TEST(f)`, `TEST_ASSERT_*`) expanding to calls; Grove has no
  symbol for a function-like macro. jansson's `json_object_foreach` is the
  same gap. The fix is macro symbols plus call-through of a macro body's
  calls to the invoking function — the next C item.
- **Java overload fan-out** (commons-io P 0.876): `IOUtils.write`/`toString`/
  `close` families with untyped arguments; the same territory as newtonsoft's
  constructor overloads.
- **Objective-C property reads** (CocoaLumberjack R 0.80, universe 79%):
  `info.fileName` lowers to a getter message the oracle records; Grove
  records no call for a property read, and category/extension methods
  declared in `.m` class extensions are outside the matched universe.
- **JavaScript object-literal modules**: koa's `module.exports = { get
  foo() {..}, bar() {..} }` produced an 18% universe (no symbols for the
  members, and the checker names accessors `get`/`set`); not pinned until
  the extractor models them.
- No second corpus yet for C# (needs `dotnet` for the Roslyn oracle) or
  Kotlin (a kotlinc-2.4-compilable dependency-free repository is rare).

Two rows are dynamic oracles (flask's pytest trace, php-parser's Xdebug
trace) and are read differently from the rest. A dynamic trace records only
the paths the test suite executes, and records reflection-driven and
dynamic-name dispatch (`$this->{'p' . $type}($node)`, werkzeug's
`LocalProxy`) that no static graph can name. Their **recall is therefore
not a target**: the gate holds it from regressing, but raising it would
mean either fabricating dynamic-dispatch edges or chasing untested paths,
and their precision is a lower bound (a correct static edge on an untested
path scores as false). Precision and recall targets apply to the static,
compiler-backed rows.

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

Current (2026-09-19): socket.io P 0.9029 / R 0.9917, express P 0.8400 /
R 1.0000. The socket.io step that crossed 0.9 precision: inherited-member
lookup resolves a base class name through the subclass file's imports, so
`this.onError()` in engine.io-client's Fetch transport binds the client's
`Transport`, not the server package's same-named class (26 of 81 false
edges were cross-package twins). Express's four remaining false edges are
`this.send(...)` / `this.json(...)` inside `res.json = function json()`
assignments — real calls the checker cannot type through an untyped
`this`, an oracle floor on a 21-edge corpus.

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

## Java (bytecode oracle)

`grove-eval truth --lang java` needs nothing beyond a JDK: compile with
`javac -g`, then read invoke* instructions and LineNumberTables out of
javap. Bytecode sees through overloads and static dispatch exactly;
overload targets resolve by arity. Lambdas/synthetic accessors skipped.
Pin: commons-lang (zero-dependency, and the overload stress test — ten
same-arity `isEmpty`/`indexOf`/`add` variants per name).

### Baseline (2026-06-12, calls edges)

| Repo | Universe | Precision | Recall | F1 |
|---|---|---|---|---|
| commons-lang (`44298fe`) | 96.7% | 0.6935 | 0.8387 | 0.7592 |

Second-day progression (0.553 → 0.759), each step measured:

| Fix | F1 |
|---|---|
| baseline (arity-narrowed) | 0.5534 |
| oracle bug: overload targets resolved by arity tie — descriptor-exact now | 0.6533 |
| maven-layout import suffix inversion; implicit-JDK qualifier drop | 0.6747 |
| AST-empty CallSites authoritative (regex fallback gated to non-AST langs) | 0.7169 |
| unknown-typed receivers drop (static typing: unknown = external); call-result receivers resolve via return type | 0.7520 |
| implicit-self narrowing (bare Foo() binds caller's own class, not every same-named method) — precision 0.680 → 0.694 | **0.7592** |

The fallback gating and import fixes lifted every language: gin 0.9372 →
0.9413, flask 0.700 → 0.708 (tests P 0.681 → 0.742, hit 31.5% → 36.3%),
socket.io 0.888 → 0.900 (R 0.96).

Day-one findings, fixed same day:

1. **Native text-matching exploded edges 6×** — the legacy Java analyzer
   edged every overload of every name appearing in a body (22k edges vs
   the oracle's 3.8k, P 0.15). Retired in favor of the graph layer's
   narrowed resolution; the native pass keeps inheritance + type-usage.
2. **Arity narrowing** (astkit v0.4.7 records `Argc`) and **argument-type
   conflict rejection** (v0.4.8 records bare-identifier `Args`; conflicts
   with known local/param types drop candidates, neutrality never does:
   varargs, type variables, and widening supertypes pass through).
3. Same-package scope (a Java directory is a package, like Go) and
   extends-based inheritance reuse the existing machinery.

Residual gap: same-arity same-type-shape overload sets and erasure-vs-
source attribution — Java's structural ceiling without full type binding.

## Rust (rust-analyzer SCIP oracle)

`grove-eval truth --lang rust` runs `rust-analyzer scip` over the cargo
workspace and reads the index: function-kind definition occurrences (each
carries its body span as `enclosing_range`) form the declaration universe,
and every reference occurrence to an in-repo function inside another
function's body becomes a caller→callee edge — calls, inferred method
dispatch, and function-as-value references alike, the same "may affect"
altitude as the Go VTA oracle. Macro-generated declarations (`rgtest!`-style
test functions with no `fn` syntax at the definition site) are skipped like
javap's synthetics. Symbol strings are NOT unique across targets of one
package (the build-script crate's `main()` collides with the bin's), so
caller identity always comes from the definition in the referencing file and
multi-definition callees resolve same-file first or drop as ambiguous.

Pin: ripgrep (multi-crate workspace with a facade crate, trait-generic
searcher/matcher/printer pipeline, builder-convention APIs — and unit tests
that live inside `#[cfg(test)] mod tests` blocks per Rust idiom).

### Baseline (2026-06-12, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| ripgrep (`82313cf`) | 99.4% | 0.8514 | 0.5967 | 0.7017 |

Day-one progression (0.4422 → 0.7017), each step measured:

| Fix | F1 |
|---|---|
| baseline (regex-era symbols, file-local scope) | 0.4422* |
| astkit: descend inline `mod` blocks (universe 64.5% → 99.4%) + trait bodies | 0.2329 |
| rustLocalTypes (params, lets, fields, generic bounds) + scoped-path qualifiers + Type::new constructor narrowing | 0.4790 |
| crate-wide scope + workspace-crate `use` resolution + unknown-receiver drops | 0.5874 |
| fan-out cap exemption (type evidence first) + builder-chain types + module-file narrowing | 0.6273 |
| call-result receiver types (union, body-mention filtered) + impl_trait default-method routing | 0.6487 |
| macro token-tree call recovery (assert!/write! bodies) + chain qualifiers through path calls | 0.7017 |

*the 0.4422 number predates the universe fix: a third of the oracle's
declarations (every unit test) were invisible, so it overstates quality.

Day-one findings, all fixed same day:

1. **Inline modules were invisible** — astkit never descended `mod_item`,
   hiding every `#[cfg(test)] mod tests` function (592 of 2304 oracle
   declarations on ripgrep). The same fix surfaced trait-body method
   signatures, Rust's dispatch points.
2. **Path-call qualifiers were dropped** — `Searcher::new` arrived as bare
   `new` (the astkit v0.4.2 lesson, third edition). Constructor-kind
   symbols also had to count in parent narrowing — Rust spells
   constructors `Type::new`, and filterByParent only admitted methods.
3. **Scope was file-local** — Rust visibility is crate-wide and `use`
   paths cross crates; ripgrep's core reaches the printer only through a
   facade crate's `pub use`, so crate scope closes transitively over
   re-exports, with package-name → directory matching on the last
   underscore token (grep_searcher lives in crates/searcher).
4. **Macro arguments are token trees** — calls inside `assert_eq!`/
   `write!` are not call-expression nodes, and idiomatic Rust tests make
   most of their calls there. Recovered by scanning the token tree text
   with string literals stripped.
5. **Static typing makes unknowns meaningful** (the Java lesson, Rust
   edition): an uppercase qualifier whose type owns no candidate is an
   external type (`PathBuf::from`); a lowercase one with no inferred type
   is a module path or an uninferable receiver — keep module-file matches
   and single same-file candidates, drop the rest. Builder chains resolve
   through return types: `.line_number(true).build()` narrows `build` by
   `line_number`'s declared return, with candidates filtered to types the
   caller's body actually mentions (word-boundary exact — `Searcher` must
   not claim a body that only names `SearcherTester`).

Current (2026-09-19): P 0.9364 / R 0.9047 (from 0.9304 / 0.8947). The
step: a bare `stats(&low)` names a free function, never a method (Rust has
no implicit self; inside `impl HiArgs` the same-module preference had
picked `HiArgs::stats`); `for flag in FLAGS.iter()` types `flag` from the
const's declared `&[&dyn Flag]` (astkit now emits const/static items, and
a trait element dispatches through the trait's declaration, which is what
rust-analyzer records); `Printer::Standard(ref mut p)` binds `p` to the
variant's payload type; `self.0` types as a tuple struct's field.

Residual gap: untyped flow the annotation surface can't see — iterator
element types (`for m in matches.iter()` over a `Vec<Match>` field),
closure parameters (`sort_by(|h1, h2| h1.path()...)`), and `?`-chained
results. The same registry-dispatch territory as Python's ceiling, to
revisit with measured variants rather than hope.

## C# (Roslyn semantic-model oracle)

`grove-eval truth --lang csharp` shells a small dotnet program (`eval/cstruth`,
built with `dotnet build -c Release -o bin`) that builds one Roslyn
compilation from every `.cs` file under the repo — framework references only,
so in-repo symbol resolution is exact regardless of unresolved third-party
types, the same altitude as the TypeScript oracle — and resolves every
invocation and object-creation to its symbol. Edges between two in-repo
declarations are the truth; lambda calls attribute to the enclosing method.
The dotnet SDK is only needed to *generate* the snapshot; CI scores against
the committed snapshot with tree-sitter alone (no dotnet).

Pin: Newtonsoft.Json (the canonical C# library — overload-heavy, and a
multi-target `#if`-laden codebase that stress-tests conditional compilation).

### Baseline (2026-06-12, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| Newtonsoft.Json (`0a2e291`) | 99.6% | 0.6617 | 0.7021 | 0.6813 |

Current (2026-09-19): P 0.9352 / R 0.9470 (from 0.9009 / 0.9481). The
precision sweep bucketed the 1,317 false edges and fixed what C#'s own
lookup rules say, each step re-gated on every corpus:

| Step | P | R | What changed |
|---|---|---|---|
| namespace lookup order | 0.9031 | 0.9478 | a simple name resolves in the caller's own type, own namespace, then enclosing/`using` namespaces (innermost rank wins); `new Person()` binds the imported TestObjects.Person — and when it declares no constructor, the call has no symbol at all; a class no visible namespace declares is the runtime's; a qualifier that IS an indexed type but declares no such member (and no base does) is `object.GetType()`, not the repo's same-named method |
| bare calls, explicit interfaces | 0.9258 | 0.8701* | a receiver-less call binds only the caller's own type chain, same-file local functions or an in-repo extension method (C# has no free functions); explicit interface implementations are never direct targets |
| typed receivers the extractor used to drop | 0.9294 | 0.9454 | astkit types string/char/bool literal receivers (`"{0}".FormatWith(..)` → the string extension), `predefined_type` statics (`string.Join`), cast receivers (`((ICollection<JToken>)a).CopyTo`) and element access (`o["x"].Children()` → "o[]", typed by o's indexer declaration, now a symbol; `rss["a"]["b"]` chains one level per index) |
| `#if`-wrapped imports | 0.9352 | 0.9470 | astkit reads `using` directives under a file-level `#if` (every newtonsoft async test file) — they had no imports at all, so every namespace looked invisible |

*the recall dip in step 2 was `new JValue(..)` calls, where a same-named
test method among the candidates disabled the constructor path; any
constructor candidate now marks a construction.

What remains (828 false edges): `ExceptionAssert.ThrowsAsync`/`Throws`
(151) — the Roslyn snapshot records no call to them from async tests
(`ThrowsAsync` is `#if !(NET20 || NET35 || NET40 || PORTABLE40)`-guarded
and the snapshot's compilation evidently did not resolve it; regenerating
needs `dotnet`); the `#if !HAVE_LINQ` LinqBridge polyfill (`ToList`,
`Select`, `ToArray`, `MinMaxImpl`… ≈200) which the library's net20 target
compiles and the tests' net46 target does not — target-dependent, and the
same edges are true for library-internal callers; and constructor/overload
sets whose arguments the extractor cannot type (`new JValue(x)`,
`WriteValue(v)`, `SerializeObject(x, converter)`).

Day-one progression (0.2632 → 0.6813), each step measured:

| Fix | F1 |
|---|---|
| baseline (native text-match calls + regex fallback, universe 79%) | 0.2948* |
| astkit: descend `#if`/`#elif`/`#else` preprocessor blocks (universe 79% → 99.6%) | 0.2632 |
| retire native C# call edges (text matching) + astkit C# call sites | 0.3339 |
| csharpLocalTypes + static-typing unknown-receiver drop | 0.3790 |
| repo-wide scope (one assembly, types mutually visible) | 0.4800 |
| overload disambiguation by arity (filterByArgc) | 0.6488 |
| generic-overload split (astkit v0.4.15 `CallSite.Generic`; `DeserializeObject<T>` vs `DeserializeObject`) — precision 0.60 → 0.66, recall unchanged | 0.6793 |
| implicit-self narrowing (bare Method() binds caller's own class) | **0.6813** |

*the 0.2948 predates the universe fix and overstates quality (a fifth of the
oracle's declarations — every file wrapped in `#if` — were invisible).

Day-one findings, all fixed same day:

1. **Whole files vanished inside `#if`** — Newtonsoft wraps every file in
   `#if !(PORTABLE || ...)`; tree-sitter nests the declarations inside a
   `preproc_if` node and astkit's C# walker didn't descend it (859 of 5373
   oracle declarations invisible). The C# analog of Rust's `mod_item`
   descent; all branches are walked so a symbol guarded by any target is
   found.
2. **Native text-matched call edges** (`internal/native/csharp.go`) fired on
   every `.csproj` repo (no toolchain gate) and edged every same-named
   overload — retired, keeping implements + uses-type, exactly as the Java
   and Rust native passes were.
3. **No call-site narrowing** — C# had no astkit call-site extractor, so the
   graph layer fell back to broad regex. Added `csCallSites` (member-access
   receiver qualifiers, object-creation → constructor) and enrolled C# in
   the AST path with `csharpLocalTypes` and the static-typing drop (an
   uninferable receiver is a BCL/third-party object — `sb.Append`,
   `list.Add` — whose method isn't ours).
4. **C# scope is the assembly, not the file** — `using` imports a namespace,
   which doesn't map to a directory, so file-level import resolution missed
   cross-file targets (recall 0.36). Within one assembly every type is
   mutually visible, so C# scope is repo-wide; precision is held by type
   narrowing, not scope. This took recall 0.36 → 0.70.
5. **Overload fan-out** — `JsonConvert` has five `DeserializeObject`
   overloads; Roslyn picks one by args. Arity narrowing (`filterByArgc`,
   shared with Java) split them, P 0.36 → 0.60.

Residual gap: same-arity overload sets (`SerializeObject(o, Formatting)` vs
`(o, JsonSerializerSettings)`) and LINQ extension methods resolving to
Newtonsoft's `LinqBridge` polyfill — the structural ceiling without full
type binding, the same territory as Java's residual.

## PHP (Xdebug dynamic-trace oracle)

`grove-eval truth --lang php` runs the repo's own phpunit under
`xdebug.mode=trace` with `eval/phptruth/boot.php` prepended; boot.php dumps a
reflection map of in-repo declaration locations at shutdown, and the Go side
reconstructs caller→callee edges from the trace's call-stack levels. This is
a dynamic, exact-but-partial oracle — the same design as Python's pytruth:
every asserted edge really executed, untested paths are absent, so **recall
is the headline and precision a lower bound** (a correct static edge on an
untested path scores as a false positive). A closure's calls attribute to
the function whose body *defines* it — Xdebug 3 names the frame
`{closure:/abs/file.php:START-END}`, and the definer is the in-repo
declaration nearest above START in that file — which is what Grove
records; the 2026-06 snapshot attributed them to the *invoking* frame
instead, so PHP-Parser's reduce callbacks (defined in
`Php8::initReduceCallbacks`, run from `ParserAbstract::doParse`) scored as
295 false edges plus 148 misses. Vendored dependencies are excluded.

Generating the snapshot needs php with xdebug and the repo's dev
dependencies (`composer install`); CI scores the committed snapshot with
tree-sitter alone (no PHP toolchain). Pin: nikic/PHP-Parser (zero runtime
deps, a dense recursive-descent + visitor call graph, and a comprehensive
suite for broad dynamic coverage).

### Baseline (2026-06-13, calls edges)

| Repo | Universe match | Precision* | Recall | F1 |
|---|---|---|---|---|
| PHP-Parser (`8eea230`) | 100% | 0.7701 | 0.5357 | 0.6319 |

*lower bound — see the partial-oracle caveat above.

Current (2026-09-19, re-pinned snapshot with lexical closure attribution):
P 0.9141 / R 0.6471 (from 0.8323 / 0.6010 against the old snapshot; the
Grove build is unchanged). The remaining 227 false edges are dominated by
`Php7::initReduceCallbacks` (80) — the PHP 7 parser's reduce closures,
which the suite never executes (it drives the Php8 parser) — and builder
tests' constructor calls on untested paths. The 1,317 misses are
dynamic-name dispatch the static graph cannot express: `$this->{'p' .
$node->getType()}($node)` in `PrettyPrinterAbstract::p` (445), reflection
over `getSubNodeNames()` in `NodeDumper::dumpRecursive` (281) and
`NodeTraverser::traverseNode` (124).

Day-one progression (0.2028 → 0.6319), each step measured:

| Fix | F1 |
|---|---|
| baseline (regex fallback only, native calls retired) | 0.2028 |
| astkit PHP call sites + AST path + phpLocalTypes + static drop + repo-wide scope + arity | 0.5453 |
| fluent-chain call-result receiver resolution (`$b->make()->addStmt()`; resolve result type or drop ambiguous) — precision 0.53 → 0.77 | **0.6319** |

The first measurement already had 100% universe and P 0.96 / R 0.11: the
regex fallback resolved a handful of calls precisely but saw almost nothing.
Enrolling PHP in the call-site path (astkit v0.4.13 emits qualified
function/member/static/new call sites), with `phpLocalTypes` (typed params,
typed and constructor-promoted properties, `new` locals), the static-typing
unknown-receiver drop, repo-wide scope (PHP `use` imports a namespace, not a
file; one library's classes are mutually visible), and arity narrowing, took
recall to 0.56 while precision settled at 0.53.

Findings:

1. **Native text-matched call edges** (`internal/native/php.go`, no toolchain
   gate) — retired, keeping implements + uses-type, as the Java/Rust/C#
   passes were.
2. **Polymorphic dispatch is the ceiling** — `$node->getSubNodeNames()` where
   `$node: Node` (an interface) executes a concrete subclass's method; the
   static graph can only see the interface or fan out to ~100 implementors.
   The dynamic oracle records the one concrete target, so these are
   irreducible without per-call-site type flow — the same registry/dynamic
   territory as Python's ~0.6 recall ceiling.
3. **Precision is a lower bound** — PHP-Parser's generated parser
   (`initReduceCallbacks`) defines a reduce closure per grammar rule;
   untested rules' closures are real call edges absent from the trace, so
   they score as false positives.

## C / C++ (clang AST oracle; scip-clang retained for C++)

`grove-eval truth --lang clang` (C, since 2026-09-19) parses every
translation unit in the project's `compile_commands.json` with `clang
-fsyntax-only -Xclang -ast-dump=json` — the entry's own defines and include
paths, so headers resolve as the real build resolves them — and reads the
typed AST: a `DeclRefExpr` to a `FunctionDecl` inside a function body is a
call (or a function passed by pointer, the same "may affect" altitude
scip-clang recorded), a `static` function binds within its own translation
unit only, a `static inline` definition in an in-repo header is a callee,
and a header the build system copied verbatim into `build/` maps back to
the source file Grove indexes. This is the Objective-C oracle's walker; see
that section for the location-sparse JSON handling.

It replaced scip-clang for jansson because scip-clang's index carries no
occurrence at all for some plain in-repo calls — `do_dump` calls
`json_array_size` at dump.c:275 and `json_array_get` at :287, and the
SCIP index has neither, while the same function's `hashtable_del` call is
present. Every correct Grove edge to such a callee scored as a false
positive (75 of jansson's 124 false edges pointed into value.c). Against
the clang AST the same Grove build scores P 0.9991 / R 0.9247.

`grove-eval truth --lang cfamily` still runs scip-clang over the project's
`compile_commands.json` (cmake generates it) and reads the SCIP index —
kept for C++, which the clang-AST walker does not yet model (methods,
templates, overloads).
scip-clang type-checks every translation unit, so each reference is a
resolved use. Unlike rust-analyzer it emits neither enclosing ranges nor
symbol kinds, so a function/method is recognized from its SCIP descriptor
(ends in `().`) and each reference is attributed to the nearest preceding
function definition in the same file — exact for C (functions don't nest), a
close approximation for C++. Generated headers under `build/` and all header
files are excluded as caller sources (their macro/prototype/inline content
mis-attributes); call-edge truth lives in `.c`/`.cc` bodies. The toolchain
(cmake + scip-clang) is only needed to generate the snapshot; CI scores it
with tree-sitter alone. `$GROVE_EVAL_CFAMILY_SCIP` points at a prebuilt
index.

Pin: jansson (zero-dependency C, CMake — `cmake -B build
-DCMAKE_EXPORT_COMPILE_COMMANDS=ON -DCMAKE_POLICY_VERSION_MINIMUM=3.5`).

### Baseline (2026-06-13, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| jansson (`684e18c`) | 97.2% | 0.8793 | 0.5642 | 0.6874 |

Current (2026-09-19, clang-AST truth): jansson 97.7% / P 0.9991 / R 0.9247.
The pinned `eval/testdata/jansson@684e18c/calls-truth.jsonl.gz` is now the
clang-AST snapshot (generator `clang-ast`). The one false edge is a
`static` test-file function (`position`) a same-named call in error.c
resolves to; the misses are macro expansions (`json_object_foreach`, the
`run_tests` entry point macro) — Grove has no symbol for a macro.

Day-one progression (0.3911 → 0.6874, scip-clang truth), each step measured:

| Fix | F1 |
|---|---|
| baseline (native calls retired, regex fallback only) | 0.3911 |
| astkit C/C++ call sites + AST path + cFamilyLocalTypes + repo-wide scope + arity | 0.6552 |
| oracle: exclude header files as caller sources (macro/prototype mis-attribution) | 0.6874 |

Findings:

1. **Native text-matched call edges** (`internal/native/cfamily.go`, no
   toolchain gate) — retired, keeping uses-type, as the Java/Rust/C#/PHP
   passes were.
2. **No call-site narrowing** — C/C++ had no astkit call-site extractor, so
   the graph layer fell back to broad regex (R 0.24). Added `cCallSites`
   (member/scoped/template calls, `new`) and enrolled C/C++ in the AST path
   with `cFamilyLocalTypes` and repo-wide scope (a C/C++ binary's symbols
   are mutually visible after include/link), which took recall to 0.56.
3. **scip-clang has no enclosing ranges** — references are attributed by file
   position. This is exact for C; header files (macros, prototypes, inline
   functions) break the partition and are excluded from the truth.

Residual gap: the C preprocessor. `json_array_append` and friends are
function-like macros expanding to `*_new` calls; Grove has no symbol for a
macro, so calls through them are absent — the structural ceiling for a
source-level graph of C.

## Swift (SourceKit index oracle)

`grove-eval truth --lang swift` runs `sourcekitten index` (a thin CLI over
SourceKit's own `source.request.index`, the same request Xcode's indexer
uses) once per file, with every `.swift` file under the repo passed as a
compiler argument so cross-file/cross-type references resolve. Unlike the
SCIP-based oracles, the output is one JSON entity tree per file with
declarations and the references inside their bodies already properly
nested — a reference's caller is just its nearest enclosing declaration
entity, no separate span search needed. Property accessors (getter/setter,
synthesized for every declared property, not just computed ones) are
excluded from the truth universe: astkit's Swift strategy records no
separate symbol for them, so scoring against them would fault Grove for a
structural gap the extractor doesn't claim to close. Needs Xcode or the
Swift toolchain (`sourcekitten`, `swiftc`, `xcrun`) — Darwin only.

Pin: SwiftyJSON (zero-dependency, one 1.4k-line file, and the overload
stress test — nine `init`s and six `subscript`s that all take one
argument). Point `--repo` at `Source/SwiftyJSON`: the repo's Example app
imports UIKit, which a flat-module index cannot see.

### Baseline (2026-09-19, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| SwiftyJSON (`3d25441`) | 100% | 0.9355 | 1.0000 | 0.9667 |

Day-one progression (0.1802 → 0.9667), each step measured with a fresh
index (see the cache note under Roadmap):

| Fix | P | R | F1 |
|---|---|---|---|
| baseline: every constructor named "init", no overload narrowing | 0.1029 | 0.7241 | 0.1802 |
| constructors named after their type (Java/C# convention) + `self.init`/`super.init` delegation | 0.1058 | 0.6897 | 0.1835 |
| overload narrowing by argument LABEL (astkit records `label:value` per Swift argument) | 0.9545 | 0.7241 | 0.8235 |
| `x[i]` subscript access recorded as a call to `subscript`; subscript bodies as callers | 0.6304 | 1.0000 | 0.7733 |
| unknown static receiver drops (Java rule); label mismatch decisive even for a lone sibling; trailing closures | 0.9032 | 0.9655 | 0.9333 |
| argument-shape narrowing (`T...` is `[T]` in its body); `let x = self` aliases; container receivers are external | 0.9355 | 1.0000 | **0.9667** |

Findings:

1. **Labels, not arity or types, are a Swift overload's identity.** Nine
   `init`s and six `subscript`s of one arity collapse under arity narrowing
   (P 0.10). The compiler tells them apart by argument labels first, and so
   does Grove now: astkit's Swift call sites record `label:value` per
   argument, declarations parse their labels (default, variadic, and
   trailing-closure parameters may be omitted), and a label mismatch is
   decisive — it is swiftc's own "not this function", so unlike arity it
   may empty the set, even for a lone same-name sibling.
2. **Constructors are named after their type.** `JSON(x)` writes the type
   name; naming every `init` literally "init" made them unfindable by name
   and interchangeable by label. `self.init(...)`/`super.init(...)` write
   "init" and get their own special form, like Java's `this()`/`super()`.
3. **Subscripts are calls.** `x[i]` is a `call_expression` with a
   bracketed suffix; read literally it was a call to a function named `x`.
4. **A typed receiver whose type is not indexed is external.** `path[0]`
   on `path: [T]` subscripts an Array, not T — local types keep container
   shape, and the Java unknown-receiver rule applies to Swift's static
   typing too.

Residual: the two remaining false edges are `self[key]` with `key` bound
by `for (key, _) in other` — typing it needs the Sequence's Element, which
the extractor does not model.

## Kotlin (kotlinc + javap bytecode oracle)

`grove-eval truth --lang kotlin` compiles with `kotlinc`, then reads
invoke* instructions and LineNumberTables out of `javap` — the exact
mechanism `JavaCallTruth` uses (`parseJavap` is pure JVM bytecode
disassembly, reused unchanged), since Kotlin compiles to ordinary class
files javap disassembles identically. Source discovery has no
package-must-match-directory convention to rely on (unlike Java), so a
`javap`-reported "Compiled from" basename is resolved against the actual
source file list instead of a reconstructed package path.

Three kotlinc code-generation habits would otherwise misattribute
source-level calls, and the oracle undoes each: a lambda body compiles to a
synthetic `outer$lambda$N` method (its invokes fold into `outer`); a
suspend lambda or object expression compiles to its own `Outer$method$N`
class (folded likewise); and a function with default parameters is called
through a static `f$default` dispatcher (resolved to the `f` overload whose
parameter count matches the dispatcher's, minus the mask and marker). What
remains unmodeled is every declared property's getter/setter, so calls
to/from those are recall misses, not false positives.

Pin: lordcodes/turtle (a shell-command library: single JVM target,
standard `src/main/kotlin` layout, kotlin-stdlib its only dependency).
Point `--repo` at the `turtle/` module. Corpus choice was constrained by
the toolchain: Homebrew's `kotlinc` is 2.4.x, which rejects most older
public Kotlin repos outright (removed stdlib APIs are hard errors,
multiplatform `expect`/`actual` needs the Gradle plugin) — a
`src/main/kotlin`, recently-maintained, dependency-free library is what
compiles.

### Baseline (2026-09-19, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| turtle (`3cfc963`) | 76.8% | 1.0000 | 0.9881 | 0.9940 |

Progression, same day, same pin (oracle edges grew 41 → 84 as the lambda,
anonymous-class and `$default` folds landed, so the columns are against a
moving-but-truer target):

| Step | Universe | P | R | What changed |
|---|---|---|---|---|
| day one | 63.2% | 0.5846 | 0.9268 | name + arity only |
| constructors, `in`, oracle folds | 76.0% | 0.8488 | 0.8795 | astkit emits primary/secondary/implicit constructors (call sites from parameter defaults, property initializers, `init` blocks) and desugars `a in b` → `b.contains(a)`; arg-type narrowing; unknown-receiver drop; lambda/`$default` folds |
| extension functions, chains | 76.0% | 0.8723 | 0.9880 | an in-repo extension function survives an untyped receiver; a call-result receiver resolves through the callee's declared return type (or a constructor) and drops otherwise; bare calls in top-level/extension functions reach only free functions, constructors and the receiver's own methods |
| operators, properties, `Any` | 76.8% | 1.0000 | 0.9881 | `a + b` → `a.plus(b)` (also `- * / %`); class properties type a receiver like locals; overload scoring ranks an exact/erasure match above a `vararg Any?` catch-all; annotation-prefixed signatures parse; chained-call qualifiers keep the leaf name only |

The universe gap is now only property accessors (kotlinc's getters and
setters have no astkit symbol). The one miss is a bare `command(...)` inside
a receiver lambda (`shellRun(...) { command(command, arguments) }`) — typing
it needs the lambda parameter's `ShellScript.() -> T` receiver type.

## Objective-C (clang AST oracle)

`grove-eval truth --lang objc` runs `clang -fsyntax-only -fmodules
-fobjc-arc -Xclang -ast-dump=json` on every non-test `.m` file (with `-I`
for every directory holding a header and `-isysroot $(xcrun
--show-sdk-path)`) and reads the typed AST back. clang has already resolved
each message send's receiver — `ObjCMessageExpr` carries the selector, the
receiver kind (instance / class / super) and the receiver's static type —
so the oracle binds an instance message to the selector's implementation on
the receiver's declared class or the nearest superclass implementing it, a
class message to the class method, `super` to the enclosing class's
superclass chain, and a `CallExpr` to its `FunctionDecl`. An `id`- or
protocol-typed receiver has no static class and records nothing: the same
"declared receiver type" altitude as the Java and Kotlin bytecode oracles.
The JSON dump is location-sparse (file and line print only when they
change), so the walk carries them along in document order. Files that do
not compile in isolation (`main.m` with app-target dependencies) are
skipped. No new tooling: clang ships with the Command Line Tools.

`scip-clang` (the C/C++ oracle) rejects `.m` inputs before invoking clang
and `sourcekitten` only indexes Swift, which is why this reads clang's AST
directly rather than reusing either.

Pin: SBJson/json-framework (SBJson 5: a streaming JSON parser/writer, five
`.m` files under `Classes/`, no dependencies beyond Foundation).

### Baseline (2026-09-19, calls edges)

| Repo | Universe match | Precision | Recall | F1 |
|---|---|---|---|---|
| json-framework (`93e4ca5`) | 98.7% | 1.0000 | 0.9914 | 0.9957 |

Progression, same day, same pin:

| Step | P | R | What changed |
|---|---|---|---|
| day one | 0.7770 | 0.9914 | name + arity; every same-name method across the repo |
| receiver rules | 0.9583 | 0.5948 | `super` walks the superclass chain; a lowercase/underscore receiver binds its declared class (and ancestors) or drops; a bare call is a C function; `id<Protocol>` is dynamic — but ivars were invisible, so most `_state` calls dropped |
| ivars and properties | 0.8099 | 0.9914 | astkit emits a field per declarator for `{ Type *a, *b; }` blocks in `@interface`/`@implementation` and for multi-declarator `@property` lines; Grove types `name` and `_name` from them |
| alloc/init, dispatch | 1.0000 | 0.9914 | `[[Type alloc] init]` is extracted with receiver `Type()` (and `[[self alloc] init]` as `self()`), typed by the class or by an in-repo method's declared return type; the scorer treats `clang-ast`, `sourcekitten-index` and `kotlinc-javap` as static oracles, so Grove's reason=dispatch override fan-out is unscored against them as it already was for javac/tsc/Roslyn |

The one miss is `self.error = ...` — property dot-syntax, which clang lowers
to a `setError:` message to the custom setter; astkit records no call for
an assignment.

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
- `swift-accuracy`/`kotlin-accuracy`/`objc-accuracy` CI jobs for the pinned
  SwiftyJSON, turtle and json-framework baselines (entries are in
  baseline.json; the jobs need a macOS runner with `sourcekitten`, `kotlinc`
  + a JDK, and the Command Line Tools' `clang` installed)
- Kotlin property-accessor symbols (turtle's remaining universe gap) and
  receiver-lambda typing (`T.() -> R` parameters) for its one miss
- Swift: type `for (key, _) in other` bindings by Sequence element (the two
  residual SwiftyJSON false edges)
- (fixed 2026-09-19) `grove-eval score` reused the repo's persistent
  `.grove` index across runs, so a rebuilt binary's edge-resolution changes
  were not measured until `ResolverVersion` was bumped or `.grove` deleted —
  it cost real time during the Swift baseline. The harness now opens Grove
  with `ForceIndex`, which re-extracts and rebuilds every edge unconditionally.
- (done 2026-09-19) Objective-C truth oracle — `clang -ast-dump=json`
  turned out to be enough; no libclang bindings needed
- (done 2026-09-19) php-parser snapshot re-pinned with lexical closure
  attribution: P 0.83 → 0.91. Its recall floor is dynamic-name dispatch
  (`$this->{'p' . $type}($node)` in the pretty printer, reflection in
  NodeDumper/NodeTraverser) — 850 of 1,317 misses.
- flask: the misses are werkzeug proxy/descriptor access (`AppContext.request`
  through a LocalProxy, `ConfigAttribute.__get__`, `with` → `__enter__`/
  `__exit__`); `with`-statement context managers are the one modelable
  bucket
