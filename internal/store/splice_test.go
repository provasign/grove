package store

import (
	"context"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// TestSpliceEdgesOverwritesStaleMetadata pins the store-vs-memory agreement
// the splice owes its caller. The in-memory edge set is authoritative and the
// only post-splice check is COUNT(*), which cannot see a metadata difference —
// so an upsert that declines to write on an equal-or-lower confidence would
// leave the stale row's source/reason in the table forever, silently.
func TestSpliceEdgesOverwritesStaleMetadata(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	stale := core.Edge{
		From: "a.go::A@s", To: "b.go::B@s", Type: core.EdgeCalls,
		Confidence: 0.9, Source: core.EvidenceSourceNative, Reason: core.ReasonDecorator,
	}
	if err := st.ReplaceEdges(ctx, []core.Edge{stale}); err != nil {
		t.Fatal(err)
	}

	// Same (from, type, to); lower confidence, different source and reason.
	fresh := stale
	fresh.Confidence = 0.7
	fresh.Source = core.EvidenceSourceHeuristic
	fresh.Reason = ""
	if err := st.SpliceEdges(ctx, nil, nil, []core.Edge{fresh}); err != nil {
		t.Fatal(err)
	}

	got, err := st.AllEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d edges, want 1", len(got))
	}
	if got[0].Confidence != fresh.Confidence || got[0].Source != fresh.Source || got[0].Reason != fresh.Reason {
		t.Errorf("stale row survived the splice: got %+v, want %+v", got[0], fresh)
	}
}

// TestSpliceEdgesDedupesWriteSetLikeMergeEdges: duplicate keys inside one
// write-set must resolve the way mergeEdges resolves them in memory — higher
// confidence wins, first occurrence wins on a tie — so the row that lands
// matches the in-memory set the COUNT(*) check compares against.
func TestSpliceEdgesDedupesWriteSetLikeMergeEdges(t *testing.T) {
	ctx := context.Background()

	base := core.Edge{From: "a.go::A@s", To: "b.go::B@s", Type: core.EdgeCalls}
	mk := func(conf float64, src core.EvidenceSource) core.Edge {
		e := base
		e.Confidence, e.Source = conf, src
		return e
	}

	cases := []struct {
		name    string
		inserts []core.Edge
		want    core.Edge
	}{
		{"higher confidence wins", []core.Edge{
			mk(0.7, core.EvidenceSourceHeuristic), mk(0.99, core.EvidenceSourceNative),
		}, mk(0.99, core.EvidenceSourceNative)},
		{"higher confidence first still wins", []core.Edge{
			mk(0.99, core.EvidenceSourceNative), mk(0.7, core.EvidenceSourceHeuristic),
		}, mk(0.99, core.EvidenceSourceNative)},
		{"first wins on tie", []core.Edge{
			mk(0.8, core.EvidenceSourceASTKit), mk(0.8, core.EvidenceSourceHeuristic),
		}, mk(0.8, core.EvidenceSourceASTKit)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openStore(t)
			if err := st.SpliceEdges(ctx, nil, nil, tc.inserts); err != nil {
				t.Fatal(err)
			}
			got, err := st.AllEdges(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d edges, want 1", len(got))
			}
			if got[0].Confidence != tc.want.Confidence || got[0].Source != tc.want.Source {
				t.Errorf("got %+v, want %+v", got[0], tc.want)
			}
		})
	}
}
