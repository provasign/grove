package graph

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// RenameEdit is one suggested line edit in a rename plan.
type RenameEdit struct {
	FilePath string
	Line     int    // 1-based
	Before   string // source line as indexed
	After    string // with the rename applied
	SiteID   string // containing symbol ID
	Site     string // "relpath:name" for relay
}

// RenamePlanResult is the change-set of ChangeImpact converted into concrete
// line edits: declaration/override name lines plus family-resolved call
// lines, each with a suggested substitution. Precision-first: lines the
// graph cannot attribute to the family with certainty (the containing method
// also calls a same-named non-family method) are bucketed Ambiguous, never
// silently included or dropped. The agent's job becomes review-and-apply.
type RenamePlanResult struct {
	Query   string
	NewName string

	Edits     []RenameEdit // confirmed: apply as-is
	Ambiguous []RenameEdit // same-named non-family callee also in scope: verify receiver type first

	SitesTotal        int // sites in the underlying change-impact set
	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string
}

// RenamePlan computes ChangeImpact(query) and converts it to line edits
// renaming the method to newName.
func (g *CodeGraph) RenamePlan(query, newName string) (*RenamePlanResult, error) {
	ci, err := g.ChangeImpact(query)
	if err != nil {
		return nil, err
	}
	_, methodName, _, err := parseChangeImpactQuery(query)
	if err != nil {
		return nil, err
	}
	if newName == "" || newName == methodName {
		return nil, fmt.Errorf("new name must be non-empty and different from %q", methodName)
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	res := &RenamePlanResult{
		Query: query, NewName: newName,
		ExternalSupers:    ci.ExternalSupers,
		OverridesExternal: ci.OverridesExternal,
		Completeness:      ci.Completeness,
	}
	sites := ci.Sites()
	res.SitesTotal = len(sites)

	family := make(map[string]bool)
	for _, s := range ci.Declarations {
		family[s.ID] = true
	}
	for _, s := range ci.Family {
		family[s.ID] = true
	}

	// identifier in call/declaration position
	pat := regexp.MustCompile(`\b` + regexp.QuoteMeta(methodName) + `\b(\s*\()`)
	editLine := func(s core.SymbolRecord, line int, confirmed bool) {
		if s.RawText == "" || line < s.Span.Start {
			return
		}
		lines := strings.Split(s.RawText, "\n")
		idx := line - s.Span.Start
		if idx < 0 || idx >= len(lines) {
			return
		}
		before := lines[idx]
		after := pat.ReplaceAllString(before, newName+"$1")
		if after == before {
			return // name not on this line in call position
		}
		e := RenameEdit{
			FilePath: s.FilePath, Line: line,
			Before: strings.TrimRight(before, "\r"), After: strings.TrimRight(after, "\r"),
			SiteID: s.ID, Site: s.FilePath + ":" + s.Name,
		}
		if confirmed {
			res.Edits = append(res.Edits, e)
		} else {
			res.Ambiguous = append(res.Ambiguous, e)
		}
	}

	// 1. Declarations + overrides: the signature line (first line in the
	// symbol's span where the name appears in declaration position).
	for _, s := range sites {
		if !family[s.ID] {
			continue
		}
		lines := strings.Split(s.RawText, "\n")
		for i := range lines {
			if pat.MatchString(lines[i]) {
				editLine(s, s.Span.Start+i, true)
				break
			}
		}
	}

	// 2. Callers: AST-extracted call sites naming the method. A caller that
	// ALSO has a call edge to a same-named NON-family symbol makes its lines
	// ambiguous — the graph cannot attribute individual lines to the family.
	for _, c := range ci.Callers {
		ambiguousCaller := false
		for _, ei := range g.outbound[c.ID] {
			e := g.edges[ei]
			if e.Type != core.EdgeCalls || family[e.To] {
				continue
			}
			if callee, ok := g.symbols[e.To]; ok && callee.Name == methodName {
				ambiguousCaller = true
				break
			}
		}
		seen := map[int]bool{}
		for _, cs := range c.CallSites {
			bare := cs.Callee
			if i := strings.LastIndex(bare, "."); i >= 0 {
				bare = bare[i+1:]
			}
			if bare != methodName || seen[cs.Line] {
				continue
			}
			seen[cs.Line] = true
			editLine(c, cs.Line, !ambiguousCaller)
		}
	}

	sortEdits(res.Edits)
	sortEdits(res.Ambiguous)
	return res, nil
}

func sortEdits(es []RenameEdit) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].FilePath != es[j].FilePath {
			return es[i].FilePath < es[j].FilePath
		}
		return es[i].Line < es[j].Line
	})
}
