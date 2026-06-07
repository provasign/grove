package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestMergeEdgesPrefersHigherConfidenceNativeEdge(t *testing.T) {
	base := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.6, Source: core.EvidenceSourceRegex,
	}}
	enriched := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.99, Source: core.EvidenceSourceNative,
	}}

	merged := mergeEdges(base, enriched)
	if len(merged) != 1 {
		t.Fatalf("got %d edges, want 1", len(merged))
	}
	if merged[0].Confidence != 0.99 || merged[0].Source != core.EvidenceSourceNative {
		t.Fatalf("native edge did not win: %#v", merged[0])
	}
}

func TestMergeEdgesKeepsBaselineWhenNativeIsWeaker(t *testing.T) {
	base := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.95, Source: core.EvidenceSourceTreeSitter,
	}}
	enriched := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.8, Source: core.EvidenceSourceNative,
	}}

	merged := mergeEdges(base, enriched)
	if len(merged) != 1 {
		t.Fatalf("got %d edges, want 1", len(merged))
	}
	if merged[0].Confidence != 0.95 || merged[0].Source != core.EvidenceSourceTreeSitter {
		t.Fatalf("baseline edge should remain: %#v", merged[0])
	}
}
