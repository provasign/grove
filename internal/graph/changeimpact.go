package graph

import (
	"fmt"
	"regexp"
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
	if len(decls) == 0 && len(family) == 0 {
		return nil, fmt.Errorf("change-impact: type %q declares no method %q and no subtype implements it", typeName, methodName)
	}
	if len(decls) == 0 {
		// The member exists in source but not as a symbol (TS interface
		// members, Go interface specs). When the seed type's body declares
		// it, synthesize the declaration record: the declaring file is part
		// of the change-set.
		for _, tid := range typeIDs {
			t, ok := g.symbols[tid]
			if !ok || !typeDeclaresMember(&t, methodName) {
				continue
			}
			decls = append(decls, core.SymbolRecord{
				ID:            t.ID + "#" + methodName,
				FilePath:      t.FilePath,
				Language:      t.Language,
				Kind:          core.KindMethod,
				Name:          methodName,
				QualifiedName: t.Name + "." + methodName,
				ParentSymbol:  t.Name,
				Span:          t.Span,
			})
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

	// 7. Contract boundary: supertype names declared anywhere in the upward
	// closure (seed types + indexed supertypes) that resolve to no indexed
	// type are external. If the queried method is a member of a well-known
	// external contract, the result is the project-local closure of a
	// contract the project does not own — flag it.
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

	sortSymbols(decls)
	sortSymbols(family)
	sortSymbols(supers)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:             query,
		Declarations:      decls,
		Supers:            supers,
		Family:            family,
		Callers:           callers,
		ExternalSupers:    externalSupers,
		OverridesExternal: overridesExternal,
		Completeness:      completeness,
	}, nil
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
