package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/provasign/grove/internal/core"
)

type Store struct {
	db *sql.DB
}

func Open(root string) (*Store, error) {
	groveDir := filepath.Join(root, ".grove")
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(groveDir, "grove.db"))
	if err != nil {
		return nil, err
	}
	// Single writer connection prevents "database is locked" errors from
	// concurrent agents (Copilot + Claude Code CLI + Relay) racing on writes.
	// WAL mode (set in schema) allows concurrent reads alongside this writer.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	pragmas := []string{
		// 30 s retry window covers slow Relay pipelines and multi-agent bursts.
		`PRAGMA busy_timeout=30000`,
		// Cap WAL file at ~1 000 pages (~4 MB) to avoid unbounded WAL growth
		// when long-running agents issue many small writes without checkpoints.
		`PRAGMA wal_autocheckpoint=1000`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	store := &Store{db: db}
	if err := store.Migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return err
	}
	return s.runAlterMigrations(ctx)
}

// runAlterMigrations adds columns that were introduced after the initial schema.
// Each migration is idempotent: it silently ignores the "duplicate column name"
// error that SQLite raises when the column already exists.
func (s *Store) runAlterMigrations(ctx context.Context) error {
	alters := []string{
		`ALTER TABLE symbols ADD COLUMN modifiers       TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE symbols ADD COLUMN type_parameters TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE symbols ADD COLUMN annotations     TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE symbols ADD COLUMN call_sites      TEXT NOT NULL DEFAULT '[]'`,
		`ALTER TABLE edges ADD COLUMN source            TEXT NOT NULL DEFAULT 'unknown'`,
		`ALTER TABLE edges ADD COLUMN reason            TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE symbols ADD COLUMN attr_sites      TEXT NOT NULL DEFAULT '[]'`,
		// Stat cache: the walk skips hashing files whose size+mtime are
		// unchanged since the last index (-1 = unknown, forces one hash).
		"ALTER TABLE file_index ADD COLUMN file_size  INTEGER NOT NULL DEFAULT -1",
		"ALTER TABLE file_index ADD COLUMN file_mtime INTEGER NOT NULL DEFAULT -1",
		// The FTS5 mirror was never queried by any retrieval path (search
		// runs in-memory over the graph), yet its sync triggers doubled the
		// cost of every symbol write and the table duplicated symbol text
		// on disk. Drop it from existing databases.
		`DROP TRIGGER IF EXISTS symbols_fts_insert`,
		`DROP TRIGGER IF EXISTS symbols_fts_delete`,
		`DROP TRIGGER IF EXISTS symbols_fts_update`,
		`DROP TABLE IF EXISTS symbols_fts`,
	}
	for _, stmt := range alters {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			// SQLite returns "duplicate column name" when the column already
			// exists — that is the happy path for a re-run migration.
			if !strings.Contains(err.Error(), "duplicate column name") {
				return fmt.Errorf("migration %q: %w", stmt, err)
			}
		}
	}
	// Heuristic tests edges were removed from the graph entirely; purge rows
	// written by earlier versions so stored counts and splice invariants
	// stay consistent. (The GROVE_MATERIALIZED_TESTS rollback flag is gone —
	// the eager/persisted behavior it restored no longer exists.)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM edges WHERE edge_type = 'tests'`); err != nil {
		return fmt.Errorf("migration (purge tests edges): %w", err)
	}
	return nil
}

func (s *Store) FileBlobSHA(ctx context.Context, filePath string) (string, bool, error) {
	var blobSHA string
	err := s.db.QueryRowContext(ctx, `SELECT blob_sha FROM file_index WHERE file_path = ?`, filePath).Scan(&blobSHA)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return blobSHA, true, nil
}

// FileMeta is the per-file identity the walk uses to decide whether a file
// needs re-hashing (stat match) or re-parsing (blob mismatch).
type FileMeta struct {
	BlobSHA string
	Size    int64
	Mtime   int64
}

// AllFileMeta returns the stat cache for every indexed file in one query.
func (s *Store) AllFileMeta(ctx context.Context) (map[string]FileMeta, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT file_path, blob_sha, file_size, file_mtime FROM file_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]FileMeta{}
	for rows.Next() {
		var p string
		var m FileMeta
		if err := rows.Scan(&p, &m.BlobSHA, &m.Size, &m.Mtime); err != nil {
			return nil, err
		}
		out[p] = m
	}
	return out, rows.Err()
}

// UpdateFileStats refreshes size+mtime for files whose content was verified
// unchanged (touched files: stat differs, blob identical).
func (s *Store) UpdateFileStats(ctx context.Context, stats map[string][2]int64) error {
	if len(stats) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for p, sm := range stats {
		if _, err := tx.ExecContext(ctx,
			`UPDATE file_index SET file_size = ?, file_mtime = ? WHERE file_path = ?`,
			sm[0], sm[1], p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertFile(ctx context.Context, filePath, blobSHA, language string, size, mtime int64, symbols []core.SymbolRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := deleteFileEdges(ctx, tx, filePath); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_path = ?`, filePath); err != nil {
		return err
	}

	insertStmt, err := tx.PrepareContext(ctx, `
		INSERT INTO symbols
			(id, file_path, blob_sha, language, kind, name, qualified_name, signature,
			 docstring, span_start, span_end, imports, exports, raw_text, parent_symbol, token_estimate,
			 modifiers, type_parameters, annotations, call_sites, attr_sites)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return err
	}
	defer insertStmt.Close()

	// Defensive: collapse duplicate IDs within a single file. Some Tree-sitter
	// walkers can revisit a node (e.g. a class declared on the same line as a
	// nested definition), and the symbols table has a PRIMARY KEY on id.
	seen := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		if seen[symbol.ID] {
			continue
		}
		seen[symbol.ID] = true
		imports, err := marshalSlice(symbol.Imports)
		if err != nil {
			return err
		}
		modifiers, err := marshalSlice(symbol.Modifiers)
		if err != nil {
			return err
		}
		typeParams, err := marshalSlice(symbol.TypeParameters)
		if err != nil {
			return err
		}
		annotations, err := marshalSlice(symbol.Annotations)
		if err != nil {
			return err
		}
		callSites, err := json.Marshal(nilToEmpty(symbol.CallSites))
		if err != nil {
			return err
		}
		attrSites, err := json.Marshal(nilToEmpty(symbol.AttrSites))
		if err != nil {
			return err
		}
		exports := 0
		if symbol.Exports {
			exports = 1
		}
		_, err = insertStmt.ExecContext(ctx,
			symbol.ID, symbol.FilePath, symbol.BlobSHA, symbol.Language, string(symbol.Kind),
			symbol.Name, symbol.QualifiedName, symbol.Signature, symbol.Docstring,
			symbol.Span.Start, symbol.Span.End, string(imports), exports,
			symbol.RawText, symbol.ParentSymbol, symbol.TokenEstimate,
			string(modifiers), string(typeParams), string(annotations), string(callSites), string(attrSites))
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO file_index (file_path, blob_sha, language, symbol_count, indexed_at, file_size, file_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			blob_sha     = excluded.blob_sha,
			language     = excluded.language,
			symbol_count = excluded.symbol_count,
			indexed_at   = excluded.indexed_at,
			file_size    = excluded.file_size,
			file_mtime   = excluded.file_mtime
	`, filePath, blobSHA, language, len(symbols), time.Now().UTC().Format(time.RFC3339), size, mtime)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// ReplaceEdges makes the edges table equal to the given set. It diffs
// against what is stored and writes only the difference: a one-file change
// on a million-edge repo touches hundreds of rows, and the previous
// delete-everything-reinsert paid the full million-row write every index
// (21s on a 19k-file monorepo). Inserts are batched multi-row.
func (s *Store) ReplaceEdges(ctx context.Context, edges []core.Edge) error {
	type edgeMeta struct {
		confidence float64
		source     string
		reason     string
	}
	stored := map[string]edgeMeta{}
	rows, err := s.db.QueryContext(ctx, `SELECT id, confidence, source, COALESCE(reason, '') FROM edges`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, source, reason string
		var conf float64
		if err := rows.Scan(&id, &conf, &source, &reason); err != nil {
			rows.Close()
			return err
		}
		stored[id] = edgeMeta{confidence: conf, source: source, reason: reason}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	wantIDs := make(map[string]bool, len(edges))
	var upserts []core.Edge
	var upsertIDs []string
	for _, edge := range edges {
		id := edge.From + "::" + string(edge.Type) + "::" + edge.To
		if wantIDs[id] {
			continue // duplicate (from, type, to); first one wins
		}
		wantIDs[id] = true
		if meta, ok := stored[id]; ok && meta.confidence == edge.Confidence && meta.source == string(edge.Source) && meta.reason == string(edge.Reason) {
			continue
		}
		upserts = append(upserts, edge)
		upsertIDs = append(upsertIDs, id)
	}
	var deletes []string
	for id := range stored {
		if !wantIDs[id] {
			deletes = append(deletes, id)
		}
	}
	if len(upserts) == 0 && len(deletes) == 0 {
		return nil
	}

	// Bulk load (cold index / huge delta): maintaining the three secondary
	// indexes incrementally while millions of rows stream in thrashes b-tree
	// pages through the default page cache — measured 600s of mostly system
	// time for 6.3M edges on a monorepo. Dropping the indexes, loading with a
	// large cache and no WAL checkpointing, then rebuilding each index as one
	// sort brings the same load to well under a minute. Single-connection pool
	// (SetMaxOpenConns(1)) means these session pragmas cannot leak to a
	// concurrent writer.
	bulk := len(upserts) > 200_000
	if bulk {
		// Insert in primary-key order: random-order TEXT-key insertion lands
		// on random b-tree pages (the PK index cannot be dropped); sorted
		// insertion is append-like and keeps the hot page set tiny.
		order := make([]int, len(upserts))
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool { return upsertIDs[order[a]] < upsertIDs[order[b]] })
		sortedUpserts := make([]core.Edge, len(upserts))
		sortedIDs := make([]string, len(upserts))
		for i, idx := range order {
			sortedUpserts[i] = upserts[idx]
			sortedIDs[i] = upsertIDs[idx]
		}
		upserts, upsertIDs = sortedUpserts, sortedIDs
		for _, p := range []string{
			`PRAGMA cache_size=-262144`, // 256MB
			`PRAGMA wal_autocheckpoint=0`,
			`DROP INDEX IF EXISTS idx_edge_from`,
			`DROP INDEX IF EXISTS idx_edge_to`,
			`DROP INDEX IF EXISTS idx_edge_type`,
		} {
			if _, err := s.db.ExecContext(ctx, p); err != nil {
				return err
			}
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const deleteChunk = 500
	for start := 0; start < len(deletes); start += deleteChunk {
		end := start + deleteChunk
		if end > len(deletes) {
			end = len(deletes)
		}
		chunk := deletes[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE id IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
	}

	const insertChunk = 342 // 7 columns × 342 = 2394 parameters per statement
	for start := 0; start < len(upserts); start += insertChunk {
		end := start + insertChunk
		if end > len(upserts) {
			end = len(upserts)
		}
		chunk := upserts[start:end]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO edges (id, from_node, to_node, edge_type, confidence, source, reason) VALUES `)
		args := make([]any, 0, len(chunk)*7)
		for i, edge := range chunk {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?,?)")
			args = append(args, upsertIDs[start+i], edge.From, edge.To, string(edge.Type), edge.Confidence, string(edge.Source), string(edge.Reason))
		}
		sb.WriteString(` ON CONFLICT(id) DO UPDATE SET confidence = excluded.confidence, source = excluded.source, reason = excluded.reason`)
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if bulk {
		// Rebuild what the bulk path dropped, restore steady-state pragmas,
		// and truncate the WAL the disabled autocheckpoint let grow.
		for _, p := range []string{
			`CREATE INDEX IF NOT EXISTS idx_edge_from ON edges(from_node)`,
			`CREATE INDEX IF NOT EXISTS idx_edge_to   ON edges(to_node)`,
			`CREATE INDEX IF NOT EXISTS idx_edge_type ON edges(edge_type)`,
			`PRAGMA wal_autocheckpoint=1000`,
			`PRAGMA wal_checkpoint(TRUNCATE)`,
			`PRAGMA cache_size=-2000`,
		} {
			if _, err := s.db.ExecContext(ctx, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// FilesNotIn returns the indexed file paths absent from current — the files
// a delete pass would prune — without deleting anything.
func (s *Store) FilesNotIn(ctx context.Context, current map[string]bool) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path FROM file_index`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var filePath string
		if err := rows.Scan(&filePath); err != nil {
			return nil, err
		}
		if !current[filePath] {
			stale = append(stale, filePath)
		}
	}
	return stale, rows.Err()
}

func (s *Store) DeleteFilesNotIn(ctx context.Context, current map[string]bool) (int, error) {
	stale, err := s.FilesNotIn(ctx, current)
	if err != nil {
		return 0, err
	}
	if len(stale) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	for _, filePath := range stale {
		if err := deleteFileEdges(ctx, tx, filePath); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_path = ?`, filePath); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM file_index WHERE file_path = ?`, filePath); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(stale), nil
}

func (s *Store) AllSymbols(ctx context.Context) ([]core.SymbolRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, file_path, blob_sha, language, kind, name, qualified_name,
		       signature, docstring, span_start, span_end, imports, exports,
		       raw_text, COALESCE(parent_symbol, ''), token_estimate,
		       modifiers, type_parameters, annotations, call_sites,
		       COALESCE(attr_sites, '[]')
		FROM symbols
		ORDER BY file_path, span_start, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var symbols []core.SymbolRecord
	for rows.Next() {
		var symbol core.SymbolRecord
		var kind, importsJSON, modifiersJSON, typeParamsJSON, annotationsJSON, callSitesJSON, attrSitesJSON string
		var exports int
		if err := rows.Scan(
			&symbol.ID, &symbol.FilePath, &symbol.BlobSHA, &symbol.Language,
			&kind, &symbol.Name, &symbol.QualifiedName, &symbol.Signature,
			&symbol.Docstring, &symbol.Span.Start, &symbol.Span.End,
			&importsJSON, &exports, &symbol.RawText, &symbol.ParentSymbol,
			&symbol.TokenEstimate,
			&modifiersJSON, &typeParamsJSON, &annotationsJSON, &callSitesJSON,
			&attrSitesJSON,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(importsJSON), &symbol.Imports)
		_ = json.Unmarshal([]byte(modifiersJSON), &symbol.Modifiers)
		_ = json.Unmarshal([]byte(typeParamsJSON), &symbol.TypeParameters)
		_ = json.Unmarshal([]byte(annotationsJSON), &symbol.Annotations)
		_ = json.Unmarshal([]byte(callSitesJSON), &symbol.CallSites)
		_ = json.Unmarshal([]byte(attrSitesJSON), &symbol.AttrSites)
		symbol.Kind = core.SymbolKind(kind)
		symbol.Exports = exports == 1
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

func (s *Store) AllEdges(ctx context.Context) ([]core.Edge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT from_node, to_node, edge_type, confidence, COALESCE(source, 'unknown'), COALESCE(reason, '')
		FROM edges
		ORDER BY from_node, edge_type, to_node
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []core.Edge
	for rows.Next() {
		var edge core.Edge
		var typ, source, reason string
		if err := rows.Scan(&edge.From, &edge.To, &typ, &edge.Confidence, &source, &reason); err != nil {
			return nil, err
		}
		edge.Type = core.EdgeType(typ)
		edge.Source = core.EvidenceSource(source)
		edge.Reason = core.EdgeReason(reason)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

// SpliceEdges applies an incremental write-set instead of ReplaceEdges' full
// 6.3M-row diff: point-delete the out-rows of recomputed owners, range-delete
// the native-source rows of natively re-analyzed files, then upsert the
// insert rows with merge semantics (higher confidence wins, first-wins on
// ties — the same resolution mergeEdges applies in memory). The caller
// verifies the result with a COUNT(*) equality check and falls back to
// ReplaceEdges on any mismatch, so a write-set miss self-heals in-run.
func (s *Store) SpliceEdges(ctx context.Context, deleteOwners []string, nativeFiles []string, inserts []core.Edge) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	const chunk = 500
	for start := 0; start < len(deleteOwners); start += chunk {
		end := start + chunk
		if end > len(deleteOwners) {
			end = len(deleteOwners)
		}
		part := deleteOwners[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE from_node IN (`+ph+`)`, args...); err != nil {
			return err
		}
	}
	for _, f := range nativeFiles {
		lo, hi := f+"::", f+":;"
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE from_node >= ? AND from_node < ? AND source = 'native'`, lo, hi); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE from_node = ? AND source = 'native'`, "file:"+f); err != nil {
			return err
		}
	}

	const insertChunk = 342
	for start := 0; start < len(inserts); start += insertChunk {
		end := start + insertChunk
		if end > len(inserts) {
			end = len(inserts)
		}
		part := inserts[start:end]
		var sb strings.Builder
		sb.WriteString(`INSERT INTO edges (id, from_node, to_node, edge_type, confidence, source, reason) VALUES `)
		args := make([]any, 0, len(part)*7)
		for i, edge := range part {
			if i > 0 {
				sb.WriteString(",")
			}
			sb.WriteString("(?,?,?,?,?,?,?)")
			id := edge.From + "::" + string(edge.Type) + "::" + edge.To
			args = append(args, id, edge.From, edge.To, string(edge.Type), edge.Confidence, string(edge.Source), string(edge.Reason))
		}
		sb.WriteString(` ON CONFLICT(id) DO UPDATE SET confidence = excluded.confidence, source = excluded.source, reason = excluded.reason WHERE excluded.confidence > edges.confidence`)
		if _, err := tx.ExecContext(ctx, sb.String(), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// EdgeCount returns COUNT(*) of the edges table — the cheap post-splice
// invariant check.
func (s *Store) EdgeCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&n)
	return n, err
}

// EdgesBySource returns stored edges whose evidence source equals source, in
// the same (from_node, edge_type, to_node) order AllEdges uses, so callers
// that previously filtered AllEdges see an identical sequence. On monorepos
// this avoids materializing millions of rows the caller would discard (the
// delta carry path keeps only native edges — ~1/6 of the table).
func (s *Store) EdgesBySource(ctx context.Context, source string) ([]core.Edge, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT from_node, to_node, edge_type, confidence, COALESCE(source, 'unknown'), COALESCE(reason, '')
		FROM edges
		WHERE source = ?
		ORDER BY from_node, edge_type, to_node
	`, source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var edges []core.Edge
	for rows.Next() {
		var edge core.Edge
		var typ, src, reason string
		if err := rows.Scan(&edge.From, &edge.To, &typ, &edge.Confidence, &src, &reason); err != nil {
			return nil, err
		}
		edge.Type = core.EdgeType(typ)
		edge.Source = core.EvidenceSource(src)
		edge.Reason = core.EdgeReason(reason)
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func (s *Store) Status(ctx context.Context) (core.Status, error) {
	var status core.Status
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM file_index`).Scan(&status.FilesIndexed); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols`).Scan(&status.SymbolCount); err != nil {
		return status, err
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM edges`).Scan(&status.EdgeCount); err != nil {
		return status, err
	}
	return status, nil
}

func (s *Store) AcquireLocks(ctx context.Context, intentID string, lockKeys []string, ttl time.Duration) ([]core.LockRecord, error) {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, lockKey := range lockKeys {
		var existing string
		err := tx.QueryRowContext(ctx,
			`SELECT intent_id FROM icr_locks WHERE lock_key = ? AND expires_at > ?`,
			lockKey, now.Format(time.RFC3339),
		).Scan(&existing)
		if err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		if existing != "" && existing != intentID {
			return nil, fmt.Errorf("lock %s already held by %s", lockKey, existing)
		}
	}

	records := make([]core.LockRecord, 0, len(lockKeys))
	for _, lockKey := range lockKeys {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO icr_locks (lock_key, intent_id, acquired_at, expires_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(lock_key) DO UPDATE SET
				intent_id   = excluded.intent_id,
				acquired_at = excluded.acquired_at,
				expires_at  = excluded.expires_at
		`, lockKey, intentID, now.Format(time.RFC3339), expires.Format(time.RFC3339)); err != nil {
			return nil, err
		}
		records = append(records, core.LockRecord{
			LockKey:    lockKey,
			IntentID:   intentID,
			AcquiredAt: now.Format(time.RFC3339),
			ExpiresAt:  expires.Format(time.RFC3339),
		})
	}
	return records, tx.Commit()
}

func (s *Store) ReleaseLocks(ctx context.Context, intentID string) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM icr_locks WHERE intent_id = ?`, intentID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ─── helpers ─────────────────────────────────────────────────────────────────

// deleteFileEdges removes every edge anchored to a file: the file-level node
// ("file:" + path) and every symbol-anchored endpoint ("path::name@sha").
// The prefix match uses half-open range predicates instead of LIKE: the old
// `LIKE ... ESCAPE` form could never be served by the BINARY-collation
// from/to indexes (and ESCAPE disables the prefix optimization anyway), so
// every per-file delete scanned the whole edge table — 16s per changed file
// on a 6.3M-edge monorepo. Ranges compare raw bytes, so no wildcard escaping
// is needed, and each statement provably uses idx_edge_from / idx_edge_to.
// The prefix "path::" ends in ':' (0x3A), so "path:;" (';' == ':'+1) is its
// exclusive upper bound. Three statements rather than one OR'd statement so
// each gets its index; DELETE is idempotent per row, so the split is
// set-identical.
func deleteFileEdges(ctx context.Context, tx *sql.Tx, filePath string) error {
	fileNode := "file:" + filePath
	lo := filePath + "::"
	hi := filePath + ":;"
	if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE from_node = ?`, fileNode); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE from_node >= ? AND from_node < ?`, lo, hi); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM edges WHERE to_node >= ? AND to_node < ?`, lo, hi); err != nil {
		return err
	}
	return nil
}

// marshalSlice marshals a string slice to JSON, emitting "[]" for nil slices
// (instead of the "null" that json.Marshal would produce for nil).
func marshalSlice(v []string) ([]byte, error) {
	if v == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(v)
}

// nilToEmpty converts a nil CallSite slice to an empty slice so json.Marshal
// emits "[]" rather than "null".
func nilToEmpty(v []core.CallSite) []core.CallSite {
	if v == nil {
		return []core.CallSite{}
	}
	return v
}

// ─── Schema ──────────────────────────────────────────────────────────────────

const schemaSQL = `
PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS file_index (
    file_path    TEXT PRIMARY KEY,
    blob_sha     TEXT NOT NULL,
    language     TEXT NOT NULL,
    symbol_count INTEGER NOT NULL DEFAULT 0,
    indexed_at   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS symbols (
    id              TEXT PRIMARY KEY,
    file_path       TEXT NOT NULL,
    blob_sha        TEXT NOT NULL,
    language        TEXT NOT NULL,
    kind            TEXT NOT NULL,
    name            TEXT NOT NULL,
    qualified_name  TEXT NOT NULL,
    signature       TEXT NOT NULL DEFAULT '',
    docstring       TEXT NOT NULL DEFAULT '',
    span_start      INTEGER NOT NULL,
    span_end        INTEGER NOT NULL,
    imports         TEXT NOT NULL DEFAULT '[]',
    exports         INTEGER NOT NULL DEFAULT 0,
    raw_text        TEXT NOT NULL DEFAULT '',
    parent_symbol   TEXT,
    token_estimate  INTEGER NOT NULL DEFAULT 0,
    modifiers       TEXT NOT NULL DEFAULT '[]',
    type_parameters TEXT NOT NULL DEFAULT '[]',
    annotations     TEXT NOT NULL DEFAULT '[]',
    call_sites      TEXT NOT NULL DEFAULT '[]',
    attr_sites      TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_sym_file      ON symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_sym_name      ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_sym_kind      ON symbols(kind);
CREATE INDEX IF NOT EXISTS idx_sym_lang      ON symbols(language);
CREATE INDEX IF NOT EXISTS idx_sym_qualified ON symbols(qualified_name);
CREATE INDEX IF NOT EXISTS idx_sym_parent    ON symbols(parent_symbol);

CREATE TABLE IF NOT EXISTS edges (
    id         TEXT PRIMARY KEY,
    from_node  TEXT NOT NULL,
    to_node    TEXT NOT NULL,
    edge_type  TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0,
    source     TEXT NOT NULL DEFAULT 'unknown'
);

CREATE INDEX IF NOT EXISTS idx_edge_from ON edges(from_node);
CREATE INDEX IF NOT EXISTS idx_edge_to   ON edges(to_node);
CREATE INDEX IF NOT EXISTS idx_edge_type ON edges(edge_type);

CREATE TABLE IF NOT EXISTS icr_locks (
    lock_key    TEXT PRIMARY KEY,
    intent_id   TEXT NOT NULL,
    acquired_at TEXT NOT NULL,
    expires_at  TEXT NOT NULL
);
`
