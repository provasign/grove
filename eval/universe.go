package eval

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// UniverseReport explains why oracle declarations failed to match a Grove
// symbol: no symbols in the file at all, no span containing the declaration
// line, or a span match whose name disagrees. The output groups misses by
// reason and directory so extraction gaps show up as patterns, not noise.
func UniverseReport(ctx context.Context, repoRoot string, truth []TruthEdge) error {
	symbols, _, err := loadGraph(ctx, repoRoot)
	if err != nil {
		return err
	}
	truthFuncs := map[string]FuncRef{}
	for _, e := range truth {
		truthFuncs[e.Caller.funcKey()] = e.Caller
		truthFuncs[e.Callee.funcKey()] = e.Callee
	}
	m := matchDecls(symbols, truthFuncs)

	type fileSym struct {
		name string
		kind core.SymbolKind
		span core.LineRange
	}
	byFile := map[string][]fileSym{}
	for i := range symbols {
		s := &symbols[i]
		file := strings.ReplaceAll(s.FilePath, "\\", "/")
		byFile[file] = append(byFile[file], fileSym{s.Name, s.Kind, s.Span})
	}

	const (
		reasonNoFile   = "file has no Grove symbols"
		reasonNoSpan   = "no Grove span contains the decl line"
		reasonBadName  = "span matches, name disagrees"
		reasonNotCallable = "span+name match a non-callable symbol kind"
	)
	misses := map[string][]string{}
	for key, ref := range truthFuncs {
		if m.keyToID[key] != "" {
			continue
		}
		syms := byFile[ref.File]
		reason := reasonNoFile
		if len(syms) > 0 {
			reason = reasonNoSpan
			base := ref.Name
			if i := strings.LastIndex(base, "."); i >= 0 {
				base = base[i+1:]
			}
			for _, s := range syms {
				if ref.Line < s.span.Start || ref.Line > s.span.End {
					continue
				}
				nameOK := s.name == base || s.name == ref.Name || strings.HasSuffix(s.name, "."+base)
				callable := s.kind == core.KindFunction || s.kind == core.KindMethod || s.kind == core.KindConstructor
				switch {
				case nameOK && !callable:
					reason = fmt.Sprintf("%s (%s %q)", reasonNotCallable, s.kind, s.name)
				case !nameOK && callable && reason == reasonNoSpan:
					reason = fmt.Sprintf("%s (grove has %q)", reasonBadName, s.name)
				}
			}
		}
		misses[reason] = append(misses[reason], fmt.Sprintf("%s:%d %s", ref.File, ref.Line, ref.Name))
	}

	fmt.Printf("universe: %d oracle decls, %d matched, %d missed\n\n",
		len(truthFuncs), len(m.keyToID), len(truthFuncs)-len(m.keyToID))
	var reasons []string
	for r := range misses {
		reasons = append(reasons, r)
	}
	sort.Slice(reasons, func(i, j int) bool { return len(misses[reasons[i]]) > len(misses[reasons[j]]) })
	for _, r := range reasons {
		list := misses[r]
		sort.Strings(list)
		fmt.Printf("== %s — %d ==\n", r, len(list))
		max := 25
		if len(list) < max {
			max = len(list)
		}
		for _, s := range list[:max] {
			fmt.Println("  " + s)
		}
		if len(list) > max {
			fmt.Printf("  ... and %d more\n", len(list)-max)
		}
		fmt.Println()
	}
	return nil
}
