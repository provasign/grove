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

// A same-named overload's ID carries a "#N" suffix. It is not an
// interface anchor: javac resolving a call to JsonWriter.value(String)
// supersedes a name-guessed dispatch to JsonTreeWriter.value(boolean)
// (ID ...value@sha#2), which change_impact then listed as a caller of the
// boolean overload.
func TestOverloadSuffixIsNotAnAnchor(t *testing.T) {
	if q, anchor := symbolQual("w/JsonTreeWriter.java::JsonTreeWriter.value@abc#2"); q != "JsonTreeWriter.value" || anchor {
		t.Fatalf("overload suffix read as %q anchor=%v", q, anchor)
	}
	if q, anchor := symbolQual("x.go::Iface@abc#Method"); q != "Iface.Method" || !anchor {
		t.Fatalf("anchor read as %q anchor=%v", q, anchor)
	}
	native := core.Edge{From: "a/E.java::E.write@s", To: "w/JsonWriter.java::JsonWriter.value@w", Type: core.EdgeCalls, Source: core.EvidenceSourceNative, Confidence: 0.99}
	guess := core.Edge{From: "a/E.java::E.write@s", To: "w/JsonTreeWriter.java::JsonTreeWriter.value@t#2", Type: core.EdgeCalls, Source: core.EvidenceSourceHeuristic, Confidence: 0.7, Reason: core.ReasonDispatch}
	for _, e := range mergeEdges([]core.Edge{guess}, []core.Edge{native}) {
		if e.To == guess.To {
			t.Fatal("a guess javac contradicts survived because its target is an overload")
		}
	}
}

// Overloads share a qualified name. javac resolving value(1) to
// value(long) supersedes guesses to the same file's value(float)/
// value(Number); a same-named declaration in another file (a Go build-tag
// twin) is still kept.
func TestCompilerPickedOverloadSupersedesSiblings(t *testing.T) {
	from := "t/GsonTest.java::GsonTest.<lambda@431:73>@s"
	native := core.Edge{From: from, To: "w/JsonWriter.java::JsonWriter.value@w#6", Type: core.EdgeCalls, Source: core.EvidenceSourceNative, Confidence: 0.99}
	sibling := core.Edge{From: from, To: "w/JsonWriter.java::JsonWriter.value@w#7", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit, Confidence: 0.95, Reason: core.ReasonASTNarrowed}
	gFrom := "b/x.go::use@s"
	gNative := core.Edge{From: gFrom, To: "b/binding.go::validate@a", Type: core.EdgeCalls, Source: core.EvidenceSourceNative, Confidence: 0.99}
	twin := core.Edge{From: gFrom, To: "b/binding_nomsgpack.go::validate@b", Type: core.EdgeCalls, Source: core.EvidenceSourceASTKit, Confidence: 0.95}
	got := map[string]bool{}
	for _, e := range mergeEdges([]core.Edge{sibling, twin}, []core.Edge{native, gNative}) {
		got[e.To] = true
	}
	if got[sibling.To] {
		t.Error("an overload javac did not pick survived")
	}
	if !got[twin.To] {
		t.Error("a build-tag twin in another file was dropped")
	}
}

func TestSameParamTypeKeepsBoxedOverloadsApart(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"boolean", "Boolean", false}, {"long", "Long", false}, {"String", "String", true}, {"string", "String", true}, {"object", "Object", true}} {
		if got := sameParamType(c.a, c.b); got != c.want {
			t.Errorf("sameParamType(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
