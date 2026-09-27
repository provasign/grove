package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// click wide bed (2026-09-27): tests take `runner` from a conftest fixture
// `def runner(request): return CliRunner()`. Untyped, runner.invoke(cli)
// bound every invoke method in the package (280 false Command.invoke
// callers). The fixture's value types the parameter.
func clickLikeSymbols() []core.SymbolRecord {
	return []core.SymbolRecord{
		{ID: "src/click/testing.py::CliRunner", FilePath: "src/click/testing.py", Language: "python", Kind: core.KindClass, Name: "CliRunner", QualifiedName: "CliRunner"},
		{ID: "src/click/testing.py::CliRunner.invoke", FilePath: "src/click/testing.py", Language: "python", Kind: core.KindMethod, Name: "invoke", QualifiedName: "CliRunner.invoke", ParentSymbol: "CliRunner",
			Signature: "def invoke(self, cli, args=None, **extra):", RawText: "def invoke(self, cli, args=None, **extra):\n    pass", Span: core.LineRange{Start: 10, End: 11}},
		{ID: "src/click/core.py::Command", FilePath: "src/click/core.py", Language: "python", Kind: core.KindClass, Name: "Command", QualifiedName: "Command"},
		{ID: "src/click/core.py::Command.invoke", FilePath: "src/click/core.py", Language: "python", Kind: core.KindMethod, Name: "invoke", QualifiedName: "Command.invoke", ParentSymbol: "Command",
			Signature: "def invoke(self, ctx: Context) -> t.Any:", RawText: "def invoke(self, ctx: Context) -> t.Any:\n    pass", Span: core.LineRange{Start: 20, End: 21}},
		{ID: "src/click/core.py::Context", FilePath: "src/click/core.py", Language: "python", Kind: core.KindClass, Name: "Context", QualifiedName: "Context"},
		{ID: "src/click/core.py::Context.invoke", FilePath: "src/click/core.py", Language: "python", Kind: core.KindMethod, Name: "invoke", QualifiedName: "Context.invoke", ParentSymbol: "Context",
			Signature: "def invoke(self, callback, *args, **kwargs):", RawText: "def invoke(self, callback, *args, **kwargs):\n    pass", Span: core.LineRange{Start: 30, End: 31}},
		{ID: "tests/conftest.py::runner", FilePath: "tests/conftest.py", Language: "python", Kind: core.KindFunction, Name: "runner", QualifiedName: "runner",
			Annotations: []string{`pytest.fixture(scope="function")`}, Signature: "def runner(request):",
			RawText: "def runner(request):\n    return CliRunner()", Span: core.LineRange{Start: 6, End: 8}, Imports: []string{"click.testing"}},
	}
}

func TestPyFixtureParameterTypesReceiver(t *testing.T) {
	syms := append(clickLikeSymbols(), core.SymbolRecord{
		ID: "tests/test_args.py::test_nargs", FilePath: "tests/test_args.py", Language: "python", Kind: core.KindFunction, Name: "test_nargs", QualifiedName: "test_nargs",
		Signature: "def test_nargs(runner):", RawText: "def test_nargs(runner):\n    result = runner.invoke(copy, [])", Span: core.LineRange{Start: 1, End: 2},
		Imports:   []string{"click"},
		CallSites: []core.CallSite{{Callee: "runner.invoke", Line: 2, Argc: 2}},
	})
	g := New()
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeCalls, "tests/test_args.py::test_nargs", "src/click/testing.py::CliRunner.invoke") {
		t.Fatal("fixture-typed runner.invoke lost its CliRunner.invoke edge")
	}
	for _, wrong := range []string{"src/click/core.py::Command.invoke", "src/click/core.py::Context.invoke"} {
		if hasEdge(g, core.EdgeCalls, "tests/test_args.py::test_nargs", wrong) {
			t.Fatalf("fixture-typed runner.invoke still bound %s", wrong)
		}
	}
}

// Python has no overloading: ctx.invoke(other_cmd, arg=42) passes two
// arguments, which Command.invoke(self, ctx) cannot take. A call that
// unpacks (**kw) has an unknown count and keeps every candidate; so does a
// decorated def whose calling convention the decorator may change.
func TestPyArityRulesOutMethodsThatCannotTakeTheCall(t *testing.T) {
	syms := append(clickLikeSymbols(),
		core.SymbolRecord{ID: "src/click/extra.py::two", FilePath: "src/click/extra.py", Language: "python", Kind: core.KindFunction, Name: "two", QualifiedName: "two",
			Signature: "def two(ctx):", Imports: []string{"click.core"}, RawText: "def two(ctx):\n    return ctx.invoke(other_cmd, arg=42)", Span: core.LineRange{Start: 1, End: 2},
			CallSites: []core.CallSite{{Callee: "ctx.invoke", Line: 2, Argc: 2}}},
		core.SymbolRecord{ID: "src/click/extra.py::splat", FilePath: "src/click/extra.py", Language: "python", Kind: core.KindFunction, Name: "splat", QualifiedName: "splat",
			Signature: "def splat(ctx):", Imports: []string{"click.core"}, RawText: "def splat(ctx):\n    return ctx.invoke(cb, **params)", Span: core.LineRange{Start: 4, End: 5},
			CallSites: []core.CallSite{{Callee: "ctx.invoke", Line: 5, Argc: 2}}},
	)
	g := New()
	g.Replace(syms, 3)
	if hasEdge(g, core.EdgeCalls, "src/click/extra.py::two", "src/click/core.py::Command.invoke") {
		t.Fatal("a two-argument call bound Command.invoke(self, ctx)")
	}
	if !hasEdge(g, core.EdgeCalls, "src/click/extra.py::two", "src/click/core.py::Context.invoke") {
		t.Fatal("the variadic Context.invoke lost its edge")
	}
	if !hasEdge(g, core.EdgeCalls, "src/click/extra.py::splat", "src/click/core.py::Command.invoke") &&
		!hasEdge(g, core.EdgeCalls, "src/click/extra.py::splat", "src/click/core.py::Context.invoke") {
		t.Fatal("an unpacking call must keep its candidates (count unknown)")
	}
}

func TestPyArity(t *testing.T) {
	for _, tc := range []struct {
		sig      string
		ann      []string
		min, max int
		ok       bool
	}{
		{"def invoke(self, ctx: Context) -> t.Any:", nil, 1, 1, true},
		{"def invoke(self, callback, *args, **kwargs):", nil, 1, -1, true},
		{"def invoke(self, cli, args=None, input=None):", nil, 1, 3, true},
		{"def f(self, a, *, b, c=1):", nil, 2, 3, true},
		{"def f(a, b):", []string{"staticmethod"}, 2, 2, true},
		{"def f(self, ctx):", []string{"click.pass_context"}, 0, 0, false},
	} {
		s := core.SymbolRecord{Language: "python", Kind: core.KindMethod, ParentSymbol: "C", RawText: tc.sig + "\n    pass", Annotations: tc.ann}
		min, max, ok := pyArity(&s)
		if ok != tc.ok || (ok && (min != tc.min || max != tc.max)) {
			t.Errorf("%s %v: got (%d, %d, %v), want (%d, %d, %v)", tc.sig, tc.ann, min, max, ok, tc.min, tc.max, tc.ok)
		}
	}
}

// An unannotated override is compatible with an annotated declaration:
// click's `def invoke(self, ctx)` inside a test overrides
// `def invoke(self, ctx: Context)`.
func TestPyUnannotatedParamsAreUnknownNotNames(t *testing.T) {
	if got := pyParamTypeTokens("self, ctx"); got != nil {
		t.Fatalf("unannotated params must be neutral, got %v", got)
	}
	if got := pyParamTypeTokens("self, ctx: Context"); len(got) != 1 || got[0] != "Context" {
		t.Fatalf("annotated param: got %v", got)
	}
}
