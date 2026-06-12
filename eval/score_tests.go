package eval

import (
	"context"

	"github.com/provasign/grove/internal/core"
)

// ScoreTests scores Grove's tests edges (test symbol → exercised function)
// against runtime coverage truth from the dynamic oracle. Unlike the calls
// comparison, precision here is fully meaningful: the oracle observed
// everything each test executed (transitively), so a Grove tests-edge to a
// function the test never touched is a real false signal.
//
// Besides edge-level P/R, the scorecard reports the function-level hit
// rate — for what fraction of covered functions does Grove point at one or
// more tests that truly cover them. That's the number RFC #5's "related
// tests" review signal lives or dies by.
func ScoreTests(ctx context.Context, repoRoot string, header TruthFile, truth []TruthEdge) (Scorecard, error) {
	symbols, edges, err := loadGraph(ctx, repoRoot)
	if err != nil {
		return Scorecard{}, err
	}

	refs := map[string]FuncRef{}
	for _, e := range truth {
		refs[e.Caller.funcKey()] = e.Caller
		refs[e.Callee.funcKey()] = e.Callee
	}
	m := matchDecls(symbols, refs)

	truthSet := map[[2]string]TruthEdge{}
	for _, e := range truth {
		tk, fk := e.Caller.funcKey(), e.Callee.funcKey()
		if tk == fk || m.keyToID[tk] == "" || m.keyToID[fk] == "" {
			continue
		}
		truthSet[[2]string{tk, fk}] = e
	}

	groveSet := map[[2]string]bool{}
	for _, e := range edges {
		if e.Type != core.EdgeTests {
			continue
		}
		tk, okFrom := m.idToKey[e.From]
		fk, okTo := m.idToKey[e.To]
		if !okFrom || !okTo || tk == fk {
			continue
		}
		groveSet[[2]string{tk, fk}] = true
	}

	tp := 0
	var falsePos, falseNeg []EdgeExample
	hitFns := map[string]bool{}
	for pair := range groveSet {
		if _, ok := truthSet[pair]; ok {
			tp++
			hitFns[pair[1]] = true
		} else if len(falsePos) < maxExamples {
			falsePos = append(falsePos, exampleFromKeys(pair, refs))
		}
	}
	coveredFns := map[string]bool{}
	for pair, e := range truthSet {
		coveredFns[pair[1]] = true
		if !groveSet[pair] && len(falseNeg) < maxExamples {
			falseNeg = append(falseNeg, EdgeExample{Caller: e.Caller.String(), Callee: e.Callee.String()})
		}
	}
	sortExamples(falsePos)
	sortExamples(falseNeg)

	card := Scorecard{
		Repo:              header.Repo,
		Commit:            header.Commit,
		Generator:         header.Generator,
		EdgeType:          string(core.EdgeTests),
		TruthFunctions:    len(refs),
		GroveFunctions:    m.groveCallable,
		MatchedUniverse:   len(m.keyToID),
		TruthEdges:        len(truthSet),
		GroveEdges:        len(groveSet),
		TruePositives:     tp,
		FalsePositives:    falsePos,
		FalseNegatives:    falseNeg,
		FunctionsCovered:  len(coveredFns),
		FunctionsHit:      len(hitFns),
	}
	if len(refs) > 0 {
		card.SymbolMatchRate = ratio(len(m.keyToID), len(refs))
	}
	if len(groveSet) > 0 {
		card.Precision = ratio(tp, len(groveSet))
	}
	if len(truthSet) > 0 {
		card.Recall = ratio(tp, len(truthSet))
	}
	if card.Precision+card.Recall > 0 {
		card.F1 = round4(2 * card.Precision * card.Recall / (card.Precision + card.Recall))
	}
	if len(coveredFns) > 0 {
		card.FunctionHitRate = ratio(len(hitFns), len(coveredFns))
	}
	return card, nil
}
