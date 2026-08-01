package graph

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
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
	Supers       []core.SymbolRecord // same-member declarations on other contracts (supertypes of the seed OR of any family member — a sibling interface satisfied by the same implementations breaks under the change exactly like the seed contract)
	Family       []core.SymbolRecord // overrides/implementations in the subtype closure (excluding Declarations)
	Callers      []core.SymbolRecord // methods with call edges into Declarations or Family (excluding both)

	// DeclaringTypes: type declarations whose bodies contain a change-set
	// member signature that is not indexed as its own symbol (Go and TS
	// interface members). For those declarations the innermost enclosing
	// symbol in the file IS the type, so the type's declaration block is
	// the change site a diff, a scorer, or a reviewer names. Empty for
	// languages whose member declarations are real symbols (Java, Python).
	DeclaringTypes []core.SymbolRecord

	// ExternalSupers lists supertype names declared in the hierarchy's
	// extends/implements clauses that resolve to no indexed type (JDK or
	// dependency types). Informational: the clause is in project source even
	// when the type is not.
	ExternalSupers []string
	// OverridesExternal is non-empty when the queried method is a member of
	// an external supertype's contract ("java.util.Iterator#next"): changing
	// its signature breaks that contract, and the change-set below is the
	// project-local dispatch closure, not a complete must-change set (calls
	// through receivers typed as the external supertype are not indexed).
	OverridesExternal []string
	// Completeness is "closed" when the override family is fully rooted in
	// indexed types, "project-local" when it is bounded by an external
	// contract (OverridesExternal non-empty, or the query named an external
	// type directly).
	Completeness string
}

// Sites returns every METHOD in the change-set — declarations, family,
// callers, and supers — as one deduplicated, file-ordered list. Supers are
// included because a same-member declaration on another contract (a sibling
// interface satisfied by the same implementations, or a supertype when the
// query seeded on an implementation) must change exactly like the seed:
// omitting it silently dropped the interface's own member from rename-plan
// and undercounted untested-surface. DeclaringTypes is deliberately NOT
// unioned here — those are TYPE declarations, not methods; rename-plan
// consumes them separately (they are edit sites) and untested-surface must
// not (a type declaration has no test).
func (r *ChangeImpactResult) Sites() []core.SymbolRecord {
	seen := make(map[string]bool)
	var out []core.SymbolRecord
	for _, group := range [][]core.SymbolRecord{r.Declarations, r.Family, r.Callers, r.Supers} {
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

	// Accept a bare member name or file:line and pin it to the canonical
	// Type.method form (unambiguous only — see resolveLooseQueryLocked).
	// Already-canonical queries pass through untouched.
	resolved, err := g.resolveLooseQueryLocked(query)
	if err != nil {
		return nil, err
	}
	query = resolved

	// A package-level function has no declaring type, so there is no override
	// family to close over and the Type.method parser below cannot represent
	// it. Its blast radius is exactly its callers — reported as such, with
	// completeness downgraded so no consumer mistakes it for a closed set.
	if free := g.freeFunctionImpactLocked(query); free != nil {
		return free, nil
	}

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
		// External-rooted query: the named type is not in the index (a JDK or
		// dependency type, e.g. "java.util.Iterator.next"). The well-posed
		// project question is the implementation closure: every indexed type
		// whose declared extends/implements clause names it, transitively.
		return g.externalRootedImpact(query, typeName, methodName, queryParams)
	}

	// 2. Declaration(s): methods named methodName contained in the named type,
	// filtered by the query's parameter list when one was given.
	decls := g.containedMethods(typeIDs, methodName)
	if len(queryParams) > 0 && len(decls) > 0 {
		if byParams := filterByParamTypes(decls, queryParams); len(byParams) > 0 {
			decls = byParams
		} else {
			return nil, fmt.Errorf("change-impact: %s.%s declares no overload matching (%s)",
				typeName, methodName, strings.Join(queryParams, ", "))
		}
	}
	// decls may be empty even though the type is indexed: TS and Go interface
	// member signatures are not parsed as symbols. Proceed — the subtype
	// closure below still yields the implementation family, which roots the
	// change-set (validated after the closure walk).

	// The declaration's signature anchors family compatibility. Type
	// parameters of the declaring type (and single-letter placeholders) are
	// wildcards: an override binds them to concrete types. With no indexed
	// declaration, the query's own parameter list (possibly nil) anchors.
	declParams := queryParams
	wildcards := map[string]bool{}
	if len(decls) > 0 {
		declParams = paramTypesOf(&decls[0])
		wildcards = typeParamWildcards(g.symbols, &decls[0])
	}

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
	declaringTypes := map[string]core.SymbolRecord{}
	if len(decls) == 0 {
		// The member exists in source but not as a symbol (TS interface
		// members, Go interface specs). When the seed type's body declares
		// it, synthesize the declaration record: the declaring file is part
		// of the change-set — and surface the TYPE too, because for an
		// unindexed member spec the innermost enclosing symbol in the file
		// is the type declaration itself (what a diff or reviewer names).
		// This MUST run before the empty-set gate below: an interface method
		// with zero current implementors (family empty) is a completely
		// ordinary "what breaks if I change this" query, and synthesis is
		// what recognizes it — gating first threw a false "declares no
		// method" error for exactly that case.
		for _, tid := range typeIDs {
			t, ok := g.symbols[tid]
			if !ok || !typeDeclaresMember(&t, methodName) {
				continue
			}
			decls = append(decls, synthesizeMemberDecl(&t, methodName))
			declaringTypes[t.ID] = t
		}
	}
	if len(decls) == 0 && len(family) == 0 {
		return nil, fmt.Errorf("change-impact: type %q declares no method %q and no subtype implements it", typeName, methodName)
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
	// 5b. Contract candidates reachable from the WHOLE closure, not just the
	// seed: a family member may also satisfy other contracts (Go structural
	// satisfaction, Java/TS multi-implements). A sibling interface that
	// declares the same member and is satisfied by the same implementations
	// breaks under the signature change exactly like the seed contract — but
	// an upward walk rooted only at the seed never reaches it (the grafana
	// routeService miss). Candidates carry the same evidence the family walk
	// uses: an extends/implements edge from a closure member plus the member
	// declared on the type.
	contractTypes := make(map[string]bool, len(superTypes))
	for id := range superTypes {
		contractTypes[id] = true
	}
	for id := range closure {
		for _, ei := range g.outbound[id] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeExtends && edge.Type != core.EdgeImplements {
				continue
			}
			if !closure[edge.To] {
				contractTypes[edge.To] = true
			}
		}
	}
	var supers []core.SymbolRecord
	superSeen := make(map[string]bool)
	if len(contractTypes) > 0 {
		contractIDs := make([]string, 0, len(contractTypes))
		for id := range contractTypes {
			contractIDs = append(contractIDs, id)
		}
		for _, m := range g.containedMethods(contractIDs, methodName) {
			if !declIDs[m.ID] && !superSeen[m.ID] && signatureCompatible(declParams, paramTypesOf(&m), wildcards) {
				superSeen[m.ID] = true
				supers = append(supers, m)
			}
		}
		// Members declared in the type body but not indexed as symbols
		// (Go/TS interface specs): synthesize the declaration and surface
		// the declaring type, as in step 4. Types that already contributed
		// a real member symbol are skipped — nothing to synthesize.
		for _, tid := range contractIDs {
			t, ok := g.symbols[tid]
			if !ok || !typeDeclaresMember(&t, methodName) {
				continue
			}
			if _, dup := declaringTypes[t.ID]; dup {
				continue
			}
			if len(g.containedMethods([]string{tid}, methodName)) > 0 {
				continue
			}
			if syn := synthesizeMemberDecl(&t, methodName); !superSeen[syn.ID] && !declIDs[syn.ID] {
				superSeen[syn.ID] = true
				supers = append(supers, syn)
				declaringTypes[t.ID] = t
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

	// 7. Contract boundary: supertype names declared anywhere in the upward
	// closure (seed types + indexed supertypes) that resolve to no indexed
	// type are external. If the queried method is a member of a well-known
	// external contract, the result is the project-local closure of a
	// contract the project does not own — flag it. Deliberately fed by the
	// SEED's hierarchy only (not the sibling contracts of step 5b), so the
	// completeness semantics of the queried contract are unchanged.
	hierarchyIDs := make([]string, 0, len(typeIDs)+len(superTypes))
	hierarchyIDs = append(hierarchyIDs, typeIDs...)
	for id := range superTypes {
		hierarchyIDs = append(hierarchyIDs, id)
	}
	externalSupers, overridesExternal := g.externalContract(hierarchyIDs, methodName)
	completeness := "closed"
	if len(overridesExternal) > 0 {
		completeness = "project-local"
	}

	declTypes := make([]core.SymbolRecord, 0, len(declaringTypes))
	for _, t := range declaringTypes {
		declTypes = append(declTypes, t)
	}
	sortSymbols(decls)
	sortSymbols(family)
	sortSymbols(supers)
	sortSymbols(callers)
	sortSymbols(declTypes)
	return &ChangeImpactResult{
		Query:             query,
		Declarations:      decls,
		Supers:            supers,
		Family:            family,
		Callers:           callers,
		DeclaringTypes:    declTypes,
		ExternalSupers:    externalSupers,
		OverridesExternal: overridesExternal,
		Completeness:      completeness,
	}, nil
}

// synthesizeMemberDecl builds the declaration record for a member that
// exists in a type's source body but not as an indexed symbol (Go interface
// specs, TS interface members). The record carries the declaring file so the
// change-set names it even without a real symbol.
func synthesizeMemberDecl(t *core.SymbolRecord, methodName string) core.SymbolRecord {
	return core.SymbolRecord{
		ID:            t.ID + "#" + methodName,
		FilePath:      t.FilePath,
		Language:      t.Language,
		Kind:          core.KindMethod,
		Name:          methodName,
		QualifiedName: t.Name + "." + methodName,
		ParentSymbol:  t.Name,
		Span:          t.Span,
	}
}

// externalContract inspects the extends/implements clauses of the given type
// symbols: names that resolve to no indexed type are external supertypes, and
// when methodName is a member of a well-known external contract (jdkContract)
// the override is flagged as "Type#method". Caller must hold g.mu.
func (g *CodeGraph) externalContract(typeIDs []string, methodName string) (externalSupers, overridesExternal []string) {
	seenSuper := make(map[string]bool)
	seenOverride := make(map[string]bool)
	for _, tid := range typeIDs {
		t, ok := g.symbols[tid]
		if !ok {
			continue
		}
		for _, name := range declaredSuperNames(&t) {
			simple := name
			if j := strings.LastIndexByte(simple, '.'); j >= 0 {
				simple = simple[j+1:]
			}
			if g.hasIndexedType(simple) {
				continue
			}
			if !seenSuper[name] {
				seenSuper[name] = true
				externalSupers = append(externalSupers, name)
			}
			if members, known := jdkContract[simple]; known && members[methodName] {
				key := name + "#" + methodName
				if !seenOverride[key] {
					seenOverride[key] = true
					overridesExternal = append(overridesExternal, key)
				}
			}
		}
	}
	return externalSupers, overridesExternal
}

// externalRootedImpact answers a change-impact query whose type is not in the
// index: the project-local implementation closure of an external member —
// every indexed type declaring the external name in its extends/implements
// clause, its subtype closure, their matching methods, and their callers.
// This is the deprecation/migration question ("what implements Iterator
// here?"); a signature change to the external member itself is out of the
// project's hands, so Completeness is always "project-local". Caller must
// hold g.mu.
func (g *CodeGraph) externalRootedImpact(query, typeName, methodName string, queryParams []string) (*ChangeImpactResult, error) {
	var seedIDs []string
	for id, s := range g.symbols {
		switch s.Kind {
		case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
		default:
			continue
		}
		for _, name := range declaredSuperNames(&s) {
			simple := name
			if j := strings.LastIndexByte(simple, '.'); j >= 0 {
				simple = simple[j+1:]
			}
			if simple == typeName {
				seedIDs = append(seedIDs, id)
				break
			}
		}
	}
	if len(seedIDs) == 0 {
		return nil, fmt.Errorf("change-impact: no type named %q in the graph and no indexed type declares it as a supertype", typeName)
	}

	// Subtype closure below the seeds (the seeds themselves implement the
	// external contract, so they are family roots, not declarations).
	closure := make(map[string]bool, len(seedIDs))
	frontier := append([]string(nil), seedIDs...)
	for _, id := range seedIDs {
		closure[id] = true
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
	closureIDs := make([]string, 0, len(closure))
	for id := range closure {
		closureIDs = append(closureIDs, id)
	}

	family := g.containedMethods(closureIDs, methodName)
	if len(queryParams) > 0 {
		if byParams := filterByParamTypes(family, queryParams); len(byParams) > 0 {
			family = byParams
		}
	}
	if len(family) == 0 {
		return nil, fmt.Errorf("change-impact: no indexed implementation of %s.%s (external type; %d project types declare it as a supertype)",
			typeName, methodName, len(seedIDs))
	}

	memberIDs := make(map[string]bool, len(family))
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

	sortSymbols(family)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:             query,
		Family:            family,
		Callers:           callers,
		ExternalSupers:    []string{typeName},
		OverridesExternal: []string{typeName + "#" + methodName},
		Completeness:      "project-local",
	}, nil
}

// declaredSuperNames extracts the supertype names written in a type's
// declaration clause — the same sources buildExtendsImplements reads — so
// external names (which produce no edges) are still visible for boundary
// reporting.
func declaredSuperNames(s *core.SymbolRecord) []string {
	switch s.Language {
	case "typescript", "tsx", "javascript", "java":
		if s.Kind != core.KindClass && s.Kind != core.KindInterface {
			return nil
		}
		text := s.Signature
		if text == "" {
			text = firstLine(s.RawText)
		}
		text = stripAngleBrackets(text)
		names := matchNameList(extendsRe, text)
		return append(names, matchNameList(implementsRe, text)...)
	case "python":
		if s.Kind != core.KindClass {
			return nil
		}
		m := pythonClassBase.FindStringSubmatch(firstLine(s.RawText))
		if len(m) < 2 {
			return nil
		}
		var out []string
		for _, base := range splitTrim(m[1], ',') {
			if base = stripPythonBase(base); base != "" {
				out = append(out, base)
			}
		}
		return out
	}
	return nil
}

// hasIndexedType reports whether any indexed symbol of a type kind has the
// given simple name. Caller must hold g.mu.
func (g *CodeGraph) hasIndexedType(name string) bool {
	for _, s := range g.symbols {
		if s.Name != name {
			continue
		}
		switch s.Kind {
		case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
			return true
		}
	}
	return false
}

// jdkContract maps well-known java.lang / java.util / java.util.function /
// java.io interface simple names to their member methods, so an override of
// an external contract can be flagged definitively rather than guessed. The
// table is deliberately small: absence here only means "not flagged", never
// "not external" (ExternalSupers still reports the name).
var jdkContract = map[string]map[string]bool{
	"Iterator":      set("hasNext", "next", "remove", "forEachRemaining"),
	"ListIterator":  set("hasNext", "next", "remove", "forEachRemaining", "hasPrevious", "previous", "nextIndex", "previousIndex", "set", "add"),
	"Iterable":      set("iterator", "forEach", "spliterator"),
	"Enumeration":   set("hasMoreElements", "nextElement", "asIterator"),
	"Collection":    set("size", "isEmpty", "contains", "iterator", "toArray", "add", "remove", "containsAll", "addAll", "removeAll", "retainAll", "clear", "removeIf", "spliterator", "stream", "parallelStream", "forEach"),
	"List":          set("size", "isEmpty", "contains", "iterator", "toArray", "add", "remove", "containsAll", "addAll", "removeAll", "retainAll", "clear", "get", "set", "indexOf", "lastIndexOf", "listIterator", "subList", "replaceAll", "sort"),
	"Set":           set("size", "isEmpty", "contains", "iterator", "toArray", "add", "remove", "containsAll", "addAll", "removeAll", "retainAll", "clear"),
	"Queue":         set("add", "offer", "remove", "poll", "element", "peek"),
	"Deque":         set("addFirst", "addLast", "offerFirst", "offerLast", "removeFirst", "removeLast", "pollFirst", "pollLast", "getFirst", "getLast", "peekFirst", "peekLast", "push", "pop", "descendingIterator"),
	"Map":           set("size", "isEmpty", "containsKey", "containsValue", "get", "put", "remove", "putAll", "clear", "keySet", "values", "entrySet", "getOrDefault", "forEach", "replaceAll", "putIfAbsent", "replace", "computeIfAbsent", "computeIfPresent", "compute", "merge"),
	"SortedMap":     set("comparator", "subMap", "headMap", "tailMap", "firstKey", "lastKey"),
	"NavigableMap":  set("lowerEntry", "lowerKey", "floorEntry", "floorKey", "ceilingEntry", "ceilingKey", "higherEntry", "higherKey", "firstEntry", "lastEntry", "pollFirstEntry", "pollLastEntry", "descendingMap", "navigableKeySet", "descendingKeySet"),
	"SortedSet":     set("comparator", "subSet", "headSet", "tailSet", "first", "last"),
	"NavigableSet":  set("lower", "floor", "ceiling", "higher", "pollFirst", "pollLast", "descendingSet", "descendingIterator"),
	"Entry":         set("getKey", "getValue", "setValue"),
	"Comparable":    set("compareTo"),
	"Comparator":    set("compare", "reversed", "thenComparing"),
	"Runnable":      set("run"),
	"Callable":      set("call"),
	"AutoCloseable": set("close"),
	"Closeable":     set("close"),
	"Flushable":     set("flush"),
	"CharSequence":  set("length", "charAt", "subSequence", "chars", "codePoints"),
	"Appendable":    set("append"),
	"Function":      set("apply", "compose", "andThen"),
	"BiFunction":    set("apply", "andThen"),
	"Supplier":      set("get"),
	"Consumer":      set("accept", "andThen"),
	"BiConsumer":    set("accept", "andThen"),
	"Predicate":     set("test", "and", "or", "negate"),
	"BiPredicate":   set("test", "and", "or", "negate"),
	"UnaryOperator": set("apply"),
	"Spliterator":   set("tryAdvance", "forEachRemaining", "trySplit", "estimateSize", "characteristics", "getComparator"),
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// typeDeclaresMember reports whether a type's body text declares a member
// method of the given name — for languages whose parsers do not emit those
// members as symbols (TS/JS interfaces, Go interface specs).
func typeDeclaresMember(t *core.SymbolRecord, method string) bool {
	if t.RawText == "" {
		return false
	}
	var re *regexp.Regexp
	switch t.Language {
	case "go":
		re = goIfaceMethodRe
	case "typescript", "tsx", "javascript":
		re = tsIfaceMethodRe
	default:
		return false
	}
	body := stripCommentsAndStrings(t.RawText)
	if i := strings.IndexByte(body, '{'); i >= 0 {
		body = body[i+1:]
	}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if m[1] == method {
			return true
		}
	}
	return false
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

// maxLooseCandidates bounds how many candidates a bare-name error lists —
// enough to disambiguate by hand, short enough to stay readable.
const maxLooseCandidates = 10

// resolveLooseQueryLocked turns a caller-friendly query into the canonical
// "Type.method" form the change-set parser requires. The caller must hold at
// least g.mu.RLock.
//
// Rationale: change_impact/rename_plan/missing_implementations demanded
// "Type.method" and rejected everything else, so a perfectly unambiguous
// `change-impact Render` failed outright — a real ergonomic wall (it is what
// broke the gin task in our own benchmark harness).
//
// Deliberately never guesses. A name that resolves to exactly one member
// symbol is pinned silently; anything ambiguous returns the candidates and
// makes the caller choose, because the completeness claim these tools carry
// is worthless if the tool silently answered about a different symbol.
//
// Accepted forms:
//   - "Type.method", "Type.method(P)", "pkg.Type.method" — returned unchanged
//     (fast path: an interior dot means it is already canonical).
//   - "method"      — pinned to "Type.method" when exactly one member matches.
//   - "file.go:120" — the member whose span contains that line.
func (g *CodeGraph) resolveLooseQueryLocked(query string) (string, error) {
	q := strings.TrimSpace(query)
	// A parameter list may itself contain dots ("m(java.util.Map)"), so judge
	// canonical-ness on the part before "(".
	head := q
	if i := strings.IndexByte(head, '('); i >= 0 {
		head = head[:i]
	}
	if dot := strings.LastIndexByte(head, '.'); dot > 0 && dot < len(head)-1 {
		if !looksLikeFileRef(head) {
			return q, nil // already canonical
		}
	}

	// file.go:120 — resolve through the symbol whose span covers the line.
	if path, line, ok := parseFileLineRef(q); ok {
		return g.resolveFileLineLocked(query, path, line)
	}

	if head == "" {
		return "", fmt.Errorf("change-impact: empty query")
	}

	// Bare member name: collect every function/method symbol with that name.
	type cand struct {
		sym *core.SymbolRecord
	}
	var matches []cand
	for i := range g.symbols {
		s := g.symbols[i]
		if s.Name != head {
			continue
		}
		switch s.Kind {
		case core.KindMethod, core.KindFunction, core.KindConstructor:
		default:
			continue
		}
		sym := s
		matches = append(matches, cand{sym: &sym})
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("change-impact: no method or function named %q is indexed%s",
			query, g.didYouMeanLocked(head))
	}

	// Collapse to the distinct canonical queries the matches imply: several
	// files declaring the same Type.method (overrides) are ONE query, and
	// resolving it is exactly what change_impact then expands.
	canonical := map[string][]*core.SymbolRecord{}
	var order []string
	for _, m := range matches {
		c := canonicalQueryFor(m.sym)
		if _, seen := canonical[c]; !seen {
			order = append(order, c)
		}
		canonical[c] = append(canonical[c], m.sym)
	}
	if len(order) == 1 {
		return order[0], nil
	}
	sort.Strings(order)
	return "", fmt.Errorf("change-impact: %q is ambiguous — %d candidates:%s\nre-run with one of these",
		query, len(order), formatCandidates(order, canonical))
}

// freeFunctionImpactLocked returns the callers-only change-set for a
// package-level function (a query with no dot that resolved to symbols
// carrying no ParentSymbol), or nil when the query is not that shape.
//
// Completeness is "callers-only": there is no type hierarchy to close over,
// so the answer is every resolved inbound call edge and nothing more. Callers
// must not read it as the closed guarantee Type.method queries carry.
func (g *CodeGraph) freeFunctionImpactLocked(query string) *ChangeImpactResult {
	if strings.ContainsAny(query, ".(") {
		return nil
	}
	var decls []core.SymbolRecord
	declIDs := map[string]bool{}
	for id := range g.symbols {
		s := g.symbols[id]
		if s.Name != query || s.ParentSymbol != "" {
			continue
		}
		switch s.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
		default:
			continue
		}
		decls = append(decls, s)
		declIDs[s.ID] = true
	}
	if len(decls) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var callers []core.SymbolRecord
	for id := range declIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeCalls || declIDs[edge.From] || seen[edge.From] {
				continue
			}
			seen[edge.From] = true
			if s, ok := g.symbols[edge.From]; ok {
				callers = append(callers, s)
			}
		}
	}
	sortSymbols(decls)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:        query,
		Declarations: decls,
		Callers:      callers,
		Completeness: "callers-only",
	}
}

// canonicalQueryFor renders the canonical query for a symbol: "Type.method"
// for members, and the bare name for a package-level function (which has no
// declaring type — change_impact handles those through its callers-only path).
func canonicalQueryFor(s *core.SymbolRecord) string {
	if s.ParentSymbol != "" {
		return s.ParentSymbol + "." + s.Name
	}
	return s.Name
}

// formatCandidates renders one line per candidate query with a sample location.
func formatCandidates(order []string, byQuery map[string][]*core.SymbolRecord) string {
	var b strings.Builder
	for i, q := range order {
		if i >= maxLooseCandidates {
			fmt.Fprintf(&b, "\n  … and %d more", len(order)-maxLooseCandidates)
			break
		}
		syms := byQuery[q]
		fmt.Fprintf(&b, "\n  %s", q)
		if len(syms) > 0 {
			fmt.Fprintf(&b, "  (%s:%d", syms[0].FilePath, syms[0].Span.Start)
			if n := len(syms); n > 1 {
				fmt.Fprintf(&b, " +%d more", n-1)
			}
			b.WriteString(")")
		}
	}
	return b.String()
}

// didYouMeanLocked appends a short list of indexed member names sharing the
// query's case-insensitive prefix, so a typo is self-correcting.
func (g *CodeGraph) didYouMeanLocked(name string) string {
	if len(name) < 3 {
		return ""
	}
	lower := strings.ToLower(name)
	seen := map[string]bool{}
	var near []string
	for id := range g.symbols {
		s := g.symbols[id]
		switch s.Kind {
		case core.KindMethod, core.KindFunction, core.KindConstructor:
		default:
			continue
		}
		ls := strings.ToLower(s.Name)
		if ls == lower || seen[s.Name] {
			continue
		}
		if strings.HasPrefix(ls, lower) || strings.HasPrefix(lower, ls) {
			seen[s.Name] = true
			near = append(near, canonicalQueryFor(&s))
		}
	}
	if len(near) == 0 {
		return ""
	}
	sort.Strings(near)
	if len(near) > maxLooseCandidates {
		near = near[:maxLooseCandidates]
	}
	return " — did you mean: " + strings.Join(near, ", ")
}

// looksLikeFileRef reports whether a dotted token is really a path reference
// ("render/json.go:12", "config.py:40") rather than a Type.method query.
func looksLikeFileRef(s string) bool {
	_, _, ok := parseFileLineRef(s)
	return ok
}

// parseFileLineRef splits "path/to/file.ext:120".
func parseFileLineRef(s string) (path string, line int, ok bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	if err != nil || n <= 0 {
		return "", 0, false
	}
	p := strings.TrimSpace(s[:i])
	if p == "" || !strings.Contains(p, ".") {
		return "", 0, false
	}
	return p, n, true
}

// resolveFileLineLocked pins the innermost member whose span contains line.
func (g *CodeGraph) resolveFileLineLocked(query, path string, line int) (string, error) {
	var best *core.SymbolRecord
	for id := range g.symbols {
		s := g.symbols[id]
		if s.FilePath != path {
			continue
		}
		switch s.Kind {
		case core.KindMethod, core.KindFunction, core.KindConstructor:
		default:
			continue
		}
		if line < s.Span.Start || line > s.Span.End {
			continue
		}
		// Innermost wins: the tightest span containing the line.
		if best == nil || (s.Span.End-s.Span.Start) < (best.Span.End-best.Span.Start) {
			sym := s
			best = &sym
		}
	}
	if best == nil {
		return "", fmt.Errorf("change-impact: no method or function at %s covering line %d", path, line)
	}
	return canonicalQueryFor(best), nil
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
