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
		// COPY edges: the include graph.
		"file:AUDITLOG.cbl -> import:CUSTREC",
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
