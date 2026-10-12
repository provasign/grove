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

func TestMergeEdgesPrefersNativeOnEqualConfidence(t *testing.T) {
	base := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.8, Source: core.EvidenceSourceHeuristic, Reason: core.ReasonDispatch,
	}}
	enriched := []core.Edge{{
		From: "a", To: "b", Type: core.EdgeCalls,
		Confidence: 0.8, Source: core.EvidenceSourceNative, Reason: core.ReasonASTNarrowed,
	}}

	merged := mergeEdges(base, enriched)
	if len(merged) != 1 {
		t.Fatalf("got %d edges, want 1", len(merged))
	}
	if merged[0].Source != core.EvidenceSourceNative || merged[0].Reason != core.ReasonASTNarrowed {
		t.Fatalf("equal-confidence native edge did not win: %#v", merged[0])
	}
}

// rust-analyzer analyzes the code active under the default features. A
// caller whose call sits in a #[cfg(...)]-disabled block has no native edge
// for it, so the parser's resolution must survive in an analyzed file; a
// guess the compiler contradicts (same caller, same callee name) is dropped.
func TestRustGuessesSurviveWhereRustAnalyzerSawNoCall(t *testing.T) {
	native := core.Edge{From: "src/fs/write.rs::write@a", To: "src/rt/handle.rs::Handle.current@b", Type: core.EdgeCalls, Source: core.EvidenceSourceNative, Confidence: 0.98}
	cfgGated := core.Edge{From: "src/fs/write.rs::write@a", To: "src/rt/handle.rs::Handle.try_current@b", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit, Confidence: 0.9}
	contradicted := core.Edge{From: "src/fs/write.rs::write@a", To: "src/other.rs::Other.current@c", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit, Confidence: 0.9}
	got := map[string]bool{}
	for _, e := range mergeEdges([]core.Edge{cfgGated, contradicted}, []core.Edge{native}) {
		got[e.To] = true
	}
	if !got[cfgGated.To] {
		t.Error("a call rust-analyzer did not see (cfg-disabled) lost its parser edge")
	}
	if got[contradicted.To] {
		t.Error("a guess the compiler contradicts survived")
	}
	if !got[native.To] {
		t.Error("native edge missing")
	}
}
