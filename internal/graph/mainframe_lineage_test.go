package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestMainframeOperandDirectionsAndQualification(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "p", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "RUN", QualifiedName: "JOB.RUN", Span: core.LineRange{Start: 10, End: 13}, RawText: strings.Join([]string{
			"WRITE WS-REC FROM WS-SRC",
			"ADD WS-A TO WS-B GIVING WS-C",
			"SET WS-IDX TO WS-A",
			"MOVE FLD-X OF GRP-A TO FLD-X OF GRP-B",
		}, "\n")},
		{ID: "rec", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-REC", QualifiedName: "WS-REC"},
		{ID: "src", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-SRC", QualifiedName: "WS-SRC"},
		{ID: "a", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-A", QualifiedName: "WS-A"},
		{ID: "b", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-B", QualifiedName: "WS-B"},
		{ID: "c", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-C", QualifiedName: "WS-C"},
		{ID: "idx", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-IDX", QualifiedName: "WS-IDX"},
		{ID: "xa", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "FLD-X", QualifiedName: "GRP-A.FLD-X", ParentSymbol: "GRP-A"},
		{ID: "xb", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "FLD-X", QualifiedName: "GRP-B.FLD-X", ParentSymbol: "GRP-B"},
	}
	edges := BuildEdges(symbols)
	got := map[string]bool{}
	for _, edge := range edges {
		if edge.Type == core.EdgeReads || edge.Type == core.EdgeWrites {
			got[edge.From+" "+string(edge.Type)+" "+edge.To] = true
		}
	}
	want := []string{
		"p writes rec", "p reads src",
		"p reads a", "p reads b", "p writes c",
		"p writes idx",
		"p reads xa", "p writes xb",
	}
	for _, edge := range want {
		if !got[edge] {
			t.Errorf("missing %s; got %#v", edge, got)
		}
	}
	for _, forbidden := range []string{"p reads rec", "p writes src", "p writes xa", "p reads xb", "p writes a", "p writes b"} {
		if got[forbidden] {
			t.Errorf("spurious %s", forbidden)
		}
	}
}

func TestMainframeRenamePlanUsesHyphenBoundariesAndReferenceEdges(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "field", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-ID", QualifiedName: "WS-ID", Span: core.LineRange{Start: 2, End: 2}, Signature: "01 WS-ID PIC X.", RawText: "01 WS-ID PIC X."},
		{ID: "caller", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "RUN", QualifiedName: "JOB.RUN", Span: core.LineRange{Start: 10, End: 10}, RawText: "MOVE WS-ID TO OUTPUT-ID"},
		{ID: "output", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "OUTPUT-ID", QualifiedName: "OUTPUT-ID"},
	}
	g := New()
	g.Replace(symbols, 1)
	plan, err := g.RenamePlan("ws-id", "CUSTOMER-ID")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Edits) != 2 {
		t.Fatalf("edits = %+v, want declaration and caller", plan.Edits)
	}
	for _, edit := range plan.Edits {
		if strings.Contains(edit.After, "OUTPUT-CUSTOMER-ID") {
			t.Fatalf("rename crossed a hyphenated identifier boundary: %+v", edit)
		}
	}
}

// A name inside a `*>` comment or a literal on an edited line is not
// renamed: only the code occurrence is replaced.
func TestMainframeRenamePlanLeavesCommentsAndLiterals(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "field", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-ID", QualifiedName: "WS-ID", Span: core.LineRange{Start: 2, End: 2}, Signature: "01 WS-ID PIC X.", RawText: "01 WS-ID PIC X."},
		{ID: "caller", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "RUN", QualifiedName: "JOB.RUN", Span: core.LineRange{Start: 10, End: 11},
			RawText: "MOVE WS-ID TO OUTPUT-ID. *> WAS WS-ID\nDISPLAY 'WS-ID' WS-ID."},
		{ID: "output", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "OUTPUT-ID", QualifiedName: "OUTPUT-ID"},
	}
	g := New()
	g.Replace(symbols, 1)
	plan, err := g.RenamePlan("ws-id", "CUSTOMER-ID")
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]string{
		10: "MOVE CUSTOMER-ID TO OUTPUT-ID. *> WAS WS-ID",
		11: "DISPLAY 'WS-ID' CUSTOMER-ID.",
	}
	got := map[int]string{}
	for _, edit := range append(plan.Edits, plan.Ambiguous...) {
		if edit.SiteID == "caller" {
			got[edit.Line] = edit.After
		}
	}
	for line, after := range want {
		if got[line] != after {
			t.Errorf("line %d after = %q, want %q", line, got[line], after)
		}
	}
}

// A statement continues across line breaks: `MOVE A` / `TO B.` writes B.
// An IF condition is read; the MOVE in its branch still writes its target.
func TestMainframeStatementsSpanLines(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "p", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "RUN", QualifiedName: "JOB.RUN", Span: core.LineRange{Start: 10, End: 13}, RawText: strings.Join([]string{
			"RUN.",
			"MOVE WS-A",
			"    TO WS-B.",
			"IF WS-C > 0 MOVE WS-A TO WS-D.",
		}, "\n")},
		{ID: "a", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-A", QualifiedName: "WS-A"},
		{ID: "b", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-B", QualifiedName: "WS-B"},
		{ID: "c", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-C", QualifiedName: "WS-C"},
		{ID: "d", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("data-item"), Name: "WS-D", QualifiedName: "WS-D"},
	}
	got := map[string]bool{}
	for _, edge := range BuildEdges(symbols) {
		if edge.Type == core.EdgeReads || edge.Type == core.EdgeWrites {
			got[edge.From+" "+string(edge.Type)+" "+edge.To] = true
		}
	}
	for _, edge := range []string{"p reads a", "p writes b", "p reads c", "p writes d"} {
		if !got[edge] {
			t.Errorf("missing %s; got %v", edge, got)
		}
	}
	for _, edge := range []string{"p reads b", "p writes a", "p reads d", "p writes c"} {
		if got[edge] {
			t.Errorf("spurious %s", edge)
		}
	}
}

func TestMainframeImpactIncludesDatasetBindings(t *testing.T) {
	logical := core.SymbolRecord{ID: "lf", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("logical-file"), Name: "INPUT", QualifiedName: "JOB.INPUT"}
	dataset := core.SymbolRecord{ID: "ds", FilePath: "job.jcl", Language: "jcl", Kind: core.SymbolKind("dataset"), Name: "DATA.SET", QualifiedName: "STEP.DATA.SET"}
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{logical, dataset}, []core.Edge{{From: logical.ID, To: dataset.ID, Type: core.EdgeBinds}}, 2)
	impact, err := g.ChangeImpact("DATA.SET")
	if err != nil {
		t.Fatal(err)
	}
	if len(impact.Callers) != 1 || impact.Callers[0].ID != logical.ID {
		t.Fatalf("binding impact callers = %+v", impact.Callers)
	}
}

func TestMainframeDeadCodeKeepsProgramEntryAndReportsUnreachedParagraph(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "program", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("program"), Name: "JOB", QualifiedName: "JOB", Exports: true},
		{ID: "entry", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "MAIN-PARA", QualifiedName: "JOB.MAIN-PARA", ParentSymbol: "JOB", Span: core.LineRange{Start: 10, End: 11}, RawText: "MAIN-PARA.\nGOBACK."},
		{ID: "dead", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("paragraph"), Name: "UNUSED-PARA", QualifiedName: "JOB.UNUSED-PARA", ParentSymbol: "JOB", Span: core.LineRange{Start: 20, End: 21}, RawText: "UNUSED-PARA.\nGOBACK."},
	}, 1)
	result := g.DeadCode(nil)
	if result.Considered != 2 || len(result.Dead) != 1 || result.Dead[0].ID != "dead" {
		t.Fatalf("mainframe dead-code = %+v", result)
	}
}
