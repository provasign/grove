// Package grove is the public, in-process Go API for the Grove code knowledge
// graph. Prism, Fuse, and Relay import this package and call it directly —
// there is no HTTP server, no port, no shared-secret token, no auto-start.
//
// Index data lives in <repoRoot>/.grove/grove.db. SQLite WAL mode handles
// concurrent readers; only one writer at a time per database file.
package grove

import (
	"context"
	"errors"
	"sync"

	"github.com/tabladrum/grove-suite/grove/internal/core"
	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

// Re-exported core types — Prism/Fuse/Relay can use these directly without
// mirroring shapes.
type (
	Symbol               = core.SymbolRecord
	Edge                 = core.Edge
	EdgeType             = core.EdgeType
	IndexResult          = core.IndexResult
	Status               = core.Status
	IsolatedChangeRegion = core.IsolatedChangeRegion
	Scored               = struct {
		Symbol *core.SymbolRecord
		Score  float64
	}
)

// Config controls how Engine opens a repository's Grove index.
type Config struct {
	// RepoRoot is the absolute path to the repository whose .grove/ directory
	// holds the index. Required.
	RepoRoot string
}

// Engine is the embedded Grove API consumed by Prism, Fuse, and Relay.
// Methods are safe for concurrent use.
type Engine struct {
	root   string
	store  *store.Store
	parser *parser.Engine
	idx    *index.Indexer

	mu    sync.RWMutex
	graph *graph.CodeGraph
}

// Open initialises the on-disk store, runs migrations, and rebuilds the
// in-memory graph from whatever symbols are already persisted.
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.RepoRoot == "" {
		return nil, errors.New("grove: RepoRoot is required")
	}
	st, err := store.Open(cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	if err := st.Migrate(ctx); err != nil {
		_ = st.Close()
		return nil, err
	}
	p := parser.NewEngine()
	e := &Engine{
		root:   cfg.RepoRoot,
		store:  st,
		parser: p,
		idx:    index.New(p, st),
		graph:  graph.New(),
	}
	// Rehydrate graph from any previously-indexed symbols so reads work
	// before the first Index call.
	if symbols, err := st.AllSymbols(ctx); err == nil && len(symbols) > 0 {
		e.graph.Replace(symbols, 0)
	}
	return e, nil
}

// Close releases the underlying SQLite handle.
func (e *Engine) Close() error {
	if e.store == nil {
		return nil
	}
	return e.store.Close()
}

// Root returns the repository root the engine is attached to.
func (e *Engine) Root() string { return e.root }

// currentGraph returns the live graph under a read lock.
func (e *Engine) currentGraph() *graph.CodeGraph {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.graph
}

// Index walks dir (defaults to RepoRoot), parses changed files via delta SHA,
// updates the persistent store, and refreshes the in-memory graph.
func (e *Engine) Index(ctx context.Context, dir string) (IndexResult, error) {
	if dir == "" {
		dir = e.root
	}
	cg, result, err := e.idx.Index(ctx, dir)
	if err != nil {
		return result, err
	}
	e.mu.Lock()
	e.graph = cg
	e.mu.Unlock()
	return result, nil
}

// Status reports the current persisted index summary.
func (e *Engine) Status(ctx context.Context) (Status, error) {
	return e.store.Status(ctx)
}

// Query resolves a natural-language intent into ranked symbols by blending
// TF-IDF semantic search with substring keyword matches.
func (e *Engine) Query(ctx context.Context, intent string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	cg := e.currentGraph()
	scored := cg.SemanticSearch(intent, limit)
	seen := make(map[string]bool, len(scored))
	out := make([]Symbol, 0, limit)
	for _, sc := range scored {
		if sc.Symbol == nil {
			continue
		}
		seen[sc.Symbol.ID] = true
		out = append(out, *sc.Symbol)
	}
	for _, sym := range cg.Search(intent, limit) {
		if seen[sym.ID] {
			continue
		}
		out = append(out, sym)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// Symbols returns symbols whose name/qualified-name matches query (substring).
func (e *Engine) Symbols(ctx context.Context, query string, limit int) ([]Symbol, error) {
	if limit <= 0 {
		limit = 50
	}
	return e.currentGraph().Search(query, limit), nil
}

// Semantic returns TF-IDF-ranked symbols with cosine-similarity scores.
func (e *Engine) Semantic(ctx context.Context, query string, limit int) ([]Scored, error) {
	if limit <= 0 {
		limit = 20
	}
	raw := e.currentGraph().SemanticSearch(query, limit)
	out := make([]Scored, 0, len(raw))
	for _, sc := range raw {
		out = append(out, Scored{Symbol: sc.Symbol, Score: sc.Score})
	}
	return out, nil
}

// Deps returns the outgoing dependency edges for filePath.
func (e *Engine) Deps(ctx context.Context, filePath string) ([]Edge, error) {
	return e.currentGraph().Deps(filePath), nil
}

// Impact returns the blast radius for a symbol/file query.
func (e *Engine) Impact(ctx context.Context, query string, maxDepth int) ([]Symbol, error) {
	return e.currentGraph().Impact(query, maxDepth), nil
}

// Tests returns the test symbols that cover the given symbol/file query.
func (e *Engine) Tests(ctx context.Context, query string) ([]Symbol, error) {
	return e.currentGraph().TestsFor(query), nil
}

// ICR computes the Isolated Change Region for a given intent.
func (e *Engine) ICR(ctx context.Context, intent string) IsolatedChangeRegion {
	return e.currentGraph().ComputeICR(intent)
}
