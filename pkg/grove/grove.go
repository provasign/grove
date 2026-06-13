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
	"strings"
	"sync"
	"time"

	"github.com/provasign/grove/internal/cert"
	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/index"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
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
	GraphDiff            = core.GraphDiff
	SymbolChange         = core.SymbolChange
	DiffInput            = core.DiffInput
	CertificationPolicy  = core.CertificationPolicy
	CertificationReport  = core.CertificationReport
	CertificationFinding = core.CertificationFinding
	EvidenceRef          = core.EvidenceRef
	Verdict              = core.Verdict
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
	// NativeAnalyzers overrides native graph enrichment when non-nil.
	NativeAnalyzers *bool
	// NativeLanguages limits native analyzers to these languages/analyzer names.
	NativeLanguages []string
	// NativeDisabledLanguages disables these languages/analyzer names.
	NativeDisabledLanguages []string
	// NativeTimeout bounds each analyzer invocation. Zero uses Grove's default.
	NativeTimeout time.Duration
}

// Engine is the embedded Grove API consumed by Prism, Fuse, and Relay.
// Methods are safe for concurrent use.
type Engine struct {
	root   string
	store  *store.Store
	parser *parser.Engine
	idx    *index.Indexer

	// indexMu serializes Index calls: two concurrent walks would interleave
	// per-file store writes and race on the final edge rewrite.
	indexMu sync.Mutex

	mu    sync.RWMutex
	graph *graph.CodeGraph
}

// Open initialises the on-disk store, runs migrations, and rebuilds the
// in-memory graph from whatever symbols are already persisted.
func Open(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.RepoRoot == "" {
		return nil, errors.New("grove: RepoRoot is required")
	}
	// store.Open runs schema migrations itself; no second Migrate needed.
	st, err := store.Open(cfg.RepoRoot)
	if err != nil {
		return nil, err
	}
	p := parser.NewEngine()
	e := &Engine{
		root:   cfg.RepoRoot,
		store:  st,
		parser: p,
		idx:    index.NewWithNativeConfig(p, st, nativeConfigFromPublic(cfg)),
		graph:  graph.New(),
	}
	// Rehydrate graph from any previously-indexed symbols so reads work
	// before the first Index call. Stored edges are the merged set the last
	// index persisted, so they are installed verbatim — no rebuild.
	symbols, err := st.AllSymbols(ctx)
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	if len(symbols) > 0 {
		edges, err := st.AllEdges(ctx)
		if err != nil {
			_ = st.Close()
			return nil, err
		}
		e.graph.ReplaceWithStoredEdges(symbols, edges, 0)
	}
	return e, nil
}

func nativeConfigFromPublic(cfg Config) native.Config {
	nativeCfg := native.ConfigFromEnv()
	if cfg.NativeAnalyzers != nil {
		nativeCfg.Enabled = *cfg.NativeAnalyzers
	}
	if len(cfg.NativeLanguages) > 0 {
		nativeCfg.Languages = publicLanguageSet(cfg.NativeLanguages)
	}
	if len(cfg.NativeDisabledLanguages) > 0 {
		nativeCfg.DisabledLanguages = publicLanguageSet(cfg.NativeDisabledLanguages)
	}
	if cfg.NativeTimeout > 0 {
		nativeCfg.Timeout = cfg.NativeTimeout
	}
	return nativeCfg
}

func publicLanguageSet(values []string) map[string]bool {
	out := map[string]bool{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out[value] = true
		}
	}
	return out
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
	e.indexMu.Lock()
	defer e.indexMu.Unlock()
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

// TestsWithEvidence returns the covering tests plus the per-reason counts of
// edges the traversal policy excluded — the weak evidence Grove chose not to
// trust. Lets a consumer report "related tests" alongside what was withheld.
func (e *Engine) TestsWithEvidence(ctx context.Context, query string) ([]Symbol, map[string]int, error) {
	tests, skips := e.currentGraph().TestsForWithStats(query)
	out := make(map[string]int, len(skips))
	for reason, n := range skips {
		out[string(reason)] = n
	}
	return tests, out, nil
}

// ICR computes the Isolated Change Region for a given intent.
func (e *Engine) ICR(ctx context.Context, intent string) IsolatedChangeRegion {
	return e.currentGraph().ComputeICR(intent)
}

// FileSymbols returns the symbols currently indexed for one repo-relative
// file path, ordered by span. Use this instead of SnapshotSymbols when only
// a handful of files matter (e.g. working-set drift checks).
func (e *Engine) FileSymbols(ctx context.Context, relPath string) []Symbol {
	return e.currentGraph().FileSymbols(relPath)
}

// SnapshotSymbols returns a deep copy of every symbol in the current graph.
// Capture one before a merge/reindex and pass it to Diff afterwards to get
// the structural delta.
func (e *Engine) SnapshotSymbols(ctx context.Context) []Symbol {
	symbols, _ := e.currentGraph().Snapshot()
	return symbols
}

// PreviewFileSymbols parses in-memory content as if it lived at relPath
// (repo-relative) and returns the symbols Grove would index for it. Combine
// with Diff to compute the structural delta of content that is not on disk
// yet — e.g. a git merge driver's result, which git writes to the worktree
// only after the driver exits.
func (e *Engine) PreviewFileSymbols(relPath string, content []byte) ([]Symbol, error) {
	return e.parser.ExtractContent(relPath, content)
}

// DiffAgainstFileContent diffs a snapshot against itself with one file's
// symbols replaced by those parsed from content: "what would change
// structurally if relPath had these bytes?".
func (e *Engine) DiffAgainstFileContent(before []Symbol, relPath string, content []byte) (GraphDiff, error) {
	preview, err := e.PreviewFileSymbols(relPath, content)
	if err != nil {
		return GraphDiff{}, err
	}
	after := make([]Symbol, 0, len(before)+len(preview))
	for _, s := range before {
		if s.FilePath != relPath {
			after = append(after, s)
		}
	}
	after = append(after, preview...)
	return Diff(before, after), nil
}

// Diff computes the structural delta between two symbol snapshots, matched
// by stable identity (file path + qualified name + kind) so line shifts and
// content-SHA churn don't register as changes. This is the primitive behind
// the stale-context loop: diff the graph across a merge, intersect the
// changed/breaking symbols with another agent's working set, and you know
// exactly whose ground shifted.
func Diff(before, after []Symbol) GraphDiff {
	return graph.DiffSymbols(before, after)
}

// DiffSince diffs a previously captured snapshot against the engine's
// current graph.
func (e *Engine) DiffSince(ctx context.Context, before []Symbol) GraphDiff {
	return Diff(before, e.SnapshotSymbols(ctx))
}

// CertifyDiff maps a unified diff onto the indexed graph and returns a
// conservative structural certification report. The report is additive:
// retrieval, MCP, and Provasign behavior do not change unless callers opt in.
// Changed files whose indexed content no longer matches the working tree are
// reported as index_stale and escalate the verdict to manual_review.
func (e *Engine) CertifyDiff(ctx context.Context, input DiffInput) (CertificationReport, error) {
	return cert.CertifyDiffWithStaleness(e.currentGraph(), input, cert.RepoFileSHA(e.root)), nil
}
