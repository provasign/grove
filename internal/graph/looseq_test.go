package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// looseFixture: two types implement Render; Helper is a package-level
// function called by Run; Solo.only is unique.
func looseFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::JSON@s", FilePath: "a.go", Language: "go", Kind: core.KindStruct, Name: "JSON", QualifiedName: "JSON"},
		{ID: "a.go::JSON.Render@s", FilePath: "a.go", Language: "go", Kind: core.KindMethod,
			Name: "Render", QualifiedName: "JSON.Render", ParentSymbol: "JSON", Span: core.LineRange{Start: 10, End: 14}},
		{ID: "b.go::XML@s", FilePath: "b.go", Language: "go", Kind: core.KindStruct, Name: "XML", QualifiedName: "XML"},
		{ID: "b.go::XML.Render@s", FilePath: "b.go", Language: "go", Kind: core.KindMethod,
			Name: "Render", QualifiedName: "XML.Render", ParentSymbol: "XML", Span: core.LineRange{Start: 20, End: 24}},
		{ID: "c.go::Solo@s", FilePath: "c.go", Language: "go", Kind: core.KindStruct, Name: "Solo", QualifiedName: "Solo"},
		{ID: "c.go::Solo.only@s", FilePath: "c.go", Language: "go", Kind: core.KindMethod,
			Name: "only", QualifiedName: "Solo.only", ParentSymbol: "Solo", Span: core.LineRange{Start: 5, End: 8}},
		{ID: "d.go::Helper@s", FilePath: "d.go", Language: "go", Kind: core.KindFunction,
			Name: "Helper", QualifiedName: "Helper", Span: core.LineRange{Start: 3, End: 6},
			RawText: "func Helper() {}"},
		{ID: "d.go::Run@s", FilePath: "d.go", Language: "go", Kind: core.KindFunction,
			Name: "Run", QualifiedName: "Run", Span: core.LineRange{Start: 8, End: 11},
			RawText: "func Run() { Helper() }", CallSites: []core.CallSite{{Callee: "Helper", Line: 9}}},
	}, 4)
	return g
}

// A canonical query must pass through untouched — this is the fast path every
// existing caller takes, and any behavior change here is a regression.
func TestResolveLooseQuery_CanonicalUnchanged(t *testing.T) {
	g := looseFixture()
	for _, q := range []string{
		"JSON.Render", "JSON.Render(http.ResponseWriter)",
		"pkg.Type.method", "Outer.Inner.method(A, B)",
	} {
		got, err := g.resolveLooseQueryLocked(q)
		if err != nil {
			t.Errorf("%q: unexpected error %v", q, err)
		}
		if got != q {
			t.Errorf("%q rewritten to %q — canonical queries must pass through", q, got)
		}
	}
}

// An unambiguous bare name is pinned silently.
func TestResolveLooseQuery_BareUnambiguous(t *testing.T) {
	g := looseFixture()
	got, err := g.resolveLooseQueryLocked("only")
	if err != nil {
		t.Fatal(err)
	}
	if got != "Solo.only" {
		t.Errorf("got %q, want Solo.only", got)
	}
}

// An ambiguous bare name must NOT guess — it must list the candidates.
func TestResolveLooseQuery_BareAmbiguous(t *testing.T) {
	g := looseFixture()
	_, err := g.resolveLooseQueryLocked("Render")
	if err == nil {
		t.Fatal("ambiguous name resolved silently — the tool would answer about the wrong symbol")
	}
	msg := err.Error()
	for _, want := range []string{"ambiguous", "JSON.Render", "XML.Render"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q: %s", want, msg)
		}
	}
}

func TestResolveLooseQuery_UnknownSuggests(t *testing.T) {
	g := looseFixture()
	_, err := g.resolveLooseQueryLocked("Rende")
	if err == nil {
		t.Fatal("expected an error for an unknown name")
	}
	if !strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no suggestion offered: %s", err)
	}
}

func TestResolveLooseQuery_FileLine(t *testing.T) {
	g := looseFixture()
	got, err := g.resolveLooseQueryLocked("a.go:12")
	if err != nil {
		t.Fatal(err)
	}
	if got != "JSON.Render" {
		t.Errorf("got %q, want JSON.Render", got)
	}
	if _, err := g.resolveLooseQueryLocked("a.go:999"); err == nil {
		t.Error("a line covered by no symbol should error")
	}
}

// A package-level function has no type to close over: report its callers and
// downgrade completeness rather than claiming a closed set.
func TestChangeImpact_FreeFunctionCallersOnly(t *testing.T) {
	g := looseFixture()
	ci, err := g.ChangeImpact("Helper")
	if err != nil {
		t.Fatal(err)
	}
	if ci.Completeness != "callers-only" {
		t.Errorf("completeness = %q, want callers-only", ci.Completeness)
	}
	if len(ci.Declarations) != 1 || ci.Declarations[0].Name != "Helper" {
		t.Errorf("declarations = %+v", ci.Declarations)
	}
	found := false
	for _, c := range ci.Callers {
		if c.Name == "Run" {
			found = true
		}
	}
	if !found {
		t.Errorf("Run calls Helper but is not reported: %+v", ci.Callers)
	}
}

// ChangeImpact must report the RESOLVED query, since RenamePlan re-parses it.
func TestChangeImpact_ReportsResolvedQuery(t *testing.T) {
	g := looseFixture()
	ci, err := g.ChangeImpact("only")
	if err != nil {
		t.Fatal(err)
	}
	if ci.Query != "Solo.only" {
		t.Errorf("Query = %q, want the resolved Solo.only", ci.Query)
	}
}

// The first unit tests for the shared parser itself.
func TestParseChangeImpactQuery(t *testing.T) {
	cases := []struct {
		in        string
		typ, meth string
		params    []string
		wantErr   bool
	}{
		{in: "JSON.Render", typ: "JSON", meth: "Render"},
		{in: "pkg.Type.method", typ: "Type", meth: "method"},
		{in: "T.m(A, B)", typ: "T", meth: "m", params: []string{"A", "B"}},
		{in: "T.m(", wantErr: true},
		{in: "bare", wantErr: true},
		{in: "T.", wantErr: true},
	}
	for _, c := range cases {
		typ, meth, params, err := parseChangeImpactQuery(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if typ != c.typ || meth != c.meth {
			t.Errorf("%q -> (%q,%q), want (%q,%q)", c.in, typ, meth, c.typ, c.meth)
		}
		if len(params) != len(c.params) {
			t.Errorf("%q params = %v, want %v", c.in, params, c.params)
		}
	}
}
