package store

import (
	"context"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// The tests-edge purge is a one-shot: it can only ever fire against a database
// written by a version that still persisted those edges. It used to run on
// every Open, and the only thing that made that cheap was idx_edge_type — an
// index over 7 distinct values across 600k rows, rebuilt at ~2s on every cold
// index, kept alive for a migration that fires once.
func TestPurgeTestsEdgesRunsOnceAndDropsItsIndex(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	done, err := st.migrationDone(ctx, purgeTestsEdgesKey)
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Error("purge marker not set after Open — the migration would re-run every time")
	}

	// The index it existed for must be gone.
	var n int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_edge_type'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("idx_edge_type still present — it has no callers left")
	}

	// ...and the index the delta carry path actually reads must exist.
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_edge_source'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Error("idx_edge_source missing — EdgesBySource falls back to a full-table scan")
	}
}

// Gating the purge must not stop it purging. A legacy row written before the
// marker existed still has to go on the next open.
func TestPurgeTestsEdgesStillPurgesLegacyRows(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	// Simulate a legacy database: a persisted tests edge, marker cleared.
	if _, err := st.db.ExecContext(ctx,
		`INSERT INTO edges (id, from_node, to_node, edge_type, confidence, source, reason)
		 VALUES ('x','a.go::A@s','b.go::B@s','tests',1.0,'heuristic','')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `DELETE FROM meta WHERE key = ?`, purgeTestsEdgesKey); err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	edges, err := st.AllEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range edges {
		if e.Type == core.EdgeType("tests") {
			t.Fatalf("legacy tests edge survived the gated migration: %+v", e)
		}
	}
}
