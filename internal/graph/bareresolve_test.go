package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// chi (2026-10-12): URLParam is a package function and Context.URLParam a
// method, both in context.go. The ambiguity's own suggested retry, the bare
// name scoped to context.go, stayed ambiguous, so the function was
// unreachable. A bare name plus a file names the top-level function there.
func TestBareNameWithFileNamesTheTopLevelFunction(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "context.go::URLParam@s", FilePath: "context.go", Language: "go", Kind: core.KindFunction,
			Name: "URLParam", QualifiedName: "URLParam", Signature: "func URLParam(r *http.Request, key string) string"},
		{ID: "context.go::Context@s", FilePath: "context.go", Language: "go", Kind: core.KindStruct,
			Name: "Context", QualifiedName: "Context", Signature: "type Context struct"},
		{ID: "context.go::Context.URLParam@s", FilePath: "context.go", Language: "go", Kind: core.KindMethod,
			Name: "URLParam", QualifiedName: "Context.URLParam", ParentSymbol: "Context",
			Signature: "func (x *Context) URLParam(key string) string"},
		{ID: "mux_test.go::TestRoute@s", FilePath: "mux_test.go", Language: "go", Kind: core.KindFunction,
			Name: "TestRoute", QualifiedName: "TestRoute", Signature: "func TestRoute(t *testing.T)",
			RawText:   "func TestRoute(t *testing.T) { id := URLParam(r, \"id\") }",
			CallSites: []core.CallSite{{Callee: "URLParam", Line: 1, Argc: 2}}},
	}, 1)

	if _, err := g.ChangeImpact("URLParam"); err == nil || !strings.Contains(err.Error(), "is ambiguous") {
		t.Fatalf("a bare name with a function and a method must stay ambiguous, got %v", err)
	}
	r, err := g.ChangeImpactScoped("URLParam", "context.go")
	if err != nil {
		t.Fatalf("bare name + file: %v", err)
	}
	if len(r.Declarations) != 1 || r.Declarations[0].QualifiedName != "URLParam" {
		t.Fatalf("want the package function, got %+v", r.Declarations)
	}
	r, err = g.ChangeImpactScoped("Context.URLParam", "context.go")
	if err != nil || len(r.Declarations) == 0 || r.Declarations[0].QualifiedName != "Context.URLParam" {
		t.Fatalf("Type.method + file must still name the method: %+v %v", r, err)
	}
}

// hono (2026-10-12): `request` is a variable in a benchmark script and a
// field of Hono and of FetchEvent. The module-level variable won silently.
// The mix is ambiguous unless a file says which; each listed retry resolves.
func TestBareDataMemberMixIsAmbiguousUnlessScoped(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "bench/get-path.ts::request@s", FilePath: "bench/get-path.ts", Language: "typescript", Kind: core.KindVariable,
			Name: "request", QualifiedName: "request", Span: core.LineRange{Start: 5, End: 5}},
		{ID: "src/hono-base.ts::Hono@s", FilePath: "src/hono-base.ts", Language: "typescript", Kind: core.KindClass,
			Name: "Hono", QualifiedName: "Hono", Span: core.LineRange{Start: 100, End: 600}},
		{ID: "src/hono-base.ts::Hono.request@s", FilePath: "src/hono-base.ts", Language: "typescript", Kind: core.KindField,
			Name: "request", QualifiedName: "Hono.request", ParentSymbol: "Hono", Span: core.LineRange{Start: 501, End: 520}},
		{ID: "src/sw.ts::FetchEvent@s", FilePath: "src/sw.ts", Language: "typescript", Kind: core.KindInterface,
			Name: "FetchEvent", QualifiedName: "FetchEvent", Span: core.LineRange{Start: 10, End: 14}},
		{ID: "src/sw.ts::FetchEvent.request@s", FilePath: "src/sw.ts", Language: "typescript", Kind: core.KindField,
			Name: "request", QualifiedName: "FetchEvent.request", ParentSymbol: "FetchEvent", Span: core.LineRange{Start: 11, End: 11}},
	}, 1)

	_, err := g.ChangeImpact("request")
	if err == nil || !strings.Contains(err.Error(), "is ambiguous — 3 candidates") {
		t.Fatalf("want a 3-candidate ambiguity, got %v", err)
	}
	for _, want := range []string{"  request  (bench/get-path.ts:5)", "  Hono.request  (src/hono-base.ts:501)", "  FetchEvent.request  (src/sw.ts:11)"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("candidate line %q missing:\n%v", want, err)
		}
	}
	// Each retry the error offers resolves to exactly its declaration.
	for _, retry := range []struct{ query, file, want string }{
		{"request", "bench/get-path.ts", "request"},
		{"Hono.request", "src/hono-base.ts", "Hono.request"},
		{"FetchEvent.request", "src/sw.ts", "FetchEvent.request"},
	} {
		r, err := g.ChangeImpactScoped(retry.query, retry.file)
		if err != nil {
			t.Fatalf("retry %v: %v", retry, err)
		}
		if len(r.Declarations) == 0 || r.Declarations[0].QualifiedName != retry.want {
			t.Fatalf("retry %v resolved to %+v", retry, r.Declarations)
		}
	}
}
