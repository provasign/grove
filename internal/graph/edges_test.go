package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// ─── extends / implements / uses-type ────────────────────────────────────────

func TestExtendsEdgeTypeScript(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/auth.ts::Base@sha", FilePath: "src/auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "src/auth.ts::Child@sha", FilePath: "src/auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Child", QualifiedName: "Child", Signature: "class Child extends Base"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "src/auth.ts::Child@sha", "src/auth.ts::Base@sha") {
		t.Fatalf("missing extends edge Child→Base")
	}
}

func TestImplementsEdgeJava(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Service.java::Runnable@sha", FilePath: "Service.java", Language: "java", Kind: core.KindInterface, Name: "Runnable", QualifiedName: "Runnable"},
		{ID: "Service.java::MyService@sha", FilePath: "Service.java", Language: "java", Kind: core.KindClass, Name: "MyService", QualifiedName: "MyService", Signature: "public class MyService implements Runnable"},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "Service.java::MyService@sha", "Service.java::Runnable@sha") {
		t.Fatalf("missing implements edge MyService→Runnable")
	}
}

// Generic bounds must not emit bogus extends edges, and generic arguments in
// the extends clause must still resolve to the base class (jackson style:
// `class ValueSer<T extends Number> extends JsonSerializer<T>`).
func TestExtendsEdgeJavaGenerics(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.java::JsonSerializer@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "JsonSerializer", QualifiedName: "JsonSerializer"},
		{ID: "a.java::Number@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "Number", QualifiedName: "Number"},
		{ID: "a.java::ValueSer@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "ValueSer", QualifiedName: "ValueSer",
			Signature: "public class ValueSer<T extends Number> extends JsonSerializer<T>"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "a.java::ValueSer@sha", "a.java::JsonSerializer@sha") {
		t.Fatalf("missing extends edge ValueSer→JsonSerializer through generics")
	}
	if hasEdge(g, core.EdgeExtends, "a.java::ValueSer@sha", "a.java::Number@sha") {
		t.Fatalf("generic bound `T extends Number` must not emit an extends edge")
	}
}

func TestPythonClassBaseExtends(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "models.py::Base@sha", FilePath: "models.py", Language: "python", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "models.py::User@sha", FilePath: "models.py", Language: "python", Kind: core.KindClass, Name: "User", QualifiedName: "User",
			RawText: "class User(Base):\n    pass\n"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "models.py::User@sha", "models.py::Base@sha") {
		t.Fatalf("missing python extends edge")
	}
}

func TestRustImplForTrait(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "lib.rs::Display@sha", FilePath: "lib.rs", Language: "rust", Kind: core.KindTrait, Name: "Display", QualifiedName: "Display"},
		{ID: "lib.rs::Point@sha", FilePath: "lib.rs", Language: "rust", Kind: core.KindStruct, Name: "Point", QualifiedName: "Point",
			RawText: "struct Point { x: i32 }\nimpl Display for Point { fn fmt() {} }"},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "lib.rs::Point@sha", "lib.rs::Display@sha") {
		t.Fatalf("missing rust implements edge")
	}
}

func TestGoStructEmbedding(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Reader@sha", FilePath: "a.go", Language: "go", Kind: core.KindStruct, Name: "Reader", QualifiedName: "Reader"},
		{ID: "a.go::Wrapper@sha", FilePath: "a.go", Language: "go", Kind: core.KindStruct, Name: "Wrapper", QualifiedName: "Wrapper",
			RawText: "type Wrapper struct {\n\tReader\n\tname string\n}"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "a.go::Wrapper@sha", "a.go::Reader@sha") {
		t.Fatalf("missing go embedding extends edge")
	}
}

func TestUsesTypeScopedToImports(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		// Importer file imports "./auth" — so symbols in auth.ts are in scope.
		{ID: "main.ts::handle@sha", FilePath: "main.ts", Language: "typescript",
			Kind: core.KindFunction, Name: "handle", QualifiedName: "handle",
			Signature: "function handle(u: User): Session", Imports: []string{"./auth"}},
		{ID: "auth.ts::User@sha", FilePath: "auth.ts", Language: "typescript", Kind: core.KindClass, Name: "User", QualifiedName: "User"},
		{ID: "auth.ts::Session@sha", FilePath: "auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Session", QualifiedName: "Session"},
		// Out-of-scope type with the same simple name: must NOT produce an edge.
		{ID: "billing.ts::Session@sha", FilePath: "billing.ts", Language: "typescript", Kind: core.KindClass, Name: "Session", QualifiedName: "Session"},
	}, 3)

	if !hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "auth.ts::User@sha") {
		t.Fatalf("missing uses-type edge handle→User (imported file)")
	}
	if !hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "auth.ts::Session@sha") {
		t.Fatalf("missing uses-type edge handle→Session (imported file)")
	}
	if hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "billing.ts::Session@sha") {
		t.Fatalf("uses-type edge MUST NOT cross to non-imported file")
	}
}

// ─── calls: scoping + comment/string stripping ───────────────────────────────

func TestCallsRespectsCommentAndStringStripping(t *testing.T) {
	// The regex fallback only serves languages without AST call-site
	// extraction; its comment/string stripping is exercised with one of
	// those. For AST languages an empty CallSites list is authoritative.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.rb::Caller@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "void Caller() {\n\t// Real() should be ignored in comments\n\t/* Real(1,2) */\n\tchar *s = \"Real(literal)\";\n}"},
		{ID: "a.rb::Real@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "void Real() {}"},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "a.rb::Caller@sha", "a.rb::Real@sha") {
		t.Fatalf("calls edge should not be emitted from comments or strings")
	}

	// Sanity: a real call must still produce the edge (fallback language).
	g.Replace([]core.SymbolRecord{
		{ID: "a.rb::Caller@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "void Caller() { Real(); }"},
		{ID: "a.rb::Real@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "void Real() {}"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.rb::Caller@sha", "a.rb::Real@sha") {
		t.Fatalf("expected calls edge for genuine call")
	}

	// AST language with an extracted call site still edges normally.
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Caller@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "func Caller() { Real() }", CallSites: []core.CallSite{{Callee: "Real", Line: 1}}},
		{ID: "a.go::Real@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "func Real() {}"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.go::Caller@sha", "a.go::Real@sha") {
		t.Fatalf("expected calls edge from AST call site")
	}
}

func TestCallsAcrossImportedFiles(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "cmd/main.go::Run@sha", FilePath: "cmd/main.go", Language: "go", Kind: core.KindFunction, Name: "Run", QualifiedName: "Run",
			RawText: "func Run() { Login() }", Imports: []string{"github.com/provasign/grove/internal/auth"},
			CallSites: []core.CallSite{{Callee: "Login", Line: 1}}},
		{ID: "internal/auth/auth.go::Login@sha", FilePath: "internal/auth/auth.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			RawText: "func Login() {}"},
		// Same name in a non-imported file: must NOT be linked.
		{ID: "internal/billing/billing.go::Login@sha", FilePath: "internal/billing/billing.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			RawText: "func Login() {}"},
	}, 3)

	if !hasEdge(g, core.EdgeCalls, "cmd/main.go::Run@sha", "internal/auth/auth.go::Login@sha") {
		t.Fatalf("missing calls edge to imported package")
	}
	if hasEdge(g, core.EdgeCalls, "cmd/main.go::Run@sha", "internal/billing/billing.go::Login@sha") {
		t.Fatalf("calls edge MUST NOT cross to non-imported file")
	}
}

// TestPythonBareCallThroughUnresolvedParamSuppressed guards against the
// flask@36e4a824 false-edge bug: a bare call through a Callable parameter
// (`loads: t.Callable = json.loads`, then `loads(value)`) must not resolve
// by matching the parameter's NAME against an unrelated same-named function
// elsewhere in scope — the caller's own from_prefixed_env never calls
// TaggedJSONSerializer.loads, it calls whatever was passed as `loads`.
func TestPythonBareCallThroughUnresolvedParamSuppressed(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "config.py::from_prefixed_env@sha", FilePath: "config.py", Language: "python",
			Kind: core.KindMethod, Name: "from_prefixed_env", QualifiedName: "Config.from_prefixed_env",
			ParentSymbol: "Config",
			RawText:      "def from_prefixed_env(self, prefix=\"FLASK\", *, loads=json.loads):\n    value = loads(raw)\n",
			CallSites:    []core.CallSite{{Callee: "loads", Line: 2}}},
		{ID: "tag.py::loads@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindMethod, Name: "loads", QualifiedName: "TaggedJSONSerializer.loads",
			ParentSymbol: "TaggedJSONSerializer", RawText: "def loads(self, value):\n    pass\n"},
	}, 2)
	if hasEdge(g, core.EdgeCalls, "config.py::from_prefixed_env@sha", "tag.py::loads@sha") {
		t.Fatalf("bare call through a shadowing parameter name must not resolve to an unrelated same-named function")
	}
}

// TestPythonTypedFactoryParamStillResolves guards the companion case: a
// parameter annotated as a class reference (`tag_class: type[JSONTag]`) used
// as a factory (`tag_class(self)`) is a genuine, statically-inferable
// constructor call and must keep resolving — the fix above only suppresses
// calls through parameters localTypes could NOT positively type.
func TestPythonTypedFactoryParamStillResolves(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "tag.py::register@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindMethod, Name: "register", QualifiedName: "TaggedJSONSerializer.register",
			ParentSymbol: "TaggedJSONSerializer",
			RawText:      "def register(self, tag_class: type[JSONTag], force=False):\n    tag_class(self)\n",
			CallSites:    []core.CallSite{{Callee: "tag_class", Line: 2}}},
		{ID: "tag.py::JSONTag@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindClass, Name: "JSONTag", QualifiedName: "JSONTag"},
		{ID: "tag.py::JSONTag.__init__@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindConstructor, Name: "__init__", QualifiedName: "JSONTag.__init__",
			ParentSymbol: "JSONTag", RawText: "def __init__(self):\n    pass\n"},
	}, 3)
	if !hasEdge(g, core.EdgeCalls, "tag.py::register@sha", "tag.py::JSONTag.__init__@sha") {
		t.Fatalf("expected constructor edge through a type[X]-typed factory parameter to still resolve")
	}
}

// TestPythonModuleGlobalProxySetattr: a test assigns `g.foo = value` where
// `g` is a module-level global typed as a proxy stub (`_AppCtxGlobalsProxy`)
// that inherits the class carrying the real __setattr__ (`_AppCtxGlobals`).
// Attribute assignment has no call site and the declared type is a
// type-checking stub, so resolving it exercises module-global type inference
// plus a base-class walk. Asserts the calls edge test → __setattr__.
func TestPythonModuleGlobalProxySetattr(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		// Module-level global: `g: _AppCtxGlobalsProxy = LocalProxy(...)`.
		{ID: "globals.py::g@sha", FilePath: "src/flask/globals.py", Language: "python",
			Kind: core.KindVariable, Name: "g", QualifiedName: "g",
			Signature: "g: _AppCtxGlobalsProxy"},
		// Type-checking proxy stub, inherits the real class.
		{ID: "globals.py::_AppCtxGlobalsProxy@sha", FilePath: "src/flask/globals.py", Language: "python",
			Kind: core.KindClass, Name: "_AppCtxGlobalsProxy", QualifiedName: "_AppCtxGlobalsProxy",
			Signature: "class _AppCtxGlobalsProxy(ProxyMixin[_AppCtxGlobals], _AppCtxGlobals)"},
		// The class carrying the custom __setattr__.
		{ID: "ctx.py::_AppCtxGlobals@sha", FilePath: "src/flask/ctx.py", Language: "python",
			Kind: core.KindClass, Name: "_AppCtxGlobals", QualifiedName: "_AppCtxGlobals"},
		{ID: "ctx.py::_AppCtxGlobals.__setattr__@sha", FilePath: "src/flask/ctx.py", Language: "python",
			Kind: core.KindMethod, Name: "__setattr__", QualifiedName: "_AppCtxGlobals.__setattr__",
			ParentSymbol: "_AppCtxGlobals", RawText: "def __setattr__(self, name, value):\n    self.__dict__[name] = value\n"},
		// A test that assigns through the global. Imports flask, NOT flask.ctx.
		{ID: "test_basic.py::test_g@sha", FilePath: "tests/test_basic.py", Language: "python",
			Kind: core.KindFunction, Name: "test_g", QualifiedName: "test_g",
			Imports: []string{"flask"},
			RawText: "def test_g():\n    flask.g.foo = 42\n"},
	}, 5)
	if !hasEdge(g, core.EdgeCalls, "test_basic.py::test_g@sha", "ctx.py::_AppCtxGlobals.__setattr__@sha") {
		t.Fatalf("expected calls edge from test through module-global proxy to _AppCtxGlobals.__setattr__")
	}
}

func TestComputeICRNoSeedsHasZeroConfidence(t *testing.T) {
	g := New()
	icr := g.ComputeICR("nonexistent-feature")
	if icr.Confidence > 0.5 {
		t.Fatalf("expected low confidence for empty ICR, got %v", icr.Confidence)
	}
}

func TestDetectConflictsFileOverlap(t *testing.T) {
	a := core.IsolatedChangeRegion{ExclusiveFiles: []string{"a.go", "shared.go"}}
	b := core.IsolatedChangeRegion{ExclusiveFiles: []string{"b.go", "shared.go"}}
	result := DetectConflicts(a, b)
	if !result.Conflicts || len(result.OverlapFiles) != 1 || result.OverlapFiles[0] != "shared.go" {
		t.Fatalf("expected file overlap conflict on shared.go, got %+v", result)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func hasEdge(g *CodeGraph, t core.EdgeType, from, to string) bool {
	_, edges := g.Snapshot()
	for _, e := range edges {
		if e.Type == t && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

// ─── benchmarks ──────────────────────────────────────────────────────────────

func BenchmarkBuildEdges10K(b *testing.B) {
	symbols := make([]core.SymbolRecord, 0, 10_000)
	for i := 0; i < 1000; i++ {
		file := "pkg/file" + itoa(i) + ".go"
		for j := 0; j < 10; j++ {
			symbols = append(symbols, core.SymbolRecord{
				ID:            file + "::Fn" + itoa(j) + "@sha",
				FilePath:      file,
				Language:      "go",
				Kind:          core.KindFunction,
				Name:          "Fn" + itoa(j),
				QualifiedName: "Fn" + itoa(j),
				RawText:       "func Fn" + itoa(j) + "() { Fn" + itoa((j+1)%10) + "() }",
			})
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildEdges(symbols)
	}
}

func BenchmarkSearch10K(b *testing.B) {
	symbols := make([]core.SymbolRecord, 0, 10_000)
	for i := 0; i < 10_000; i++ {
		symbols = append(symbols, core.SymbolRecord{
			ID:            "f.go::Sym" + itoa(i) + "@s",
			FilePath:      "f.go",
			Kind:          core.KindFunction,
			Name:          "Sym" + itoa(i),
			QualifiedName: "Sym" + itoa(i),
		})
	}
	g := New()
	g.Replace(symbols, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.Search("sym42", 20)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b strings.Builder
	for i > 0 {
		b.WriteByte(byte('0' + i%10))
		i /= 10
	}
	// reverse
	out := []byte(b.String())
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return string(out)
}
// @property method blueprints, and never to a plain (non-property) method.
func TestBuildCalls_PropertyReadEdges(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "ctx.py::AppContext.f@1", FilePath: "ctx.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "f", QualifiedName: "AppContext.f", ParentSymbol: "AppContext",
		Imports:   []string{"wrappers"},
		AttrSites: []core.CallSite{{Callee: "request.blueprints", Line: 3}, {Callee: "request.environ", Line: 4}},
	}
	prop := core.SymbolRecord{
		ID: "wrappers.py::Request.blueprints@10", FilePath: "wrappers.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "blueprints", QualifiedName: "Request.blueprints", ParentSymbol: "Request",
		Annotations: []string{"property"},
	}
	plain := core.SymbolRecord{
		ID: "wrappers.py::Request.environ@20", FilePath: "wrappers.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "environ", QualifiedName: "Request.environ", ParentSymbol: "Request",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, prop, plain})
	var gotProp, gotPlain bool
	for _, e := range edges {
		if e.Type != core.EdgeCalls || e.From != caller.ID {
			continue
		}
		switch e.To {
		case prop.ID:
			gotProp = true
			if e.Confidence > 0.75 {
				t.Errorf("property edge confidence = %v, want reduced", e.Confidence)
			}
		case plain.ID:
			gotPlain = true
		}
	}
	if !gotProp {
		t.Error("expected property-read edge to Request.blueprints")
	}
	if gotPlain {
		t.Error("attribute access must not edge to non-property methods")
	}
}
