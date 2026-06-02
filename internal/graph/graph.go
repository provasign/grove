package graph

import (
	"crypto/sha1"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/embeddings"
	"github.com/provasign/grove/internal/embeddings/model2vec"
)

// newSemanticEngine selects the embedding backend per process. Model2Vec is
// the default — it ships embedded in the binary and delivers true semantic
// similarity (synonym/paraphrase matching). TF-IDF is the lexical fallback
// for users who explicitly opt out via GROVE_EMBEDDINGS=tfidf, and the
// automatic fallback if Model2Vec initialisation ever fails.
func newSemanticEngine() embeddings.Engine {
	if os.Getenv("GROVE_EMBEDDINGS") == "tfidf" {
		return embeddings.NewTFIDF()
	}
	eng, err := model2vec.Default()
	if err != nil {
		// Defensive: the model is //go:embed'd so this should never fail
		// in practice, but if it ever does we want search to keep working
		// rather than crash. TF-IDF retains the lexical baseline.
		return embeddings.NewTFIDF()
	}
	return eng
}

type CodeGraph struct {
	mu           sync.RWMutex
	symbols      map[string]core.SymbolRecord
	edges        []core.Edge
	filesIndexed int

	// Lazily-built semantic-search engine. Invalidated on every Replace().
	semMu     sync.Mutex
	semEngine embeddings.Engine
	semDirty  bool
}

func New() *CodeGraph {
	return &CodeGraph{symbols: map[string]core.SymbolRecord{}, semDirty: true}
}

func (g *CodeGraph) Replace(symbols []core.SymbolRecord, filesIndexed int) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.symbols = make(map[string]core.SymbolRecord, len(symbols))
	for _, s := range symbols {
		g.symbols[s.ID] = s
	}
	g.edges = BuildEdges(symbols)
	g.filesIndexed = filesIndexed

	g.semMu.Lock()
	g.semDirty = true
	g.semEngine = nil
	g.semMu.Unlock()
}

// BuildEdges constructs all 8 edge types from the symbol set.
//
// Edge construction order (matches Implementation Plan §3.1):
//  1. defines      (file        → symbol)         confidence 1.0
//  2. contains     (parent      → child)          confidence 1.0
//  3. imports      (file        → import:path)    confidence 0.9
//  4. extends      (subtype     → supertype)      confidence 0.85
//  5. implements   (concrete    → interface/trait) confidence 0.85
//  6. uses-type    (symbol      → referenced type) confidence 0.5
//  7. calls        (caller      → callee)         confidence 0.85 same-file, 0.6 cross-file
//  8. tests        (test sym    → tested sym)     confidence 0.8
//
// "calls" and "uses-type" are scoped to same-file + imported-file symbols
// per the non-negotiable accuracy rule in the plan.
func BuildEdges(symbols []core.SymbolRecord) []core.Edge {
	idx := newEdgeIndex(symbols)

	edges := make([]core.Edge, 0, len(symbols)*4)
	edges = append(edges, buildDefinesAndImports(symbols)...)
	edges = append(edges, buildContains(idx, symbols)...)
	edges = append(edges, buildExtendsImplements(idx, symbols)...)
	edges = append(edges, buildUsesType(idx, symbols)...)
	edges = append(edges, buildCalls(idx, symbols)...)
	edges = append(edges, buildTests(idx, symbols)...)
	return edges
}

func (g *CodeGraph) Snapshot() ([]core.SymbolRecord, []core.Edge) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	symbols := make([]core.SymbolRecord, 0, len(g.symbols))
	for _, s := range g.symbols {
		symbols = append(symbols, deepCopySymbol(s))
	}
	return symbols, append([]core.Edge(nil), g.edges...)
}

// deepCopySymbol copies all slice fields so callers cannot corrupt the graph's
// internal backing arrays via append-within-capacity.
func deepCopySymbol(s core.SymbolRecord) core.SymbolRecord {
	if s.Imports != nil {
		c := make([]string, len(s.Imports))
		copy(c, s.Imports)
		s.Imports = c
	}
	if s.Modifiers != nil {
		c := make([]string, len(s.Modifiers))
		copy(c, s.Modifiers)
		s.Modifiers = c
	}
	if s.TypeParameters != nil {
		c := make([]string, len(s.TypeParameters))
		copy(c, s.TypeParameters)
		s.TypeParameters = c
	}
	if s.Annotations != nil {
		c := make([]string, len(s.Annotations))
		copy(c, s.Annotations)
		s.Annotations = c
	}
	if s.CallSites != nil {
		c := make([]core.CallSite, len(s.CallSites))
		copy(c, s.CallSites)
		s.CallSites = c
	}
	return s
}

func (g *CodeGraph) Status() core.Status {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return core.Status{
		FilesIndexed: g.filesIndexed,
		SymbolCount:  len(g.symbols),
		EdgeCount:    len(g.edges),
	}
}

// Search returns symbols whose name, qualified name, file path, or signature
// contains the query string (case-insensitive).
func (g *CodeGraph) Search(query string, limit int) []core.SymbolRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()

	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 50
	}

	var results []core.SymbolRecord
	for _, symbol := range g.symbols {
		text := strings.ToLower(symbol.Name + " " + symbol.QualifiedName + " " + symbol.FilePath + " " + symbol.Signature)
		if query == "" || strings.Contains(text, query) {
			results = append(results, symbol)
		}
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].FilePath == results[j].FilePath {
			return results[i].Span.Start < results[j].Span.Start
		}
		return results[i].FilePath < results[j].FilePath
	})
	if len(results) > limit {
		return results[:limit]
	}
	return results
}

// Deps returns all edges that touch the given file path.
// Uses exact-prefix matching: edges from "file:<path>" or whose node ID
// begins with "<path>::" (symbol IDs in that file).
func (g *CodeGraph) Deps(filePath string) []core.Edge {
	g.mu.RLock()
	defer g.mu.RUnlock()

	fileNode := "file:" + filePath
	idPrefix := filePath + "::"

	var deps []core.Edge
	for _, edge := range g.edges {
		if edge.From == fileNode || strings.HasPrefix(edge.From, idPrefix) ||
			strings.HasPrefix(edge.To, idPrefix) {
			deps = append(deps, edge)
		}
	}
	return deps
}

// Impact returns all symbols reachable from the seed (identified by query)
// by traversing inbound edges up to maxDepth.
// "Inbound" means: things that call, test, or contain the seed symbol —
// i.e., the blast radius if the seed changes.
func (g *CodeGraph) Impact(query string, maxDepth int) []core.SymbolRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()

	if maxDepth <= 0 {
		maxDepth = 3
	}
	needle := strings.ToLower(strings.TrimSpace(query))

	// Find seed symbol IDs
	seeds := make(map[string]bool)
	for id, symbol := range g.symbols {
		if needle != "" && (strings.EqualFold(symbol.Name, query) ||
			strings.EqualFold(symbol.QualifiedName, query) ||
			strings.EqualFold(symbol.ID, query)) {
			seeds[id] = true
		}
	}
	// Fallback: substring search
	if len(seeds) == 0 {
		for id, symbol := range g.symbols {
			text := strings.ToLower(symbol.ID + " " + symbol.Name + " " + symbol.QualifiedName + " " + symbol.FilePath)
			if needle == "" || strings.Contains(text, needle) {
				seeds[id] = true
			}
		}
	}

	// BFS over inbound edges (things that depend on the seed)
	visited := make(map[string]int) // node → depth
	queue := make([]string, 0, len(seeds))
	for seed := range seeds {
		visited[seed] = 0
		queue = append(queue, seed)
	}

	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		depth := visited[node]
		if depth >= maxDepth {
			continue
		}
		for _, edge := range g.edges {
			// Only traverse meaningful inbound edge types for blast radius
			if edge.To != node {
				continue
			}
			if edge.Type != core.EdgeCalls && edge.Type != core.EdgeTests &&
				edge.Type != core.EdgeContains && edge.Type != core.EdgeImplements &&
				edge.Type != core.EdgeExtends && edge.Type != core.EdgeUsesType {
				continue
			}
			if _, ok := visited[edge.From]; ok {
				continue
			}
			visited[edge.From] = depth + 1
			queue = append(queue, edge.From)
		}
	}

	var impacted []core.SymbolRecord
	for id := range visited {
		if symbol, ok := g.symbols[id]; ok && !seeds[id] {
			impacted = append(impacted, symbol)
		}
	}
	sort.Slice(impacted, func(i, j int) bool {
		if impacted[i].FilePath == impacted[j].FilePath {
			return impacted[i].Span.Start < impacted[j].Span.Start
		}
		return impacted[i].FilePath < impacted[j].FilePath
	})
	return impacted
}

// TestsFor returns all test symbols that cover the given query target.
// Resolution order:
//  1. If the query matches an existing symbol name, follow inbound `tests`
//     edges (and inbound `calls` one hop further) to gather covering tests.
//  2. Fallback: substring search in test files (for free-text queries).
func (g *CodeGraph) TestsFor(query string) []core.SymbolRecord {
	g.mu.RLock()
	defer g.mu.RUnlock()

	needle := strings.ToLower(strings.TrimSpace(query))

	// Phase 1: locate target symbols by exact or substring match.
	targets := make(map[string]bool)
	for id, symbol := range g.symbols {
		if needle == "" {
			break
		}
		if strings.EqualFold(symbol.Name, query) ||
			strings.EqualFold(symbol.QualifiedName, query) ||
			strings.EqualFold(symbol.FilePath, query) {
			targets[id] = true
		}
	}

	// Phase 2: walk inbound `tests` edges from each target. If a caller has
	// its own inbound `tests` edges, include those (transitive coverage,
	// depth 1).
	tests := make(map[string]core.SymbolRecord)
	if len(targets) > 0 {
		callers := make(map[string]bool)
		for _, edge := range g.edges {
			if !targets[edge.To] {
				continue
			}
			if edge.Type == core.EdgeTests {
				if t, ok := g.symbols[edge.From]; ok {
					tests[t.ID] = t
				}
			}
			if edge.Type == core.EdgeCalls {
				callers[edge.From] = true
			}
		}
		for _, edge := range g.edges {
			if edge.Type != core.EdgeTests || !callers[edge.To] {
				continue
			}
			if t, ok := g.symbols[edge.From]; ok {
				tests[t.ID] = t
			}
		}
	}

	// Phase 3: substring fallback in test files.
	if len(tests) == 0 {
		for _, symbol := range g.symbols {
			if !isTestFile(symbol.FilePath) {
				continue
			}
			text := strings.ToLower(symbol.FilePath + " " + symbol.Name + " " + symbol.QualifiedName + " " + symbol.Signature)
			if needle == "" || strings.Contains(text, needle) {
				tests[symbol.ID] = symbol
			}
		}
	}

	out := make([]core.SymbolRecord, 0, len(tests))
	for _, t := range tests {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FilePath == out[j].FilePath {
			return out[i].Span.Start < out[j].Span.Start
		}
		return out[i].FilePath < out[j].FilePath
	})
	return out
}

// ComputeICR computes an Isolated Change Region for the given intent string.
func (g *CodeGraph) ComputeICR(intent string) core.IsolatedChangeRegion {
	seeds := g.Search(intent, 20)
	if len(seeds) == 0 {
		seeds = g.Search("", 20)
	}

	exclusive := make(map[string]bool)
	shared := make(map[string]bool)
	boundary := make(map[string]bool)
	files := make(map[string]bool)
	readable := make(map[string]bool)

	for _, symbol := range seeds {
		exclusive[symbol.ID] = true
		files[symbol.FilePath] = true
		readable[symbol.FilePath] = true
		for _, edge := range g.Deps(symbol.FilePath) {
			boundary[edge.From+"::"+string(edge.Type)+"::"+edge.To] = true
			shared[edge.From] = true
			shared[edge.To] = true
		}
	}

	intentID := "icr-" + shortSHA(intent)
	lockKeys := make([]string, 0, len(files))
	for file := range files {
		lockKeys = append(lockKeys, "grove:lock:file:"+file)
	}
	sort.Strings(lockKeys)

	return core.IsolatedChangeRegion{
		IntentID:       intentID,
		Exclusive:      mapKeys(exclusive),
		SharedRead:     mapKeys(shared),
		Boundary:       mapKeys(boundary),
		ExclusiveFiles: mapKeys(files),
		ReadableFiles:  mapKeys(readable),
		Confidence:     confidenceForSeeds(len(seeds)),
		LockKeys:       lockKeys,
	}
}

// DetectConflicts checks whether two ICRs have overlapping exclusive symbols or files.
func DetectConflicts(a, b core.IsolatedChangeRegion) core.ConflictResult {
	symbolsA := sliceSet(a.Exclusive)
	filesA := sliceSet(a.ExclusiveFiles)
	var overlapSymbols, overlapFiles []string
	for _, s := range b.Exclusive {
		if symbolsA[s] {
			overlapSymbols = append(overlapSymbols, s)
		}
	}
	for _, f := range b.ExclusiveFiles {
		if filesA[f] {
			overlapFiles = append(overlapFiles, f)
		}
	}
	sort.Strings(overlapSymbols)
	sort.Strings(overlapFiles)
	return core.ConflictResult{
		Conflicts:      len(overlapSymbols) > 0 || len(overlapFiles) > 0,
		OverlapSymbols: overlapSymbols,
		OverlapFiles:   overlapFiles,
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func isTestFile(path string) bool {
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	return strings.HasSuffix(lower, "_test.go") ||
		(strings.HasPrefix(lower, "test_") && strings.HasSuffix(lower, ".py")) ||
		strings.HasSuffix(lower, "_test.py") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".spec.js") ||
		strings.HasSuffix(base, "Test.java") ||
		strings.HasSuffix(base, "Spec.java")
}

func mapKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sliceSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

func shortSHA(value string) string {
	sum := sha1.Sum([]byte(value))
	return hex.EncodeToString(sum[:])[:12]
}

func confidenceForSeeds(count int) float64 {
	switch {
	case count == 0:
		return 0.2
	case count < 3:
		return 0.65
	case count < 10:
		return 0.8
	default:
		return 0.9
	}
}

// SemanticSearch ranks symbols against a free-text intent using the
// configured embedding backend (Model2Vec by default; TF-IDF if
// GROVE_EMBEDDINGS=tfidf). Documents are constructed from
// (name + qualifiedName + signature + docstring + parent). The engine is
// built lazily and cached until the next Replace().
func (g *CodeGraph) SemanticSearch(query string, limit int) []embeddings.Scored {
	if limit <= 0 {
		limit = 20
	}
	// Always acquire g.mu before g.semMu to match the order in Replace(),
	// which holds g.mu.Lock then acquires g.semMu. Inverting the order
	// (semMu then g.mu) creates a deadlock with a concurrent Replace().
	g.mu.RLock()
	g.semMu.Lock()
	if g.semEngine == nil || g.semDirty {
		syms := make([]core.SymbolRecord, 0, len(g.symbols))
		for _, s := range g.symbols {
			syms = append(syms, s)
		}
		eng := newSemanticEngine()
		eng.Index(syms)
		g.semEngine = eng
		g.semDirty = false
	}
	eng := g.semEngine
	g.semMu.Unlock()
	g.mu.RUnlock()
	return eng.Query(query, limit)
}
