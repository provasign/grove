package graph_test

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

// Golden conformance test over testdata/mainframe-estate: the full
// parser→graph pipeline on a small estate whose files carry the traps that
// break naive column handling (sequence areas, identification area 73-80,
// comment lines containing PIC clauses, COBOL and JCL continuations,
// concatenated DDs, in-stream data). The expected sets are exact — a
// missing OR extra symbol/edge fails, so extractor drift is loud.

func indexEstate(t *testing.T) ([]core.SymbolRecord, []core.Edge) {
	t.Helper()
	symbols, files, err := parser.NewEngine().Walk("testdata/mainframe-estate")
	if err != nil {
		t.Fatal(err)
	}
	if files != 5 {
		t.Fatalf("indexed %d files, want 5", files)
	}
	return symbols, graph.BuildEdges(symbols)
}

func TestMainframeEstate_Symbols(t *testing.T) {
	symbols, _ := indexEstate(t)
	got := make([]string, 0, len(symbols))
	for _, s := range symbols {
		got = append(got, fmt.Sprintf("%s %s %s", s.Kind, s.QualifiedName, s.FilePath))
	}
	sort.Strings(got)

	want := []string{
		"condition-name WS-EOF.END-OF-FILE CUSTUPD.cbl",
		"data-item CUST-REC CUSTREC.cpy",
		"data-item CUST-REC.CUST-ALT CUSTREC.cpy",
		"data-item CUST-REC.CUST-KEY CUSTREC.cpy",
		"data-item CUST-REC.CUST-KEY.CUST-ID CUSTREC.cpy",
		"data-item CUST-REC.CUST-ORDERS CUSTREC.cpy",
		"data-item CUST-REC.CUST-SSN CUSTREC.cpy",
		"data-item WS-FLAGS CUSTUPD.cbl",
		"data-item WS-FLAGS.WS-EOF CUSTUPD.cbl",
		"data-item WS-RPT-PGM CUSTUPD.cbl",
		// Datasets are per-occurrence symbols; Name stays the bare DSN for
		// estate-wide unification, QualifiedName carries the binding step.
		"dataset UPDATE.PROD.CUST.MASTER NIGHTLY.jcl",
		"dataset UPDATE.PROD.CUST.MASTER.G0001V00 NIGHTLY.jcl",
		"dataset UPDATE.PROD.CUST.MASTER.NEW NIGHTLY.jcl",
		"job NIGHTLY NIGHTLY.jcl",
		// Paragraphs and logical files are program-qualified by grove's
		// projection (ParentName folded into QualifiedName) — consistent
		// with the IDs call edges use.
		"logical-file CUSTUPD.CUST-FILE CUSTUPD.cbl",
		"paragraph AUDITLOG.LOG-PARA AUDITLOG.cbl",
		"paragraph CUSTRPT.RPT-PARA CUSTRPT.cbl",
		"paragraph CUSTUPD.INIT-EXIT CUSTUPD.cbl",
		"paragraph CUSTUPD.INIT-PARA CUSTUPD.cbl",
		"paragraph CUSTUPD.MAIN-PARA CUSTUPD.cbl",
		"program AUDITLOG AUDITLOG.cbl",
		"program CUSTRPT CUSTRPT.cbl",
		"program CUSTUPD CUSTUPD.cbl",
		"step NIGHTLY.REPORT NIGHTLY.jcl",
		"step NIGHTLY.UPDATE NIGHTLY.jcl",
	}
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("symbol set drift:\n%s", diff)
	}
}

func TestMainframeEstate_CallEdges(t *testing.T) {
	_, edges := indexEstate(t)
	var got []string
	for _, e := range edges {
		if e.Type != core.EdgeCalls {
			continue
		}
		got = append(got, fmt.Sprintf("%s -> %s conf=%.1f %s",
			trimID(e.From), trimID(e.To), e.Confidence, e.Reason))
	}
	sort.Strings(got)

	want := []string{
		// PERFORM THRU: both endpoints, same unit, full confidence.
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA -> CUSTUPD.cbl::CUSTUPD.INIT-EXIT conf=1.0 ast-narrowed",
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA -> CUSTUPD.cbl::CUSTUPD.INIT-PARA conf=1.0 ast-narrowed",
		// CALL 'AUDITLOG': estate-wide program resolution.
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA -> AUDITLOG.cbl::AUDITLOG conf=0.9 ast-narrowed",
		// CALL WS-RPT-PGM: dynamic, constant-propagated from VALUE 'CUSTRPT'.
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA -> CUSTRPT.cbl::CUSTRPT conf=0.6 dispatch",
		// JCL EXEC PGM= and bare EXEC proc-name.
		"NIGHTLY.jcl::NIGHTLY.UPDATE -> CUSTUPD.cbl::CUSTUPD conf=0.9 ast-narrowed",
	}
	sort.Strings(want)
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("call-edge drift:\n%s", diff)
	}
}

func TestMainframeEstate_IncludeAndDatasetEdges(t *testing.T) {
	_, edges := indexEstate(t)
	var got []string
	for _, e := range edges {
		if e.Type != core.EdgeImports {
			continue
		}
		got = append(got, fmt.Sprintf("%s -> %s", e.From, e.To))
	}
	sort.Strings(got)

	want := []string{
		// COPY edges: the textual include record plus the resolved
		// member-to-file join.
		"file:AUDITLOG.cbl -> file:CUSTREC.cpy",
		"file:AUDITLOG.cbl -> import:CUSTREC",
		"file:CUSTUPD.cbl -> file:CUSTREC.cpy",
		"file:CUSTUPD.cbl -> import:CUSTREC",
		// DD DSN= bindings, including the concatenated unnamed DD and the
		// continued statement.
		"file:NIGHTLY.jcl -> import:PROD.CUST.MASTER",
		"file:NIGHTLY.jcl -> import:PROD.CUST.MASTER.G0001V00",
		"file:NIGHTLY.jcl -> import:PROD.CUST.MASTER.NEW",
	}
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("import/binding edge drift:\n%s", diff)
	}
}

// The trap assertions: things that MUST NOT be in the index.
func TestMainframeEstate_Traps(t *testing.T) {
	symbols, _ := indexEstate(t)
	for _, s := range symbols {
		if strings.Contains(s.Name, "FAKE-ITEM") {
			t.Error("comment-line PIC clause parsed as a data item")
		}
		if strings.Contains(s.Signature, "ID73-80X") {
			t.Errorf("identification area leaked into %s", s.QualifiedName)
		}
		if strings.Contains(s.Name, "IN-STREAM") {
			t.Error("JCL in-stream data parsed as code")
		}
	}
}

func trimID(id string) string {
	if i := strings.LastIndexByte(id, '@'); i >= 0 {
		return id[:i]
	}
	return id
}

func diffSets(want, got []string) string {
	w := map[string]bool{}
	g := map[string]bool{}
	for _, x := range want {
		w[x] = true
	}
	for _, x := range got {
		g[x] = true
	}
	var b strings.Builder
	for _, x := range want {
		if !g[x] {
			fmt.Fprintf(&b, "  missing: %s\n", x)
		}
	}
	for _, x := range got {
		if !w[x] {
			fmt.Fprintf(&b, "  extra:   %s\n", x)
		}
	}
	return b.String()
}

// Directional field-reference (lineage) edges: verb-classified reads and
// writes. Same-file fields roll up to the program symbol (volume bound);
// copybook fields keep paragraph granularity.
func TestMainframeEstate_FieldReferenceEdges(t *testing.T) {
	_, edges := indexEstate(t)
	var got []string
	for _, e := range edges {
		if e.Type != core.EdgeReads && e.Type != core.EdgeWrites {
			continue
		}
		got = append(got, fmt.Sprintf("%s %s %s", trimID(e.From), e.Type, trimID(e.To)))
	}
	sort.Strings(got)
	want := []string{
		// MOVE ZEROS TO CUST-SSN: copybook field, paragraph-level write.
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA writes CUSTREC.cpy::CUST-REC.CUST-SSN",
		// CALL USING CUST-REC: copybook group, read.
		"CUSTUPD.cbl::CUSTUPD.MAIN-PARA reads CUSTREC.cpy::CUST-REC",
		// MOVE 'N' TO WS-EOF: same-file target — rolled up to the program.
		"CUSTUPD.cbl::CUSTUPD writes CUSTUPD.cbl::WS-FLAGS.WS-EOF",
		// CALL WS-RPT-PGM: same-file read, rolled up to the program.
		"CUSTUPD.cbl::CUSTUPD reads CUSTUPD.cbl::WS-RPT-PGM",
	}
	sort.Strings(want)
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("field-reference edge drift:\n%s", diff)
	}
}

// REDEFINES is a first-class edge: CUST-ALT is an alternate view of CUST-SSN.
func TestMainframeEstate_RedefinesEdges(t *testing.T) {
	_, edges := indexEstate(t)
	var got []string
	for _, e := range edges {
		if e.Type == core.EdgeRedefines {
			got = append(got, fmt.Sprintf("%s -> %s", trimID(e.From), trimID(e.To)))
		}
	}
	want := []string{"CUSTREC.cpy::CUST-REC.CUST-ALT -> CUSTREC.cpy::CUST-REC.CUST-SSN"}
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("redefines edge drift:\n%s", diff)
	}
}

// Cross-artifact dataset binding (R-5.3): CUSTUPD declares CUST-FILE ASSIGN
// TO CUSTIN; step NIGHTLY.UPDATE executes CUSTUPD with CUSTIN DD naming two
// concatenated datasets. The derived edges join both artifacts.
func TestMainframeEstate_DatasetBinding(t *testing.T) {
	_, edges := indexEstate(t)
	var got []string
	for _, e := range edges {
		if e.Type == core.EdgeBinds {
			got = append(got, fmt.Sprintf("%s -> %s conf=%.1f %s", trimID(e.From), trimID(e.To), e.Confidence, e.Reason))
		}
	}
	sort.Strings(got)
	want := []string{
		"CUSTUPD.cbl::CUSTUPD.CUST-FILE -> NIGHTLY.jcl::UPDATE.PROD.CUST.MASTER conf=0.8 cross-artifact",
		"CUSTUPD.cbl::CUSTUPD.CUST-FILE -> NIGHTLY.jcl::UPDATE.PROD.CUST.MASTER.G0001V00 conf=0.8 cross-artifact",
	}
	sort.Strings(want)
	if diff := diffSets(want, got); diff != "" {
		t.Errorf("dataset binding drift:\n%s", diff)
	}
}

// The volume trap: the modern uses-type builder must never see mainframe
// symbols (measured leak on a real estate: 2.08M vague edges, 63% of the
// index). Their data flow is expressed by reads/writes/redefines only.
func TestMainframeEstate_NoModernUsesTypeLeak(t *testing.T) {
	symbols, edges := indexEstate(t)
	mfIDs := map[string]bool{}
	for _, s := range symbols {
		if s.Language == "cobol" || s.Language == "jcl" {
			mfIDs[s.ID] = true
		}
	}
	for _, e := range edges {
		if e.Type == core.EdgeUsesType && (mfIDs[e.From] || mfIDs[e.To]) {
			t.Errorf("uses-type edge on mainframe symbol: %s -> %s", trimID(e.From), trimID(e.To))
		}
	}
}
