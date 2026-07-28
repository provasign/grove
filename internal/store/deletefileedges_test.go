package store

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// seedEdges inserts raw edge rows directly, bypassing UpsertFile, so tests
// can construct exact from/to node shapes.
func seedEdges(t *testing.T, st *Store, edges [][2]string) {
	t.Helper()
	for _, e := range edges {
		if _, err := st.db.Exec(
			`INSERT INTO edges (id, from_node, to_node, edge_type, confidence, source) VALUES (?, ?, ?, 'calls', 1.0, 'astkit')`,
			e[0]+"::"+e[1], e[0], e[1],
		); err != nil {
			t.Fatalf("seed edge %v: %v", e, err)
		}
	}
}

func edgeNodes(t *testing.T, st *Store) []string {
	t.Helper()
	rows, err := st.db.Query(`SELECT from_node || ' -> ' || to_node FROM edges ORDER BY id`)
	if err != nil {
		t.Fatalf("query edges: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, s)
	}
	return out
}

// TestUpsertFileDeletesExactPrefixOnly covers the range-predicate rewrite of
// the per-file edge delete: wildcard-looking paths must not over-match, and
// prefix-sibling paths ("a.go" vs "a.gox", "a.go2") must survive.
func TestUpsertFileDeletesExactPrefixOnly(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	seedEdges(t, st, [][2]string{
		// belongs to a_b.go — must be deleted
		{"a_b.go::Foo@s1", "other.go::Bar@s2"},
		{"other.go::Bar@s2", "a_b.go::Foo@s1"},
		{"file:a_b.go", "a_b.go::Foo@s1"},
		// "_" as LIKE wildcard would have matched axb.go — must survive
		{"axb.go::Foo@s1", "other.go::Bar@s2"},
		// prefix siblings — must survive ("a_b.gox::" sorts above "a_b.go:;")
		{"a_b.gox::Foo@s1", "other.go::Bar@s2"},
		{"a_b.go2::Foo@s1", "other.go::Bar@s2"},
		// path containing % and \ — deleted only for its own upsert
		{`pa%th\x.go::F@s1`, "other.go::Bar@s2"},
		// unicode path
		{"päth.go::F@s1", "other.go::Bar@s2"},
	})

	if err := st.UpsertFile(ctx, "a_b.go", "sha", "go", -1, -1, nil); err != nil {
		t.Fatalf("upsert a_b.go: %v", err)
	}
	got := strings.Join(edgeNodes(t, st), "\n")
	for _, deleted := range []string{"a_b.go::Foo@s1 ->", "-> a_b.go::Foo@s1", "file:a_b.go ->"} {
		if strings.Contains(got, deleted) {
			t.Fatalf("edge anchored to a_b.go survived:\n%s", got)
		}
	}
	for _, kept := range []string{"axb.go::", "a_b.gox::", "a_b.go2::", `pa%th\x.go::`, "päth.go::"} {
		if !strings.Contains(got, kept) {
			t.Fatalf("unrelated edge %q was deleted:\n%s", kept, got)
		}
	}

	if err := st.UpsertFile(ctx, `pa%th\x.go`, "sha", "go", -1, -1, nil); err != nil {
		t.Fatalf("upsert wildcard path: %v", err)
	}
	if got := strings.Join(edgeNodes(t, st), "\n"); strings.Contains(got, `pa%th\x.go::`) {
		t.Fatalf("wildcard-char path edges survived their own upsert:\n%s", got)
	}
}

// TestDeleteFilesNotInUsesSamePrefixSemantics exercises the shared helper via
// the prune path.
func TestDeleteFilesNotInUsesSamePrefixSemantics(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()

	for _, f := range []string{"keep.go", "stale.go"} {
		if err := st.UpsertFile(ctx, f, "sha", "go", -1, -1, []core.SymbolRecord{
			{ID: f + "::F@sha", FilePath: f, BlobSHA: "sha", Language: "go", Kind: core.KindFunction, Name: "F"},
		}); err != nil {
			t.Fatalf("upsert %s: %v", f, err)
		}
	}
	seedEdges(t, st, [][2]string{
		{"stale.go::F@sha", "keep.go::F@sha"},
		{"keep.go::F@sha", "stale.go::F@sha"},
		{"stale.gox::F@sha", "keep.go::F@sha"}, // prefix sibling must survive
	})

	n, err := st.DeleteFilesNotIn(ctx, map[string]bool{"keep.go": true, "stale.gox": true})
	if err != nil {
		t.Fatalf("delete files not in: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d files, want 1", n)
	}
	got := strings.Join(edgeNodes(t, st), "\n")
	if strings.Contains(got, "stale.go::") {
		t.Fatalf("stale.go edges survived prune:\n%s", got)
	}
	if !strings.Contains(got, "stale.gox::") {
		t.Fatalf("prefix-sibling stale.gox edges were wrongly pruned:\n%s", got)
	}
}

// TestDeleteFileEdgesUsesIndexes pins the point of the rewrite: every DELETE
// must be served by idx_edge_from / idx_edge_to, never a full table scan.
func TestDeleteFileEdgesUsesIndexes(t *testing.T) {
	st := openStore(t)
	plans := []struct {
		sql       string
		args      []any
		wantIndex string
	}{
		{`DELETE FROM edges WHERE from_node = ?`, []any{"file:x.go"}, "idx_edge_from"},
		{`DELETE FROM edges WHERE from_node >= ? AND from_node < ?`, []any{"x.go::", "x.go:;"}, "idx_edge_from"},
		{`DELETE FROM edges WHERE to_node >= ? AND to_node < ?`, []any{"x.go::", "x.go:;"}, "idx_edge_to"},
	}
	for _, p := range plans {
		rows, err := st.db.Query("EXPLAIN QUERY PLAN "+p.sql, p.args...)
		if err != nil {
			t.Fatalf("explain %q: %v", p.sql, err)
		}
		var plan strings.Builder
		for rows.Next() {
			cols, err := rows.Columns()
			if err != nil {
				t.Fatalf("columns: %v", err)
			}
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatalf("scan plan: %v", err)
			}
			for _, v := range vals {
				if b, ok := v.([]byte); ok {
					plan.WriteString(string(b))
				} else if s, ok := v.(string); ok {
					plan.WriteString(s)
				}
				plan.WriteString(" ")
			}
		}
		rows.Close()
		if !strings.Contains(plan.String(), p.wantIndex) {
			t.Fatalf("query %q does not use %s; plan: %s", p.sql, p.wantIndex, plan.String())
		}
	}
}
