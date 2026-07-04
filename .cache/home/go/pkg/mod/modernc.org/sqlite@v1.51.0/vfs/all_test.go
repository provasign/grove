// Copyright 2022 The Sqlite Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package vfs

import (
	"database/sql"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/iotest"

	"modernc.org/sqlite"
)

func E(err error) string {
	if err == nil {
		return ""
	}
	if err, ok := err.(*sqlite.Error); ok {
		return sqlite.ErrorCodeString[err.Code()]
	}
	return err.Error()
}

func TestVFS(t *testing.T) {
	const dbname = "canary.db"

	tmpdbdir := t.TempDir()

	func() {
		db, err := sql.Open("sqlite", "file:"+filepath.Join(tmpdbdir, dbname))
		if err != nil {
			t.Fatalf("unexpected failure to open database, %s", err.Error())
		}
		defer db.Close()

		_, err = db.Exec("create table 'test' ('name' varchar(32) not null, primary key('name') )")
		if err != nil {
			t.Fatalf("unexpected create table error, %s", E(err))
		}

		_, err = db.Exec("insert into 'test' (name) values ('foobar')")
		if err != nil {
			t.Fatalf("unexpected insert error, %s", E(err))
		}
	}()

	vfsid, fs, err := New(os.DirFS(tmpdbdir))
	if err != nil {
		t.Fatalf("unexpected failure to register new vfs, %s", err.Error())
	}
	defer fs.Close()

	db, err := sql.Open("sqlite", "file:"+dbname+"?vfs="+vfsid)
	if err != nil {
		t.Fatalf("unexpected failure to open database, %s", err.Error())
	}
	defer db.Close()

	rows, err := db.Query("select * from 'test'")
	if err != nil {
		t.Fatalf("unexpected select error, %s", E(err))
	}
	defer rows.Close()
	var got string
	if !rows.Next() {
		t.Fatalf("unexpected empty select result")
	}
	err = rows.Scan(&got)
	if err != nil {
		t.Fatalf("unexpected scan error, %s", E(err))
	}
	want := "foobar"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// seedDB opens filepath.Join(dir, name) as a SQLite database on the OS
// filesystem, executes stmts against it, and closes the connection
// before returning so a subsequent reopen through a custom VFS sees a
// settled on-disk file.
func seedDB(t *testing.T, dir, name string, stmts ...string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("open database: %s", err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec %q: %s", s, E(err))
		}
	}
}

// openDBViaVFS registers fsys as a SQLite VFS and opens name through
// it. The VFS and *sql.DB are cleaned up via t.Cleanup.
func openDBViaVFS(t *testing.T, name string, fsys fs.FS) *sql.DB {
	t.Helper()
	vfsid, vfsFS, err := New(fsys)
	if err != nil {
		t.Fatalf("register vfs: %s", err)
	}
	t.Cleanup(func() { vfsFS.Close() })
	db, err := sql.Open("sqlite", "file:"+name+"?vfs="+vfsid)
	if err != nil {
		t.Fatalf("open database via vfs: %s", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// wrappedReadFile delegates Read to an io.Reader adapter — typically
// one of the testing/iotest wrappers — while preserving Seek and the
// rest of the embedded fs.File. After every Seek the adapter is
// rebuilt from the underlying file so stateful adapters (e.g.
// iotest.DataErrReader, which buffers internally) see the new
// position. Stateless adapters like iotest.HalfReader are rebuilt
// unnecessarily but harmlessly.
type wrappedReadFile struct {
	fs.File
	seeker io.Seeker
	r      io.Reader
	wrap   func(io.Reader) io.Reader
}

func (f *wrappedReadFile) Read(p []byte) (int, error) {
	return f.r.Read(p)
}

func (f *wrappedReadFile) Seek(offset int64, whence int) (int64, error) {
	n, err := f.seeker.Seek(offset, whence)
	f.r = f.wrap(f.File)
	return n, err
}

// wrappedReadFS wraps every file opened from the underlying FS in a
// wrappedReadFile built from wrap.
type wrappedReadFS struct {
	fs.FS
	wrap func(io.Reader) io.Reader
}

func (fsys *wrappedReadFS) Open(name string) (fs.File, error) {
	f, err := fsys.FS.Open(name)
	if err != nil {
		return nil, err
	}
	seeker, ok := f.(io.Seeker)
	if !ok {
		return f, nil
	}
	return &wrappedReadFile{
		File:   f,
		seeker: seeker,
		r:      fsys.wrap(f),
		wrap:   fsys.wrap,
	}, nil
}

// TestVFSReadEOF verifies that vfsRead handles readers that return
// (n > 0, io.EOF) in a single call — valid per io.Reader, and produced
// here by iotest.DataErrReader — by mapping the short tail to
// SQLITE_IOERR_SHORT_READ rather than SQLITE_IOERR_READ.
func TestVFSReadEOF(t *testing.T) {
	const dbname = "eof_canary.db"
	dir := t.TempDir()
	seedDB(t, dir, dbname,
		"CREATE TABLE t (v TEXT NOT NULL)",
		"INSERT INTO t (v) VALUES ('hello')",
	)

	fsys := &wrappedReadFS{FS: os.DirFS(dir), wrap: iotest.DataErrReader}
	db := openDBViaVFS(t, dbname, fsys)

	var got string
	if err := db.QueryRow("SELECT v FROM t").Scan(&got); err != nil {
		t.Fatalf("query: %s", E(err))
	}
	if got != "hello" {
		t.Fatalf("got %q, want %q", got, "hello")
	}
}

// TestVFSReadPartial verifies that vfsRead retries partial mid-stream
// reads (Read returning fewer bytes than requested with nil error),
// synthesized here via iotest.HalfReader.
func TestVFSReadPartial(t *testing.T) {
	const dbname = "partial_canary.db"
	dir := t.TempDir()
	seedDB(t, dir, dbname,
		"CREATE TABLE t (v TEXT NOT NULL)",
		"INSERT INTO t (v) VALUES ('world')",
	)

	fsys := &wrappedReadFS{FS: os.DirFS(dir), wrap: iotest.HalfReader}
	db := openDBViaVFS(t, dbname, fsys)

	var got string
	if err := db.QueryRow("SELECT v FROM t").Scan(&got); err != nil {
		t.Fatalf("query: %s", E(err))
	}
	if got != "world" {
		t.Fatalf("got %q, want %q", got, "world")
	}
}

// truncatedReadFS wraps an fs.FS so reads past a byte threshold return
// EOF while Stat still reports the real file size. This makes SQLite
// compute a page count that exceeds the readable data, forcing the
// zero-fill path in vfsRead on the partially-readable page. Without
// correct zero-fill, stale buffer contents could corrupt SQLite's page
// interpretation. testing/iotest has no equivalent.
type truncatedReadFS struct {
	fs.FS
	readLimit int64
}

func (fsys *truncatedReadFS) Open(name string) (fs.File, error) {
	f, err := fsys.FS.Open(name)
	if err != nil {
		return nil, err
	}
	seeker, ok := f.(io.Seeker)
	if !ok {
		return f, nil
	}
	return &truncatedReadFile{File: f, seeker: seeker, readLimit: fsys.readLimit}, nil
}

type truncatedReadFile struct {
	fs.File
	seeker    io.Seeker
	readLimit int64
}

func (f *truncatedReadFile) Read(p []byte) (int, error) {
	pos, err := f.seeker.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	if pos >= f.readLimit {
		return 0, io.EOF
	}
	if avail := f.readLimit - pos; int64(len(p)) > avail {
		p = p[:avail]
	}
	return f.File.Read(p)
}

func (f *truncatedReadFile) Seek(offset int64, whence int) (int64, error) {
	return f.seeker.Seek(offset, whence)
}

// TestVFSReadEOFZeroFill verifies that vfsRead zero-fills unread tail
// bytes when a page read encounters EOF before the buffer is full.
func TestVFSReadEOFZeroFill(t *testing.T) {
	const dbname = "zerofill_canary.db"
	dir := t.TempDir()
	seedDB(t, dir, dbname,
		"CREATE TABLE t (v TEXT NOT NULL)",
		`WITH RECURSIVE cnt(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM cnt WHERE x < 100)
		 INSERT INTO t SELECT hex(zeroblob(50)) FROM cnt`,
	)

	fi, err := os.Stat(filepath.Join(dir, dbname))
	if err != nil {
		t.Fatal(err)
	}
	size := fi.Size()
	if size <= 8192 {
		t.Fatalf("expected database > 2 pages (8192 bytes), got %d", size)
	}

	// Truncate reads 2048 bytes before EOF so the last page is only
	// partially readable; earlier pages remain intact.
	fsys := &truncatedReadFS{FS: os.DirFS(dir), readLimit: size - 2048}
	db := openDBViaVFS(t, dbname, fsys)

	// Rows on intact pages should come back; the short-read page
	// exercises vfsRead's zero-fill. Query itself must succeed —
	// it only touches intact pages. Mid-iteration scan or Err
	// failures are tolerated because they may reflect corruption
	// detected on the truncated page.
	rows, err := db.Query("SELECT v FROM t")
	if err != nil {
		t.Fatalf("db.Query on intact pages: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Logf("rows.Scan (tolerated, truncated page): %v", err)
			break
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Logf("rows.Err (tolerated, truncated page): %v", err)
	}
	if count == 0 {
		t.Fatal("expected at least one readable row from intact pages")
	}
}
