package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// TestEdgesBySourceMatchesFilteredAllEdges pins the sequence-equality
// contract the delta carry path depends on: EdgesBySource("native") must
// return exactly the rows a caller would get by filtering AllEdges, in the
// identical order (mergeEdges is first-wins on duplicate keys, so order is
// part of the contract).
func TestEdgesBySourceMatchesFilteredAllEdges(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	seed := []core.Edge{
		{From: "b.go::B@s", To: "a.go::A@s", Type: core.EdgeCalls, Confidence: 0.99, Source: core.EvidenceSourceNative},
		{From: "a.go::A@s", To: "b.go::B@s", Type: core.EdgeCalls, Confidence: 0.7, Source: core.EvidenceSourceHeuristic},
		{From: "a.go::A@s", To: "c.go::C@s", Type: core.EdgeUsesType, Confidence: 0.97, Source: core.EvidenceSourceNative},
		{From: "file:a.go", To: "a.go::A@s", Type: core.EdgeDefines, Confidence: 1.0, Source: core.EvidenceSourceASTKit},
		{From: "c.go::C@s", To: "a.go::A@s", Type: core.EdgeCalls, Confidence: 0.8, Source: core.EvidenceSourceHeuristic},
	}
	if err := st.ReplaceEdges(ctx, seed); err != nil {
		t.Fatal(err)
	}

	all, err := st.AllEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wantNative []core.Edge
	for _, e := range all {
		if e.Source == core.EvidenceSourceNative {
			wantNative = append(wantNative, e)
		}
	}

	got, err := st.EdgesBySource(ctx, string(core.EvidenceSourceNative))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wantNative) {
		t.Fatalf("EdgesBySource != filtered AllEdges:\ngot  %+v\nwant %+v", got, wantNative)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 native edges, got %d", len(got))
	}
}
