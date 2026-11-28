package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A checkpoint truncates the WAL back to journal_size_limit: without it the
// file kept its largest size (a 36-50 MB WAL beside the database, IDX-01).
func TestWALIsTruncatedAfterCheckpoint(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS wal_probe (v TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	row := strings.Repeat("x", 4096)
	for i := 0; i < 4000; i++ { // ~16 MB of pages into the WAL
		if _, err := tx.ExecContext(ctx, `INSERT INTO wal_probe VALUES (?)`, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	wal := filepath.Join(root, ".grove", "grove.db-wal")
	before := fileSize(t, wal)
	if before < 8<<20 {
		t.Fatalf("setup: WAL only %d bytes", before)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(PASSIVE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO wal_probe VALUES ('small')`); err != nil {
		t.Fatal(err)
	}
	if after := fileSize(t, wal); after > 4<<20 {
		t.Errorf("WAL is %d bytes after checkpoint and a small write (was %d); want <= 4 MiB", after, before)
	}
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
