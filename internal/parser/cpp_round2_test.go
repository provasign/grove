package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestCPPDestructorAndOperatorDeclarations(t *testing.T) {
	source := `class Base {
public:
    ~Base();
    Base& operator+=(const Base& other);
    int operator[](int index) const;
    void operator()();
};
Base& operator+(const Base& left, const Base& right);
`
	syms := extractSymbols("cpp", "base.hpp", "sha", source, nil)
	got := map[string]core.SymbolKind{}
	for _, sym := range syms {
		got[sym.QualifiedName] = sym.Kind
	}
	for _, name := range []string{"Base.~Base", "Base.operator+=", "Base.operator[]", "Base.operator()"} {
		if got[name] != core.KindMethod {
			t.Errorf("%s kind = %q; symbols=%v", name, got[name], got)
		}
	}
	if got["operator+"] != core.KindFunction {
		t.Errorf("global operator+ missing; symbols=%v", got)
	}
}

func TestCPPFieldReceiverCallEndToEnd(t *testing.T) {
	source := `class Widget {
public:
    void render() {}
};
class Other {
public:
    void render() {}
};
class Owner {
    Widget field;
public:
    void go() { field.render(); }
};
`
	syms := extractSymbols("cpp", "owner.cpp", "sha", source, nil)
	ids := map[string]string{}
	for _, sym := range syms {
		ids[sym.QualifiedName] = sym.ID
	}
	if ids["Owner.go"] == "" || ids["Widget.render"] == "" || ids["Other.render"] == "" {
		t.Fatalf("missing C++ fixture symbols: %v", ids)
	}
	for _, edge := range graph.BuildEdges(syms) {
		if edge.Type != core.EdgeCalls || edge.From != ids["Owner.go"] {
			continue
		}
		if edge.To == ids["Other.render"] {
			t.Fatal("C++ field receiver reached unrelated Other.render")
		}
		if edge.To == ids["Widget.render"] {
			return
		}
	}
	t.Fatal("C++ field receiver did not reach Widget.render")
}

func TestCPPRegexSymbolUsesFullBraceBody(t *testing.T) {
	source := "int helper() {\n    return 1;\n}\n"
	syms := extractSymbolsRegex("cpp", "helper.cpp", "sha", source, nil)
	for _, sym := range syms {
		if sym.Name == "helper" {
			if sym.Span.End != 3 || sym.RawText != "int helper() {\n    return 1;\n}" {
				t.Fatalf("helper span/body = %+v %q", sym.Span, sym.RawText)
			}
			return
		}
	}
	t.Fatal("missing regex helper symbol")
}

func TestCPPStructMethodsEndToEnd(t *testing.T) {
	source := `struct Widget {
    void inlineMethod() {}
    int multilineMethod()
    {
        return 1;
    }
};
`
	syms := extractSymbols("cpp", "widget.cpp", "sha", source, nil)
	want := map[string]core.SymbolKind{
		"Widget":                 core.KindStruct,
		"Widget.inlineMethod":    core.KindMethod,
		"Widget.multilineMethod": core.KindMethod,
	}
	for _, sym := range syms {
		kind, ok := want[sym.QualifiedName]
		if !ok {
			continue
		}
		if sym.Kind != kind {
			t.Errorf("%s kind = %s, want %s", sym.QualifiedName, sym.Kind, kind)
		}
		delete(want, sym.QualifiedName)
	}
	if len(want) != 0 {
		t.Fatalf("missing C++ struct symbols: %v", want)
	}
}

func TestCPPVisibilityDrivesDeadCodeEndToEnd(t *testing.T) {
	source := `static void fileLocal() {}
class Widget {
public:
    void pubM() {}
protected:
    void protM() {}
private:
    void privM() {}
};
`
	syms := extractSymbols("cpp", "widget.cpp", "sha", source, nil)
	byName := map[string]core.SymbolRecord{}
	for _, sym := range syms {
		byName[sym.QualifiedName] = sym
	}
	for _, name := range []string{"fileLocal", "Widget.protM", "Widget.privM"} {
		if sym, ok := byName[name]; !ok || sym.Exports {
			t.Errorf("%s = %+v, want non-exported symbol", name, sym)
		}
	}
	if sym := byName["Widget.pubM"]; !sym.Exports {
		t.Errorf("Widget.pubM = %+v, want exported public method", sym)
	}
	g := graph.New()
	g.Replace(syms, 1)
	dead := map[string]bool{}
	for _, sym := range g.DeadCode(nil).Dead {
		dead[sym.QualifiedName] = true
	}
	for _, name := range []string{"fileLocal", "Widget.protM", "Widget.privM"} {
		if !dead[name] {
			t.Errorf("%s not reported dead; dead=%v", name, dead)
		}
	}
	if dead["Widget.pubM"] {
		t.Fatal("public C++ method reported as private dead code")
	}
}
