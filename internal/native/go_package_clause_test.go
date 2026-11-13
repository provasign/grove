package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A directory holds its package and its external test package. Checked as
// one package, go/types rejects every file whose clause differs from the
// first file's: with a_example_test.go (package p_test) first, p.go lost all
// type information (client_golang prometheus/push, 2026-11-13); with p.go
// first, the external tests lost theirs. Each clause is now checked alone.
func TestGoEachPackageClauseIsTypeChecked(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"a_example_test.go": "package p_test\n\ntype C struct{}\n\nfunc (C) M() {}\n\nfunc ExampleX() {\n\tvar c C\n\tc.M()\n}\n",
		"p.go":              "package p\n\ntype T struct{}\n\nfunc (T) M() {}\n\nfunc Use() {\n\tvar t T\n\tt.M()\n}\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sym := func(file, qn, parent string, kind core.SymbolKind, start, end int) core.SymbolRecord {
		name := qn
		if parent != "" {
			name = qn[len(parent)+1:]
		}
		return core.SymbolRecord{ID: file + "::" + qn + "@b", FilePath: file, BlobSHA: "b", Language: "go", Kind: kind,
			Name: name, QualifiedName: qn, ParentSymbol: parent, Span: core.LineRange{Start: start, End: end}}
	}
	parsed := []core.SymbolRecord{
		sym("a_example_test.go", "C", "", core.KindStruct, 3, 3),
		sym("a_example_test.go", "C.M", "C", core.KindMethod, 5, 5),
		sym("a_example_test.go", "ExampleX", "", core.KindFunction, 7, 10),
		sym("p.go", "T", "", core.KindStruct, 3, 3),
		sym("p.go", "T.M", "T", core.KindMethod, 5, 5),
		sym("p.go", "Use", "", core.KindFunction, 7, 10),
	}
	for _, order := range [][]string{{"a_example_test.go", "p.go"}, {"p.go", "a_example_test.go"}} {
		edges, _, _ := goSemanticEdges(context.Background(), root, order, parsed, nil, goDeps{})
		got := map[string]bool{}
		for _, e := range edges {
			if e.Type == core.EdgeCalls {
				got[e.From+" -> "+e.To] = true
			}
		}
		for _, want := range []string{"p.go::Use@b -> p.go::T.M@b", "a_example_test.go::ExampleX@b -> a_example_test.go::C.M@b"} {
			if !got[want] {
				t.Errorf("files %v: missing typed call %s; got %v", order, want, got)
			}
		}
	}
}
