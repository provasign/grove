package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/scipexport"
	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

// Grove's SCIP export passes the official-type checks: symbols parse with
// scip's grammar, ranges are valid, every definition is inside its enclosing
// range, no SymbolInformation is duplicated (SCIP-01).
func TestGroveExportPassesCheck(t *testing.T) {
	root := t.TempDir()
	src := "package a\n\nfunc Run() int { return helper() }\n\nfunc helper() int {\n\treturn 1\n}\n"
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	syms := []core.SymbolRecord{
		{ID: "a.go::Run", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Run", QualifiedName: "Run", Span: core.LineRange{Start: 3, End: 3}},
		{ID: "a.go::helper", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper", Span: core.LineRange{Start: 5, End: 7}},
	}
	edges := []core.Edge{{From: "a.go::Run", To: "a.go::helper", Type: core.EdgeCalls, Lines: "3:25"}}
	raw, err := scipexport.Export(syms, edges, scipexport.Options{Root: root, ToolName: "grove", ToolVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if errs, st := Check(raw); len(errs) != 0 || st.occs != 3 {
		t.Fatalf("errors %v, stats %+v", errs, st)
	}
}

// The check catches v0.64.7's empty enclosing range on a one-line function.
func TestCheckRejectsEnclosingRangeWithoutTheName(t *testing.T) {
	sym := "prism . . . `a.go`/Run()."
	idx := &scip.Index{
		Metadata: &scip.Metadata{ToolInfo: &scip.ToolInfo{Name: "grove"}, ProjectRoot: "file:///r"},
		Documents: []*scip.Document{{
			RelativePath: "a.go",
			Occurrences:  []*scip.Occurrence{{Range: []int32{2, 5, 8}, Symbol: sym, SymbolRoles: int32(scip.SymbolRole_Definition), EnclosingRange: []int32{2, 0, 2, 0}}},
			Symbols:      []*scip.SymbolInformation{{Symbol: sym}},
		}},
	}
	raw, err := proto.Marshal(idx)
	if err != nil {
		t.Fatal(err)
	}
	errs, _ := Check(raw)
	if len(errs) != 1 || !strings.Contains(errs[0], "does not contain") {
		t.Fatalf("errors = %v", errs)
	}
}
