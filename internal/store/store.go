package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/tabladrum/grove-suite/grove/internal/core"
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		_ = db.Close()
		return nil, err
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

func (s *Store) UpsertFile(ctx context.Context, filePath, blobSHA, language string, symbols []core.SymbolRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Delete edges for this file using exact-prefix matching.
	// Symbol IDs for a file have the form  "filePath::symbolName@sha".
	// The file-level defines-edge source node is "file:filePath".
	fileNode := "file:" + filePath
	idPrefix := filePath + "::"
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM edges WHERE from_node = ? OR from_node LIKE ? OR to_node LIKE ?`,
		fileNode, idPrefix+"%", idPrefix+"%",
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM symbols WHERE file_path = ?`, filePath); err != nil {
		return err
	}

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
		exports := 0
		if symbol.Exports {
			exports = 1
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO symbols
				(id, file_path, blob_sha, language, kind, name, qualified_name, signature,
				 docstring, span_start, span_end, imports, exports, raw_text, parent_symbol, token_estimate,
				 modifiers, type_parameters, annotations, call_sites)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, symbol.ID, symbol.FilePath, symbol.BlobSHA, symbol.Language, string(symbol.Kind),
			symbol.Name, symbol.QualifiedName, symbol.Signature, symbol.Docstring,
			symbol.Span.Start, symbol.Span.End, string(imports), exports,
			symbol.RawText, symbol.ParentSymbol, symbol.TokenEstimate,
			string(modifiers), string(typeParams), string(annotations), string(callSites))
		if err != nil {
			return err
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO file_index (file_path, blob_sha, language, symbol_count, indexed_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(file_path) DO UPDATE SET
			blob_sha     = excluded.blob_sha,
			language     = excluded.language,
			symbol_count = excluded.symbol_count,
			indexed_at   = excluded.indexed_at
	`, filePath, blobSHA, language, len(symbols), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}

	return tx.Commit()
}

func (s *Store) ReplaceEdges(ctx context.Context, edges []core.Edge) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM edges`); err != nil {
		return err
	}
	for _, edge := range edges {
		id := fmt.Sprintf("%s::%s::%s", edge.From, edge.Type, edge.To)
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO edges (id, from_node, to_node, edge_type, confidence) VALUES (?, ?, ?, ?, ?)`,
			id, edge.From, edge.To, string(edge.Type), edge.Confidence,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) DeleteFilesNotIn(ctx context.Context, current map[string]bool) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_path FROM file_index`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var filePath string
		if err := rows.Scan(&filePath); err != nil {
			return 0, err
		}
		if !current[filePath] {
			stale = append(stale, filePath)
		}
	}
	if err := rows.Err(); err != nil {
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
		// Same exact-prefix edge deletion as in UpsertFile
		fileNode := "file:" + filePath
		idPrefix := filePath + "::"
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM edges WHERE from_node = ? OR from_node LIKE ? OR to_node LIKE ?`,
			fileNode, idPrefix+"%", idPrefix+"%",
		); err != nil {
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
		       modifiers, type_parameters, annotations, call_sites
		FROM symbols
		ORDER BY file_path, span_start
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var symbols []core.SymbolRecord
	for rows.Next() {
		var symbol core.SymbolRecord
		var kind, importsJSON, modifiersJSON, typeParamsJSON, annotationsJSON, callSitesJSON string
		var exports int
		if err := rows.Scan(
			&symbol.ID, &symbol.FilePath, &symbol.BlobSHA, &symbol.Language,
			&kind, &symbol.Name, &symbol.QualifiedName, &symbol.Signature,
			&symbol.Docstring, &symbol.Span.Start, &symbol.Span.End,
			&importsJSON, &exports, &symbol.RawText, &symbol.ParentSymbol,
			&symbol.TokenEstimate,
			&modifiersJSON, &typeParamsJSON, &annotationsJSON, &callSitesJSON,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(importsJSON), &symbol.Imports)
		_ = json.Unmarshal([]byte(modifiersJSON), &symbol.Modifiers)
		_ = json.Unmarshal([]byte(typeParamsJSON), &symbol.TypeParameters)
		_ = json.Unmarshal([]byte(annotationsJSON), &symbol.Annotations)
		_ = json.Unmarshal([]byte(callSitesJSON), &symbol.CallSites)
		symbol.Kind = core.SymbolKind(kind)
		symbol.Exports = exports == 1
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
}

// SearchFTS5 runs a full-text search against the symbols_fts virtual table.
// Returns up to limit results ranked by FTS5 relevance (bm25).
func (s *Store) SearchFTS5(ctx context.Context, query string, limit int) ([]core.SymbolRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT s.id, s.file_path, s.blob_sha, s.language, s.kind, s.name,
		       s.qualified_name, s.signature, s.docstring, s.span_start, s.span_end,
		       s.imports, s.exports, s.raw_text, COALESCE(s.parent_symbol, ''), s.token_estimate,
		       s.modifiers, s.type_parameters, s.annotations, s.call_sites
		FROM symbols s
		JOIN symbols_fts f ON s.rowid = f.rowid
		WHERE symbols_fts MATCH ?
		ORDER BY rank
		LIMIT ?
	`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var symbols []core.SymbolRecord
	for rows.Next() {
		var symbol core.SymbolRecord
		var kind, importsJSON, modifiersJSON, typeParamsJSON, annotationsJSON, callSitesJSON string
		var exports int
		if err := rows.Scan(
			&symbol.ID, &symbol.FilePath, &symbol.BlobSHA, &symbol.Language,
			&kind, &symbol.Name, &symbol.QualifiedName, &symbol.Signature,
			&symbol.Docstring, &symbol.Span.Start, &symbol.Span.End,
			&importsJSON, &exports, &symbol.RawText, &symbol.ParentSymbol,
			&symbol.TokenEstimate,
			&modifiersJSON, &typeParamsJSON, &annotationsJSON, &callSitesJSON,
		); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(importsJSON), &symbol.Imports)
		_ = json.Unmarshal([]byte(modifiersJSON), &symbol.Modifiers)
		_ = json.Unmarshal([]byte(typeParamsJSON), &symbol.TypeParameters)
		_ = json.Unmarshal([]byte(annotationsJSON), &symbol.Annotations)
		_ = json.Unmarshal([]byte(callSitesJSON), &symbol.CallSites)
		symbol.Kind = core.SymbolKind(kind)
		symbol.Exports = exports == 1
		symbols = append(symbols, symbol)
	}
	return symbols, rows.Err()
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
    call_sites      TEXT NOT NULL DEFAULT '[]'
);

CREATE INDEX IF NOT EXISTS idx_sym_file      ON symbols(file_path);
CREATE INDEX IF NOT EXISTS idx_sym_name      ON symbols(name);
CREATE INDEX IF NOT EXISTS idx_sym_kind      ON symbols(kind);
CREATE INDEX IF NOT EXISTS idx_sym_lang      ON symbols(language);
CREATE INDEX IF NOT EXISTS idx_sym_qualified ON symbols(qualified_name);
CREATE INDEX IF NOT EXISTS idx_sym_parent    ON symbols(parent_symbol);

CREATE VIRTUAL TABLE IF NOT EXISTS symbols_fts USING fts5(
    name, qualified_name, signature, docstring,
    content=symbols, content_rowid=rowid
);

-- FTS5 sync triggers (keep symbols_fts in sync with symbols table)
CREATE TRIGGER IF NOT EXISTS symbols_fts_insert
AFTER INSERT ON symbols BEGIN
    INSERT INTO symbols_fts(rowid, name, qualified_name, signature, docstring)
    VALUES (new.rowid, new.name, new.qualified_name, new.signature, new.docstring);
END;

CREATE TRIGGER IF NOT EXISTS symbols_fts_delete
AFTER DELETE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature, docstring)
    VALUES ('delete', old.rowid, old.name, old.qualified_name, old.signature, old.docstring);
END;

CREATE TRIGGER IF NOT EXISTS symbols_fts_update
AFTER UPDATE ON symbols BEGIN
    INSERT INTO symbols_fts(symbols_fts, rowid, name, qualified_name, signature, docstring)
    VALUES ('delete', old.rowid, old.name, old.qualified_name, old.signature, old.docstring);
    INSERT INTO symbols_fts(rowid, name, qualified_name, signature, docstring)
    VALUES (new.rowid, new.name, new.qualified_name, new.signature, new.docstring);
END;

CREATE TABLE IF NOT EXISTS edges (
    id         TEXT PRIMARY KEY,
    from_node  TEXT NOT NULL,
    to_node    TEXT NOT NULL,
    edge_type  TEXT NOT NULL,
    confidence REAL NOT NULL DEFAULT 1.0
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
