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
		{PolicyImpact, dispatch, true}, {PolicyImpact, regex, false}, {PolicyImpact, typeUse, false},
		{PolicyCertification, astExact, true}, {PolicyCertification, dispatch, false}, // dispatch not guarantee-grade
	}
	for _, c := range cases {
		if got := c.policy.Allows(c.edge); got != c.want {
			t.Errorf("%s.Allows(%s @%.2f) = %v, want %v", c.policy.Name, c.edge.Reason, c.edge.Confidence, got, c.want)
		}
	}
}
