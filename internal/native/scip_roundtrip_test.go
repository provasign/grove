package native

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/scipexport"
)

// The SCIP exporter's output reads back through the decoder that reads
// rust-analyzer's real SCIP: documents, definition occurrences with their
// enclosing ranges, a reference, and the symbol kinds (MKT-10).
func TestSCIPExportRoundTripsThroughDecoder(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc Run() int { return helper() }\n\nfunc helper() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	syms := []core.SymbolRecord{
		{ID: "a.go::Run", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Run", QualifiedName: "Run", Span: core.LineRange{Start: 3, End: 3}},
		{ID: "a.go::helper", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper", Span: core.LineRange{Start: 5, End: 5}},
	}
	edges := []core.Edge{{From: "a.go::Run", To: "a.go::helper", Type: core.EdgeCalls, Lines: "3:25"}}
	raw, err := scipexport.Export(syms, edges, scipexport.Options{Root: root, ToolName: "grove", ToolVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	docs, err := decodeScip(raw)
	if err != nil || len(docs) != 1 || docs[0].Path != "a.go" {
		t.Fatalf("decode: %v %+v", err, docs)
	}
	var defs, refs int
	for _, o := range docs[0].Occurrences {
		if o.Definition {
			defs++
			if o.Enclosing[0] < 0 {
				t.Errorf("definition without enclosing range: %+v", o)
			}
		} else if o.Line == 2 && o.Col == 24 && o.EndCol == 30 {
			refs++
		}
	}
	if defs != 2 || refs != 1 {
		t.Errorf("defs %d refs %d: %+v", defs, refs, docs[0].Occurrences)
	}
	if docs[0].Kinds["prism . . . `a.go`/helper()."] != scipKindFunction {
		t.Errorf("kinds %v", docs[0].Kinds)
	}
}
