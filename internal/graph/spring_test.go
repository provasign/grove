package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestSplitDerivedProps(t *testing.T) {
	cases := map[string][]string{
		"LoanId":            {"LoanId"},
		"LoanIdAndStatus":   {"LoanId", "Status"},
		"NameOrEmail":       {"Name", "Email"},
		"AndroidId":         {"AndroidId"}, // "And" inside a word must not split
		"OrderDateAndTotal": {"OrderDate", "Total"},
	}
	for in, want := range cases {
		got := splitDerivedProps(in)
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", in, got, want)
				break
			}
		}
	}
}

func TestBuildFrameworkEdges(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "e1", Language: "java", Kind: core.KindClass, Name: "Loan", QualifiedName: "Loan", FilePath: "Loan.java"},
		{ID: "g1", Language: "java", Kind: core.KindMethod, Name: "getLoanId", QualifiedName: "Loan.getLoanId", ParentSymbol: "Loan", FilePath: "Loan.java"},
		{ID: "g2", Language: "java", Kind: core.KindMethod, Name: "getStatus", QualifiedName: "Loan.getStatus", ParentSymbol: "Loan", FilePath: "Loan.java"},
		{ID: "gx", Language: "java", Kind: core.KindMethod, Name: "getLoanId", QualifiedName: "Other.getLoanId", ParentSymbol: "Other", FilePath: "Other.java"},
		{ID: "r1", Language: "java", Kind: core.KindInterface, Name: "LoanRepo", QualifiedName: "LoanRepo",
			Signature: "public interface LoanRepo extends JpaRepository<Loan, Long>", FilePath: "LoanRepo.java"},
		{ID: "m1", Language: "java", Kind: core.KindMethod, Name: "findByLoanIdAndStatus", QualifiedName: "LoanRepo.findByLoanIdAndStatus", ParentSymbol: "LoanRepo", FilePath: "LoanRepo.java"},
		{ID: "t1", Language: "plaintext", Kind: core.KindFile, Name: "view", FilePath: "templates/view.html",
			RawText: `<td th:text="${loan.loanId}"></td><input th:field="*{status}"/>`},
	}
	idx := newEdgeIndex(syms)
	edges := buildFrameworkEdges(idx, syms)
	got := map[string]float64{}
	for _, e := range edges {
		got[e.From+"->"+e.To] = e.Confidence
	}
	// Derived query: both properties, scoped to the repo's OWN entity —
	// Other.getLoanId must not receive a derived-query edge.
	if got["m1->g1"] != 0.7 || got["m1->g2"] != 0.7 {
		t.Errorf("derived-query edges missing: %v", got)
	}
	if _, ok := got["m1->gx"]; ok {
		t.Error("derived query leaked to another entity's accessor")
	}
	// Template EL: both identifiers, estate-wide (gx legitimately included).
	if got["t1->g1"] != 0.6 || got["t1->g2"] != 0.6 || got["t1->gx"] != 0.6 {
		t.Errorf("template EL edges missing: %v", got)
	}
}
