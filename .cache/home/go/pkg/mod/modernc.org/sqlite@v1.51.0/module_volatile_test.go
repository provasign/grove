// Copyright 2025 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sqlite

import (
	"database/sql"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"modernc.org/sqlite/vtab"
)

// volNoopModule is a minimal vtab module used by the VolatileArgs vtab
// benchmarks and correctness tests. Filter and the Updater callbacks do as
// little as possible so that the cost measured is dominated by argument
// marshalling in functionArgs, not by user code.
type volNoopModule struct {
	volatile      bool
	recordFilter  func(vals []vtab.Value)
	insertedRowid int64
	mu            sync.Mutex
}

// VolatileArgs opts the module into zero-copy TEXT/BLOB argument access on
// the Cursor.Filter and Updater.Insert / Updater.Update paths.
func (m *volNoopModule) VolatileArgs() bool { return m.volatile }

func (m *volNoopModule) Create(ctx vtab.Context, args []string) (vtab.Table, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("volnoop: missing table name")
	}
	if err := ctx.Declare(fmt.Sprintf("CREATE TABLE %s(s TEXT, b BLOB)", args[2])); err != nil {
		return nil, err
	}
	return &volNoopTable{mod: m}, nil
}

func (m *volNoopModule) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Create(ctx, args)
}

type volNoopTable struct {
	mod *volNoopModule
}

func (t *volNoopTable) BestIndex(info *vtab.IndexInfo) error {
	// Accept every usable EQ constraint and assign sequential argv positions
	// so the RHS values reach Cursor.Filter through xFilter's argv. Omit
	// pushes the constraint fully to the vtab so SQLite does not re-evaluate
	// it on the engine side.
	n := 0
	for i := range info.Constraints {
		c := &info.Constraints[i]
		if !c.Usable || c.Op != vtab.OpEQ {
			continue
		}
		c.ArgIndex = n
		c.Omit = true
		n++
	}
	return nil
}

func (t *volNoopTable) Open() (vtab.Cursor, error) { return &volNoopCursor{tbl: t}, nil }
func (t *volNoopTable) Disconnect() error          { return nil }
func (t *volNoopTable) Destroy() error             { return nil }

// Updater methods (optional). These are no-ops so the cost measured in the
// vtab Update benchmarks is dominated by argument marshalling.
func (t *volNoopTable) Insert(cols []vtab.Value, rowid *int64) error {
	t.mod.mu.Lock()
	t.mod.insertedRowid++
	id := t.mod.insertedRowid
	t.mod.mu.Unlock()
	if rowid != nil && *rowid == 0 {
		*rowid = id
	}
	return nil
}

func (t *volNoopTable) Update(oldRowid int64, cols []vtab.Value, newRowid *int64) error {
	return nil
}

func (t *volNoopTable) Delete(oldRowid int64) error { return nil }

type volNoopCursor struct {
	tbl *volNoopTable
	eof bool
}

func (c *volNoopCursor) Filter(idxNum int, idxStr string, vals []vtab.Value) error {
	if c.tbl.mod.recordFilter != nil {
		c.tbl.mod.recordFilter(vals)
	}
	// Mark Eof immediately so the query returns no rows. Filter still ran
	// once per query, which is what the benchmark and tests count on.
	c.eof = true
	return nil
}

func (c *volNoopCursor) Next() error                        { return nil }
func (c *volNoopCursor) Eof() bool                          { return c.eof }
func (c *volNoopCursor) Column(col int) (vtab.Value, error) { return nil, nil }
func (c *volNoopCursor) Rowid() (int64, error)              { return 0, nil }
func (c *volNoopCursor) Close() error                       { return nil }

// volStoredModule is a stateful counterpart to volNoopModule used by the
// Update correctness test. It stores rows in memory so that SQLite's xUpdate
// dispatch can find a row by rowid and reach Updater.Update.
type volStoredModule struct {
	volatile     bool
	rows         []volStoredRow
	nextID       int64
	recordInsert func(cols []vtab.Value)
	recordUpdate func(oldRowid int64, cols []vtab.Value)
	mu           sync.Mutex
}

type volStoredRow struct {
	id int64
	s  string
	b  []byte
}

func (m *volStoredModule) VolatileArgs() bool { return m.volatile }

func (m *volStoredModule) Create(ctx vtab.Context, args []string) (vtab.Table, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("volstored: missing table name")
	}
	if err := ctx.Declare(fmt.Sprintf("CREATE TABLE %s(s TEXT, b BLOB)", args[2])); err != nil {
		return nil, err
	}
	return &volStoredTable{mod: m}, nil
}

func (m *volStoredModule) Connect(ctx vtab.Context, args []string) (vtab.Table, error) {
	return m.Create(ctx, args)
}

type volStoredTable struct{ mod *volStoredModule }

func (t *volStoredTable) BestIndex(info *vtab.IndexInfo) error { return nil }
func (t *volStoredTable) Open() (vtab.Cursor, error)           { return &volStoredCursor{tbl: t, pos: -1}, nil }
func (t *volStoredTable) Disconnect() error                    { return nil }
func (t *volStoredTable) Destroy() error                       { return nil }

func (t *volStoredTable) Insert(cols []vtab.Value, rowid *int64) error {
	if t.mod.recordInsert != nil {
		t.mod.recordInsert(cols)
	}
	t.mod.mu.Lock()
	defer t.mod.mu.Unlock()
	t.mod.nextID++
	id := t.mod.nextID
	if rowid != nil && *rowid != 0 {
		id = *rowid
	}
	s := cols[0].(string)
	var b []byte
	if cols[1] != nil {
		b = cloneBytes(cols[1].([]byte))
	}
	t.mod.rows = append(t.mod.rows, volStoredRow{id: id, s: strings.Clone(s), b: b})
	if rowid != nil {
		*rowid = id
	}
	return nil
}

func (t *volStoredTable) Update(oldRowid int64, cols []vtab.Value, newRowid *int64) error {
	if t.mod.recordUpdate != nil {
		t.mod.recordUpdate(oldRowid, cols)
	}
	t.mod.mu.Lock()
	defer t.mod.mu.Unlock()
	for i := range t.mod.rows {
		if t.mod.rows[i].id == oldRowid {
			t.mod.rows[i].s = strings.Clone(cols[0].(string))
			if cols[1] == nil {
				t.mod.rows[i].b = nil
			} else {
				t.mod.rows[i].b = cloneBytes(cols[1].([]byte))
			}
			return nil
		}
	}
	return fmt.Errorf("row %d not found", oldRowid)
}

func (t *volStoredTable) Delete(oldRowid int64) error {
	t.mod.mu.Lock()
	defer t.mod.mu.Unlock()
	for i := range t.mod.rows {
		if t.mod.rows[i].id == oldRowid {
			t.mod.rows = append(t.mod.rows[:i], t.mod.rows[i+1:]...)
			return nil
		}
	}
	return nil
}

type volStoredCursor struct {
	tbl *volStoredTable
	pos int
}

func (c *volStoredCursor) Filter(idxNum int, idxStr string, vals []vtab.Value) error {
	c.pos = 0
	return nil
}
func (c *volStoredCursor) Next() error { c.pos++; return nil }
func (c *volStoredCursor) Eof() bool   { return c.pos >= len(c.tbl.mod.rows) }
func (c *volStoredCursor) Column(col int) (vtab.Value, error) {
	if c.pos < 0 || c.pos >= len(c.tbl.mod.rows) {
		return nil, nil
	}
	r := c.tbl.mod.rows[c.pos]
	switch col {
	case 0:
		return r.s, nil
	case 1:
		return r.b, nil
	}
	return nil, nil
}
func (c *volStoredCursor) Rowid() (int64, error) {
	if c.pos < 0 || c.pos >= len(c.tbl.mod.rows) {
		return 0, nil
	}
	return c.tbl.mod.rows[c.pos].id, nil
}
func (c *volStoredCursor) Close() error { return nil }

// volBenchModules registers the package-global vtab modules used by the
// VolatileArgs benchmarks. Module registration is process-global (one entry
// per name in modernc.org/sqlite), so a benchmark loop that warms up and
// re-enters its setup must not register repeatedly.
func init() {
	for _, m := range []struct {
		name string
		mod  *volNoopModule
	}{
		{"volnoop_filter_default", &volNoopModule{volatile: false}},
		{"volnoop_filter_volatile", &volNoopModule{volatile: true}},
		{"volnoop_update_default", &volNoopModule{volatile: false}},
		{"volnoop_update_volatile", &volNoopModule{volatile: true}},
	} {
		if err := vtab.RegisterModule(nil, m.name, m.mod); err != nil {
			panic(fmt.Sprintf("volatile bench init: RegisterModule(%s): %v", m.name, err))
		}
	}
}

// benchVTabFilterArgs runs many xFilter invocations with a TEXT and a BLOB
// argv value, so that the per-call cost is dominated by argument marshalling
// in functionArgs.
func benchVTabFilterArgs(b *testing.B, moduleName string) {
	db, err := sql.Open(driverName, "file::memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE vt USING %s()`, moduleName)); err != nil {
		b.Fatalf("create virtual table: %v", err)
	}

	stmt, err := db.Prepare(`SELECT * FROM vt WHERE s = ? AND b = ?`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()

	text := "hello"
	blob := []byte{1, 2, 3}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r, err := stmt.Query(text, blob)
		if err != nil {
			b.Fatal(err)
		}
		for r.Next() {
		}
		if err := r.Err(); err != nil {
			b.Fatal(err)
		}
		r.Close()
	}
}

// BenchmarkVTabFilterArgs measures allocations in functionArgs on the
// xFilter -> Cursor.Filter path with VolatileArgs=false.
func BenchmarkVTabFilterArgs(b *testing.B) {
	benchVTabFilterArgs(b, "volnoop_filter_default")
}

// BenchmarkVTabFilterArgsVolatile mirrors BenchmarkVTabFilterArgs but uses a
// module that opts into VolatileArgs. The difference between the two
// benchmarks isolates the per-call cost of copying TEXT and BLOB argument
// bodies into Go-owned memory on the vtab Filter path.
func BenchmarkVTabFilterArgsVolatile(b *testing.B) {
	benchVTabFilterArgs(b, "volnoop_filter_volatile")
}

// benchVTabUpdateArgs runs many INSERTs against a writable vtab so that
// xUpdate -> Updater.Insert is invoked once per iteration with TEXT + BLOB
// column values.
func benchVTabUpdateArgs(b *testing.B, moduleName string) {
	db, err := sql.Open(driverName, "file::memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(fmt.Sprintf(`CREATE VIRTUAL TABLE vt USING %s()`, moduleName)); err != nil {
		b.Fatalf("create virtual table: %v", err)
	}

	stmt, err := db.Prepare(`INSERT INTO vt(s, b) VALUES(?, ?)`)
	if err != nil {
		b.Fatal(err)
	}
	defer stmt.Close()

	text := "hello"
	blob := []byte{1, 2, 3}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := stmt.Exec(text, blob); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkVTabUpdateArgs measures allocations in functionArgs on the
// xUpdate -> Updater.Insert path with VolatileArgs=false.
func BenchmarkVTabUpdateArgs(b *testing.B) {
	benchVTabUpdateArgs(b, "volnoop_update_default")
}

// BenchmarkVTabUpdateArgsVolatile mirrors BenchmarkVTabUpdateArgs but uses a
// module that opts into VolatileArgs.
func BenchmarkVTabUpdateArgsVolatile(b *testing.B) {
	benchVTabUpdateArgs(b, "volnoop_update_volatile")
}

// TestVTabVolatileFilter verifies that a vtab module opting into
// VolatileArgs still receives correct TEXT and BLOB argument values for
// every xFilter invocation, including the empty cases that take a
// short-circuit path in functionArgs. The recorder copies each value
// immediately, which is the required usage pattern for VolatileArgs
// callbacks.
func TestVTabVolatileFilter(t *testing.T) {
	var (
		mu         sync.Mutex
		gotStrings []string
		gotBlobs   [][]byte
	)
	mod := &volNoopModule{
		volatile: true,
		recordFilter: func(vals []vtab.Value) {
			if len(vals) != 2 {
				return
			}
			s := vals[0].(string)
			b := vals[1].([]byte)
			mu.Lock()
			gotStrings = append(gotStrings, strings.Clone(s))
			gotBlobs = append(gotBlobs, cloneBytes(b))
			mu.Unlock()
		},
	}
	if err := vtab.RegisterModule(nil, "volfilter_recorder", mod); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE vt USING volfilter_recorder()`); err != nil {
		t.Fatalf("create virtual table: %v", err)
	}

	cases := []struct {
		s string
		b []byte
	}{
		{"alpha", []byte{1, 2, 3, 4}},
		{"beta", []byte{0xAA, 0xBB}},
		{"", []byte{}},
	}
	for _, c := range cases {
		r, err := db.Query(`SELECT * FROM vt WHERE s = ? AND b = ?`, c.s, c.b)
		if err != nil {
			t.Fatalf("query %q: %v", c.s, err)
		}
		for r.Next() {
		}
		if err := r.Err(); err != nil {
			t.Fatalf("rows.Err: %v", err)
		}
		r.Close()
	}

	wantStrings := []string{"alpha", "beta", ""}
	if !reflect.DeepEqual(gotStrings, wantStrings) {
		t.Errorf("volatile Filter TEXT: got %q, want %q", gotStrings, wantStrings)
	}
	wantBlobs := [][]byte{{1, 2, 3, 4}, {0xAA, 0xBB}, {}}
	if !reflect.DeepEqual(gotBlobs, wantBlobs) {
		t.Errorf("volatile Filter BLOB: got %v, want %v", gotBlobs, wantBlobs)
	}
}

// TestVTabVolatileUpdate verifies that a vtab module opting into
// VolatileArgs receives correct column values for Updater.Insert and
// Updater.Update. The recorder copies values immediately before returning.
func TestVTabVolatileUpdate(t *testing.T) {
	var (
		mu          sync.Mutex
		insertStrs  []string
		insertBlobs [][]byte
		updateStrs  []string
		updateBlobs [][]byte
	)
	mod := &volStoredModule{
		volatile: true,
		recordInsert: func(cols []vtab.Value) {
			if len(cols) != 2 {
				return
			}
			s := cols[0].(string)
			b := cols[1].([]byte)
			mu.Lock()
			insertStrs = append(insertStrs, strings.Clone(s))
			insertBlobs = append(insertBlobs, cloneBytes(b))
			mu.Unlock()
		},
		recordUpdate: func(oldRowid int64, cols []vtab.Value) {
			if len(cols) != 2 {
				return
			}
			s := cols[0].(string)
			b := cols[1].([]byte)
			mu.Lock()
			updateStrs = append(updateStrs, strings.Clone(s))
			updateBlobs = append(updateBlobs, cloneBytes(b))
			mu.Unlock()
		},
	}
	if err := vtab.RegisterModule(nil, "volupdate_recorder", mod); err != nil {
		t.Fatalf("RegisterModule: %v", err)
	}

	db, err := sql.Open(driverName, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE VIRTUAL TABLE vt USING volupdate_recorder()`); err != nil {
		t.Fatalf("create virtual table: %v", err)
	}

	// Insert rows with TEXT + BLOB.
	if _, err := db.Exec(`INSERT INTO vt(s, b) VALUES('alpha', X'01020304'), ('beta', X'AABB'), ('', X'')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	wantInsertStrs := []string{"alpha", "beta", ""}
	if !reflect.DeepEqual(insertStrs, wantInsertStrs) {
		t.Errorf("volatile Insert TEXT: got %q, want %q", insertStrs, wantInsertStrs)
	}
	wantInsertBlobs := [][]byte{{1, 2, 3, 4}, {0xAA, 0xBB}, {}}
	if !reflect.DeepEqual(insertBlobs, wantInsertBlobs) {
		t.Errorf("volatile Insert BLOB: got %v, want %v", insertBlobs, wantInsertBlobs)
	}

	// Update one row by rowid so xUpdate dispatches to Updater.Update with
	// non-NULL oldRowid.
	if _, err := db.Exec(`UPDATE vt SET s = 'gamma', b = X'CCDD' WHERE rowid = 1`); err != nil {
		t.Fatalf("update: %v", err)
	}
	wantUpdateStrs := []string{"gamma"}
	if !reflect.DeepEqual(updateStrs, wantUpdateStrs) {
		t.Errorf("volatile Update TEXT: got %q, want %q", updateStrs, wantUpdateStrs)
	}
	wantUpdateBlobs := [][]byte{{0xCC, 0xDD}}
	if !reflect.DeepEqual(updateBlobs, wantUpdateBlobs) {
		t.Errorf("volatile Update BLOB: got %v, want %v", updateBlobs, wantUpdateBlobs)
	}
}
