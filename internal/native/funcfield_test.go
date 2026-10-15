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
