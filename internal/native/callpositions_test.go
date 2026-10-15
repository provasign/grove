package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A go/types call edge carries every call's position: line and the
// character column of the called name (UTF-16 units, as javac and tsc
// report), one entry per call, ascending.
func TestGoCallEdgesCarryTheirPositions(t *testing.T) {
	root := t.TempDir()
	src := "package main\n\ntype W struct{}\n\nfunc (W) Value(int) {}\n\nfunc use(w W) {\n\tw.Value(1)\n\t_ = \"é\"; w.Value(2)\n}\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := []core.SymbolRecord{
		{ID: "main.go::W@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindStruct, Name: "W", QualifiedName: "W", Span: core.LineRange{Start: 3, End: 3}},
		{ID: "main.go::W.Value@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindMethod, Name: "Value", QualifiedName: "W.Value", ParentSymbol: "W", Span: core.LineRange{Start: 5, End: 5}},
		{ID: "main.go::use@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindFunction, Name: "use", QualifiedName: "use", Span: core.LineRange{Start: 7, End: 10}},
	}
	edges, _, _ := goSemanticEdges(context.Background(), root, []string{"main.go"}, parsed, nil, goDeps{})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == "main.go::use@b" && e.To == "main.go::W.Value@b" {
			// line 8: "\tw.Value(1)" -> col 4; line 9: "\t_ = \"é\"; w.Value(2)" -> col 13 (é is one unit)
			if e.Lines != "8:4,9:13" {
				t.Fatalf("positions %q, want 8:4,9:13", e.Lines)
			}
			return
		}
	}
	t.Fatalf("use -> W.Value call edge missing: %+v", edges)
}

func TestCharColumnCountsUTF16Units(t *testing.T) {
	for _, c := range []struct {
		line string
		off  int
		want int
	}{{"abc", 0, 1}, {"abc", 2, 3}, {"é.x", 3, 3}, {"😀x", 4, 3}, {"abc", 9, 0}} {
		if got := charColumn(c.line, c.off); got != c.want {
			t.Errorf("charColumn(%q, %d) = %d, want %d", c.line, c.off, got, c.want)
		}
	}
}
