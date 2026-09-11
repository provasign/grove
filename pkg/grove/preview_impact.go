package grove

import (
	"context"
	"fmt"
	"sort"

	"github.com/provasign/grove/internal/graph"
)

// PreviewChangeImpacts answers a batch against an in-memory source overlay.
// Queries are (qualified member, declaring file) pairs. A nil file body removes
// that file from the preview. No index, worktree, or engine state is modified.
// The graph is built once for the entire batch. Per-query errors align with
// results; an outer error means the preview could not be built.
func (e *Engine) PreviewChangeImpacts(ctx context.Context, queries [][2]string, files map[string][]byte) ([]ChangeImpactResult, []string, error) {
	symbols, err := e.SnapshotSymbols(ctx)
	if err != nil {
		return nil, nil, err
	}
	var base []Symbol
	for _, s := range symbols {
		if _, replaced := files[s.FilePath]; !replaced {
			base = append(base, s)
		}
	}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if files[path] == nil {
			continue
		}
		syms, err := e.PreviewFileSymbols(path, files[path])
		if err != nil {
			return nil, nil, fmt.Errorf("preview %s: %w", path, err)
		}
		base = append(base, syms...)
	}
	g := graph.New()
	g.Replace(base, 0)
	results := make([]ChangeImpactResult, len(queries))
	failures := make([]string, len(queries))
	for i, q := range queries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		r, err := g.ChangeImpactScoped(q[0], q[1])
		if err != nil {
			failures[i] = err.Error()
			continue
		}
		results[i] = ChangeImpactResult{
			Query: r.Query, Declarations: r.Declarations, Supers: r.Supers,
			Family: r.Family, Callers: r.Callers, DeclaringTypes: r.DeclaringTypes,
			ExternalSupers: r.ExternalSupers, OverridesExternal: r.OverridesExternal,
			Completeness: r.Completeness, CallerCoverage: r.CallerCoverage,
			HasHeuristicRefs: r.HasHeuristicRefs,
		}
	}
	return results, failures, nil
}
