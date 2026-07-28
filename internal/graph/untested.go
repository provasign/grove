package graph

import (
	"sort"

	"github.com/provasign/grove/internal/core"
)

// CoverageSite pairs a change-set site with the tests that reach it
// (directly or through the inbound dependency closure).
type CoverageSite struct {
	Symbol    core.SymbolRecord
	TestCount int
	// Tests is capped (TestCount carries the truth) to keep the payload
	// agent-sized: the agent needs "is it covered, and where do I look",
	// not every transitive test.
	Tests []core.SymbolRecord
}

// UntestedSurfaceResult partitions a method's change-set by test coverage:
// the answer to "before I change Type.method, what in its blast radius has
// no test pinning it?". The natural pipeline is change_impact →
// untested_surface(same query) → write tests for exactly the Untested list.
type UntestedSurfaceResult struct {
	Query string
	// Untested: change-set sites (declaration, override family, callers)
	// with no covering test in their inbound dependency closure. These are
	// the sites a signature change can break silently.
	Untested []core.SymbolRecord
	// Covered: sites with at least one covering test.
	Covered    []CoverageSite
	TotalSites int

	// Same contract-boundary reporting as ChangeImpact — the change-set this
	// partition is computed over is subject to the same bound.
	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string // "closed" | "project-local"
}

const (
	maxTestsPerSite = 3
	// coverageDepth bounds the inbound closure the coverage walk explores.
	// Unbounded, a densely connected repo marks every site "covered" by
	// thousands of far-transitive tests (measured: 14k+ on django) — evidence
	// with no discriminating power. A test within a few caller hops pins the
	// site's behavior; one twenty hops away does not.
	coverageDepth = 3
)

// UntestedSurface computes the change-set for a "Type.method" or
// "Type.method(ParamType, ...)" query (exactly as ChangeImpact does) and
// partitions it by covering-test evidence under PolicyTests — evidence-backed
// edges only, so "covered" is never asserted off a regex-fallback edge, and
// depth-bounded so "covered" means a test within coverageDepth caller hops.
func (g *CodeGraph) UntestedSurface(query string) (*UntestedSurfaceResult, error) {
	impact, err := g.ChangeImpact(query)
	if err != nil {
		return nil, err
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	res := &UntestedSurfaceResult{
		Query:             query,
		ExternalSupers:    impact.ExternalSupers,
		OverridesExternal: impact.OverridesExternal,
		Completeness:      impact.Completeness,
	}
	for _, site := range impact.Sites() {
		res.TotalSites++
		covering := g.boundedCoveringTests(site.ID, coverageDepth)
		if len(covering) == 0 {
			res.Untested = append(res.Untested, site)
			continue
		}
		cs := CoverageSite{Symbol: site, TestCount: len(covering)}
		// Sort BEFORE capping: taking the first maxTestsPerSite entries in
		// map-iteration order made the sampled tests differ run to run —
		// nondeterministic CONTENT, not just ordering.
		all := make([]core.SymbolRecord, 0, len(covering))
		for _, t := range covering {
			all = append(all, t)
		}
		sortSymbols(all)
		if len(all) > maxTestsPerSite {
			all = all[:maxTestsPerSite]
		}
		cs.Tests = all
		res.Covered = append(res.Covered, cs)
	}
	sortSymbols(res.Untested)
	sort.Slice(res.Covered, func(i, j int) bool {
		return lessSymbols(&res.Covered[i].Symbol, &res.Covered[j].Symbol)
	})
	return res, nil
}

// boundedCoveringTests is coveringTestsLocked with a hop limit: it walks the
// inbound dependency closure at most maxDepth hops from the seed and returns
// the test symbols that reach it within that horizon. Caller must hold g.mu.
func (g *CodeGraph) boundedCoveringTests(seedID string, maxDepth int) map[string]core.SymbolRecord {
	tests := make(map[string]core.SymbolRecord)
	type qn struct {
		id    string
		depth int
	}
	visited := map[string]bool{seedID: true}
	queue := []qn{{seedID, 0}}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		for _, ei := range g.inbound[node.id] {
			edge := g.edges[ei]
			switch edge.Type {
			case core.EdgeTests:
				if t, ok := g.symbols[edge.From]; ok {
					tests[t.ID] = t
				}
			case core.EdgeCalls:
				// Calls only: a test reaching the site through a caller chain
				// pins its behavior. Contains/extends/uses-type paths sweep
				// whole class hierarchies into the closure and credit tests
				// that never execute the site.
				if node.depth >= maxDepth || !PolicyTests.Allows(edge) {
					continue
				}
				if !visited[edge.From] {
					visited[edge.From] = true
					queue = append(queue, qn{edge.From, node.depth + 1})
				}
			}
		}
	}
	return tests
}
