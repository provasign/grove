package graph

import (
	"fmt"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// ChangeImpactResult is the full, deterministic change-set for a method
// signature change: the declaration, every override/implementation in the
// subtype closure, and every method with a resolved call edge into any of
// them. This is the task-shaped answer to "what must change if X changes" —
// computed in the engine so no agent has to orchestrate the traversal over
// primitives (references → overrides → callers → dedup).
type ChangeImpactResult struct {
	Query        string              // the query as given
	Declarations []core.SymbolRecord // resolved declaration(s) on the named type
	Supers       []core.SymbolRecord // same-signature declarations up the hierarchy (informational: changing a mid-hierarchy override usually forces these too)
	Family       []core.SymbolRecord // overrides/implementations in the subtype closure (excluding Declarations)
	Callers      []core.SymbolRecord // methods with call edges into Declarations or Family (excluding both)
}

// Sites returns every symbol in the change-set (declarations, family,
// callers) as one deduplicated, file-ordered list.
func (r *ChangeImpactResult) Sites() []core.SymbolRecord {
	seen := make(map[string]bool)
	var out []core.SymbolRecord
	for _, group := range [][]core.SymbolRecord{r.Declarations, r.Family, r.Callers} {
		for _, s := range group {
			if !seen[s.ID] {
				seen[s.ID] = true
				out = append(out, s)
			}
		}
	}
	sortSymbols(out)
	return out
}

// ChangeImpact resolves a "Type.method" or "Type.method(ParamType, ...)"
// query to the exact change-set for that method's signature. Unlike Impact
// (name-substring seeded, type-erased BFS), seeding here is type-resolved:
// the named type's declaration is found via contains edges, the family via
// the extends/implements subtype closure filtered by signature
// compatibility, and callers via inbound call edges to those exact symbol
// IDs — never to same-named methods on unrelated types.
func (g *CodeGraph) ChangeImpact(query string) (*ChangeImpactResult, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	typeName, methodName, queryParams, err := parseChangeImpactQuery(query)
	if err != nil {
		return nil, err
	}

	// 1. The named type(s). Same-named types in distinct files all seed —
	// the caller disambiguates by file path in the result.
	var typeIDs []string
	for id, s := range g.symbols {
		if s.Name != typeName {
			continue
		}
		switch s.Kind {
		case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
			typeIDs = append(typeIDs, id)
		}
	}
	if len(typeIDs) == 0 {
		return nil, fmt.Errorf("change-impact: no type named %q in the graph", typeName)
	}

	// 2. Declaration(s): methods named methodName contained in the named type,
	// filtered by the query's parameter list when one was given.
	decls := g.containedMethods(typeIDs, methodName)
	if len(queryParams) > 0 {
		if byParams := filterByParamTypes(decls, queryParams); len(byParams) > 0 {
			decls = byParams
		} else {
			return nil, fmt.Errorf("change-impact: %s.%s declares no overload matching (%s)",
				typeName, methodName, strings.Join(queryParams, ", "))
		}
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("change-impact: type %q declares no method %q", typeName, methodName)
	}

	// The declaration's signature anchors family compatibility. Type
	// parameters of the declaring type (and single-letter placeholders) are
	// wildcards: an override binds them to concrete types.
	declParams := paramTypesOf(&decls[0])
	wildcards := typeParamWildcards(g.symbols, &decls[0])

	// 3. Subtype closure (downward): inbound extends/implements edges.
	closure := make(map[string]bool, len(typeIDs))
	frontier := make([]string, 0, len(typeIDs))
	for _, id := range typeIDs {
		closure[id] = true
		frontier = append(frontier, id)
	}
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		for _, ei := range g.inbound[node] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeExtends && edge.Type != core.EdgeImplements {
				continue
			}
			if !closure[edge.From] {
				closure[edge.From] = true
				frontier = append(frontier, edge.From)
			}
		}
	}

	// 4. Family: same-named, signature-compatible methods on closure types.
	declIDs := make(map[string]bool, len(decls))
	for _, d := range decls {
		declIDs[d.ID] = true
	}
	closureIDs := make([]string, 0, len(closure))
	for id := range closure {
		closureIDs = append(closureIDs, id)
	}
	var family []core.SymbolRecord
	for _, m := range g.containedMethods(closureIDs, methodName) {
		if declIDs[m.ID] {
			continue
		}
		if signatureCompatible(declParams, paramTypesOf(&m), wildcards) {
			family = append(family, m)
		}
	}

	// 5. Supers (upward, informational): same-signature declarations on
	// supertypes — changing a mid-hierarchy override usually forces these.
	superTypes := make(map[string]bool)
	frontier = append(frontier[:0], typeIDs...)
	visitedUp := make(map[string]bool, len(typeIDs))
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		if visitedUp[node] {
			continue
		}
		visitedUp[node] = true
		for _, ei := range g.outbound[node] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeExtends && edge.Type != core.EdgeImplements {
				continue
			}
			if !visitedUp[edge.To] {
				superTypes[edge.To] = true
				frontier = append(frontier, edge.To)
			}
		}
	}
	var supers []core.SymbolRecord
	if len(superTypes) > 0 {
		superIDs := make([]string, 0, len(superTypes))
		for id := range superTypes {
			superIDs = append(superIDs, id)
		}
		for _, m := range g.containedMethods(superIDs, methodName) {
			if !declIDs[m.ID] && signatureCompatible(declParams, paramTypesOf(&m), wildcards) {
				supers = append(supers, m)
			}
		}
	}

	// 6. Callers: inbound call edges to any declaration or family member,
	// resolved by symbol ID — never by name.
	memberIDs := make(map[string]bool, len(decls)+len(family))
	for id := range declIDs {
		memberIDs[id] = true
	}
	for _, m := range family {
		memberIDs[m.ID] = true
	}
	callerSeen := make(map[string]bool)
	var callers []core.SymbolRecord
	for id := range memberIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeCalls {
				continue
			}
			if memberIDs[edge.From] || callerSeen[edge.From] {
				continue
			}
			callerSeen[edge.From] = true
			if s, ok := g.symbols[edge.From]; ok {
				callers = append(callers, s)
			}
		}
	}

	sortSymbols(decls)
	sortSymbols(family)
	sortSymbols(supers)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:        query,
		Declarations: decls,
		Supers:       supers,
		Family:       family,
		Callers:      callers,
	}, nil
}

// containedMethods returns methods/functions named methodName reached from
// the given type symbol IDs via contains edges.
func (g *CodeGraph) containedMethods(typeIDs []string, methodName string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for _, tid := range typeIDs {
		for _, ei := range g.outbound[tid] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeContains {
				continue
			}
			s, ok := g.symbols[edge.To]
			if !ok || s.Name != methodName {
				continue
			}
			if s.Kind == core.KindMethod || s.Kind == core.KindFunction {
				out = append(out, s)
			}
		}
	}
	return out
}

// parseChangeImpactQuery splits "Type.method" / "Type.method(A, B)" /
// "Outer.Inner.method(...)" into the type's simple name, the method name, and
// optional bare parameter types.
func parseChangeImpactQuery(query string) (typeName, methodName string, params []string, err error) {
	q := strings.TrimSpace(query)
	if i := strings.IndexByte(q, '('); i >= 0 {
		if !strings.HasSuffix(q, ")") {
			return "", "", nil, fmt.Errorf("change-impact: unbalanced parameter list in %q", query)
		}
		inner := strings.TrimSpace(q[i+1 : len(q)-1])
		if inner != "" {
			for _, p := range splitTopLevel(inner, ',') {
				params = append(params, bareTypeToken(p))
			}
		}
		q = q[:i]
	}
	dot := strings.LastIndexByte(q, '.')
	if dot <= 0 || dot == len(q)-1 {
		return "", "", nil, fmt.Errorf("change-impact: query must be Type.method or Type.method(Params), got %q", query)
	}
	methodName = q[dot+1:]
	typeName = q[:dot]
	// Nested types keep only the innermost segment: symbols are indexed by
	// simple name, and contains edges do the precise scoping.
	if j := strings.LastIndexByte(typeName, '.'); j >= 0 {
		typeName = typeName[j+1:]
	}
	return typeName, methodName, params, nil
}

// paramTypesOf returns the bare parameter types of a method, or nil when the
// signature is unparseable (neutral evidence).
func paramTypesOf(s *core.SymbolRecord) []string {
	if s.Language == "java" {
		return javaParamTypes(s)
	}
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = s.RawText
	}
	inner := tsDeclParams(src)
	if inner == "" {
		return nil
	}
	var out []string
	for _, gr := range splitTopLevel(inner, ',') {
		out = append(out, bareTypeToken(gr))
	}
	return out
}

// bareTypeToken reduces a parameter fragment to a bare comparable type token:
// last dotted segment, generics stripped, arrays kept.
func bareTypeToken(p string) string {
	p = strings.TrimSpace(p)
	// "final @Nullable CharSequence seq" → take the type-looking field.
	fields := strings.Fields(p)
	for len(fields) > 1 && (fields[0] == "final" || strings.HasPrefix(fields[0], "@")) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return ""
	}
	t := fields[0]
	if i := strings.IndexByte(t, '<'); i > 0 && strings.Contains(t, ">") {
		t = t[:i] + t[strings.LastIndexByte(t, '>')+1:]
	}
	t = strings.ReplaceAll(t, "...", "[]")
	arr := strings.HasSuffix(t, "[]")
	t = strings.TrimSuffix(t, "[]")
	if j := strings.LastIndexByte(t, '.'); j >= 0 {
		t = t[j+1:]
	}
	if arr {
		t += "[]"
	}
	return t
}

// typeParamWildcards collects the declaration's generic placeholders (its own
// and its declaring type's type parameters) — parameter positions holding one
// of these match any concrete type in an override.
func typeParamWildcards(symbols map[string]core.SymbolRecord, decl *core.SymbolRecord) map[string]bool {
	wild := make(map[string]bool)
	add := func(tps []string) {
		for _, tp := range tps {
			// "T extends Foo" → "T"
			name := strings.Fields(strings.TrimSpace(tp))
			if len(name) > 0 {
				wild[name[0]] = true
			}
		}
	}
	add(decl.TypeParameters)
	if decl.ParentSymbol != "" {
		for _, s := range symbols {
			if s.Name == decl.ParentSymbol && s.FilePath == decl.FilePath {
				add(s.TypeParameters)
				break
			}
		}
	}
	return wild
}

// signatureCompatible reports whether a candidate override's parameter list
// can implement the declaration's: same arity, each position equal — where a
// wildcard (type-parameter) position on the declaration side matches
// anything, and an unparseable side is neutral (kept).
func signatureCompatible(declParams, candParams []string, wildcards map[string]bool) bool {
	if declParams == nil || candParams == nil {
		return true // neutral: absence of evidence never excludes
	}
	if len(declParams) != len(candParams) {
		return false
	}
	for i := range declParams {
		if declParams[i] == candParams[i] {
			continue
		}
		if wildcards[declParams[i]] {
			continue
		}
		// Single-uppercase-letter tokens are conventionally type variables
		// even when the declaring type's parameter list wasn't captured.
		if len(declParams[i]) == 1 && declParams[i][0] >= 'A' && declParams[i][0] <= 'Z' {
			continue
		}
		return false
	}
	return true
}

// filterByParamTypes keeps candidates whose parameter types equal the given
// list exactly (bare-token comparison).
func filterByParamTypes(cands []core.SymbolRecord, params []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for _, c := range cands {
		got := paramTypesOf(&c)
		if len(got) != len(params) {
			continue
		}
		match := true
		for i := range params {
			if !strings.EqualFold(got[i], params[i]) {
				match = false
				break
			}
		}
		if match {
			out = append(out, c)
		}
	}
	return out
}
