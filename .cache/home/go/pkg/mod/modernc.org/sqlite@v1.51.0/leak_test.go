package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"modernc.org/libc"
)

// TestOpenV2FailureResourceLeak verifies that a failed sql.Open+Ping (e.g.,
// opening an invalid path) does not leak C-heap memory. Before the fix,
// every failed open leaked a libc.TLS and potentially a sqlite3 handle.
//
// Run with -tags memory.counters to enable allocation tracking.
func TestOpenV2FailureResourceLeak(t *testing.T) {
	// Use a path that will reliably fail to open: a non-existent directory.
	// SQLITE_OPEN_CREATE can create the file, but not intermediate dirs.
	badDSN := filepath.Join(t.TempDir(), "missing", "impossible.db")

	tryOpen := func() {
		db, err := sql.Open(driverName, badDSN)
		if err != nil {
			// sql.Open itself rarely errors; the real error comes from Ping/conn.
			return
		}
		err = db.Ping()
		if err == nil {
			t.Fatal("expected Ping to fail for invalid path")
		}
		db.Close()
	}

	// Warm up to reach steady state.
	for range 100 {
		tryOpen()
	}

	before := libc.MemStat()
	if before.Allocs == 0 && before.Bytes == 0 {
		t.Skip("requires -tags memory.counters")
	}
	for range 1000 {
		tryOpen()
	}
	after := libc.MemStat()

	leaked := after.Allocs - before.Allocs
	t.Logf("allocs before=%d after=%d delta=%d", before.Allocs, after.Allocs, leaked)
	if leaked > 100 {
		t.Fatalf("memory leak: net alloc count grew by %d over 1000 failed opens", leaked)
	}
}

// TestMultiStmtNopAllocsLeak exercises a leak in the multi-statement query
// path: when a middle statement binds parameters and returns SQLITE_DONE while
// a previous statement already set the rows result, the bind-parameter
// allocations are silently dropped. If a later statement also binds, it
// overwrites the allocs slice, leaking the earlier allocations.
//
// Run with -tags memory.counters to enable allocation tracking.
//
// Pattern: SELECT 1; UPDATE t SET v = ? WHERE 0; SELECT ?
//   - SELECT 1         → SQLITE_ROW, r set, no bind allocs
//   - UPDATE … WHERE 0 → bind allocs=[ptr_A], SQLITE_DONE, r!=nil → nop (ptr_A retained in allocs)
//   - SELECT ?         → bind allocs=[ptr_B] (ptr_A leaked), SQLITE_ROW
func TestMultiStmtNopAllocsLeak(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "CREATE TABLE t(v TEXT)"); err != nil {
		t.Fatal(err)
	}

	run := func(iters int) {
		for i := range iters {
			val := fmt.Sprintf("iter-%04d-%s", i, strings.Repeat("x", 1024))
			rows, err := conn.QueryContext(ctx, "SELECT 1; UPDATE t SET v = ? WHERE 0; SELECT ?", val)
			if err != nil {
				t.Fatalf("(%d) query: %v", i, err)
			}
			if err := rows.Close(); err != nil {
				t.Fatalf("(%d) close: %v", i, err)
			}
		}
	}

	// Warm up to reach steady state.
	run(100)

	before := libc.MemStat()
	run(1000)
	after := libc.MemStat()

	leaked := after.Allocs - before.Allocs
	t.Logf("allocs before=%d after=%d delta=%d", before.Allocs, after.Allocs, leaked)
	if leaked > 100 {
		t.Fatalf("memory leak: net alloc count grew by %d over 1000 iterations", leaked)
	}
}

// TestMultiStmtErrorAllocsLeak exercises a leak on the step-error path in the
// multi-statement query closure: when step returns an error (e.g. a UNIQUE
// constraint violation), allocs from the preceding bind are not freed.
//
// The failing statement is placed first so that no prior rows object exists —
// this isolates the allocs leak from the separate "orphaned rows" bug.
//
// Run with -tags memory.counters to enable allocation tracking.
//
// Pattern: INSERT INTO t VALUES(?); SELECT 1 — with a duplicate value
//   - INSERT INTO t VALUES(?) → bind allocs=[ptr_A], step error → allocs leaked
func TestMultiStmtErrorAllocsLeak(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "CREATE TABLE t(v TEXT UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	// Seed the table with a value that we'll collide with.
	collideVal := fmt.Sprintf("collide-%s", strings.Repeat("x", 1024))
	if _, err := conn.ExecContext(ctx, "INSERT INTO t VALUES(?)", collideVal); err != nil {
		t.Fatal(err)
	}

	run := func(iters int) {
		for i := range iters {
			_, err := conn.QueryContext(ctx, "INSERT INTO t VALUES(?); SELECT 1", collideVal)
			if err == nil {
				t.Fatalf("(%d) expected UNIQUE constraint error", i)
			}
		}
	}

	// Warm up to reach steady state.
	run(100)

	before := libc.MemStat()
	run(1000)
	after := libc.MemStat()

	leaked := after.Allocs - before.Allocs
	t.Logf("allocs before=%d after=%d delta=%d", before.Allocs, after.Allocs, leaked)
	if leaked > 100 {
		t.Fatalf("memory leak: net alloc count grew by %d over 1000 iterations", leaked)
	}
}

// TestMultiStmtOrphanedRowsOnError exercises a resource leak when a later
// statement in a multi-statement query errors after an earlier statement
// already produced a rows result: the error path discards the rows object
// without closing it, leaking its prepared statement handle and bind-parameter
// allocations.
//
// Run with -tags memory.counters to enable allocation tracking.
//
// Pattern: SELECT ?; INSERT INTO t VALUES(?) — with a duplicate value
//   - SELECT ? → SQLITE_ROW, r set (holds pstmt + allocs)
//   - INSERT   → step error (UNIQUE violation) → return nil, err → r orphaned
func TestMultiStmtOrphanedRowsOnError(t *testing.T) {
	db, err := sql.Open("sqlite", "file::memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "CREATE TABLE t(v TEXT UNIQUE)"); err != nil {
		t.Fatal(err)
	}
	collideVal := fmt.Sprintf("collide-%s", strings.Repeat("x", 1024))
	if _, err := conn.ExecContext(ctx, "INSERT INTO t VALUES(?)", collideVal); err != nil {
		t.Fatal(err)
	}

	run := func(iters int) {
		for i := range iters {
			_, err := conn.QueryContext(ctx, "SELECT ?; INSERT INTO t VALUES(?)", collideVal)
			if err == nil {
				t.Fatalf("(%d) expected UNIQUE constraint error", i)
			}
		}
	}

	// Warm up to reach steady state.
	run(100)

	before := libc.MemStat()
	run(1000)
	after := libc.MemStat()

	leaked := after.Allocs - before.Allocs
	t.Logf("allocs before=%d after=%d delta=%d", before.Allocs, after.Allocs, leaked)
	if leaked > 100 {
		t.Fatalf("memory leak: net alloc count grew by %d over 1000 iterations", leaked)
	}
}
