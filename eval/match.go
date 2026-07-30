package eval

import (
	"context"
	"fmt"
	"strings"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/pkg/grove"
)

// loadGraph indexes the repo with Grove and returns the graph's symbols and
// edges (including the lazily-computed tests view).
func loadGraph(ctx context.Context, repoRoot string) ([]core.SymbolRecord, []core.Edge, error) {
	engine, err := grove.Open(ctx, grove.Config{RepoRoot: repoRoot})
	if err != nil {
		return nil, nil, fmt.Errorf("grove open: %w", err)
	}
	defer engine.Close()
	if _, err := engine.Index(ctx, repoRoot); err != nil {
		return nil, nil, fmt.Errorf("grove index: %w", err)
	}
	// Read through the graph snapshot, not the store: tests edges are a
	// session-computed view that never persists, and the snapshot is what
	// materializes it — scoring the store would score zero tests edges.
	symbols, edges := engine.SnapshotGraph(ctx)
	return symbols, edges, nil
}

// declMatch pairs oracle declarations with Grove symbols: file + declaration
// line within the symbol's span + name agreement; tightest span wins.
type declMatch struct {
	idToKey       map[string]string
	keyToID       map[string]string
	groveCallable int
}

func matchDecls(symbols []core.SymbolRecord, refs map[string]FuncRef) declMatch {
	type groveSym struct {
		id   string
		name string
		span core.LineRange
	}
	byFile := map[string][]groveSym{}
	callable := 0
	for i := range symbols {
		s := &symbols[i]
		switch s.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
		default:
			continue
		}
		callable++
		file := strings.ReplaceAll(s.FilePath, "\\", "/")
		byFile[file] = append(byFile[file], groveSym{id: s.ID, name: s.Name, span: s.Span})
	}
	m := declMatch{idToKey: map[string]string{}, keyToID: map[string]string{}, groveCallable: callable}
	for key, ref := range refs {
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
			m.idToKey[best] = key
			m.keyToID[key] = best
		}
	}
	return m
}
