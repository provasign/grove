package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func deadCodeFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		// main -> alive() ; orphan() unreached ; helperViaValue passed as
		// value (name in main's text, no call edge) ; ExportedIdle exported
		// and unreferenced ; deadCaller -> deadCallee cluster.
		{ID: "main.go::main@sha", FilePath: "main.go", Language: "go", Kind: core.KindFunction,
			Name: "main", QualifiedName: "pkg.main",
			RawText:   "func main() { alive(); register(helperViaValue) }",
			CallSites: []core.CallSite{{Callee: "alive", Line: 1, Argc: 0}}},
		{ID: "a.go::alive@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "alive", QualifiedName: "pkg.alive", RawText: "func alive() {}"},
		{ID: "a.go::orphan@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "orphan", QualifiedName: "pkg.orphan", RawText: "func orphan() {}"},
		{ID: "a.go::helperViaValue@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "helperViaValue", QualifiedName: "pkg.helperViaValue", RawText: "func helperViaValue() {}"},
		{ID: "b.go::ExportedIdle@sha", FilePath: "b.go", Language: "go", Kind: core.KindFunction,
			Name: "ExportedIdle", QualifiedName: "pkg.ExportedIdle", Exports: true,
			RawText: "func ExportedIdle() {}"},
		{ID: "c.go::deadCaller@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCaller", QualifiedName: "pkg.deadCaller",
			RawText:   "func deadCaller() { deadCallee() }",
			CallSites: []core.CallSite{{Callee: "deadCallee", Line: 1, Argc: 0}}},
		{ID: "c.go::deadCallee@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCallee", QualifiedName: "pkg.deadCallee", RawText: "func deadCallee() {}"},
	}, 3)
	return g
}

func TestDeadCodeBuckets(t *testing.T) {
	g := deadCodeFixture()
	r := g.DeadCode(nil)
	dead := names(r.Dead)
	if !dead["orphan"] {
		t.Errorf("Dead = %v, want orphan", dead)
	}
	if dead["alive"] || dead["main"] {
		t.Errorf("Dead = %v wrongly contains live code", dead)
	}
	// Passed as a value: no call edge, but the name occurs in main's text.
	if dead["helperViaValue"] {
		t.Errorf("Dead wrongly contains helperViaValue (referenced by value)")
	}
	// Transitively-dead cluster: only the top (deadCaller) is reported;
	// deadCallee stays because deadCaller's text still mentions it.
	if !dead["deadCaller"] || dead["deadCallee"] {
		t.Errorf("Dead = %v, want deadCaller only from the dead cluster", dead)
	}
	if exp := names(r.ExportedUnreferenced); !exp["ExportedIdle"] {
		t.Errorf("ExportedUnreferenced = %v, want ExportedIdle", exp)
	}
	if len(r.Caveats) == 0 {
		t.Error("Caveats must always be present")
	}
	if r.Considered == 0 || r.RootCount == 0 {
		t.Errorf("counters: considered=%d roots=%d", r.Considered, r.RootCount)
	}
}

func TestDeadCodeExcludesInlineRustTestsAndTheirMentions(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/lib.rs::dead_one", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindFunction, Name: "dead_one", QualifiedName: "dead_one", RawText: "fn dead_one() {}"},
		{ID: "src/lib.rs::t_dead_one", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindFunction, Name: "t_dead_one", QualifiedName: "tests.t_dead_one", RawText: "#[test]\nfn t_dead_one() { dead_one(); }", Annotations: []string{"test"}, CallSites: []core.CallSite{{Callee: "dead_one", Line: 3}}},
	}, 1)
	dead := names(g.DeadCode(nil).Dead)
	if dead["t_dead_one"] {
		t.Fatal("#[test] function reported as dead production code")
	}
	if !dead["dead_one"] {
		t.Fatal("a reference only from an inline Rust test kept production code alive")
	}
}

func TestDeadCodeIgnoresDuplicatedEnclosingClassText(t *testing.T) {
	class := core.SymbolRecord{ID: "widget.cpp::Widget", FilePath: "widget.cpp", Language: "cpp", Kind: core.KindClass, Name: "Widget", QualifiedName: "Widget", RawText: "class Widget { void unused(); };"}
	method := core.SymbolRecord{ID: "widget.cpp::Widget.unused", FilePath: "widget.cpp", Language: "cpp", Kind: core.KindMethod, Name: "unused", QualifiedName: "Widget.unused", ParentSymbol: "Widget", RawText: "void Widget::unused() {}"}
	g := New()
	g.Replace([]core.SymbolRecord{class, method}, 1)
	if !names(g.DeadCode(nil).Dead)["unused"] {
		t.Fatal("enclosing class RawText must not keep its unreferenced method alive")
	}
}

func TestDeadCodeIgnoresDuplicatedTransitiveNamespaceText(t *testing.T) {
	namespace := core.SymbolRecord{ID: "widget.cpp::app", FilePath: "widget.cpp", Language: "cpp", Kind: core.KindNamespace, Name: "app", QualifiedName: "app", RawText: "namespace app { class Widget { void unused(); }; }"}
	class := core.SymbolRecord{ID: "widget.cpp::app.Widget", FilePath: "widget.cpp", Language: "cpp", Kind: core.KindClass, Name: "Widget", QualifiedName: "app::Widget", ParentSymbol: "app", RawText: "class Widget { void unused(); };"}
	method := core.SymbolRecord{ID: "widget.cpp::app.Widget.unused", FilePath: "widget.cpp", Language: "cpp", Kind: core.KindMethod, Name: "unused", QualifiedName: "app::Widget::unused", ParentSymbol: "app::Widget", RawText: "void unused() {}"}
	g := New()
	g.Replace([]core.SymbolRecord{namespace, class, method}, 1)
	if !names(g.DeadCode(nil).Dead)["unused"] {
		t.Fatal("transitive namespace RawText must not keep its unreferenced method alive")
	}
}
