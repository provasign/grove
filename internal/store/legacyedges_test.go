package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
)

// A database written before graph_edges (TEXT id column in "edges") is
// converted in place on open: every edge, with all its metadata, survives;
// the old table is gone; and a database that was current apart from the
// layout has its stamp advanced so the next index does not re-extract.
func TestLegacyEdgesMigrateLosslessly(t *testing.T) {
	for _, tc := range []struct {
		stamp, want string
	}{
		{legacyEdgesStamp, parser.ExtractorVersion}, // current data: no rebuild
		{"2026-10-12.7", "2026-10-12.7"},            // older data: indexer rebuilds
	} {
		dir := t.TempDir()
		st, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		legacy := []core.Edge{
			{From: "a.go::A@s", To: "b.go::B@s", Type: "calls", Confidence: 0.95, Source: "astkit", Reason: "ast-narrowed"},
			{From: "a.go::A@s", To: "b.go::B@s", Type: "uses-type", Confidence: 0.7, Source: "heuristic"},
			{From: "c.go::C@s", To: "a.go::A@s", Type: "calls", Confidence: 1, Source: "native", Lines: "12:5,40:9"},
		}
		mustExec := func(q string, args ...any) {
			t.Helper()
			if _, err := st.db.ExecContext(ctx, q, args...); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
		}
		mustExec(`CREATE TABLE edges (id TEXT PRIMARY KEY, from_node TEXT NOT NULL, to_node TEXT NOT NULL,
			edge_type TEXT NOT NULL, confidence REAL NOT NULL DEFAULT 1.0, source TEXT NOT NULL DEFAULT 'unknown',
			reason TEXT NOT NULL DEFAULT '', lines TEXT NOT NULL DEFAULT '')`)
		for _, e := range legacy {
			mustExec(`INSERT INTO edges VALUES (?,?,?,?,?,?,?,?)`,
				e.From+"::"+string(e.Type)+"::"+e.To, e.From, e.To, string(e.Type), e.Confidence, string(e.Source), string(e.Reason), e.Lines)
		}
		must(t, st.SetMeta(ctx, "extractor-version", tc.stamp))
		must(t, st.Close())

		st, err = Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.AllEdges(ctx)
		must(t, err)
		want := append([]core.Edge(nil), legacy...)
		// AllEdges order: (from, type, to).
		want[0], want[1], want[2] = legacy[0], legacy[1], legacy[2]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("stamp %s: edges after migration\n got %+v\nwant %+v", tc.stamp, got, want)
		}
		var n int
		must(t, st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'edges'`).Scan(&n))
		if n != 0 {
			t.Fatalf("stamp %s: legacy edges table still present", tc.stamp)
		}
		stamp, _, err := st.GetMeta(ctx, "extractor-version")
		must(t, err)
		if stamp != tc.want {
			t.Fatalf("stamp %s: after migration = %s, want %s", tc.stamp, stamp, tc.want)
		}
		must(t, st.Close())
	}
}
