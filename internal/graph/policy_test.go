// Traversal-policy gating (Wave 4). Confidence + resolver reason decide which
// edges a consumer closure may walk; profiles let tests/impact/certification
// opt into different strictness, and the choice is explainable (excluded edges
// are counted by reason).
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestTraversalPolicy_Allows(t *testing.T) {
	astExact := core.Edge{Confidence: 0.95, Reason: core.ReasonASTNarrowed}
	regex := core.Edge{Confidence: 0.85, Reason: core.ReasonRegexFallbck}
	dispatch := core.Edge{Confidence: 0.7, Reason: core.ReasonDispatch}
	typeUse := core.Edge{Confidence: 0.5, Reason: core.ReasonTypeRef}

	cases := []struct {
		policy TraversalPolicy
		edge   core.Edge
		want   bool
	}{
		{PolicyDiagnostic, regex, true}, {PolicyDiagnostic, typeUse, true},
		{PolicyTests, astExact, true}, {PolicyTests, regex, false}, // regex excluded by reason even at 0.85
		{PolicyTests, dispatch, true}, {PolicyTests, typeUse, false}, // 0.5 below floor
		{PolicyImpact, dispatch, true}, {PolicyImpact, regex, false}, {PolicyImpact, typeUse, false},
		{PolicyCertification, astExact, true}, {PolicyCertification, dispatch, false}, // dispatch not guarantee-grade
	}
	for _, c := range cases {
		if got := c.policy.Allows(c.edge); got != c.want {
			t.Errorf("%s.Allows(%s @%.2f) = %v, want %v", c.policy.Name, c.edge.Reason, c.edge.Confidence, got, c.want)
		}
	}
}

func TestTestsFor_PolicyExcludesRegexFallback(t *testing.T) {
	// T tests Y; Y calls X via a regex-fallback edge. So T transitively covers
	// X only if the closure walks that weak edge. PolicyTests must not (regex
	// excluded); PolicyDiagnostic must.
	g := New()
	syms := []core.SymbolRecord{
		{ID: "a.rb::X@s", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "X", QualifiedName: "X"},
		{ID: "a.rb::Y@s", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Y", QualifiedName: "Y"},
		{ID: "a_test.rb::TestY@s", FilePath: "a_test.rb", Language: "ruby", Kind: core.KindFunction, Name: "TestY", QualifiedName: "TestY"},
	}
	edges := []core.Edge{
		{From: "a.rb::Y@s", To: "a.rb::X@s", Type: core.EdgeCalls, Confidence: 0.85, Source: core.EvidenceSourceRegex, Reason: core.ReasonRegexFallbck},
		{From: "a_test.rb::TestY@s", To: "a.rb::Y@s", Type: core.EdgeTests, Confidence: 0.8, Source: core.EvidenceSourceHeuristic, Reason: core.ReasonTestEvidence},
	}
	g.ReplaceWithStoredEdges(syms, edges, 2)

	strict, skips := g.testsForWithPolicy("X", PolicyTests)
	if len(strict) != 0 {
		t.Fatalf("PolicyTests must not reach TestY through a regex-fallback call edge; got %+v", strict)
	}
	if skips[core.ReasonRegexFallbck] != 1 {
		t.Fatalf("expected 1 regex-fallback skip recorded, got %v", skips)
	}
	diag, _ := g.testsForWithPolicy("X", PolicyDiagnostic)
	if len(diag) != 1 || diag[0].Name != "TestY" {
		t.Fatalf("PolicyDiagnostic should reach TestY through the weak edge; got %+v", diag)
	}
}
