package graph

// Change impact for DATA members: fields, properties, class constants, and
// module/package variables and constants.
//
// The failure this replaces (ops sweep, 2026-09-26): change-impact on a field
// returned exactly one site — its declaration — in every language, and told
// the agent to copy that 1-site inventory. Field reads and writes are not call
// edges, so the call graph never sees them (gin Context.Errors: 1 of 25 sites;
// jackson _ignoreAllUnknown: 1 of 18). Variables and constants did worse: the
// loose-query resolver only admits methods and functions, so
// `change-impact DefaultWriter` failed with "did you mean: Default".
//
// Data members are answered from source instead: a MemberScanner (the
// engine's tree-sitter pass) lists every syntactic occurrence of the name
// with its shape — receiver text, literal type, bare identifier — and this
// file decides which occurrences bind to the queried declaration:
//
//	confirmed  — evidence ties the occurrence to the declaration: a receiver
//	             whose type (local-type inference, receiver chains) is the
//	             owner or a subtype; self/this inside the owner hierarchy; a
//	             type qualifier naming the owner; a struct/object literal of
//	             the owner type; a bare name in the declaring scope.
//	ambiguous  — the name matches but nothing typed the receiver. Reported,
//	             labelled with the reason, never mixed into the confirmed set.
//	excluded   — evidence ties the occurrence to something else: a receiver
//	             typed as another declarer of the name, a local that shadows
//	             it, a scope the declaration is not visible from. Counted.

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// MemberScanner lists syntactic occurrences of name in files of the given
// languages. The int is the number of files that could not be scanned.
type MemberScanner func(name string, languages []string) ([]core.MemberOccurrence, int)

// MemberAccess is one source line that reads, writes, initializes, or
// declares the queried data member.
type MemberAccess struct {
	FilePath  string
	Line      int
	Enclosing core.SymbolRecord // tightest indexed symbol containing the line (zero when none)
	Access    string            // "decl" | "read" | "write" | "init" | "call"
	Evidence  string            // why it is confirmed, or why it is ambiguous
	Text      string            // the source line, verbatim
	// Cols are the 0-based byte columns of the member's name on the line
	// that this classification covers (a line may also hold same-named
	// occurrences that bind elsewhere: `c.Errors.Errors()`).
	Cols []int
}

// SetMemberScanner installs the source scanner data-member change impact
// uses. Without one, a member query returns its declaration(s) and inbound
// call-graph references only, labelled declaration-only.
func (g *CodeGraph) SetMemberScanner(s MemberScanner) {
	g.mu.Lock()
	g.memberScanner = s
	g.mu.Unlock()
}

var dataMemberKinds = map[core.SymbolKind]bool{
	core.KindField: true, core.KindConst: true, core.KindVariable: true,
}

func isTypeKind(k core.SymbolKind) bool {
	switch k {
	case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
		return true
	}
	return false
}

func isCallableKind(k core.SymbolKind) bool {
	return k == core.KindMethod || k == core.KindFunction || k == core.KindConstructor
}

// memberAnchor is a resolved data-member query.
type memberAnchor struct {
	query  string
	name   string
	decls  []core.SymbolRecord
	owners map[string]bool // owner type names plus every subtype (inherits the member)
	// ownerless: module/package-level variable or constant.
	ownerless bool
}

// memberAnchorLocked resolves query to a data member, or returns nil when the
// query names a method/function (those keep the ordinary change-set path) or
// no data member at all. Ambiguity is an error listing the candidates —
// never a silent pick.
func (g *CodeGraph) memberAnchorLocked(query, file string) (*memberAnchor, error) {
	q := strings.TrimSpace(query)
	if q == "" || strings.ContainsAny(q, "(") || looksLikeFileRef(q) {
		return nil, nil
	}
	fileOK := func(s core.SymbolRecord) bool { return file == "" || strings.Contains(s.FilePath, file) }

	if dot := strings.LastIndexAny(q, ".:"); dot > 0 && dot < len(q)-1 {
		typeName, member, _, err := parseChangeImpactQuery(strings.ReplaceAll(q, "::", "."))
		if err != nil {
			return nil, nil
		}
		var typeIDs []string
		for _, id := range g.idsNamed(typeName) {
			if isTypeKind(g.symbols[id].Kind) {
				typeIDs = append(typeIDs, id)
			}
		}
		if len(typeIDs) == 0 {
			// pkg.Var / module.CONST: a package or module qualifier.
			var decls []core.SymbolRecord
			for _, id := range g.idsNamed(member) {
				s := g.symbols[id]
				if !dataMemberKinds[s.Kind] || s.ParentSymbol != "" || !fileOK(s) {
					continue
				}
				dir := dirOf(s.FilePath)
				stem := strings.TrimSuffix(path.Base(s.FilePath), path.Ext(s.FilePath))
				if path.Base(dir) == typeName || stem == typeName {
					decls = append(decls, s)
				}
			}
			if len(decls) == 0 {
				return nil, nil
			}
			return g.ownerlessAnchorLocked(q, member, decls)
		}
		if len(g.containedMethods(typeIDs, member)) > 0 {
			return nil, nil // a method of this name: the ordinary path answers
		}
		decls := g.containedDataMembers(typeIDs, member, fileOK)
		if len(decls) == 0 {
			// Inherited: Sub.f where f is declared on a supertype.
			decls = g.inheritedDataMembers(typeIDs, member, fileOK)
		}
		if len(decls) == 0 {
			// Dynamic languages declare instance attributes by assigning
			// them (`self.config = ...` in __init__): no symbol exists, but
			// the attribute is as real as a declared field.
			if decl, ownerID, ok := g.assignedAttributeLocked(typeIDs, member); ok {
				a := g.ownedAnchorLocked(typeName+"."+member, member, []core.SymbolRecord{decl})
				g.addOwnerClosureLocked(a, []string{ownerID})
				return a, nil
			}
			return nil, nil
		}
		return g.ownedAnchorLocked(typeName+"."+member, member, decls), nil
	}

	// Bare name. A method or function of that name keeps priority.
	for _, id := range g.idsNamed(q) {
		s := g.symbols[id]
		if fileOK(s) && (isCallableKind(s.Kind) || mainframeAnchorKind(s.Kind)) {
			return nil, nil
		}
	}
	var moduleLevel, owned []core.SymbolRecord
	for _, id := range g.idsNamed(q) {
		s := g.symbols[id]
		if !dataMemberKinds[s.Kind] || !fileOK(s) {
			continue
		}
		if s.ParentSymbol == "" {
			moduleLevel = append(moduleLevel, s)
		} else {
			owned = append(owned, s)
		}
	}
	if len(moduleLevel) > 0 {
		return g.ownerlessAnchorLocked(q, q, moduleLevel)
	}
	if len(owned) == 0 {
		return nil, nil
	}
	byOwner := map[string][]core.SymbolRecord{}
	for _, s := range owned {
		byOwner[s.ParentSymbol] = append(byOwner[s.ParentSymbol], s)
	}
	if len(byOwner) > 1 {
		var order []string
		cands := map[string][]*core.SymbolRecord{}
		for owner, ss := range byOwner {
			key := owner + "." + q
			order = append(order, key)
			for i := range ss {
				cands[key] = append(cands[key], &ss[i])
			}
		}
		sort.Strings(order)
		return nil, fmt.Errorf("change-impact: %q is ambiguous — %d candidates:%s\nre-run with one of these",
			query, len(order), formatCandidates(order, cands))
	}
	for owner := range byOwner {
		return g.ownedAnchorLocked(owner+"."+q, q, owned), nil
	}
	return nil, nil
}

func (g *CodeGraph) containedDataMembers(typeIDs []string, member string, fileOK func(core.SymbolRecord) bool) []core.SymbolRecord {
	var out []core.SymbolRecord
	seen := map[string]bool{}
	for _, tid := range typeIDs {
		for _, ei := range g.outbound[tid] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeContains {
				continue
			}
			s, ok := g.symbols[edge.To]
			if !ok || s.Name != member || !dataMemberKinds[s.Kind] || seen[s.ID] {
				continue
			}
			if !fileOK(s) && !fileOK(g.symbols[tid]) {
				continue
			}
			seen[s.ID] = true
			out = append(out, s)
		}
	}
	sortSymbols(out)
	return out
}

// inheritedDataMembers walks supertypes (extends/implements, Go embedding)
// breadth-first and returns the nearest declaration of member.
func (g *CodeGraph) inheritedDataMembers(typeIDs []string, member string, fileOK func(core.SymbolRecord) bool) []core.SymbolRecord {
	seen := map[string]bool{}
	frontier := append([]string(nil), typeIDs...)
	for _, id := range frontier {
		seen[id] = true
	}
	for depth := 0; depth < 8 && len(frontier) > 0; depth++ {
		var next []string
		for _, id := range frontier {
			for _, ei := range g.outbound[id] {
				e := g.edges[ei]
				if (e.Type == core.EdgeExtends || e.Type == core.EdgeImplements) && !seen[e.To] {
					seen[e.To] = true
					next = append(next, e.To)
				}
			}
		}
		if found := g.containedDataMembers(next, member, func(core.SymbolRecord) bool { return true }); len(found) > 0 {
			return found
		}
		frontier = next
	}
	return nil
}

func (g *CodeGraph) ownedAnchorLocked(query, member string, decls []core.SymbolRecord) *memberAnchor {
	a := &memberAnchor{query: query, name: member, decls: decls, owners: map[string]bool{}}
	var ownerIDs []string
	for _, d := range decls {
		for _, ei := range g.inbound[d.ID] {
			if e := g.edges[ei]; e.Type == core.EdgeContains && isTypeKind(g.symbols[e.From].Kind) {
				ownerIDs = append(ownerIDs, e.From)
			}
		}
		if d.ParentSymbol != "" {
			a.owners[d.ParentSymbol] = true
		}
	}
	g.addOwnerClosureLocked(a, ownerIDs)
	return a
}

// addOwnerClosureLocked adds the owner types and their whole subtype
// closure to a.owners: a subtype inherits the member, so receivers typed as
// any subtype (and Go structs embedding the owner) reach it.
func (g *CodeGraph) addOwnerClosureLocked(a *memberAnchor, ownerIDs []string) {
	seen := map[string]bool{}
	frontier := ownerIDs
	for _, id := range frontier {
		seen[id] = true
	}
	for len(frontier) > 0 {
		var next []string
		for _, id := range frontier {
			if s, ok := g.symbols[id]; ok {
				a.owners[s.Name] = true
			}
			for _, ei := range g.inbound[id] {
				e := g.edges[ei]
				if (e.Type == core.EdgeExtends || e.Type == core.EdgeImplements) && !seen[e.From] {
					seen[e.From] = true
					next = append(next, e.From)
				}
			}
		}
		frontier = next
	}
}

// assignedAttributeLocked finds the first `self.member = ...` (Python) or
// `this.member = ...` (JS/TS) assignment inside a type's methods and
// synthesizes a field declaration for it at that line.
func (g *CodeGraph) assignedAttributeLocked(typeIDs []string, member string) (core.SymbolRecord, string, bool) {
	re := regexp.MustCompile(`(?m)^(.*\b(?:self|this)\.` + regexp.QuoteMeta(member) + `\s*(?::[^=\n]+)?=[^=].*)$`)
	type hit struct {
		sym  core.SymbolRecord
		line int
		text string
		tid  string
	}
	var best *hit
	for _, tid := range typeIDs {
		t := g.symbols[tid]
		switch t.Language {
		case "python", "javascript", "typescript", "tsx":
		default:
			continue
		}
		for _, ei := range g.outbound[tid] {
			e := g.edges[ei]
			if e.Type != core.EdgeContains {
				continue
			}
			m, ok := g.symbols[e.To]
			if !ok || !isCallableKind(m.Kind) || m.RawText == "" {
				continue
			}
			lines := strings.Split(m.RawText, "\n")
			for i, ln := range lines {
				if !re.MatchString(ln) {
					continue
				}
				h := &hit{sym: m, line: m.Span.Start + i, text: strings.TrimSpace(ln), tid: tid}
				// Prefer the constructor (__init__/constructor), then the
				// earliest file:line — deterministic whatever the map order.
				if best == nil || betterAttrHit(h.sym, h.line, best.sym, best.line) {
					best = h
				}
				break
			}
		}
	}
	if best == nil {
		return core.SymbolRecord{}, "", false
	}
	t := g.symbols[best.tid]
	return core.SymbolRecord{
		ID: best.tid + "#" + member, FilePath: best.sym.FilePath, Language: best.sym.Language,
		Kind: core.KindField, Name: member, QualifiedName: t.Name + "." + member, ParentSymbol: t.Name,
		Signature: best.text, Span: core.LineRange{Start: best.line, End: best.line},
	}, best.tid, true
}

func isCtorSymbol(s core.SymbolRecord) bool {
	return s.Name == "__init__" || s.Name == "constructor" || s.Kind == core.KindConstructor
}

func betterAttrHit(a core.SymbolRecord, aLine int, b core.SymbolRecord, bLine int) bool {
	if isCtorSymbol(a) != isCtorSymbol(b) {
		return isCtorSymbol(a)
	}
	if a.FilePath != b.FilePath {
		return a.FilePath < b.FilePath
	}
	return aLine < bLine
}

func (g *CodeGraph) ownerlessAnchorLocked(query, member string, decls []core.SymbolRecord) (*memberAnchor, error) {
	// One scope only: a Go package, or a single file elsewhere. Distinct
	// scopes are distinct variables — merging them would report one
	// variable's readers as the other's.
	scopes := map[string][]core.SymbolRecord{}
	for _, d := range decls {
		key := d.FilePath
		if d.Language == "go" {
			key = dirOf(d.FilePath) + "/"
		}
		scopes[key] = append(scopes[key], d)
	}
	if len(scopes) > 1 {
		var order []string
		cands := map[string][]*core.SymbolRecord{}
		for key, ss := range scopes {
			label := member + " in " + ss[0].FilePath
			order = append(order, label)
			for i := range ss {
				cands[label] = append(cands[label], &ss[i])
			}
			_ = key
		}
		sort.Strings(order)
		return nil, fmt.Errorf("change-impact: %q is ambiguous — %d candidates:%s\nre-run with file=<path fragment> naming one declaration",
			query, len(order), formatCandidates(order, cands))
	}
	sortSymbols(decls)
	return &memberAnchor{query: member, name: member, decls: decls, ownerless: true}, nil
}

// memberImpactLocked builds the change-set for a data-member anchor.
func (g *CodeGraph) memberImpactLocked(a *memberAnchor) *ChangeImpactResult {
	r := &ChangeImpactResult{
		Query:        a.query,
		Declarations: a.decls,
		Completeness: "member-accesses",
		MemberKind:   string(a.decls[0].Kind),
	}
	declIDs := map[string]bool{}
	for _, d := range a.decls {
		declIDs[d.ID] = true
	}
	// Inbound call-graph references (framework template bindings, ORM
	// attribute references) stay part of the answer.
	seen := map[string]bool{}
	for id := range declIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeCalls || declIDs[edge.From] || seen[edge.From] {
				continue
			}
			seen[edge.From] = true
			if edge.Source == core.EvidenceSourceHeuristic || edge.Source == core.EvidenceSourceRegex {
				r.HasHeuristicRefs = true
			}
			if s, ok := g.symbols[edge.From]; ok {
				r.Callers = append(r.Callers, s)
			}
		}
	}
	if g.memberScanner == nil {
		r.AccessCoverage = "declaration-only"
		r.AccessNote = "no source scanner is attached to this graph, so field/variable reads and writes were not searched; only the declaration and call-graph references are listed"
		sortSymbols(r.Callers)
		return r
	}
	occs, skipped := g.memberScanner(a.name, memberLanguages(a.decls[0].Language))
	c := newMemberClassifier(g, a)
	c.classify(occs)
	r.Accesses, r.AmbiguousAccesses, r.ExcludedAccesses = c.confirmed, c.ambiguous, c.excluded
	for _, acc := range r.Accesses {
		if acc.Access == "decl" || acc.Enclosing.ID == "" || declIDs[acc.Enclosing.ID] || seen[acc.Enclosing.ID] {
			continue
		}
		if isTypeKind(acc.Enclosing.Kind) {
			continue
		}
		seen[acc.Enclosing.ID] = true
		r.Callers = append(r.Callers, acc.Enclosing)
	}
	sortSymbols(r.Callers)
	switch {
	case len(r.AmbiguousAccesses) == 0 && skipped == 0:
		r.AccessCoverage = "receiver-typed"
	case len(r.Accesses) <= len(a.decls) && len(r.AmbiguousAccesses) > 0:
		r.AccessCoverage = "name-matched"
	default:
		r.AccessCoverage = "partial"
	}
	var note strings.Builder
	fmt.Fprintf(&note, "%d access line(s) confirmed by receiver-type or declaring-scope evidence", len(r.Accesses))
	if n := len(r.AmbiguousAccesses); n > 0 {
		fmt.Fprintf(&note, "; %d more match the name but their receiver could not be typed — they are listed separately as ambiguous and each needs a check", n)
	}
	if r.ExcludedAccesses > 0 {
		fmt.Fprintf(&note, "; %d same-named occurrence(s) excluded (receiver typed as another declarer, shadowed by a local, or out of scope)", r.ExcludedAccesses)
	}
	if skipped > 0 {
		fmt.Fprintf(&note, "; %d file(s) could not be scanned", skipped)
	}
	note.WriteString(". Reflection, string-keyed access (getattr, JSON/ORM tags, templates) and generated code are not tracked.")
	r.AccessNote = note.String()
	return r
}

// memberLanguages mirrors parser.MemberLanguages (kept local: graph must not
// import parser).
func memberLanguages(lang string) []string {
	switch lang {
	case "c", "cpp", "objc":
		return []string{"c", "cpp", "objc"}
	case "java", "kotlin":
		return []string{"java", "kotlin"}
	case "typescript", "tsx", "javascript":
		return []string{"typescript", "tsx", "javascript"}
	}
	return []string{lang}
}
