package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/store"
	"github.com/provasign/grove/pkg/grove"
)

const maxExamples = 20

// funcKey is the toolchain-independent identity used to compare edges.
func (r FuncRef) funcKey() string {
	return r.File + "\x00" + fmt.Sprint(r.Line) + "\x00" + r.Name
}

func (r FuncRef) String() string {
	return fmt.Sprintf("%s:%d %s", r.File, r.Line, r.Name)
}

// ScoreCalls indexes the repo with Grove, then scores Grove's calls edges
// against the supplied ground truth. Comparison is restricted to the matched
// universe: declarations both Grove and the oracle identified, so symbol
// extraction differences don't pollute edge accuracy.
func ScoreCalls(ctx context.Context, repoRoot string, header TruthFile, truth []TruthEdge) (Scorecard, error) {
	engine, err := grove.Open(ctx, grove.Config{RepoRoot: repoRoot})
	if err != nil {
		return Scorecard{}, fmt.Errorf("grove open: %w", err)
	}
	if _, err := engine.Index(ctx, repoRoot); err != nil {
		_ = engine.Close()
		return Scorecard{}, fmt.Errorf("grove index: %w", err)
	}
	if err := engine.Close(); err != nil {
		return Scorecard{}, err
	}

	st, err := store.Open(repoRoot)
	if err != nil {
		return Scorecard{}, fmt.Errorf("store open: %w", err)
	}
	defer st.Close()
	symbols, err := st.AllSymbols(ctx)
	if err != nil {
		return Scorecard{}, err
	}
	edges, err := st.AllEdges(ctx)
	if err != nil {
		return Scorecard{}, err
	}

	// Collect the oracle's declaration set from the truth edges.
	truthFuncs := map[string]FuncRef{}
	for _, e := range truth {
		truthFuncs[e.Caller.funcKey()] = e.Caller
		truthFuncs[e.Callee.funcKey()] = e.Callee
	}

	// Index Grove's callable symbols by file for span matching.
	type groveSym struct {
		id   string
		name string
		span core.LineRange
	}
	byFile := map[string][]groveSym{}
	groveCallable := 0
	for i := range symbols {
		s := &symbols[i]
		switch s.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
		default:
			continue
		}
		groveCallable++
		file := strings.ReplaceAll(s.FilePath, "\\", "/")
		byFile[file] = append(byFile[file], groveSym{id: s.ID, name: s.Name, span: s.Span})
	}

	// Match each oracle declaration to the tightest Grove symbol in the same
	// file whose span contains the declaration line and whose name agrees.
	groveIDToKey := map[string]string{}
	keyToGroveID := map[string]string{}
	for key, ref := range truthFuncs {
		base := ref.Name
		if i := strings.LastIndex(base, "."); i >= 0 {
			base = base[i+1:]
		}
		best := ""
		bestSize := int(^uint(0) >> 1)
		for _, cand := range byFile[ref.File] {
			if ref.Line < cand.span.Start || ref.Line > cand.span.End {
				continue
			}
			if cand.name != base && cand.name != ref.Name && !strings.HasSuffix(cand.name, "."+base) {
				continue
			}
			if size := cand.span.End - cand.span.Start; size < bestSize {
				bestSize = size
				best = cand.id
			}
		}
		if best != "" {
			groveIDToKey[best] = key
			keyToGroveID[key] = best
		}
	}

	// Truth edge set over the matched universe. Self-edges (recursion) are
	// excluded on both sides — they carry no blast-radius information.
	truthSet := map[[2]string]TruthEdge{}
	for _, e := range truth {
		ck, ek := e.Caller.funcKey(), e.Callee.funcKey()
		if ck == ek || keyToGroveID[ck] == "" || keyToGroveID[ek] == "" {
			continue
		}
		truthSet[[2]string{ck, ek}] = e
	}

	// Grove calls-edge set over the matched universe.
	groveSet := map[[2]string]bool{}
	for _, e := range edges {
		if e.Type != core.EdgeCalls {
			continue
		}
		fromKey, okFrom := groveIDToKey[e.From]
		toKey, okTo := groveIDToKey[e.To]
		if !okFrom || !okTo || fromKey == toKey {
			continue
		}
		groveSet[[2]string{fromKey, toKey}] = true
	}

	tp := 0
	var falsePos, falseNeg []EdgeExample
	for pair := range groveSet {
		if _, ok := truthSet[pair]; ok {
			tp++
		} else if len(falsePos) < maxExamples {
			falsePos = append(falsePos, exampleFromKeys(pair, truthFuncs))
		}
	}
	for pair, e := range truthSet {
		if !groveSet[pair] {
			if len(falseNeg) < maxExamples {
				falseNeg = append(falseNeg, EdgeExample{Caller: e.Caller.String(), Callee: e.Callee.String()})
			}
		}
	}
	sortExamples(falsePos)
	sortExamples(falseNeg)

	card := Scorecard{
		Repo:            header.Repo,
		Commit:          header.Commit,
		Generator:       header.Generator,
		EdgeType:        string(core.EdgeCalls),
		TruthFunctions:  len(truthFuncs),
		GroveFunctions:  groveCallable,
		MatchedUniverse: len(keyToGroveID),
		TruthEdges:      len(truthSet),
		GroveEdges:      len(groveSet),
		TruePositives:   tp,
		FalsePositives:  falsePos,
		FalseNegatives:  falseNeg,
	}
	if len(truthFuncs) > 0 {
		card.SymbolMatchRate = ratio(len(keyToGroveID), len(truthFuncs))
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
	return card, nil
}

func exampleFromKeys(pair [2]string, funcs map[string]FuncRef) EdgeExample {
	return EdgeExample{Caller: funcs[pair[0]].String(), Callee: funcs[pair[1]].String()}
}

func sortExamples(ex []EdgeExample) {
	sort.Slice(ex, func(i, j int) bool {
		if ex[i].Caller != ex[j].Caller {
			return ex[i].Caller < ex[j].Caller
		}
		return ex[i].Callee < ex[j].Callee
	})
}

func ratio(num, den int) float64 { return round4(float64(num) / float64(den)) }

func round4(f float64) float64 { return float64(int(f*10000+0.5)) / 10000 }

// Markdown renders the scorecard as a readable report table.
func (c Scorecard) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Grove edge accuracy — %s (`%s`)\n\n", c.Repo, c.EdgeType)
	if c.Commit != "" {
		fmt.Fprintf(&b, "Commit: `%s` · ", c.Commit)
	}
	fmt.Fprintf(&b, "Oracle: `%s`\n\n", c.Generator)
	fmt.Fprintf(&b, "| Metric | Value |\n|---|---|\n")
	fmt.Fprintf(&b, "| Oracle declarations | %d |\n", c.TruthFunctions)
	fmt.Fprintf(&b, "| Grove callable symbols | %d |\n", c.GroveFunctions)
	fmt.Fprintf(&b, "| Matched universe | %d (%.1f%%) |\n", c.MatchedUniverse, c.SymbolMatchRate*100)
	fmt.Fprintf(&b, "| Oracle edges (in universe) | %d |\n", c.TruthEdges)
	fmt.Fprintf(&b, "| Grove edges (in universe) | %d |\n", c.GroveEdges)
	fmt.Fprintf(&b, "| True positives | %d |\n", c.TruePositives)
	fmt.Fprintf(&b, "| **Precision** | **%.4f** |\n", c.Precision)
	fmt.Fprintf(&b, "| **Recall** | **%.4f** |\n", c.Recall)
	fmt.Fprintf(&b, "| **F1** | **%.4f** |\n", c.F1)
	writeExamples(&b, "False positives (Grove edge, oracle disagrees)", c.FalsePositives)
	writeExamples(&b, "False negatives (oracle edge Grove missed)", c.FalseNegatives)
	return b.String()
}

func writeExamples(b *strings.Builder, title string, ex []EdgeExample) {
	if len(ex) == 0 {
		return
	}
	fmt.Fprintf(b, "\n## %s (first %d)\n\n", title, len(ex))
	for _, e := range ex {
		fmt.Fprintf(b, "- `%s` → `%s`\n", e.Caller, e.Callee)
	}
}
