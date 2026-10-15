package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Python function values (flask oracle review 2026-10-17): a constructor
// keyword stored on self and called later, a registry iterated and called
// through an identity helper bound per call site, a function handed to a
// library call, and stores from tests left out.
func TestPythonValueFlow(t *testing.T) {
	g := New()
	imports := []string{"config.py", "app.py", "tests/test_app.py"}
	sym := func(kind core.SymbolKind, file, name, parent, raw string, start, end int, sites ...core.CallSite) core.SymbolRecord {
		s := pySym(kind, file, name, parent, raw, start, end)
		s.CallSites = sites
		s.Imports = imports
		return s
	}
	syms := []core.SymbolRecord{
		sym(core.KindClass, "config.py", "ConfigAttribute", "", "class ConfigAttribute:\n    pass", 1, 12),
		sym(core.KindConstructor, "config.py", "__init__", "ConfigAttribute",
			"def __init__(self, name: str, get_converter: t.Callable | None = None) -> None:\n        self.get_converter = get_converter", 2, 3),
		sym(core.KindMethod, "config.py", "__get__", "ConfigAttribute",
			"def __get__(self, obj, owner=None):\n        return self.get_converter(obj)", 4, 5,
			core.CallSite{Callee: "self.get_converter", Line: 5, Col: 21, Argc: 1, Args: []string{"obj"}}),
		sym(core.KindFunction, "app.py", "_make_timedelta", "", "def _make_timedelta(value):\n    return value", 1, 2),
		sym(core.KindFunction, "app.py", "_default_ctx", "", "def _default_ctx():\n    return {}", 3, 4),
		sym(core.KindFunction, "app.py", "_other_ctx", "", "def _other_ctx():\n    return {}", 5, 6),
		sym(core.KindFunction, "app.py", "_json_default", "", "def _json_default(o):\n    return str(o)", 7, 8),
		sym(core.KindClass, "app.py", "App", "",
			"class App:\n    lifetime = ConfigAttribute(\"LIFETIME\", get_converter=_make_timedelta)\n    def __init__(self):\n        self.processors = {None: [_default_ctx]}", 10, 40),
		sym(core.KindConstructor, "app.py", "__init__", "App", "def __init__(self):\n        self.processors = {None: [_default_ctx]}", 12, 13),
		sym(core.KindMethod, "app.py", "ensure_sync", "App", "def ensure_sync(self, func: t.Callable) -> t.Callable:\n        return func", 14, 15),
		sym(core.KindMethod, "app.py", "update", "App",
			"def update(self, ctx):\n        for func in self.processors[None]:\n            ctx.update(self.ensure_sync(func)())", 16, 18,
			core.CallSite{Callee: "ctx.update", Line: 18, Col: 17, Argc: 1},
			core.CallSite{Callee: "self.ensure_sync", Line: 18, Col: 29, Argc: 1, Args: []string{"func"}}),
		sym(core.KindMethod, "app.py", "other", "App",
			"def other(self):\n        return self.ensure_sync(_other_ctx)()", 19, 20,
			core.CallSite{Callee: "self.ensure_sync", Line: 20, Col: 21, Argc: 1, Args: []string{"_other_ctx"}}),
		sym(core.KindFunction, "app.py", "dumps", "",
			"def dumps(obj, **kwargs: t.Any) -> str:\n    kwargs.setdefault(\"default\", _json_default)\n    return _json.dumps(obj, **kwargs)", 41, 43,
			core.CallSite{Callee: "kwargs.setdefault", Line: 42, Col: 12, Argc: 2, Args: []string{"#String", "_json_default"}},
			core.CallSite{Callee: "_json.dumps", Line: 43, Col: 18, Argc: 2, Args: []string{"obj", ""}}),
		sym(core.KindFunction, "tests/test_app.py", "fake", "", "def fake(value):\n    return 1", 1, 2),
		sym(core.KindFunction, "tests/test_app.py", "test_it", "",
			"def test_it():\n    ConfigAttribute(\"X\", get_converter=fake)", 3, 4,
			core.CallSite{Callee: "ConfigAttribute", Line: 4, Col: 5, Argc: 2, Args: []string{"#String", ""}}),
	}
	g.Replace(syms, 3)
	for _, want := range [][2]string{
		{"config.py::ConfigAttribute.__get__@b", "app.py::_make_timedelta@b"},
		{"app.py::App.update@b", "app.py::_default_ctx@b"},
		{"app.py::App.other@b", "app.py::_other_ctx@b"},
		{"app.py::dumps@b", "app.py::_json_default@b"},
	} {
		if !hasEdge(g, core.EdgeCalls, want[0], want[1]) {
			t.Errorf("missing %s -> %s", want[0], want[1])
		}
	}
	for _, wrong := range [][2]string{
		{"app.py::App.update@b", "app.py::_other_ctx@b"}, // another site's argument
		{"app.py::App.other@b", "app.py::_default_ctx@b"},
		{"config.py::ConfigAttribute.__get__@b", "tests/test_app.py::fake@b"}, // a test's store
	} {
		if hasEdge(g, core.EdgeCalls, wrong[0], wrong[1]) {
			t.Errorf("unexpected %s -> %s", wrong[0], wrong[1])
		}
	}
}

// Receiver typing: tuple loop targets from a Generator annotation (an
// undeclared component is the library's), a dict element from a
// subscripted annotated attribute, a lambda parameter from the callee's
// Callable alias, and isinstance narrowing.
func TestPythonReceiverTyping(t *testing.T) {
	g := New()
	imports := []string{"t.py"}
	sym := func(kind core.SymbolKind, name, parent, raw string, start, end int, sites ...core.CallSite) core.SymbolRecord {
		s := pySym(kind, "t.py", name, parent, raw, start, end)
		s.CallSites = sites
		s.Imports = imports
		return s
	}
	syms := []core.SymbolRecord{
		sym(core.KindClass, "Scaffold", "", "class Scaffold:\n    def get(self, rule):\n        pass", 1, 9),
		sym(core.KindMethod, "get", "Scaffold", "def get(self, rule):\n        pass", 2, 3),
		sym(core.KindClass, "Loader", "", "class Loader:\n    pass", 10, 30),
		sym(core.KindMethod, "get_source", "Loader", "def get_source(self, env, template):\n        pass", 11, 12),
		sym(core.KindMethod, "_iter", "Loader", "def _iter(self, template: str) -> t.Iterator[tuple[Scaffold, BaseLoader]]:\n        yield 1", 13, 14),
		sym(core.KindMethod, "explained", "Loader",
			"def explained(self, env, template):\n        for srcobj, loader in self._iter(template):\n            rv = loader.get_source(env, template)", 15, 17,
			core.CallSite{Callee: "self._iter", Line: 16, Col: 40, Argc: 1, Args: []string{"template"}},
			core.CallSite{Callee: "loader.get_source", Line: 17, Col: 25, Argc: 2, Args: []string{"env", "template"}}),
		sym(core.KindClass, "App", "Scaffold", "class App(Scaffold):\n    def __init__(self):\n        self.handlers: dict[str, dict[int, t.Callable]] = {}", 40, 60),
		sym(core.KindMethod, "find", "App",
			"def find(self, name):\n        handler_map = self.handlers[name]\n        return handler_map.get(1)", 45, 47,
			core.CallSite{Callee: "handler_map.get", Line: 47, Col: 28, Argc: 1, Args: []string{"#int"}}),
		sym(core.KindVariable, "Deferred", "", "Deferred = t.Callable[[\"State\"], None]", 61, 61),
		sym(core.KindClass, "State", "", "class State:\n    pass", 62, 65),
		sym(core.KindMethod, "add_rule", "State", "def add_rule(self, rule):\n        pass", 63, 64),
		sym(core.KindMethod, "add_rule", "App", "def add_rule(self, rule):\n        pass", 66, 67),
		sym(core.KindMethod, "record", "App", "def record(self, func: Deferred) -> None:\n        pass", 68, 69),
		sym(core.KindMethod, "bp_rule", "App",
			"def bp_rule(self, rule):\n        self.record(lambda s: s.add_rule(rule))", 70, 71,
			core.CallSite{Callee: "self.record", Line: 71, Col: 14, Argc: 1},
			core.CallSite{Callee: "s.add_rule", Line: 71, Col: 33, Argc: 1, Args: []string{"rule"}}),
		sym(core.KindFunction, "explain", "",
			"def explain(obj: Scaffold):\n    if isinstance(obj, State):\n        obj.add_rule(1)", 80, 82,
			core.CallSite{Callee: "obj.add_rule", Line: 82, Col: 13, Argc: 1, Args: []string{"#int"}}),
	}
	g.Replace(syms, 1)
	if hasEdge(g, core.EdgeCalls, "t.py::Loader.explained@b", "t.py::Loader.get_source@b") {
		t.Errorf("loader is the library's BaseLoader: its get_source is not ours")
	}
	if hasEdge(g, core.EdgeCalls, "t.py::App.find@b", "t.py::Scaffold.get@b") {
		t.Errorf("handler_map is a dict: .get is dict.get")
	}
	if !hasEdge(g, core.EdgeCalls, "t.py::App.bp_rule@b", "t.py::State.add_rule@b") || hasEdge(g, core.EdgeCalls, "t.py::App.bp_rule@b", "t.py::App.add_rule@b") {
		t.Errorf("the lambda's s is a State (Deferred = Callable[[State], None])")
	}
	if !hasEdge(g, core.EdgeCalls, "t.py::explain@b", "t.py::State.add_rule@b") || hasEdge(g, core.EdgeCalls, "t.py::explain@b", "t.py::App.add_rule@b") {
		t.Errorf("inside isinstance(obj, State), obj is a State")
	}
}

// A malformed component ("mod." from an annotation the regex cut short)
// panicked indexing click (replay 2026-10-17).
func TestPyComponentTypeMalformed(t *testing.T) {
	sym := pySym(core.KindFunction, "t.py", "f", "", "def f(): pass", 1, 1)
	for _, c := range []string{"click.", ".", "", "''"} {
		if got := pyComponentType(nil, &sym, c); got != "" {
			t.Errorf("%q: got %q", c, got)
		}
	}
}

// result.exception is annotated BaseException | None; calling a method
// BaseException lacks means the object is one of our subclasses.
func TestPythonExceptionReceiverReachesSubclasses(t *testing.T) {
	g := New()
	sym := func(kind core.SymbolKind, name, parent, raw string, start, end int, sites ...core.CallSite) core.SymbolRecord {
		s := pySym(kind, "t.py", name, parent, raw, start, end)
		s.CallSites = sites
		s.Imports = []string{"t.py"}
		return s
	}
	syms := []core.SymbolRecord{
		sym(core.KindClass, "ClickException", "", "class ClickException(Exception):\n    pass", 1, 4),
		sym(core.KindMethod, "format_message", "ClickException", "def format_message(self) -> str:\n        return ''", 2, 3),
		sym(core.KindClass, "Fmt", "", "class Fmt:\n    pass", 5, 8),
		sym(core.KindMethod, "format_message", "Fmt", "def format_message(self) -> str:\n        return ''", 6, 7),
		sym(core.KindClass, "Result", "", "class Result:\n    def __init__(self, exception: BaseException | None):\n        self.exception = exception", 10, 20),
		sym(core.KindFunction, "check", "",
			"def check(err: BaseException):\n    err.format_message()\n    err.with_traceback(None)", 30, 32,
			core.CallSite{Callee: "err.format_message", Line: 31, Col: 9, Argc: 0},
			core.CallSite{Callee: "err.with_traceback", Line: 32, Col: 9, Argc: 1}),
	}
	g.Replace(syms, 1)
	if !hasEdge(g, core.EdgeCalls, "t.py::check@b", "t.py::ClickException.format_message@b") {
		t.Errorf("err: BaseException calling format_message holds a ClickException (an Exception)")
	}
	if hasEdge(g, core.EdgeCalls, "t.py::check@b", "t.py::Fmt.format_message@b") {
		t.Errorf("Fmt is no exception")
	}
}
