package store

import (
	"context"
	"strings"
	"testing"
)

// The per-file native deletes in SpliceEdges (and NativeEdgesInto) must use
// the from_node / to_node range index. With a plain source = 'native' SQLite
// chose idx_edge_source and scanned every native edge once per file: 25s of
// a 31s guava edit. A query edit that loses the "+source" guard regresses
// this silently, so pin the plans.
func TestNativeEdgeRangeQueriesUseNodeIndexes(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	plans := map[string]string{
		sqlDeleteNativeFromFile:   "idx_edge_from",
		sqlDeleteNativeFromFileID: "idx_edge_from",
		sqlNativeEdgesInto:        "idx_edge_to",
	}
	for q, want := range plans {
		rows, err := st.db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+q, "a::", "a:;", "a::", "a:;")
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "; ")
		}
		rows.Close()
		if !strings.Contains(plan.String(), want) {
			t.Errorf("%s\n  plan %q, want %s", q, plan.String(), want)
		}
	}
}
