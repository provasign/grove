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
	Reference            = parser.Reference
	ReferenceResult      = parser.ReferenceResult
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

// Edge-type constants re-exported so consumers can filter Neighbors() without
// importing internal/core.
const (
	EdgeDefines    = core.EdgeDefines
	EdgeImports    = core.EdgeImports
	EdgeCalls      = core.EdgeCalls
	EdgeExtends    = core.EdgeExtends
	EdgeImplements = core.EdgeImplements
	EdgeUsesType   = core.EdgeUsesType
	EdgeTests      = core.EdgeTests
	EdgeContains   = core.EdgeContains
	EdgeOverrides  = core.EdgeOverrides
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

// ChangeImpactResult is the deterministic change-set for a method signature
// change: declaration(s), the override/implementation family in the subtype
// closure, super-declarations up the hierarchy, and every method with a
// resolved call edge into the set. Computed in the engine so no agent has to
// orchestrate references → overrides → callers over primitives.
type ChangeImpactResult struct {
	Query        string
	Declarations []Symbol
	Supers       []Symbol
	Family       []Symbol
	Callers      []Symbol

	// DeclaringTypes: type declarations whose bodies contain a change-set
	// member signature that is not indexed as its own symbol (Go and TS
	// interface members) — the type's declaration block is itself a change
	// site. Empty for languages whose member declarations are real symbols.
	DeclaringTypes []Symbol

	// ExternalSupers: supertype names declared in the hierarchy that resolve
	// to no indexed type (JDK / dependency types). Informational.
	ExternalSupers []string
	// OverridesExternal: "Type#method" entries when the queried method is a
	// member of an external supertype's contract — changing its signature
	// breaks a contract the project does not own, and the change-set is the
	// project-local closure only.
	OverridesExternal []string
	// Completeness: "closed" (family fully rooted in indexed types) or
	// "project-local" (bounded by an external contract).
	Completeness string
}

// Sites returns the change-set methods (declarations ∪ family ∪ callers ∪
// supers) as one deduplicated, file-ordered list. Supers are included so a
// sibling/supertype contract's member is not silently dropped; DeclaringTypes
// (type declarations, not methods) is intentionally excluded here.
func (r ChangeImpactResult) Sites() []Symbol {
	seen := make(map[string]bool)
	var out []Symbol
	for _, group := range [][]Symbol{r.Declarations, r.Family, r.Callers, r.Supers} {
		for _, s := range group {
			if !seen[s.ID] {
				seen[s.ID] = true
				out = append(out, s)
			}
		}
	}
	return out
}

// ChangeImpact resolves a "Type.method" or "Type.method(ParamType, ...)"
// query to the exact change-set for that method's signature — type-resolved
// seeding, not name-substring seeding (contrast Impact).
func (e *Engine) ChangeImpact(ctx context.Context, query string) (ChangeImpactResult, error) {
	raw, err := e.currentGraph().ChangeImpact(query)
	if err != nil {
		return ChangeImpactResult{}, err
	}
	return ChangeImpactResult{
		Query:             raw.Query,
		Declarations:      raw.Declarations,
		Supers:            raw.Supers,
		Family:            raw.Family,
		Callers:           raw.Callers,
		DeclaringTypes:    raw.DeclaringTypes,
		ExternalSupers:    raw.ExternalSupers,
		OverridesExternal: raw.OverridesExternal,
		Completeness:      raw.Completeness,
	}, nil
}

// RenameEdit is one suggested line edit in a rename plan.
type RenameEdit struct {
	FilePath string
	Line     int    // 1-based
	Before   string // source line as indexed
	After    string // with the rename applied
	SiteID   string // containing symbol ID
	Site     string // "relpath:name" for relay
}

// RenamePlanResult converts a ChangeImpact set into concrete line edits.
// Edits are confirmed (apply as-is); Ambiguous lines belong to methods that
// also call a same-named non-family method and need receiver-type
// verification before applying. Precision-first: nothing is silently
// included or dropped.
type RenamePlanResult struct {
	Query   string
	NewName string

	Edits     []RenameEdit
	Ambiguous []RenameEdit
	// Unresolved lists change-set sites for which no line edit could be
	// derived — the agent must handle these manually.
	Unresolved []string

	SitesTotal        int
	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string // "closed" | "project-local"
}

// RenamePlan computes the change-impact set for query and converts it into
// line edits renaming the member to newName.
func (e *Engine) RenamePlan(ctx context.Context, query, newName string) (RenamePlanResult, error) {
	raw, err := e.currentGraph().RenamePlan(query, newName)
	if err != nil {
		return RenamePlanResult{}, err
	}
	res := RenamePlanResult{
		Query:             raw.Query,
		NewName:           raw.NewName,
		Unresolved:        raw.Unresolved,
		SitesTotal:        raw.SitesTotal,
		ExternalSupers:    raw.ExternalSupers,
		OverridesExternal: raw.OverridesExternal,
		Completeness:      raw.Completeness,
	}
	for _, e := range raw.Edits {
		res.Edits = append(res.Edits, RenameEdit(e))
	}
	for _, e := range raw.Ambiguous {
		res.Ambiguous = append(res.Ambiguous, RenameEdit(e))
	}
	return res, nil
}

// MissingImplementationsResult is the deterministic answer to "which types
// claiming this contract do not implement Type.method" — the companion to
// ChangeImpact for interface evolution: ChangeImpact returns what must change
// when a signature changes; this returns who is broken once the member is
// required.
type MissingImplementationsResult struct {
	Query    string
	Contract []Symbol // the member's declaration(s) on the named type

	Missing         []Symbol // concrete closure types with no impl, own or inherited: compile errors
	AbstractMissing []Symbol // abstract classes without an impl (their concrete subtypes are in Missing)
	Unverifiable    []Symbol // types whose class-extends chain leaves the index (an external base may provide it)

	ImplementedCount int  // closure types that do carry a compatible implementation
	DefaultProvided  bool // the contract supplies a body every subtype inherits; nothing can be missing

	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string // "closed" | "project-local"
}

// MissingImplementations resolves a "Type.method" or
// "Type.method(ParamType, ...)" query to every type in the subtype closure
// that fails to implement the member.
func (e *Engine) MissingImplementations(ctx context.Context, query string) (MissingImplementationsResult, error) {
	raw, err := e.currentGraph().MissingImplementations(query)
	if err != nil {
		return MissingImplementationsResult{}, err
	}
	return MissingImplementationsResult{
		Query:             raw.Query,
		Contract:          raw.Contract,
		Missing:           raw.Missing,
		AbstractMissing:   raw.AbstractMissing,
		Unverifiable:      raw.Unverifiable,
		ImplementedCount:  raw.ImplementedCount,
		DefaultProvided:   raw.DefaultProvided,
		ExternalSupers:    raw.ExternalSupers,
		OverridesExternal: raw.OverridesExternal,
		Completeness:      raw.Completeness,
	}, nil
}

// CoverageSite pairs a change-set site with the tests that reach it.
type CoverageSite struct {
	Symbol    Symbol
	TestCount int
	Tests     []Symbol // capped; TestCount carries the truth
}

// UntestedSurfaceResult partitions a method's change-set by test coverage:
// "before I change Type.method, what in its blast radius has no test?"
type UntestedSurfaceResult struct {
	Query      string
	Untested   []Symbol // change-set sites with no covering test
	Covered    []CoverageSite
	TotalSites int

	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string
}

// UntestedSurface computes the change-set for a "Type.method" query and
// partitions it by covering-test evidence.
func (e *Engine) UntestedSurface(ctx context.Context, query string) (UntestedSurfaceResult, error) {
	raw, err := e.currentGraph().UntestedSurface(query)
	if err != nil {
		return UntestedSurfaceResult{}, err
	}
	covered := make([]CoverageSite, 0, len(raw.Covered))
	for _, c := range raw.Covered {
		covered = append(covered, CoverageSite{Symbol: c.Symbol, TestCount: c.TestCount, Tests: c.Tests})
	}
	return UntestedSurfaceResult{
		Query:             raw.Query,
		Untested:          raw.Untested,
		Covered:           covered,
		TotalSites:        raw.TotalSites,
		ExternalSupers:    raw.ExternalSupers,
		OverridesExternal: raw.OverridesExternal,
		Completeness:      raw.Completeness,
	}, nil
}

// DeadCodeResult reports production functions/methods nothing reaches.
// Precision-first; Caveats are part of the result and must be relayed.
type DeadCodeResult struct {
	RootCount            int
	ReachableCount       int
	Considered           int
	Dead                 []Symbol // unreachable, non-exported, name unreferenced: deletion candidates
	ExportedUnreferenced []Symbol // exported with zero in-project reference; external liveness unknown
	Caveats              []string
}

// DeadCode computes forward reachability from every entry point (main/init,
// tests, exported symbols, plus extraRoots by name) and reports what nothing
// reaches.
func (e *Engine) DeadCode(ctx context.Context, extraRoots []string) (DeadCodeResult, error) {
	raw := e.currentGraph().DeadCode(extraRoots)
	return DeadCodeResult{
		RootCount:            raw.RootCount,
		ReachableCount:       raw.ReachableCount,
		Considered:           raw.Considered,
		Dead:                 raw.Dead,
		ExportedUnreferenced: raw.ExportedUnreferenced,
		Caveats:              raw.Caveats,
	}, nil
}

// Neighbor is a symbol reached from a seed by one typed edge.
type Neighbor struct {
	Symbol     Symbol
	EdgeType   EdgeType
	Direction  string // "out" = seed→symbol; "in" = symbol→seed
	Confidence float64
}

// Neighbors returns a symbol's direct typed neighbors with edge types preserved
// — the precise "what does X call / who calls X / what tests X" answer, as
// opposed to Impact's flattened, type-erased blast radius. direction is "out",
// "in", or "both"; kinds filters by edge type (empty = all).
func (e *Engine) Neighbors(ctx context.Context, query, direction string, kinds ...EdgeType) ([]Neighbor, error) {
	kindSet := make(map[core.EdgeType]bool, len(kinds))
	for _, k := range kinds {
		kindSet[k] = true
	}
	raw := e.currentGraph().Neighbors(query, direction, kindSet)
	out := make([]Neighbor, 0, len(raw))
	for _, n := range raw {
		out = append(out, Neighbor{
			Symbol:     n.Symbol,
			EdgeType:   n.EdgeType,
			Direction:  n.Direction,
			Confidence: n.Confidence,
		})
	}
	return out, nil
}

// Tests returns the test symbols that cover the given symbol/file query.
func (e *Engine) Tests(ctx context.Context, query string) ([]Symbol, error) {
	return e.currentGraph().TestsFor(query), nil
}

// AffectedTests returns the test symbols covering any symbol defined in the
// given repo-relative files — "given this diff, which tests must run". The
// file-diff analog of Tests(query): feed it `git diff --name-only` to select
// exactly the tests a change can break, for a "run only affected tests" CI step.
func (e *Engine) AffectedTests(ctx context.Context, files []string) ([]Symbol, error) {
	return e.currentGraph().AffectedTests(files), nil
}

// References answers "where is NAME used?" by scanning code occurrences of the
// name (comments/strings excluded), each attributed to its enclosing symbol.
// Unlike Impact (which walks the resolved call graph), this is the resolution-
// free reference layer: near-complete for types/classes/constants that calls
// edges never capture. ReferenceResult.Ambiguous reports whether several
// definitions share the name. Catches syntactic references only — reflection /
// dynamic usage is invisible, so "no references" is best-effort, not proof of
// dead code.
func (e *Engine) References(ctx context.Context, name string) (ReferenceResult, error) {
	return parser.NewEngine().References(e.Root(), name)
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

// SnapshotGraph returns a deep copy of every symbol plus every edge in the
// current graph. This is the bulk export consumers use to build derived
// projections (e.g. Prism's component-level views) without N per-symbol
// round-trips; edges carry their evidence Source/Reason/Confidence so
// derived results can report the tier of their constituent evidence.
func (e *Engine) SnapshotGraph(ctx context.Context) ([]Symbol, []Edge) {
	return e.currentGraph().Snapshot()
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
