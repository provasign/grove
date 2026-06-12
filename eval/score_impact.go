package eval

import (
	"context"
	"fmt"
	"sort"

	"github.com/provasign/grove/internal/core"
)

// ImpactScorecard reports blast-radius quality: for every truth function as
// a seed, compare reverse reachability (callers within depth hops) over
// Grove's calls edges against the oracle call graph.
type ImpactScorecard struct {
	Repo          string  `json:"repo"`
	Commit        string  `json:"commit,omitempty"`
	Generator     string  `json:"generator"`
	Depth         int     `json:"depth"`
	MinPathConf   float64 `json:"minPathConf"`
	Seeds         int     `json:"seeds"`
	MeanPrecision float64 `json:"meanPrecision"`
	MeanRecall    float64 `json:"meanRecall"`
	MeanF1        float64 `json:"meanF1"`
	MeanGroveSize float64 `json:"meanGroveSize"`
	MeanTruthSize float64 `json:"meanTruthSize"`
}

type confEdge struct {
	from string
	conf float64
}

// ScoreImpact computes per-seed reverse-reachability accuracy at the given
// depth. minPathConf prunes Grove traversal when the product of edge
// confidences along the path falls below it (0 = no pruning, the current
// product behavior).
func ScoreImpact(ctx context.Context, repoRoot string, header TruthFile, truth []TruthEdge, depth int, minPathConf float64) (ImpactScorecard, error) {
	symbols, edges, err := loadGraph(ctx, repoRoot)
	if err != nil {
		return ImpactScorecard{}, err
	}
	refs := map[string]FuncRef{}
	for _, e := range truth {
		refs[e.Caller.funcKey()] = e.Caller
		refs[e.Callee.funcKey()] = e.Callee
	}
	m := matchDecls(symbols, refs)

	truthRadj := map[string][]string{}
	for _, e := range truth {
		ck, ek := e.Caller.funcKey(), e.Callee.funcKey()
		if ck == ek || m.keyToID[ck] == "" || m.keyToID[ek] == "" {
			continue
		}
		truthRadj[ek] = append(truthRadj[ek], ck)
	}
	groveRadj := map[string][]confEdge{}
	for _, e := range edges {
		if e.Type != core.EdgeCalls {
			continue
		}
		fromKey, okFrom := m.idToKey[e.From]
		toKey, okTo := m.idToKey[e.To]
		if !okFrom || !okTo || fromKey == toKey {
			continue
		}
		groveRadj[toKey] = append(groveRadj[toKey], confEdge{from: fromKey, conf: e.Confidence})
	}

	seeds := make([]string, 0, len(m.keyToID))
	for key := range m.keyToID {
		seeds = append(seeds, key)
	}
	sort.Strings(seeds)

	var sumP, sumR, sumF1, sumG, sumT float64
	used := 0
	for _, seed := range seeds {
		truthSet := bfsPlain(truthRadj, seed, depth)
		groveSet := bfsConfidence(groveRadj, seed, depth, minPathConf)
		if len(truthSet) == 0 && len(groveSet) == 0 {
			continue
		}
		used++
		inter := 0
		for k := range groveSet {
			if truthSet[k] {
				inter++
			}
		}
		p, r := 1.0, 1.0
		if len(groveSet) > 0 {
			p = float64(inter) / float64(len(groveSet))
		}
		if len(truthSet) > 0 {
			r = float64(inter) / float64(len(truthSet))
		}
		sumP += p
		sumR += r
		if p+r > 0 {
			sumF1 += 2 * p * r / (p + r)
		}
		sumG += float64(len(groveSet))
		sumT += float64(len(truthSet))
	}

	card := ImpactScorecard{
		Repo:        header.Repo,
		Commit:      header.Commit,
		Generator:   header.Generator,
		Depth:       depth,
		MinPathConf: minPathConf,
		Seeds:       used,
	}
	if used > 0 {
		n := float64(used)
		card.MeanPrecision = round4(sumP / n)
		card.MeanRecall = round4(sumR / n)
		card.MeanF1 = round4(sumF1 / n)
		card.MeanGroveSize = round4(sumG / n)
		card.MeanTruthSize = round4(sumT / n)
	}
	return card, nil
}

func bfsPlain(radj map[string][]string, seed string, depth int) map[string]bool {
	out := map[string]bool{}
	frontier := []string{seed}
	seen := map[string]bool{seed: true}
	for d := 0; d < depth; d++ {
		var next []string
		for _, node := range frontier {
			for _, caller := range radj[node] {
				if !seen[caller] {
					seen[caller] = true
					out[caller] = true
					next = append(next, caller)
				}
			}
		}
		frontier = next
	}
	return out
}

// bfsConfidence expands a node only while the best path-confidence product
// to it stays at or above minPathConf. Nodes are revisited if a stronger
// path is found.
func bfsConfidence(radj map[string][]confEdge, seed string, depth int, minPathConf float64) map[string]bool {
	type state struct {
		key   string
		conf  float64
		depth int
	}
	best := map[string]float64{seed: 1.0}
	bestDepth := map[string]int{seed: 0}
	queue := []state{{seed, 1.0, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= depth {
			continue
		}
		for _, e := range radj[cur.key] {
			conf := cur.conf * e.conf
			if conf < minPathConf {
				continue
			}
			if prev, ok := best[e.from]; ok && prev >= conf && bestDepth[e.from] <= cur.depth+1 {
				continue
			}
			best[e.from] = conf
			bestDepth[e.from] = cur.depth + 1
			queue = append(queue, state{e.from, conf, cur.depth + 1})
		}
	}
	delete(best, seed)
	out := make(map[string]bool, len(best))
	for k := range best {
		out[k] = true
	}
	return out
}

// SummaryLine renders one sweep row.
func (c ImpactScorecard) SummaryLine() string {
	return fmt.Sprintf("depth %d · minPathConf %.2f · seeds %4d · P %.4f R %.4f F1 %.4f · mean size grove %.1f / truth %.1f",
		c.Depth, c.MinPathConf, c.Seeds, c.MeanPrecision, c.MeanRecall, c.MeanF1, c.MeanGroveSize, c.MeanTruthSize)
}
