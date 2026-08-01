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
	// Unresolved lists change-set sites for which no line edit could be
	// derived (empty indexed text, or the name not found in call position
	// on the recorded lines) — the agent must handle these manually.
	Unresolved []string

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
	// Parse the RESOLVED query ChangeImpact reports, not the caller's raw
	// input: with lenient resolution the two can differ ("Render" ->
	// "Render.Render"), and re-parsing the raw form would either fail or
	// recover a method name the plan was not computed for.
	_, methodName, _, err := parseChangeImpactQuery(ci.Query)
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
	// Supers are renamed at their declaration line exactly like family
	// members: when the query seeds on an implementation, the interface's own
	// member arrives here (as an indexed method, or as a synthesized
	// empty-RawText member that the synthetic branch below turns into the
	// interface spec-line edit). Omitting them silently dropped the contract
	// declaration from the plan → an applied rename that breaks the build.
	for _, s := range ci.Supers {
		family[s.ID] = true
	}

	// identifier in call/declaration position
	pat := regexp.MustCompile(`\b` + regexp.QuoteMeta(methodName) + `\b(\s*\()`)
	// ReplaceAllString treats $ in the replacement as a template variable —
	// escape it so a $-bearing newName substitutes literally.
	replacement := strings.ReplaceAll(newName, "$", "$$") + "$1"
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
		rawN := len(pat.FindAllStringIndex(before, -1))
		strippedN := len(pat.FindAllStringIndex(stripCommentsAndStrings(before), -1))
		if strippedN == 0 {
			return // only comment/string-literal occurrences on this line
		}
		after := pat.ReplaceAllString(before, replacement)
		if after == before {
			return // name not on this line in call position
		}
		// A string-literal occurrence alongside a real one, or multiple
		// same-name calls on one line (one may target an unindexed object):
		// the blanket rewrite cannot be attributed per-occurrence — bucket
		// Ambiguous, never confirmed.
		if rawN != strippedN || rawN > 1 {
			confirmed = false
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

	// hasNonFamilySameName reports whether sym has a call edge to a
	// same-named method OUTSIDE the family — per-line attribution is then
	// uncertain for its call sites.
	hasNonFamilySameName := func(id string) bool {
		for _, ei := range g.outbound[id] {
			e := g.edges[ei]
			if e.Type != core.EdgeCalls || family[e.To] {
				continue
			}
			if callee, ok := g.symbols[e.To]; ok && callee.Name == methodName {
				return true
			}
		}
		return false
	}

	// 1. Declarations + overrides: the signature line (first line in the
	// symbol's span where the name appears in declaration position), PLUS
	// the member's own same-name call sites — the delegating-override shape
	// (`void speak() { super.speak(); }`) is the most common override body,
	// and ChangeImpact's Callers list excludes family members.
	syntheticEdited := map[string]bool{}
	for _, s := range sites {
		if !family[s.ID] {
			continue
		}
		if s.RawText == "" {
			// Synthesized member declaration (Go/TS interface spec — the
			// member is body text of the type, not an indexed symbol). The
			// spec line lives in the declaring TYPE's RawText: without this
			// branch the site fell straight into Unresolved, so an applied
			// plan renamed every impl and caller but not the contract —
			// guaranteed compile breakage. Every matching spec line is
			// edited (TS interfaces may declare overload signatures).
			if hash := strings.LastIndexByte(s.ID, '#'); hash > 0 {
				if t, ok := g.symbols[s.ID[:hash]]; ok && t.RawText != "" {
					before := len(res.Edits) + len(res.Ambiguous)
					tLines := strings.Split(t.RawText, "\n")
					for i := range tLines {
						if pat.MatchString(stripCommentsAndStrings(tLines[i])) {
							editLine(t, t.Span.Start+i, true)
						}
					}
					if len(res.Edits)+len(res.Ambiguous) > before {
						syntheticEdited[s.ID] = true
					}
				}
			}
			continue
		}
		declLine := -1
		lines := strings.Split(s.RawText, "\n")
		for i := range lines {
			if pat.MatchString(stripCommentsAndStrings(lines[i])) {
				declLine = s.Span.Start + i
				editLine(s, declLine, true)
				break
			}
		}
		ambiguous := hasNonFamilySameName(s.ID)
		seen := map[int]bool{declLine: true}
		for _, cs := range s.CallSites {
			bare := cs.Callee
			if i := strings.LastIndex(bare, "."); i >= 0 {
				bare = bare[i+1:]
			}
			if bare != methodName || seen[cs.Line] {
				continue
			}
			seen[cs.Line] = true
			editLine(s, cs.Line, !ambiguous)
		}
	}

	// 2. Callers: AST-extracted call sites naming the method. A caller that
	// ALSO has a call edge to a same-named NON-family symbol makes its lines
	// ambiguous — the graph cannot attribute individual lines to the family.
	for _, c := range ci.Callers {
		ambiguousCaller := hasNonFamilySameName(c.ID)
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
	editedSites := map[string]bool{}
	for _, e := range res.Edits {
		editedSites[e.SiteID] = true
	}
	for _, e := range res.Ambiguous {
		editedSites[e.SiteID] = true
	}
	for _, s := range sites {
		if !editedSites[s.ID] && !syntheticEdited[s.ID] {
			res.Unresolved = append(res.Unresolved, s.FilePath+":"+s.Name)
		}
	}
	sort.Strings(res.Unresolved)
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
