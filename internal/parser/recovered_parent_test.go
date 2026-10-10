package parser

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Recovered class spans can overlap without nesting. The method's class is
// the one that opens later, whatever the two spans' line counts: a comment
// line inside Outer (making it wider or narrower) must not change it.
func TestEnrichRecoveredClassParents_OverlapPicksLaterOpening(t *testing.T) {
	for _, outerEnd := range []int{60, 52, 100} {
		syms := []core.SymbolRecord{
			{FilePath: "A.cs", Kind: core.KindClass, Name: "Outer", Span: core.LineRange{Start: 10, End: outerEnd}},
			{FilePath: "A.cs", Kind: core.KindClass, Name: "Inner", Span: core.LineRange{Start: 11, End: 55}},
			{FilePath: "A.cs", Kind: core.KindMethod, Name: "Run", QualifiedName: "Run", Span: core.LineRange{Start: 20, End: 30}},
		}
		enrichRecoveredClassParents(syms)
		if got := syms[2].ParentSymbol; got != "Inner" {
			t.Fatalf("Outer ends at %d: parent = %q, want Inner", outerEnd, got)
		}
	}
}
