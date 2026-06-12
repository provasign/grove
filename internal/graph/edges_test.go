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
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Caller@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "func Caller() {\n\t// Real() should be ignored in comments\n\t/* Real(1,2) */\n\ts := \"Real(literal)\"\n\t_ = s\n}"},
		{ID: "a.go::Real@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "func Real() {}"},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "a.go::Caller@sha", "a.go::Real@sha") {
		t.Fatalf("calls edge should not be emitted from comments or strings")
	}

	// Sanity: a real call must still produce the edge.
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Caller@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "func Caller() { Real() }"},
		{ID: "a.go::Real@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "func Real() {}"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.go::Caller@sha", "a.go::Real@sha") {
		t.Fatalf("expected calls edge for genuine call")
	}
}

func TestCallsAcrossImportedFiles(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "cmd/main.go::Run@sha", FilePath: "cmd/main.go", Language: "go", Kind: core.KindFunction, Name: "Run", QualifiedName: "Run",
			RawText: "func Run() { Login() }", Imports: []string{"github.com/provasign/grove/internal/auth"}},
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

// ─── TestsFor traverses tests edges and transitive callers ───────────────────

func TestTestsForFollowsTestsEdge(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "auth.go::Login@sha", FilePath: "auth.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login"},
		{ID: "auth_test.go::TestLogin@sha", FilePath: "auth_test.go", Language: "go", Kind: core.KindFunction, Name: "TestLogin", QualifiedName: "TestLogin"},
	}, 2)

	tests := g.TestsFor("Login")
	if len(tests) != 1 || tests[0].Name != "TestLogin" {
		t.Fatalf("expected TestLogin to cover Login, got: %+v", tests)
	}
}

func TestTestsForTransitiveCaller(t *testing.T) {
	// CallSites are populated as the parser would on real code: the closure
	// follows AST-evidenced call edges (0.95). Cross-file regex-fallback
	// edges (0.6) are deliberately below the traversal cut — see
	// TestTestsForSkipsLowConfidenceEdges.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "auth.go::Login@sha", FilePath: "auth.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			RawText: "func Login() {}"},
		{ID: "handler.go::Handle@sha", FilePath: "handler.go", Language: "go", Kind: core.KindFunction, Name: "Handle", QualifiedName: "Handle",
			RawText: "func Handle() { Login() }", Imports: []string{"github.com/provasign/grove/auth"},
			CallSites: []core.CallSite{{Callee: "Login", Line: 1}}},
		{ID: "service.go::Serve@sha", FilePath: "service.go", Language: "go", Kind: core.KindFunction, Name: "Serve", QualifiedName: "Serve",
			RawText: "func Serve() { Handle() }", Imports: []string{"github.com/provasign/grove/handler"},
			CallSites: []core.CallSite{{Callee: "Handle", Line: 1}}},
		{ID: "handler_test.go::TestHandle@sha", FilePath: "handler_test.go", Language: "go", Kind: core.KindFunction, Name: "TestHandle", QualifiedName: "TestHandle"},
		{ID: "service_test.go::TestServe@sha", FilePath: "service_test.go", Language: "go", Kind: core.KindFunction, Name: "TestServe", QualifiedName: "TestServe"},
	}, 3)

	tests := g.TestsFor("Login")
	foundHandle := false
	foundServe := false
	for _, t2 := range tests {
		if t2.Name == "TestHandle" {
			foundHandle = true
		}
		if t2.Name == "TestServe" {
			foundServe = true
		}
	}
	if !foundHandle || !foundServe {
		t.Fatalf("expected transitive downstream tests for Login, got: %+v", tests)
	}
}

// TestTestsEdgeScopedToImports guards against H3: TestOpen in one package
// must not produce a tests edge to a same-named symbol in an unrelated,
// non-imported package.
func TestTestsEdgeScopedToImports(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "internal/store/store.go::Open@sha", FilePath: "internal/store/store.go", Language: "go", Kind: core.KindFunction, Name: "Open", QualifiedName: "Open"},
		{ID: "pkg/grove/grove.go::Open@sha", FilePath: "pkg/grove/grove.go", Language: "go", Kind: core.KindFunction, Name: "Open", QualifiedName: "Open"},
		{ID: "internal/store/store_test.go::TestOpen@sha", FilePath: "internal/store/store_test.go", Language: "go", Kind: core.KindFunction, Name: "TestOpen", QualifiedName: "TestOpen"},
	}, 3)

	if !hasEdge(g, core.EdgeTests, "internal/store/store_test.go::TestOpen@sha", "internal/store/store.go::Open@sha") {
		t.Fatal("missing tests edge to same-package Open")
	}
	if hasEdge(g, core.EdgeTests, "internal/store/store_test.go::TestOpen@sha", "pkg/grove/grove.go::Open@sha") {
		t.Fatal("tests edge leaked to non-imported package's Open")
	}
}

// TestRustAnnotatedTestGetsSameFileTestsEdge: Rust tests live in the same
// file as production code (mod tests) and are identified by #[test], not by
// file naming. Call-site evidence links them to what they exercise.
func TestRustAnnotatedTestGetsSameFileTestsEdge(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/lib.rs::parse_input@sha", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindFunction, Name: "parse_input", QualifiedName: "parse_input"},
		{ID: "src/lib.rs::handles_empty@sha", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindFunction, Name: "handles_empty", QualifiedName: "handles_empty",
			Annotations: []string{"#[test]"},
			CallSites:   []core.CallSite{{Callee: "parse_input", Line: 42}}},
	}, 1)

	if !hasEdge(g, core.EdgeTests, "src/lib.rs::handles_empty@sha", "src/lib.rs::parse_input@sha") {
		t.Fatal("missing tests edge from #[test] fn to called function")
	}
}

// ─── ICR / conflict ─────────────────────────────────────────────────────────

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
