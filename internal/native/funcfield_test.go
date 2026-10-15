package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// Function values flowing through a parameter slot (All(NoArgs) then c(n)
// inside All) and a result slot (Def returns NoArgs, f := Def(); f(n)) are
// keyed like fields; a call through a library interface (w.Write on an
// io.Writer) is recorded for the external-contract join.
func TestGoFuncValueFlowAndExternalIfaceMarkers(t *testing.T) {
	root := t.TempDir()
	src := "package fv\n\nimport \"io\"\n\ntype Check func(n int) error\n\nfunc NoArgs(n int) error { return nil }\n\n" +
		"func All(cs ...Check) Check {\n\treturn func(n int) error {\n\t\tfor _, c := range cs {\n\t\t\tif err := c(n); err != nil {\n\t\t\t\treturn err\n\t\t\t}\n\t\t}\n\t\treturn nil\n\t}\n}\n\n" +
		"func Def() Check { return NoArgs }\n\nfunc Use(n int) error {\n\tf := Def()\n\treturn f(n)\n}\n\nfunc Build() Check { return All(NoArgs) }\n\n" +
		"type W struct{}\n\nfunc (W) Write(p []byte) (int, error) { return len(p), nil }\n\nfunc Emit(w io.Writer) { w.Write(nil) }\n"
	if err := os.WriteFile(filepath.Join(root, "fv.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	sym := func(kind core.SymbolKind, name, qual, parent string, start, end int) core.SymbolRecord {
		return core.SymbolRecord{ID: "fv.go::" + qual + "@b", FilePath: "fv.go", BlobSHA: "b", Language: "go", Kind: kind,
			Name: name, QualifiedName: qual, ParentSymbol: parent, Span: core.LineRange{Start: start, End: end}}
	}
	parsed := []core.SymbolRecord{
		sym(core.KindType, "Check", "Check", "", 5, 5),
		sym(core.KindFunction, "NoArgs", "NoArgs", "", 7, 7),
		sym(core.KindFunction, "All", "All", "", 9, 18),
		sym(core.KindFunction, "Def", "Def", "", 20, 20),
		sym(core.KindFunction, "Use", "Use", "", 22, 25),
		sym(core.KindFunction, "Build", "Build", "", 27, 27),
		sym(core.KindStruct, "W", "W", "", 29, 29),
		sym(core.KindMethod, "Write", "W.Write", "W", 31, 31),
		sym(core.KindFunction, "Emit", "Emit", "", 33, 33),
	}
	edges, _, _ := goSemanticEdges(context.Background(), root, []string{"fv.go"}, parsed, nil, goDeps{})
	got := map[string]bool{}
	for _, e := range edges {
		switch e.Type {
		case core.EdgeFuncFieldStore:
			got["store "+e.Lines+" "+e.To] = true
		case core.EdgeFuncFieldCall, core.EdgeExternalIfaceCall, core.EdgeExternalContract:
			got[string(e.Type)+" "+e.From+" "+e.Lines] = true
		}
	}
	for _, want := range []string{
		"store .:All#p0 fv.go::NoArgs@b",
		"store .:Def#ret fv.go::NoArgs@b",
		"func-field-call fv.go::All@b .:All#p0@12:14",
		"func-field-call fv.go::Use@b .:Def#ret@24:9",
		"external-iface-call fv.go::Emit@b io.Writer.Write@33:28",
	} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	contract := false
	for k := range got {
		if strings.HasPrefix(k, "external-contract fv.go::W.Write@b ") && strings.Contains(k, "io.Writer") {
			contract = true
		}
	}
	if !contract {
		t.Errorf("W.Write must record io.Writer as an external contract: %v", got)
	}
}
