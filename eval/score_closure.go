package eval

import (
	"context"

	"github.com/provasign/grove/internal/graph"
)

// ClosureMetrics is the test-coverage closure quality under one traversal policy.
type ClosureMetrics struct {
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	TP        int     `json:"tp"`
	FP        int     `json:"fp"`
	FN        int     `json:"fn"`
}

// ClosureScorecard measures TestsFor closure quality — for each covered symbol,
// how well g.TestsForSymbol recovers the tests that (transitively) exercise it,
// scored against the dynamic-trace coverage relation. Reported per policy so
// the confidence policy's effect on coverage precision is measurable. (Like the
// tests-edge oracle, precision is a lower bound: a real static coverage path the
// dynamic trace didn't execute scores as a false positive — the caveat applies
// equally to both policies, so the delta between them is the trustworthy signal.)
type ClosureScorecard struct {
	Repo     string                    `json:"repo"`
	Commit   string                    `json:"commit,omitempty"`
	Targets  int                       `json:"targets"`
	ByPolicy map[string]ClosureMetrics `json:"byPolicy"`
}

// ScoreTestsClosure scores the TestsFor closure under PolicyTests vs
// PolicyDiagnostic against the tests-edge truth (Caller=test, Callee=symbol).
func ScoreTestsClosure(ctx context.Context, repoRoot string, header TruthFile, truth []TruthEdge) (ClosureScorecard, error) {
	symbols, edges, err := loadGraph(ctx, repoRoot)
	if err != nil {
		return ClosureScorecard{}, err
	}

	refs := map[string]FuncRef{}
	for _, e := range truth {
		refs[e.Caller.funcKey()] = e.Caller
		refs[e.Callee.funcKey()] = e.Callee
	}
	m := matchDecls(symbols, refs)

	// truthCoverage: symbolKey → set of test keys that cover it.
	truthCov := map[string]map[string]bool{}
	for _, e := range truth {
		tk, sk := e.Caller.funcKey(), e.Callee.funcKey()
		if tk == sk || m.keyToID[tk] == "" || m.keyToID[sk] == "" {
			continue
		}
		if truthCov[sk] == nil {
			truthCov[sk] = map[string]bool{}
		}
		truthCov[sk][tk] = true
	}

	g := graph.New()
	g.ReplaceWithStoredEdges(symbols, edges, len(symbols))

	card := ClosureScorecard{
		Repo: header.Repo, Commit: header.Commit,
		Targets: len(truthCov), ByPolicy: map[string]ClosureMetrics{},
	}
	for _, pol := range []graph.TraversalPolicy{graph.PolicyTests, graph.PolicyDiagnostic} {
		var tp, fp, fn int
		for sk, truthTests := range truthCov {
			groveTests := map[string]bool{}
			for _, t := range g.TestsForSymbol(m.keyToID[sk], pol) {
				if tk, ok := m.idToKey[t.ID]; ok {
					groveTests[tk] = true
				}
			}
			for tk := range groveTests {
				if truthTests[tk] {
					tp++
				} else {
					fp++
				}
			}
			for tk := range truthTests {
				if !groveTests[tk] {
					fn++
				}
			}
		}
		met := ClosureMetrics{TP: tp, FP: fp, FN: fn}
		if tp+fp > 0 {
			met.Precision = round4(float64(tp) / float64(tp+fp))
		}
		if tp+fn > 0 {
			met.Recall = round4(float64(tp) / float64(tp+fn))
		}
		card.ByPolicy[pol.Name] = met
	}
	return card, nil
}
