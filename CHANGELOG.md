# Changelog

## v0.59.0 - 2026-09-26

The index now contains the declarations each "precise" language was missing,
and graph edges stay deterministic. Found by a declaration-coverage test
(internal/parser/declaration_coverage_test.go, keyed to the capability
manifest) and a four-way gap sweep of real agent transcripts, declaration
kinds, lookup name forms and operations on the new kinds.

- New symbols (astkit v0.15.0):
  - Go struct fields, grouped/multi-name vars and consts, interface methods,
    type aliases.
  - C/C++ members, file-scope vars, enum constants, nested types, `using` and
    class typedefs.
  - Python module globals and `__init__` attributes.
  - PHP properties, consts, enum cases, promoted properties, `define()`.
  - JS/TS module values, enum and interface members, constructor parameter
    fields, CommonJS and default exports, `this.x`.
  - Rust enum variants, `macro_rules!`, associated items, unions.
  - C#/ObjC/Java/Kotlin/Swift enum members, records, events, operators,
    protocols, aliases.
- New data-like kinds add no uses-type edges (`declarationOnlySymbol`), so
  existing graphs keep their edges. Edge diffs before and after were
  explained per repo on gin, jansson, typeorm, express, flask, django,
  php-semver, flysystem, laravel, nlohmann/json, fmt, walkdir,
  Newtonsoft.Json, SwiftyJSON, SBJson and moshi.
- C++: a trailing `}  // namespace foo` no longer doubles qualified names.
  Namespace/specifier macros (nlohmann, fmt) no longer collapse a file into
  one pseudo-symbol. The regex fallback no longer makes symbols out of
  comments ("Copyright", `int`). C++ names always use `::`.
- Member change-impact: fields, properties, variables and constants now return
  their read, write and initializer sites (parser.MemberOccurrences). Sites
  with receiver or type evidence are confirmed; name-only matches are listed
  as ambiguous. On 13 ground-truth targets across Go, Java, C, TS, PHP and
  Python, recall went from 1 site per target to 100% at 100% precision.
  rename-plan counts and the declaration edit are fixed.
- Determinism: PHP `new X` narrowing and two import scans no longer depend on
  Go map order. Laravel had 263-345 edges changing between runs of the same
  binary; three cold indexes are now identical.
- Symbol-scope search accepts `**` globs.

Known limits: a C++ `#else` branch can lose a few call sites when the
`#if`-only re-parse wins; ES5 constructor functions and anonymous
`module.exports = class {}` are not indexed. Full release gate: go test
./... green and ci_invariants all held.

## v0.58.2 - 2026-09-22

`grove doctor`'s capability manifest was missing COBOL and JCL entries even
though both have been fully registered, working astkit strategies since
v0.13.0 -- a user report ("doctor doesn't say it supports COBOL") surfaced
both this gap and a stale README line pointing at a nonexistent `grove
capabilities` command (the real command has always been `grove doctor`; the
capabilities field is one part of its JSON output). Added both languages to
the manifest as structural/heuristic (no grammar, no published accuracy
oracle yet, unlike the ten CI-gated languages). Docs-and-metadata only; no
parser, graph, or resolution code changed. Full release gate (go test ./...,
ci_invariants against a workspace-built candidate) green.

## v0.58.1 - 2026-09-19

Fixed v0.58.0's own bug the same day. Typing a bare `this` argument against
the enclosing class was meant for constructor overloads
(`new WildcardFileFilter(this)`) but applied to every call: on an ordinary
method, `ser.serialize(null, gen, this)` lost the valid overload because the
lightweight overload matcher checks argument-type equality, not
assignability, and `this` (`Provider`) isn't equal to a parameter typed
`SerializerProvider` even though it satisfies it. The fix now types `this`
only when a constructor is among the call's candidates. commons-lang
recovers exactly to its first-corpus baseline (R 0.9509 → 0.9517); the other
19 corpora are unchanged. Resolver `v22`.

## v0.58.0 - 2026-09-19

Java bare-call overload fan-out (commons-io P 0.876 → 0.894; commons-lang
unaffected). A bare call unresolved on the caller's own class or its
resolvable ancestors now drops rather than falls through to same-package
name matching — but only when the caller's own class extends a base Grove
cannot resolve in-repo, the positive signal that distinguishes
`IORandomAccessFile extends java.io.RandomAccessFile`'s bare `write(...)`
(really `RandomAccessFile.write`, wrongly fanning into every same-arity
`IOUtils`/`FileUtils`/`FilesUncheck` overload sharing the package) from
`StrSubstitutor`'s `new StrBuilder(n).append(x)` (chained onto a
constructor, also arrives bare, but `StrSubstitutor` has no unresolved
superclass, and the import-scoped fallback keeps resolving it correctly).
`this` as a constructor argument (`new WildcardFileFilter(this)`) now types
as the enclosing class (astkit v0.14.4: a keyword node, not an identifier
node, in every grammar). Resolver `v21`, extractor `2026-09-19.12`.

## v0.57.0 - 2026-09-19

C preprocessor call-through (cJSON R 0.646 → 0.999 at P 0.998; jansson R
0.925 → 0.984). Every in-repo `#define` is now a `macro` symbol (astkit
v0.14.3) carrying its body's calls and its parameter list; at an invocation
the graph layer expands those calls with the arguments substituted for the
parameters, transitively through nested macros and every conditional
definition of a name, and a body call whose callee is a parameter calls the
argument — so `RUN_TEST(f)` reaches both `UnityDefaultTestRun` and `f`, and
`json_object_foreach` reaches its iterator functions. Two C extraction holes
found on the way: declarations inside an `extern "C" {` block opened under
`#ifdef __cplusplus` were invisible (the grammar hands the rest of the header
to the block), and a calling-convention macro between return type and name
(`int CJSON_CDECL main(void)`) lost the function's body. Both are fixed.
Resolver `v20`, extractor `2026-09-19.11`.

## v0.56.0 - 2026-09-19

A second, untuned accuracy corpus per language (cobra, commons-io, cJSON, fd,
p-queue, Files, CocoaLumberjack, league/csv), each pinned and gated, as the
overfitting check on rules tuned against one repository. They found and
this release fixes: the Java oracle silently dropped every method with a
`throws` clause and charged its calls to the previous method (commons-lang's
snapshot is re-pinned — recall 0.920 → 0.952 on an unchanged build); Java
and C# constructors without `super(...)` now call the superclass's
parameterless constructor, with nested helper types resolved from the
constructor's own file; Cargo `tests/`, `benches/` and `examples/` files
form crates (fd R 0.73 → 0.91); Swift resolves `typealias` constructions,
types receivers from the enclosing type's or protocol's properties, and
reaches in-repo extensions of external types (Files R 0.53 → 0.92); the
Objective-C oracle handles `include/<Module>/` header layouts. Remaining
second-corpus gaps are documented: C function-like macros (cJSON R 0.65),
Java overload fan-out, Objective-C property reads, and JavaScript
object-literal modules.

## v0.55.0 - 2026-09-19

C# precision (Newtonsoft.Json: P 0.901 → 0.935, R 0.947). Resolution now
follows C#'s simple-name lookup order — the caller's own type, its
namespace, then enclosing and `using` namespaces — so `new Person()` binds
the imported class (and binds nothing when that class has only an implicit
constructor), a class no visible namespace declares is the runtime's, and a
receiver-less call reaches only the caller's own type chain, same-file
functions or an in-repo extension method. Explicit interface implementations
are never direct targets. A qualifier that is an indexed type without the
member (nor any base) is the runtime's member. astkit v0.14.2 types literal,
cast, `predefined_type` and element-access receivers (indexers are symbols;
`o["x"].Children()` resolves through `JObject`'s indexer), classifies
verbatim and interpolated strings, and reads `using` directives under a
file-level `#if` — every newtonsoft async test file previously indexed with
no imports. Requires astkit v0.14.2.

## v0.54.0 - 2026-09-19

PHP accuracy baseline re-pinned. The Xdebug oracle's snapshot for PHP-Parser
is regenerated with the lexical closure attribution introduced in v0.51.0
(a closure's calls belong to the function that defines it, as Grove
records, not the frame that invokes it). Against the corrected truth the
unchanged Grove build scores P 0.914 / R 0.647 (was 0.832 / 0.601); the
CI gate moves with it. Remaining misses are dynamic-name dispatch and
reflection the static graph cannot express.

## v0.53.0 - 2026-09-19

Resolve-phase memory. Files of whole-repo-scope languages (C#, PHP, C/C++,
Swift, Objective-C) and files within a Rust crate each materialized their
own copy of the visible-file set — O(files²), 25,849² map entries on the
C# monorepo behind the v0.52.0 field report and 37% of the heap even on a
971-file repository. Every such file now shares one set per repository (or
per crate). C# type-fragment lookup no longer copies a class body per call
site. On a synthetic 4,800-file / 848k-edge C# corpus: peak heap 3.99 GB →
1.3 GB, max RSS 4.45 → 2.24 GB, wall 78 → 57 s; every accuracy corpus
scores byte-identically. `cmd/memprobe` reproduces the measurement.

## v0.52.0 - 2026-09-19

Operability for large repositories, from a production field report. `grove
status` now reports the index run's `phase`, a monotonic `progress` counter
(symbols resolved during edge construction, the phase where the counts used
to freeze for minutes), `indexStarted`/`indexFinished`, and the per-analyzer
`native` verdict of the run that built the stored edges — all persisted in
the database, so a supervisor polling status or a consumer reading `grove.db`
later can tell a busy index from a wedged one and a Roslyn-resolved index
from a grammar-only one. `grove index --vacuum` compacts the database after
the write; `--min-confidence` drops edges below a persisted floor. The README
documents what each native analyzer needs on disk and measured sizing
(peak memory, database size, edge row counts). The release adds a fully
static linux/amd64 musl binary for images whose glibc predates 2.34.

## v0.51.0 - 2026-09-19

Swift, Kotlin and Objective-C are now measured languages: each scores against
a compiler oracle on a pinned corpus and is gated in CI's baseline. Swift
(SwiftyJSON, SourceKit index) P 0.936 / R 1.000; Kotlin (turtle, kotlinc +
javap) P 1.000 / R 0.988; Objective-C (SBJson, clang AST) P 1.000 / R 0.991.
Kotlin resolution narrows overloads by declared parameter type, types
receivers from parameters, locals and class properties, resolves extension
functions, call-result receivers and constructor delegation, and desugars
`in`/`+` operators; Objective-C types receivers from ivars and properties,
walks `super` through the superclass chain, treats `id`/protocol receivers
as dynamic, and types `[[Type alloc] init]` results.

Existing languages: TypeScript inherited-member lookup resolves a base class
through the subclass file's imports (socket.io P 0.898 → 0.903); Rust bare
calls never bind methods, and const-slice elements, enum-variant patterns
and tuple-struct fields type their receivers (ripgrep P 0.930 / R 0.895 →
0.936 / 0.905); C's jansson corpus is re-pinned against a new clang-AST
oracle after scip-clang was found to omit plain call occurrences (P 0.877 →
0.999). The PHP oracle now attributes closure bodies to the function that
defines them (php-parser's snapshot awaits regeneration). C# record base
lists with constructor arguments parse; `.h` files are content-sniffed for
Objective-C; the eval harness re-indexes unconditionally.

Requires astkit v0.14.1.

## v0.50.0 - 2026-09-19

Swift, Kotlin and Objective-C parsing and graph support (astkit v0.13.0):
symbols, imports, call sites, inheritance/conformance edges, local-type
inference and change-impact for all three. Java multi-declarator field
receivers resolve; overload narrowing, chained-call receivers and C phantom
symbols are corrected; stale graph indexes refresh once under the new
extractor and resolver stamps.

## v0.49.0 - 2026-09-12

Java call resolution now preserves overload identity through dynamic dispatch,
infers uninitialized locals and nested-class fields, recognizes indexed
receivers in fluent chains, and applies known external call-result types when
narrowing overloads. Python resolution now recovers bounded dynamic member
families through same-name callable aliases, registry-sourced receivers, and
one additional import hop while retaining heuristic evidence and fanout caps.

These changes restore the Jackson `JsonNode.get(int)` and
`SettableBeanProperty.set` ceilings and Django `quote_name` coverage. The full
unit and race suites, vet, all nine edge-accuracy corpora, and Prism's complete
release invariant suite pass.

## v0.48.0 - 2026-09-11

Grove now builds and evaluates against astkit v0.11.0, incorporating improved
COBOL copybook, continuation, span, and PERFORM-range metadata; JCL symbolic
execution and INCLUDE resolution; and explicit C prototype annotations. The
full unit and race suites, vet, Prism verification, and all nine edge-accuracy
corpora pass unchanged against the updated extractor.

## v0.47.0 - 2026-09-11

Graph resolution is now scoped by language family, and ambiguous cross-language
change-impact, missing-implementation, and rename requests fail closed until the
caller supplies a file-qualified identity. Call resolution gains arity-aware
dispatch, inherited-method and local-type recovery across supported languages,
top-level JavaScript coverage, TypeScript project-reference discovery, and
correct handling for nested Python callables and imports.

Indexing now serializes concurrent writers, recovers safely from incomplete
zero-edge stores, rejects foreign-root MCP indexing, and rebuilds persisted
graphs under new extractor and resolver stamps. Mainframe impact, lineage,
rename, and dead-code analysis now cover COBOL/JCL containment and data-flow
relationships. The nine-corpus edge-accuracy suite, full unit suite, race
detector, vet, and staged-diff checks pass for this release.

## v0.45.0 - 2026-09-10

Graph correctness across all supported language backends. Shared hierarchy
resolution now handles interfaces, multiple and generic bases, Java
`implements`, C++ inheritance and namespaces, Rust traits and supertraits,
modern C# declarations, and PHP multi-trait/interface forms. Go native
resolution covers exact import paths, aliases, explicit generic calls, and
generic interface dispatch; Python covers relative/module aliases, annotation-
only locals, parenthesized `with`, directory-local class attributes, and C3
method resolution. JavaScript/TypeScript adds CommonJS and dynamic-import
edges, implementation-line selection for overloads, callable arrow fields,
and multi-segment constructor types.

C++ type-use analysis now ignores comments, understands smart pointers, keeps
namespace-qualified identities distinct, and honors file-scope `using
namespace`, namespace aliases, and `using ns::Type` declarations. Existing
indexes rebuild through new extractor and resolver stamps. Regression coverage
exercises every reproducible defect from the 2026-09-10 language graph review.

## v0.44.1 - 2026-09-10

Java chained-call resolution now binds an inner call only when its owner is
the caller's class hierarchy or an explicitly named type at the call site.
This removes name-only matches such as `List.stream()` being attributed to an
unrelated in-repo `Streams.stream()` while preserving concrete factory and
singleton chains. On the pinned Commons Lang oracle, precision improves from
0.8478 to 0.8653, recall from 0.9039 to 0.9047, and F1 from 0.8750 to 0.8846.

## v0.44.0 - 2026-09-10

Graph correctness: imported and historical change-impact resolution is now
binding-aware. Python preserves generic receiver types, import aliases, lexical
import ownership, and the distinction between member calls and free functions;
JavaScript and TypeScript preserve named, namespace, default, and type-only
aliases; Go package aliases disambiguate same-named functions across packages.
File-scoped free-function queries and methods whose receiver is declared in a
different Go file now resolve to the intended contract instead of same-name
decoys. Unresolved dynamic Python dispatch is reported as heuristic rather than
high-confidence evidence.

The public in-memory `PreviewChangeImpacts` API builds isolated base-source
overlays without mutating the worktree or live index. Extractor and resolver
version stamps force upgraded indexes to rebuild stale graph edges, including
obsolete Python-native name-only calls. Regression coverage includes real
parser fixtures for Python, JavaScript/TypeScript, and Go import bindings,
persisted-index upgrades, overlay isolation, and incremental/full graph
equivalence.

Windows native Go analysis now preserves `%LocalAppData%` in its hardened
subprocess environment so `go list` can locate the build cache. Failed
`go list` diagnostics retain stderr instead of reporting only an exit code.

## v0.43.3 - 2026-09-08

Python: type-use edges now cover modern annotations and resolve them to the
correct symbol. PEP 604 unions, parameterized generics, and quoted forward
references are traversed instead of dropping their contained types. Repeated
method names in one file are attributed by source span, and annotations that
refer to locally imported or re-exported types resolve across files without
binding third-party imports to unrelated local declarations. On the pinned
urllib3 benchmark source, change-impact for `ProxyConfig` improves from its
declaration alone to the declaration plus all eight annotated function and
method sites across five additional files.

## v0.43.2 - 2026-09-06

Go: interface contracts resolve through method sets, across packages.
The native pass type-checks sibling packages from source
(`goProjectImporter`; `importer.Default` could not load an unbuilt
package in the same module, so cross-package interface information was
dropped and change-impact on an imported interface method failed
outright). Interface methods become `<iface>#method` contract nodes:
`contains` from the interface, `implements`/`overrides` from every local
type whose method set satisfies it (`types.Implements` on `*T`, so
promotion and embedding are handled), and `calls` from each call through
an interface-typed receiver to the contract node and to every local
implementation (`ReasonMethodSet`, native evidence). Interfaces with
type parameters are skipped (design question, not narrowed unsafely).
Signatures containing Invalid after a best-effort check never count as
compatible. Cross-package change-impact on an imported Go interface goes
from a hard failure to the exact site set; wrong-signature decoys are
excluded. gin P .9339 -> .9406, R .9488 -> .9505; indexing cost neutral
(0.601s -> 0.598s over six alternating trials).

## v0.43.1 - 2026-09-05

Java: chained call-result receivers resolve outside the caller's import
scope. `javaCallResultTypes` now returns the SET of an overloaded call's
return types instead of requiring all overloads to agree (previously any
disagreement dropped the type entirely); `javaMethodsOfTypes` looks up the
callee on those types without the import-scope filter — a call-result
receiver's type is never required to be imported by the calling file
(`ConfigManager.getProtocol(url).getTriple()` names `ConfigManager` and
`TripleConfig` but never `ProtocolConfig`). Found via a wide-bed autopsy:
`ProtocolConfig.getTriple` had 0 resolved callers against 4 real call
sites. commons-lang P .8466 -> .8478, R .8927 -> .9039.

## v0.43.0 - 2026-09-05

Engine-ceiling program, measured against compiler/runtime oracles
(`eval/baseline.json` floors raised; P/R before -> after):
commons-lang .697/.891 -> .856/.896, newtonsoft .672/.702 -> .849/.816,
flask .830/.646 -> .829/.704, socket.io .849/.963 -> .898/.975,
ripgrep .851/.597 -> .899/.896, php-parser .770/.536 -> .825/.571,
jansson .879/.564 -> .876/.867.

- Java: overload resolution by argument types — primitive arrays never
  bind `T[]`/`Object[]`, exact-type overloads beat boxing/wildcard
  siblings per declaring type, varargs element binding, shape rules
  (array vs Collection, lambda vs primitive), boxing pairs, class field
  types and a JDK return-type table as argument evidence; declaration
  parsing skips leading annotations/Javadoc; cast, array-element and
  `X.class` receivers are qualified; `super.` receivers reach the call.
- C#: base-class parsing (previously absent), `base.`/`this.` receivers
  reach the call, argument-type overload narrowing with BCL alias
  folding, `params`, extension-method receivers, subtype assignability,
  literal conversion ranking, enum-member typing, nested-`new` markers,
  preprocessor-split files re-parsed with `#else` branches blanked.
- Python: `with` items -> `__enter__`/`__exit__`, return-annotation
  typing, from-import submodule binding, imported module globals,
  template-method dispatch, class-attr types only through `self.`.
- Rust: re-export-only facade files and `mod x;` exist to the graph,
  inline crate paths and grouped/nested `use` join scope, builder chains
  survive one unparseable return type, same-file-wins no longer shadows
  typed type paths, same-named cross-crate types pinned by path/import.
- PHP: namespace-aware `new`, `$x = Class::m()` locals, interface
  member parsing.
- C: a regex-found callable the AST already declared is not a second
  function (jansson do_dump lost all 64 calls to a 1-line twin).
- TypeScript: multi-line generic field types keep their head, declared
  non-class receivers drop, inherited self-calls prefer in-scope ancestors.
- All class languages: class-hierarchy dispatch through typed receivers
  (`reason=dispatch`, capped); `new X(...)` sites bind constructors only;
  bare calls to functions declared in the caller's own body emit nothing;
  candidate lists sorted for determinism.
- change-impact: constructors count as members (`GlobSetBuilder.new`
  reported "declares no method new").
- eval: deterministic declaration claiming when two oracle decls land on
  one symbol; `GROVE_EVAL_MAX_EXAMPLES`; dispatch edges are neither TP
  nor FP under declaration-binding oracles (`ignoredDispatch`).
- `GROVE_TRACE_CALLS=1` prints every call site's candidates, arguments
  and narrowed set, plus the Rust scope walk.
- Requires astkit v0.9.0.

## v0.42.0 - 2026-09-02

- change-impact: `file=` scoping disambiguates same-named types in
  different packages (`Engine.ChangeImpactScoped`); unscoped behavior
  unchanged.
- graph: Java package scope spans Maven/Gradle source roots —
  same-package test callers (src/test/java mirroring src/main/java, no
  import needed) join call resolution and change-impact sets.

## v0.41.0 - 2026-08-30

- graph: Python class attributes join the graph as fields, with
  uses-type/fan-out guards.

## v0.40.0 - 2026-08-30

- change-impact: bare TYPE-NAME queries answer with the type-level
  dependent set instead of dead-ending on the constructor.

## v0.39.0 - 2026-08-29

- framework edges generalize past Java (Angular templates, Flask routes);
  field-anchor change-impact.

## v0.38.0 / v0.38.1 - 2026-08-29

- framework edges: JPA derived queries, template expression language;
  honest completeness reporting (heuristic-refs as a structured field).

## v0.37.0 - 2026-08-28

- index: extractor-version stamp; `--force` actually re-extracts.

## v0.33.0 - v0.36.3 - 2026-08-28

- Mainframe estate: COBOL/JCL parsing conformance corpus, data-flow
  lineage (directional reads/writes, REDEFINES, dataset binding),
  change-impact anchors for mainframe kinds, extensionless-member
  content sniffing, uppercase-extension detection (32% -> 99% include
  resolution).

## v0.30.0 - v0.32.0 - 2026-08-10 - 2026-08-20

- Receiver resolution: multi-hop chains, generic-constraint extends,
  signature-aware interface satisfaction, per-site receiver
  classification for rename plans, Go local types for conversions/call
  results/closure params.

## Earlier removals worth knowing (v0.26-v0.27, 2026-08)

- v0.26.0 REMOVED heuristic test-coverage (`tests`) edges: measured
  4-12% recall against real per-test runtime coverage — an unreliable
  signal shipped as a guarantee is worse than none. Covering-test
  questions answer over resolved `calls` edges (a test that exercises
  code calls it); dead-code reachability verified byte-identical
  before/after.
- v0.27.0 REMOVED embedding-based semantic search (Model2Vec): measured
  2026-08-01 on 15 hand-verified concept queries across 5 corpora, an
  agent guessing one keyword through lexical search beat or tied the
  embedding fallback in 12/15 cases. The graph is calls/types, queried
  lexically.


## v0.29.1 - 2026-08-09

- **Windows:** the v0.29.0 permission tightening is now Unix-only —
  `Chmod(0o700)` on Windows cleared the read-only attribute on a
  deliberately read-only `.grove`, defeating the read-only diagnostics.
  Windows has no POSIX permission bits to tighten.
- eval module: astkit pin caught up with the main module (v0.4.23).

## v0.29.0 - 2026-08-09

Robustness fixes from the 2026-08-09 cross-repo audit.

- **BREAKING — store errors surface instead of returning empty results:**
  `Engine.ICR`, `FileSymbols`, `SnapshotSymbols`, `SnapshotGraph`, and
  `DiffSince` now return an error; every graph-backed method propagates
  rehydration/database failures instead of printing to stderr and answering
  with an empty graph (corruption was indistinguishable from "no matches").
- **Indexer never prunes what it could not see:** a nonexistent/unreadable
  root aborts the run with an error, and files under an unreadable subtree
  are shielded from the deletion pass (a transient FS error could previously
  empty a valid index).
- **Parse failures invalidate stale symbols:** a changed file that fails to
  parse now has its previously stored symbols pruned instead of serving the
  last successfully parsed version.
- **Private index permissions:** `.grove` is created (and tightened) to
  0700 and `grove.db`/WAL/SHM to 0600 — the database stores full source
  bodies.

## v0.6.3 - 2026-06-12

Two precision fixes found by Prism's grafana-scale benchmark
(prism/docs/AB-Test-Payflow-2026-06-12.md):

- **TestsFor traversal confidence gate:** the "tests for X" closure no
  longer follows low-confidence fallback edges (ambiguous cross-file
  bare-name call matches at 0.6, type-use guesses at 0.5), which connected
  unrelated subsystems on monorepos. Direct `tests` edges are unaffected.
  Known limitation: residual cross-subsystem noise can still arrive over
  high-confidence edges when a bare callee name resolves to ≤16 candidates
  (all get 0.95 edges); the durable fix is type-aware callee resolution.
- **GraphDiff rename pairing for common and partially-renamed names:**
  body normalization now blanks only standalone identifier occurrences of
  the symbol's own name (a substring ReplaceAll mangled "Get" inside
  "GetKeys" and broke pairing for short names), and a bounded pairwise
  second pass blanks BOTH names on both sides so mechanical renames that
  leave the old name in the doc comment ("// Get an item…") still pair.
  Both cases previously fell back to removed+addition — breaking flag
  correct, continuity signal lost.

## v0.6.2 - 2026-06-12

Real-repo validation pass (prometheus / django / grafana) plus token
discipline for the MCP surface.

- **Scoped native analyzers:** only languages whose files changed re-run;
  skipped analyzers' stored edges are carried forward. A one-file Go edit
  on a 19k-file polyglot monorepo no longer re-runs the TypeScript
  program check.
- **O(import-depth) import resolution:** package-import matching used a
  per-import scan over every directory (~0.5B string comparisons on
  grafana); slash-suffix lookups produce a bit-identical graph.
- **Diff-based edge persistence:** the edge table is synced by difference
  (batched multi-row writes) instead of delete-everything-reinsert.
- Net effect on grafana (18,979 files / 98.5k symbols / 1.16M edges):
  one-file change 78.3s → 18.7s; cold index 87.4s → 56.6s. README
  publishes the measured table.
- **MCP token discipline:** symbol payloads no longer carry full bodies
  (one grove_query response was ~10.7k tokens; now ~1.1k);
  grove_impact caps at 50 minimal refs with an exact count; all
  responses are compact JSON.

## v0.6.1 - 2026-06-11

- Added `Engine.FileSymbols(ctx, relPath)`: the indexed symbols for one
  file, without paying for a whole-graph snapshot. Supports working-set
  drift checks in Prism.

## v0.6.0 - 2026-06-11

- **GraphDiff rename detection:** a removed symbol whose body matches an
  added one (modulo its own name) is reported as `renamed` instead of an
  unrelated removal + addition. Only unambiguous 1:1 body matches pair;
  trivial bodies never pair. An exported rename is a breaking change
  (callers of the old name break); a pure file move is not.

Accuracy, performance, and trust fixes from the 2026-06-11 assessment
(recorded in the 2026-06-11 repository assessment).

### Fixed — correctness
- **Symbol-ID collisions (critical):** same-named members in one file (two
  receivers' `Close()`, two classes' `__init__`) collapsed into a single
  stored symbol. Qualified names now include the parent
  (`Service.Login`); residual collisions get deterministic ID suffixes.
- **ICR no-match fallback (critical):** an intent matching no symbol
  returned the first 20 symbols alphabetically at confidence 0.9 with real
  lock keys. It now returns an empty region at confidence 0.2 with no locks.
- **Go analyzer environment (critical):** `go list` ran with
  `HOME=<repo>/.grove`, downloading a full per-repo module cache (hundreds
  of MB of read-only files) and breaking GOPRIVATE/.netrc auth. The user
  environment is preserved; legacy `.grove/home` and `.grove/go-build`
  caches are cleaned up on the next index.
- **CertifyDiff hunk mapping:** changed-symbol ranges now cover only the
  lines a hunk actually adds/deletes; context lines no longer mark adjacent
  untouched symbols as changed. Deletion-only hunks map to their enclosing
  symbol.
- **CertifyDiff staleness gate:** changed files whose indexed content no
  longer matches the working tree produce an `index_stale` unknown and
  escalate to `manual_review` instead of silently certifying outdated spans.
- **Test-edge scoping:** `tests` edges are now scoped through the import
  graph (TestOpen no longer "covers" every `Open` in the repo) and gain
  call-site evidence; Rust `#[test]` / JUnit `@Test` / xUnit `[Fact]`
  annotated tests and `tests/`-dir conventions are recognised.
- **Qualified cross-package Go call edges** resolved against the wrong
  package-dir comparison and silently never matched for nested packages.
- Diff paths with traditional `+++ file\t<timestamp>` suffixes parse
  correctly; SQLite LIKE wildcards in file paths are escaped; ICR JSON
  arguments are no longer mis-decoded as base64; engine `Open` surfaces
  rehydration errors; concurrent `Engine.Index` calls are serialized.
- **Python native analyzer** no longer executes repository code at index
  time (`find_spec` imported parent packages' `__init__.py`; resolution is
  now pure-filesystem via `PathFinder`).

### Changed — performance
- No-change reindex short-circuits: persisted edges are reused instead of
  re-running native analyzers and edge construction (~3.7 s → ~35 ms on an
  80-file repo). `grove index --force` re-runs everything.
- Call-edge fallback extracts callees in a single pass instead of matching
  every callable's regex against every body (synthetic 10K-symbol corpus:
  39.7 s → 0.5 s); ambiguous callee names (> 16 cross-file candidates) emit
  no edges instead of fanning out to all of them.
- BFS traversals (Impact, TestsFor, certification) use a per-node inbound
  edge index instead of scanning the whole edge list per visited node.
- Go type-use analysis tokenizes each body once and honours the analyzer
  timeout; per-pair regex compilation removed across analyzers.
- Edge and symbol writes use prepared statements.

### Added
- `PreviewFileSymbols` / `DiffAgainstFileContent` (`pkg/grove`): parse
  in-memory content as if it lived at a path and diff it against a
  snapshot — for callers whose result is not on disk yet, like a git merge
  driver (git writes `%A` to the worktree only after the driver exits).
- **GraphDiff API** (`pkg/grove`: `SnapshotSymbols`, `Diff`, `DiffSince`):
  structural delta between two snapshots matched by stable identity
  (file path + qualified name + kind), with `BreakingChanges` for exported
  symbols removed or re-signatured. Line shifts and content-SHA churn do
  not register — only symbols whose signature or body changed appear. This
  is the primitive for cross-agent drift notification (the Fuse
  stale-context loop).
- Nested `.gitignore`/`.groveignore` files now apply relative to their own
  directory with last-match-wins override, and `**` globs are supported.
- `grove_certify` MCP tool; all MCP tools now publish full JSON schemas
  with per-parameter descriptions.
- Ranked symbol search (exact name > prefix > substring) replacing
  alphabetical-by-path ordering; tighter Impact seed fallback.
- `grove index --force` and `force` argument on `grove_index`.

### Changed — performance (second batch)
- Changed files are parsed on a worker pool (tree-sitter parsing dominates
  cold indexing; astkit engines are concurrency-safe); store writes remain
  serial and ordered, so results are deterministic.
- Embedding vectors are cached by symbol ID across index rebuilds: the
  first query after a delta reindex re-embeds only changed files' symbols
  instead of the whole corpus.

### Removed
- The unused FTS5 mirror (`symbols_fts` + sync triggers): no retrieval
  path ever queried it, while its triggers doubled the cost of every
  symbol write. Existing databases are migrated (table and triggers
  dropped) on next open.
- Vestigial daemon-mode config (`server.port: 7777`) from `grove init`.

## v0.5.0 - 2026-06-07

- Added native semantic analyzers for Go, Python, Java, Rust, C, C++, C#, PHP, JavaScript, and TypeScript.
- Persisted native edge source so graph consumers can distinguish AST, heuristic, and native evidence.
- Fixed symlink-root normalization so `/tmp` and `/private/tmp` resolve consistently during indexing.
- Tightened Go fallback resolution and C++ member extraction to reduce false positives and symbol loss.
- Updated documentation to describe the native enrichment architecture and current release surface.
