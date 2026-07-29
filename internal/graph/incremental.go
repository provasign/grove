package graph

import (
	"fmt"
	"os"
	"runtime"
	"sort"
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

// BuildEdgesDelta computes the new edge set from the previous edge set (a
// prior base output, or the merged-with-native set when nativeAnalyzedDirs
// names every natively re-analyzed package dir), the previous and current
// full symbol slices, and the set of changed (added/modified/removed)
// repo-relative file paths. Falls back to BuildEdges(symbols) whenever
// soundness of the scoping cannot be guaranteed cheaply.
//
// Identity remap: symbol IDs embed the file blob SHA, so EVERY edit turns
// over every ID in the file. Symbols whose resolution-relevant content is
// unchanged (identityKey) keep their edges via an oldID→newID rewrite
// instead of caller re-resolution — a comment-level edit re-resolves almost
// nothing. Only semantically added/removed/changed symbols contribute to the
// name delta and the affected set.
// DeltaMeta describes the write-set of an incremental rebuild — everything a
// store splice needs to bring the persisted table to the new state without a
// full-table diff. Nil when the delta fell back to a full rebuild.
type DeltaMeta struct {
	// AffectedOwners: symbol IDs (in unchanged files) whose out-edges were
	// recomputed — their stored rows must be deleted and reinserted.
	AffectedOwners map[string]bool
	// AffectedTests: test symbol IDs whose tests edges were recomputed.
	AffectedTests map[string]bool
	// ChangedFiles / NativeAnalyzedDirs: as passed in (splice needs them to
	// select insert rows and native-row deletions).
	ChangedFiles       map[string]bool
	NativeAnalyzedDirs map[string]bool
}

// BuildEdgesDelta is the []core.Edge-only form of BuildEdgesDeltaMeta.
func BuildEdgesDelta(prevEdges []core.Edge, prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool, nativeAnalyzedDirs map[string]bool) []core.Edge {
	edges, _ := BuildEdgesDeltaMeta(prevEdges, prevSymbols, symbols, changedFiles, nativeAnalyzedDirs)
	return edges
}

func BuildEdgesDeltaMeta(prevEdges []core.Edge, prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool, nativeAnalyzedDirs map[string]bool) ([]core.Edge, *DeltaMeta) {
	if len(prevEdges) == 0 || len(changedFiles) == 0 {
		deltaStats.fallback++
		return BuildEdges(symbols), nil
	}

	tick := edgeTimer()
	idx := newEdgeIndex(symbols)
	tick("edge-index")

	remap, semanticOld, semanticNew := identityRemap(prevSymbols, symbols, changedFiles)

	// Names whose resolution meaning may have changed: names and parent-type
	// names of SEMANTICALLY changed symbols only, on both sides. Lowercased:
	// edgeIndex.byName buckets are lowercase-keyed, so name-rule membership
	// must compare in the same fold.
	nameDelta := map[string]bool{}
	changedSemFiles := map[string]bool{}
	collectNames := func(list []core.SymbolRecord, semantic map[string]bool) {
		for i := range list {
			s := &list[i]
			if semantic[s.ID] {
				nameDelta[strings.ToLower(s.Name)] = true
				if s.ParentSymbol != "" {
					nameDelta[strings.ToLower(s.ParentSymbol)] = true
				}
				changedSemFiles[s.FilePath] = true
			}
		}
	}
	collectNames(prevSymbols, semanticOld)
	collectNames(symbols, semanticNew)

	affected, ok := affectedCallers(idx, symbols, changedSemFiles, nameDelta)
	for id := range semanticNew {
		affected[id] = true
	}
	if !ok || len(affected) > int(maxAffectedFraction*float64(len(symbols))) {
		deltaStats.fallback++
		if os.Getenv("GROVE_TIMING") != "" {
			fmt.Fprintf(os.Stderr, "[timing] delta-fallback: affected=%d/%d changedFiles=%d nameDelta=%d ok=%v\n",
				len(affected), len(symbols), len(changedFiles), len(nameDelta), ok)
		}
		return BuildEdges(symbols), nil
	}
	deltaStats.incremental++
	if os.Getenv("GROVE_TIMING") != "" {
		fmt.Fprintf(os.Stderr, "[timing] delta-scope: affected=%d/%d changedFiles=%d nameDelta=%d\n",
			len(affected), len(symbols), len(changedFiles), len(nameDelta))
	}
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

	// Partition the previous edges once, normalizing endpoints into the new
	// ID space via the identity remap. An edge that cannot be normalized
	// (an endpoint's symbol was semantically changed or removed) is dropped
	// here — its owner is in the affected set by construction, so the
	// recomputation below replaces it. Previous native-source edges owned by
	// a natively re-analyzed package are also dropped: this round's fresh
	// native results replace them, and keeping them could strand an edge the
	// re-analysis no longer produces.
	currentID := make(map[string]bool, len(symbols))
	for i := range symbols {
		currentID[symbols[i].ID] = true
	}
	normalize := func(id string) (string, bool) {
		if currentID[id] {
			return id, true
		}
		if nid, ok := remap[id]; ok {
			return nid, true
		}
		return "", false
	}
	var prevCalls, prevTests []core.Edge
	for _, e := range prevEdges {
		if e.Type != core.EdgeCalls && e.Type != core.EdgeTests {
			continue
		}
		if e.Source == core.EvidenceSourceNative && nativeAnalyzedDirs[fileDirOfNode(e.From)] {
			continue
		}
		from, ok := normalize(e.From)
		if !ok {
			continue
		}
		to, ok := normalize(e.To)
		if !ok {
			continue
		}
		e.From, e.To = from, to
		if e.Type == core.EdgeCalls {
			prevCalls = append(prevCalls, e)
		} else {
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
		if !affected[e.From] { // endpoints already normalized to current IDs
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
	testsAffected := affectedTests(idx, symbols, affected, changedSemFiles, nameDelta, prevCalls, allCalls)
	testsNew := buildTests(idx, symbols, allCalls, testsAffected)
	keptTests := make([]core.Edge, 0, len(prevTests))
	for _, e := range prevTests {
		if !testsAffected[e.From] { // endpoints already normalized
			keptTests = append(keptTests, e)
		}
	}
	edges = append(edges, keptTests...)
	edges = append(edges, testsNew...)
	tick("delta-tests")

	// Write-set metadata for the store splice: owners in UNCHANGED files
	// whose rows must be replaced (changed-file rows are already handled by
	// the per-file deletes in the persist phase).
	ownersOutsideChanged := make(map[string]bool, len(affected))
	for i := range symbols {
		s := &symbols[i]
		if affected[s.ID] && !changedFiles[s.FilePath] {
			ownersOutsideChanged[s.ID] = true
		}
	}
	testsOutsideChanged := make(map[string]bool, len(testsAffected))
	for i := range symbols {
		s := &symbols[i]
		if testsAffected[s.ID] && !changedFiles[s.FilePath] {
			testsOutsideChanged[s.ID] = true
		}
	}
	meta := &DeltaMeta{
		AffectedOwners:     ownersOutsideChanged,
		AffectedTests:      testsOutsideChanged,
		ChangedFiles:       changedFiles,
		NativeAnalyzedDirs: nativeAnalyzedDirs,
	}
	return edges, meta
}

// contentIdentityKey captures everything call/test resolution can read from a
// symbol EXCEPT its ID and source spans (IDs embed the file blob SHA and
// turn over on every edit; spans shift under comment/whitespace edits; edges
// carry neither). Two symbols with equal keys resolve identically.
func contentIdentityKey(s *core.SymbolRecord) string {
	var b strings.Builder
	b.WriteString(s.FilePath)
	b.WriteByte(0)
	b.WriteString(s.Language)
	b.WriteByte(0)
	b.WriteString(string(s.Kind))
	b.WriteByte(0)
	b.WriteString(s.Name)
	b.WriteByte(0)
	b.WriteString(s.QualifiedName)
	b.WriteByte(0)
	b.WriteString(s.ParentSymbol)
	b.WriteByte(0)
	b.WriteString(s.Signature)
	b.WriteByte(0)
	b.WriteString(s.RawText)
	b.WriteByte(0)
	if s.Exports {
		b.WriteByte(1)
	}
	for _, imp := range s.Imports {
		b.WriteString(imp)
		b.WriteByte(1)
	}
	b.WriteByte(0)
	for _, cs := range s.CallSites {
		b.WriteString(cs.Callee)
		b.WriteByte(1) // Line excluded: resolution reads Callee/Argc only
		b.WriteString(fmt.Sprint(cs.Argc))
		b.WriteByte(2)
	}
	b.WriteByte(0)
	for _, as := range s.AttrSites {
		b.WriteString(as.Callee)
		b.WriteByte(1)
	}
	b.WriteByte(0)
	for _, a := range s.Annotations {
		b.WriteString(a)
		b.WriteByte(1)
	}
	return b.String()
}

// identityRemap pairs old and new symbols of the changed files by content
// identity. Equal-key groups pair positionally in span order when their
// counts match; everything unpaired on either side is semantically changed.
// Returns oldID→newID for the paired symbols plus the semantic remainder
// (old-side IDs, new-side IDs).
func identityRemap(prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool) (map[string]string, map[string]bool, map[string]bool) {
	group := func(list []core.SymbolRecord) map[string][]*core.SymbolRecord {
		out := map[string][]*core.SymbolRecord{}
		for i := range list {
			s := &list[i]
			if changedFiles[s.FilePath] {
				out[contentIdentityKey(s)] = append(out[contentIdentityKey(s)], s)
			}
		}
		for _, g := range out {
			sort.Slice(g, func(a, b int) bool { return g[a].Span.Start < g[b].Span.Start })
		}
		return out
	}
	oldG, newG := group(prevSymbols), group(symbols)

	remap := map[string]string{}
	semanticOld := map[string]bool{}
	semanticNew := map[string]bool{}
	for key, olds := range oldG {
		news := newG[key]
		if len(news) == len(olds) {
			for i := range olds {
				remap[olds[i].ID] = news[i].ID
			}
			continue
		}
		for _, s := range olds {
			semanticOld[s.ID] = true
		}
		for _, s := range news {
			semanticNew[s.ID] = true
		}
	}
	for key, news := range newG {
		if _, seen := oldG[key]; !seen {
			for _, s := range news {
				semanticNew[s.ID] = true
			}
		}
	}
	return remap, semanticOld, semanticNew
}

// fileDirOfNode returns the package dir of the file a node ID is anchored to
// ("path::name@sha" and "file:path" forms; anything else yields "").
func fileDirOfNode(node string) string {
	path := node
	if rest, ok := strings.CutPrefix(node, "file:"); ok {
		path = rest
	} else if i := strings.Index(node, "::"); i >= 0 {
		path = node[:i]
	} else {
		return ""
	}
	return fileDirOf(path)
}

// fileDirOf returns the slash-path directory of a repo-relative file path.
func fileDirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
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
