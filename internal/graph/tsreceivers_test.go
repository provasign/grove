package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Receivers whose type the source states — `new Map()`, a module-level
// `new WeakMap()`, a class field initialised with `new Map()`, a chain ending
// on a Map field, an arrow parameter typed by a function-type alias — must not
// bind `.get(...)` to the repository's only `get` (zod $ZodRegistry.get:
// 17 of 28 reported callers were Map/WeakMap calls, 2026-10-14). The real
// Registry receivers still bind.
func TestTSStatedReceiverTypesNarrowCalls(t *testing.T) {
	ts := func(id, file string, kind core.SymbolKind, name, parent, sig, raw string, calls ...core.CallSite) core.SymbolRecord {
		qn := name
		if parent != "" {
			qn = parent + "." + name
		}
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "1", Language: "typescript", Kind: kind,
			Name: name, QualifiedName: qn, ParentSymbol: parent, Signature: sig, RawText: raw, CallSites: calls,
			Imports: []string{"./registry"}}
	}
	syms := []core.SymbolRecord{
		ts("r::Registry", "registry.ts", core.KindClass, "Registry", "", "class Registry", "class Registry {\n  get(s: string) { return s }\n}"),
		ts("r::Registry.get", "registry.ts", core.KindMethod, "get", "Registry", "get(s: string)", "get(s: string) { return s }"),
		ts("u::Ctx", "use.ts", core.KindInterface, "Ctx", "", "interface Ctx", "interface Ctx { seen: Map<string, number>; reg: Registry }"),
		ts("u::Ctx.seen", "use.ts", core.KindField, "seen", "Ctx", "seen: Map<string, number>", "seen: Map<string, number>"),
		ts("u::Ctx.reg", "use.ts", core.KindField, "reg", "Ctx", "reg: Registry", "reg: Registry"),
		ts("u::Fn", "use.ts", core.KindType, "Fn", "", "type Fn<T> = (\n  x: T,\n  ctx: Ctx\n) => void;", "type Fn<T> = (\n  x: T,\n  ctx: Ctx\n) => void;"),
		ts("u::pending", "use.ts", core.KindVariable, "pending", "", "pending = new WeakMap<object, number>()", "pending = new WeakMap<object, number>()"),
		ts("u::real", "use.ts", core.KindFunction, "real", "", "function real(r: Registry)", "function real(r: Registry) { return r.get('a') }",
			core.CallSite{Callee: "r.get", Line: 1, Col: 40, Argc: 1}),
		ts("u::localMap", "use.ts", core.KindFunction, "localMap", "", "function localMap()", "function localMap() { const m = new Map(); return m.get('x') }",
			core.CallSite{Callee: "m.get", Line: 1, Col: 50, Argc: 1}),
		ts("u::moduleMap", "use.ts", core.KindFunction, "moduleMap", "", "function moduleMap(o: object)", "function moduleMap(o: object) { return pending.get(o) }",
			core.CallSite{Callee: "pending.get", Line: 1, Col: 50, Argc: 1}),
		ts("u::chain", "use.ts", core.KindFunction, "chain", "", "function chain(ctx: Ctx)", "function chain(ctx: Ctx) { return ctx.seen.get('k') }",
			core.CallSite{Callee: "ctx.seen.get", Line: 1, Col: 45, Argc: 1}),
		ts("u::arrow", "use.ts", core.KindFunction, "arrow", "", "arrow: Fn<string> = (x, ctx) => {", "arrow: Fn<string> = (x, ctx) => {\n  ctx.seen.get(x)\n}",
			core.CallSite{Callee: "ctx.seen.get", Line: 2, Col: 12, Argc: 1}),
		ts("u::Holder", "use.ts", core.KindClass, "Holder", "", "class Holder", "class Holder {\n  private cache = new Map<string, string>()\n  a() { return this.cache.get('z') }\n}"),
		ts("u::Holder.cache", "use.ts", core.KindField, "cache", "Holder", "private cache = new Map<string, string>()", "private cache = new Map<string, string>()"),
		ts("u::Holder.a", "use.ts", core.KindMethod, "a", "Holder", "a()", "a() { return this.cache.get('z') }",
			core.CallSite{Callee: "cache.get", Line: 3, Col: 27, Argc: 1}),
	}
	got := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.To == "r::Registry.get" {
			got[e.From] = true
		}
	}
	if !got["u::real"] {
		t.Errorf("r: Registry must still bind to Registry.get; got %v", got)
	}
	for _, from := range []string{"u::localMap", "u::moduleMap", "u::chain", "u::arrow", "u::Holder.a"} {
		if got[from] {
			t.Errorf("%s calls a Map/WeakMap get, not Registry.get", from)
		}
	}
}

// One body assigning a name several classes (a benchmark's `const router =
// new RegExpRouter()` / `new TrieRouter()` in sibling callbacks): each call
// binds to the closest assignment above it. A function-valued field is the
// method (`match: typeof match = match`). A `name!: T` field is typed.
func TestTSReassignedReceiversFieldMethodsAndDefiniteFields(t *testing.T) {
	mk := func(id, file string, kind core.SymbolKind, name, parent, sig, raw string, span int, calls ...core.CallSite) core.SymbolRecord {
		qn := name
		if parent != "" {
			qn = parent + "." + name
		}
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "1", Language: "typescript", Kind: kind,
			Name: name, QualifiedName: qn, ParentSymbol: parent, Signature: sig, RawText: raw, CallSites: calls,
			Span: core.LineRange{Start: span, End: span + 20}, Imports: []string{"./r"}}
	}
	syms := []core.SymbolRecord{
		mk("r::A", "r.ts", core.KindClass, "A", "", "class A", "class A {\n  match(m: string) { return m }\n}", 1),
		mk("r::A.match", "r.ts", core.KindMethod, "match", "A", "match(m: string)", "match(m: string) { return m }", 2),
		mk("r::B", "r.ts", core.KindClass, "B", "", "class B", "class B {\n  match: typeof f = f\n}", 5),
		mk("r::B.match", "r.ts", core.KindField, "match", "B", "match: typeof f = f", "match: typeof f = f", 6),
		mk("r::App", "r.ts", core.KindClass, "App", "", "class App", "class App {\n  router!: A\n  run() { this.router.match('x') }\n}", 9),
		mk("r::App.router", "r.ts", core.KindField, "router", "App", "router!: A", "router!: A", 10),
		mk("r::App.run", "r.ts", core.KindMethod, "run", "App", "run()", "run() { this.router.match('x') }", 11,
			core.CallSite{Callee: "router.match", Line: 11, Col: 28, Argc: 1}),
		mk("b::<top-level>", "b.ts", core.KindFunction, "<top-level>", "", "",
			"bench(() => {\n  const router = new A()\n  router.match('x')\n})\nbench(() => {\n  const router = new B()\n  router.match('y')\n})\n", 1,
			core.CallSite{Callee: "router.match", Line: 3, Col: 10, Argc: 1},
			core.CallSite{Callee: "router.match", Line: 7, Col: 10, Argc: 1}),
	}
	lines := map[string]string{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls {
			lines[e.From+">"+e.To] = e.Lines
		}
	}
	for key, want := range map[string]string{
		"b::<top-level>>r::A.match": "3:10",
		"b::<top-level>>r::B.match": "7:10",
		"r::App.run>r::A.match":     "11:28",
	} {
		if lines[key] != want {
			t.Errorf("%s lines = %q, want %q (all %v)", key, lines[key], want, lines)
		}
	}
}
