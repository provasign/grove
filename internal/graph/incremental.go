package graph

import (
	"runtime"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
)

// ─── Incremental edge construction ───────────────────────────────────────────
//
// BuildEdgesDelta produces the same edge multiset BuildEdges(symbols) would,
// without re-resolving every caller. The cheap passes (defines/imports,
// contains, extends/implements, uses-type, interface-satisfaction) run
// globally exactly as in BuildEdges — their cost is ~3s even on a 160k-symbol
// monorepo and running them unmodified makes their output identical by
// construction. Only the two expensive passes are owner-scoped:
//
//	calls+decorators — recomputed only for AFFECTED caller symbols; every
//	  other caller's previous call/decorator edges are kept verbatim.
//	tests            — recomputed only for AFFECTED test symbols.
//
// A caller is affected when any input its resolution reads could have
// changed (see affectedCallers). The rules are deliberately conservative:
// over-including a caller costs a little CPU; missing one is a silent stale
// edge, which is unacceptable (change-impact/verify recall depend on it).
// When the affected set degenerates (common-name edits) the whole thing
// falls back to a full rebuild — correctness never depends on the scoping
// being tight, only on it being sound, which equivalence_test.go checks by
// comparing against the BuildEdges oracle over randomized edit sequences.
//
// maxAffectedFraction: above this share of affected symbols a full rebuild is
// both simpler and usually faster.
const maxAffectedFraction = 0.3

// deltaStats counts path decisions for observability and tests; incremented
// only under the caller's single-threaded index path.
var deltaStats struct {
	incremental int
	fallback    int
}

// BuildEdgesDelta computes the new edge set from the previous BASE edge set
// (a prior BuildEdges/BuildEdgesDelta output — not the merged-with-native
// set), the previous and current full symbol slices, and the set of changed
// (added/modified/removed) repo-relative file paths. Falls back to
// BuildEdges(symbols) whenever soundness of the scoping cannot be guaranteed
// cheaply.
func BuildEdgesDelta(prevEdges []core.Edge, prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool) []core.Edge {
	if len(prevEdges) == 0 || len(changedFiles) == 0 {
		deltaStats.fallback++
		return BuildEdges(symbols)
	}

	tick := edgeTimer()
	idx := newEdgeIndex(symbols)
	tick("edge-index")

	// Names whose resolution meaning may have changed: every symbol name and
	// parent-type name occurring in a changed file, in either the old or the
	// new symbol set. A caller anywhere that references one of these names is
	// re-resolved. Lowercased: edgeIndex.byName buckets are lowercase-keyed,
	// so name-rule membership must compare in the same fold.
	nameDelta := map[string]bool{}
	collectNames := func(list []core.SymbolRecord) {
		for i := range list {
			s := &list[i]
			if changedFiles[s.FilePath] {
				nameDelta[strings.ToLower(s.Name)] = true
				if s.ParentSymbol != "" {
					nameDelta[strings.ToLower(s.ParentSymbol)] = true
				}
			}
		}
	}
	collectNames(prevSymbols)
	collectNames(symbols)

	affected, ok := affectedCallers(idx, symbols, changedFiles, nameDelta)
	if !ok || len(affected) > int(maxAffectedFraction*float64(len(symbols))) {
		deltaStats.fallback++
		return BuildEdges(symbols)
	}
	deltaStats.incremental++
	tick("delta-affected-set")

	// Cheap passes: identical to BuildEdges.
	edges := make([]core.Edge, 0, len(prevEdges))
	edges = append(edges, buildDefinesAndImports(symbols)...)
	edges = append(edges, buildContains(idx, symbols)...)
	edges = append(edges, buildExtendsImplements(idx, symbols)...)
	edges = append(edges, buildUsesType(idx, symbols)...)
	sat, satEdges := buildInterfaceSatisfaction(idx, symbols)
	edges = append(edges, satEdges...)
	tick("delta-cheap-passes")

	// Partition the previous base edges once.
	currentID := make(map[string]bool, len(symbols))
	for i := range symbols {
		currentID[symbols[i].ID] = true
	}
	var prevCalls, prevTests []core.Edge
	for _, e := range prevEdges {
		switch e.Type {
		case core.EdgeCalls:
			prevCalls = append(prevCalls, e)
		case core.EdgeTests:
			prevTests = append(prevTests, e)
		}
	}

	// calls + decorators, owner-scoped. An owner's edges are a pure function
	// of inputs the affected-set rules cover, so keeping unaffected owners'
	// previous edges verbatim reproduces the full rebuild's multiset.
	affectedSyms := make([]core.SymbolRecord, 0, len(affected))
	for i := range symbols {
		if affected[symbols[i].ID] {
			affectedSyms = append(affectedSyms, symbols[i])
		}
	}
	callsNew := scopedCalls(idx, affectedSyms, sat)
	decoNew := buildDecoratorEdges(idx, affectedSyms, callsNew)
	keptCalls := make([]core.Edge, 0, len(prevCalls))
	for _, e := range prevCalls {
		if currentID[e.From] && !affected[e.From] {
			keptCalls = append(keptCalls, e)
		}
	}
	allCalls := make([]core.Edge, 0, len(keptCalls)+len(callsNew)+len(decoNew))
	allCalls = append(allCalls, keptCalls...)
	allCalls = append(allCalls, callsNew...)
	allCalls = append(allCalls, decoNew...)
	edges = append(edges, allCalls...)
	tick("delta-calls")

	// tests, owner-scoped: a test's edges depend on the naming convention
	// (name buckets), its import scope, and the call-graph closure within
	// its bounded walk. affectedTests covers all three plus any test whose
	// closure could touch a changed call edge.
	testsAffected := affectedTests(idx, symbols, affected, changedFiles, nameDelta, prevCalls, allCalls)
	testsNew := buildTests(idx, symbols, allCalls, testsAffected)
	keptTests := make([]core.Edge, 0, len(prevTests))
	for _, e := range prevTests {
		if currentID[e.From] && !testsAffected[e.From] {
			keptTests = append(keptTests, e)
		}
	}
	edges = append(edges, keptTests...)
	edges = append(edges, testsNew...)
	tick("delta-tests")
	return edges
}

// affectedCallers returns the set of symbol IDs whose call/decorator edges
// must be recomputed. Returns ok=false when a caller's inputs cannot be
// enumerated cheaply (forcing a full rebuild).
func affectedCallers(idx *edgeIndex, symbols []core.SymbolRecord, changedFiles map[string]bool, nameDelta map[string]bool) (map[string]bool, bool) {
	affected := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		if changedFiles[s.FilePath] {
			affected[s.ID] = true
			continue
		}
		// Non-AST-language symbols resolve through the regex fallback over
		// raw text — their reference set is not cheaply enumerable, so they
		// are always re-resolved (they are rare; whole-repo-scope languages
		// are additionally caught by the scope rule below).
		if !astCallSiteLanguages[s.Language] {
			affected[s.ID] = true
			continue
		}
		// Scope rule: resolution only reads symbols in importedFiles(file),
		// so a change to any file in scope re-resolves the caller.
		scope := idx.importedFiles(s.FilePath)
		inScope := false
		for cf := range changedFiles {
			if _, ok := scope[cf]; ok {
				inScope = true
				break
			}
		}
		if inScope {
			affected[s.ID] = true
			continue
		}
		// Name rule: dispatch/interface-satisfaction resolution can bind a
		// call site to implementations OUTSIDE the caller's file scope, so a
		// caller referencing any name whose meaning changed is re-resolved
		// even when the changed file is out of scope.
		if callSitesTouchNames(s.CallSites, nameDelta) || callSitesTouchNames(s.AttrSites, nameDelta) {
			affected[s.ID] = true
		}
	}
	return affected, true
}

// callSitesTouchNames reports whether any site's callee (bare name, method
// name, or receiver qualifier) appears in the lowercased name-delta set.
func callSitesTouchNames(sites []core.CallSite, nameDelta map[string]bool) bool {
	for _, cs := range sites {
		q, n := splitCallSiteName(cs.Callee)
		if nameDelta[strings.ToLower(n)] {
			return true
		}
		if q != "" {
			// The qualifier may itself be dotted (pkg.recv.method); any
			// segment matching a changed name re-resolves the caller.
			for _, seg := range strings.Split(q, ".") {
				if nameDelta[strings.ToLower(seg)] {
					return true
				}
			}
		}
	}
	return false
}

// splitCallSiteName splits "recv.method" into ("recv", "method"); bare names
// return ("", name).
func splitCallSiteName(callee string) (string, string) {
	for i := len(callee) - 1; i >= 0; i-- {
		if callee[i] == '.' {
			return callee[:i], callee[i+1:]
		}
	}
	return "", callee
}

// affectedTests returns the test symbols whose tests edges must be
// recomputed: tests in changed files or already in the affected caller set,
// tests whose naming-convention target intersects the name delta, and tests
// whose bounded call-graph closure can reach an endpoint of any changed call
// edge (computed as a reverse BFS from changed-edge endpoints over the union
// of the old and new call adjacencies).
func affectedTests(idx *edgeIndex, symbols []core.SymbolRecord, affected map[string]bool, changedFiles map[string]bool, nameDelta map[string]bool, prevCalls, newCalls []core.Edge) map[string]bool {
	out := map[string]bool{}

	// Endpoints of call edges that changed between the runs.
	edgeKey := func(e core.Edge) string { return e.From + "\x00" + e.To }
	prevSet := make(map[string]core.Edge, len(prevCalls))
	for _, e := range prevCalls {
		prevSet[edgeKey(e)] = e
	}
	newSet := make(map[string]core.Edge, len(newCalls))
	for _, e := range newCalls {
		newSet[edgeKey(e)] = e
	}
	deltaEndpoints := map[string]bool{}
	for k, e := range prevSet {
		if _, ok := newSet[k]; !ok {
			deltaEndpoints[e.From] = true
			deltaEndpoints[e.To] = true
		}
	}
	for k, e := range newSet {
		if _, ok := prevSet[k]; !ok {
			deltaEndpoints[e.From] = true
			deltaEndpoints[e.To] = true
		}
	}

	// Reverse adjacency over old ∪ new call edges.
	reverse := map[string][]string{}
	for _, e := range prevCalls {
		reverse[e.To] = append(reverse[e.To], e.From)
	}
	for _, e := range newCalls {
		reverse[e.To] = append(reverse[e.To], e.From)
	}

	// The tests walk goes maxHelperDepth hops through helpers plus
	// maxProdDepth past the first production symbol; +1 slack keeps the
	// invalidation strictly conservative.
	const reach = 5
	visited := map[string]bool{}
	frontier := make([]string, 0, len(deltaEndpoints))
	for id := range deltaEndpoints {
		visited[id] = true
		frontier = append(frontier, id)
	}
	reachable := map[string]bool{}
	for id := range deltaEndpoints {
		reachable[id] = true
	}
	for depth := 0; depth < reach && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, from := range reverse[id] {
				if !visited[from] {
					visited[from] = true
					reachable[from] = true
					next = append(next, from)
				}
			}
		}
		frontier = next
	}

	for i := range symbols {
		s := &symbols[i]
		if !isTestFile(s.FilePath) && !core.HasTestAnnotation(s) {
			continue
		}
		switch {
		case changedFiles[s.FilePath], affected[s.ID], reachable[s.ID]:
			out[s.ID] = true
			continue
		}
		// Naming-convention rule: the test's derived target names resolve
		// through lowercased byName buckets; if any bucket it could hit
		// changed, the test is re-resolved.
		for _, candidate := range testTargetNames(s.Name) {
			if nameDelta[candidate] {
				out[s.ID] = true
				break
			}
		}
	}
	return out
}

// testTargetNames returns the lowercased naming-convention target candidates
// for a test symbol name — the same stripping rules testTargets applies
// before its byName lookups.
func testTargetNames(name string) []string {
	var candidates []string
	switch {
	case strings.HasPrefix(name, "Test"):
		candidates = append(candidates, strings.TrimPrefix(name, "Test"))
	case strings.HasPrefix(name, "test_"):
		candidates = append(candidates, strings.TrimPrefix(name, "test_"))
	case strings.HasSuffix(name, "Test"):
		candidates = append(candidates, strings.TrimSuffix(name, "Test"))
	case strings.HasSuffix(name, "Spec"):
		candidates = append(candidates, strings.TrimSuffix(name, "Spec"))
	}
	out := candidates[:0]
	for _, c := range candidates {
		if c != "" {
			out = append(out, strings.ToLower(c))
		}
	}
	return out
}

// scopedCalls is buildCalls restricted to the given symbols (the full index
// still provides global resolution context). Mirrors buildCalls' parallel
// structure and ordering guarantees.
func scopedCalls(idx *edgeIndex, symbols []core.SymbolRecord, sat *interfaceSatisfaction) []core.Edge {
	for f := range idx.byFile {
		idx.importedFiles(f)
	}
	results := make([][]core.Edge, len(symbols))
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	taskCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range taskCh {
				results[i] = resolveCallEdges(idx, symbols[i], sat)
			}
		}()
	}
	for i := range symbols {
		taskCh <- i
	}
	close(taskCh)
	wg.Wait()
	var edges []core.Edge
	for _, r := range results {
		edges = append(edges, r...)
	}
	return edges
}
