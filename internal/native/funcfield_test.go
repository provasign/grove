package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A call through a function-valued field and a function stored into it are
// both recorded, keyed by the field.
func TestGoFuncFieldMarkers(t *testing.T) {
	root := t.TempDir()
	src := "package fv\n\ntype Check func(n int) error\n\nfunc NoArgs(n int) error { return nil }\n\ntype Cmd struct{ Args Check }\n\n" +
		"func (c *Cmd) Validate(n int) error {\n\treturn c.Args(n)\n}\n\nfunc New() *Cmd { return &Cmd{Args: NoArgs} }\n\nfunc Set(c *Cmd) { c.Args = NoArgs }\n"
	if err := os.WriteFile(filepath.Join(root, "fv.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := []core.SymbolRecord{
		{ID: "fv.go::Check@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindType, Name: "Check", QualifiedName: "Check", Span: core.LineRange{Start: 3, End: 3}},
		{ID: "fv.go::NoArgs@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindFunction, Name: "NoArgs", QualifiedName: "NoArgs", Span: core.LineRange{Start: 5, End: 5}},
		{ID: "fv.go::Cmd@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindStruct, Name: "Cmd", QualifiedName: "Cmd", Span: core.LineRange{Start: 7, End: 7}},
		{ID: "fv.go::Cmd.Validate@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindMethod, Name: "Validate", QualifiedName: "Cmd.Validate", ParentSymbol: "Cmd", Span: core.LineRange{Start: 9, End: 11}},
		{ID: "fv.go::New@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindFunction, Name: "New", QualifiedName: "New", Span: core.LineRange{Start: 13, End: 13}},
		{ID: "fv.go::Set@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: core.KindFunction, Name: "Set", QualifiedName: "Set", Span: core.LineRange{Start: 15, End: 15}},
	}
	edges, _, _ := goSemanticEdges(context.Background(), root, []string{"fv.go"}, parsed, nil, goDeps{})
	var calls, stores int
	for _, e := range edges {
		switch e.Type {
		case core.EdgeFuncFieldCall:
			calls++
		case core.EdgeFuncFieldStore:
			stores++
			if e.To != "fv.go::NoArgs@b" || e.Lines != ".:Cmd.Args" {
				t.Errorf("store %+v", e)
			}
		}
	}
	if calls != 1 || stores != 2 {
		t.Fatalf("calls=%d stores=%d, want 1 and 2: %+v", calls, stores, edges)
	}
}

// The locator binds a compiler-reported (file, name, line) to the symbol
// whose span holds the line, or one starting within a few lines (an
// annotation above the parsed declaration); among several same-named
// symbols with none near, it binds nothing rather than the first.
func TestSymbolLocatorDoesNotGuessAmongNamesakes(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "a::Inner.parse", FilePath: "A.java", Language: "java", Name: "parse", Span: core.LineRange{Start: 169, End: 184}},
		{ID: "a::A.parse", FilePath: "A.java", Language: "java", Name: "parse", Span: core.LineRange{Start: 1015, End: 1029}},
		{ID: "a::A.only", FilePath: "A.java", Language: "java", Name: "only", Span: core.LineRange{Start: 50, End: 60}},
	}
	loc := newSymbolLocator(syms, map[string]bool{"java": true})
	if s, ok := loc.at("A.java", "parse", 1020); !ok || s.ID != "a::A.parse" {
		t.Errorf("containing span: %v %v", s.ID, ok)
	}
	if s, ok := loc.at("A.java", "parse", 1013); !ok || s.ID != "a::A.parse" {
		t.Errorf("annotation above the declaration: %v %v", s.ID, ok)
	}
	if s, ok := loc.at("A.java", "parse", 1040); ok {
		t.Errorf("an unindexed overload bound to %s", s.ID)
	}
	if s, ok := loc.at("A.java", "only", 90); !ok || s.ID != "a::A.only" {
		t.Errorf("a single namesake still answers: %v %v", s.ID, ok)
	}
}
