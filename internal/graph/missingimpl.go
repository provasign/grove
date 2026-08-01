package graph

import (
	"fmt"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// MissingImplementationsResult is the deterministic answer to "I added (or am
// adding) Type.method — which types claiming that contract do not implement
// it?": the companion operation to ChangeImpact for interface evolution.
// ChangeImpact returns the sites that must change when a signature changes;
// MissingImplementations returns the types the compiler will reject once the
// member is required. Computed in the engine so no agent has to orchestrate
// the traversal (closure → per-type member check → inheritance walk) over
// primitives.
type MissingImplementationsResult struct {
	Query    string              // the query as given
	Contract []core.SymbolRecord // the method's declaration(s) on the named type

	// Missing lists concrete types (class/struct/enum) in the subtype
	// closure with no signature-compatible implementation of their own and
	// none inherited through their class-extends chain. Each is a compile
	// error once the member is required.
	Missing []core.SymbolRecord
	// AbstractMissing lists abstract classes in the closure without an
	// implementation. Not compile-broken themselves — their concrete
	// subtypes appear in Missing — but listed because adding the
	// implementation there often fixes a whole subtree.
	AbstractMissing []core.SymbolRecord
	// Unverifiable lists closure types without a visible implementation
	// whose class-extends chain leaves the index (an external superclass may
	// provide the member). Deliberately separated from Missing: flagging a
	// working type as broken sends an agent to "fix" correct code.
	Unverifiable []core.SymbolRecord

	// ImplementedCount is the number of closure types that do carry a
	// compatible implementation (own or inherited) — evidence of coverage
	// without inflating the payload; ChangeImpact returns the full family.
	ImplementedCount int

	// DefaultProvided is true when the contract itself supplies a body every
	// subtype inherits (a Java default method, or a concrete method on a
	// class/abstract-class seed). No type is then compile-broken today, and
	// Missing reads as "inherits the default — breaks if the member becomes
	// abstract/required", which is the interface-evolution question that
	// motivates querying a defaulted member.
	DefaultProvided bool

	// Same contract-boundary reporting as ChangeImpactResult.
	ExternalSupers    []string
	OverridesExternal []string
	Completeness      string // "closed" | "project-local"
}

// MissingImplementations resolves a "Type.method" or
// "Type.method(ParamType, ...)" query to every type in the subtype closure
// that fails to implement the member. Seeding is type-resolved exactly as in
// ChangeImpact; nominal closure only, so for Go the structural caveat is the
// same as ChangeImpact's.
func (g *CodeGraph) MissingImplementations(query string) (*MissingImplementationsResult, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Same lenient front-end as ChangeImpact: a bare, unambiguous member name
	// resolves to Type.method; ambiguity errors with candidates.
	resolved, err := g.resolveLooseQueryLocked(query)
	if err != nil {
		return nil, err
	}
	query = resolved

	typeName, methodName, queryParams, err := parseChangeImpactQuery(query)
	if err != nil {
		return nil, err
	}

	// 1. The named type(s) — same seeding as ChangeImpact.
	// idsNamed is pre-sorted. The seed order matters: contract[0] anchors
	// declParams/wildcards for the whole closure filter, so an unstable
	// order silently changes which types are reported as missing an
	// implementation.
	var typeIDs []string
	for _, id := range g.idsNamed(typeName) {
		switch g.symbols[id].Kind {
		case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
			typeIDs = append(typeIDs, id)
		}
	}
	if len(typeIDs) == 0 {
		return nil, fmt.Errorf("missing-implementations: type %q is not indexed (for an external contract, query a project type that declares it)", typeName)
	}

	// 2. The contract: the member's declaration(s) on the seed type.
	contract := g.containedMethods(typeIDs, methodName)
	sortSymbols(contract)
	if len(queryParams) > 0 && len(contract) > 0 {
		if byParams := filterByParamTypes(contract, queryParams); len(byParams) > 0 {
			contract = byParams
		} else {
			return nil, fmt.Errorf("missing-implementations: %s.%s declares no overload matching (%s)",
				typeName, methodName, strings.Join(queryParams, ", "))
		}
	}
	declParams := queryParams
	wildcards := map[string]bool{}
	if len(contract) > 0 {
		declParams = paramTypesOf(&contract[0])
		wildcards = typeParamWildcards(g.symbols, &contract[0])
	}
	if len(contract) == 0 {
		// TS interface members and Go interface specs are body text, not
		// symbols — synthesize, as ChangeImpact does.
		for _, tid := range typeIDs {
			t, ok := g.symbols[tid]
			if !ok || !typeDeclaresMember(&t, methodName) {
				continue
			}
			contract = append(contract, core.SymbolRecord{
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
	if len(contract) == 0 {
		return nil, fmt.Errorf("missing-implementations: type %q declares no member %q", typeName, methodName)
	}

	externalSupers, overridesExternal := g.externalContract(typeIDs, methodName)
	completeness := "closed"
	if len(overridesExternal) > 0 {
		completeness = "project-local"
	}

	res := &MissingImplementationsResult{
		Query:             query,
		Contract:          contract,
		ExternalSupers:    externalSupers,
		OverridesExternal: overridesExternal,
		Completeness:      completeness,
	}

	// 3. A contract that carries its own body (a Java default method, or a
	// concrete method on a class seed) is inherited by every subtype, so no
	// type is compile-broken today. The buckets are still computed: under
	// DefaultProvided they answer the evolution question "who inherits the
	// default and breaks if the member becomes abstract/required?" — asking
	// that hypothetical is the main reason to run this operation on a
	// defaulted member.
	seedKinds := make(map[string]core.SymbolKind, len(typeIDs))
	for _, tid := range typeIDs {
		if t, ok := g.symbols[tid]; ok {
			seedKinds[tid] = t.Kind
		}
	}
	res.DefaultProvided = contractProvidesBody(contract, seedKinds)
	contractIDs := make(map[string]bool, len(contract))
	for _, c := range contract {
		contractIDs[c.ID] = true
	}

	// 4. Subtype closure (downward), identical to ChangeImpact step 3.
	closure := make(map[string]bool, len(typeIDs))
	seeds := make(map[string]bool, len(typeIDs))
	frontier := make([]string, 0, len(typeIDs))
	for _, id := range typeIDs {
		closure[id] = true
		seeds[id] = true
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

	// 5. Bucket every candidate. Interfaces/traits/type-aliases in the
	// closure extend the contract; they owe no implementation.
	for id := range closure {
		if seeds[id] {
			continue
		}
		t, ok := g.symbols[id]
		if !ok {
			continue
		}
		switch t.Kind {
		// KindType: a Go named non-struct type (`type Status int`) implements
		// interfaces via methods on it — accepted as a seed at line 78 and by
		// ChangeImpact, so it must be a valid closure member here too.
		case core.KindClass, core.KindStruct, core.KindEnum, core.KindType:
		default:
			continue
		}
		covered, escapesIndex := g.implementationCoverage(id, methodName, declParams, wildcards, contractIDs)
		switch {
		case covered:
			res.ImplementedCount++
		case isAbstractType(&t):
			res.AbstractMissing = append(res.AbstractMissing, t)
		case escapesIndex:
			res.Unverifiable = append(res.Unverifiable, t)
		default:
			res.Missing = append(res.Missing, t)
		}
	}
	sortSymbols(res.Missing)
	sortSymbols(res.AbstractMissing)
	sortSymbols(res.Unverifiable)
	return res, nil
}

// implementationCoverage walks the type's class-inheritance chain (outbound
// extends edges — implements grants no implementation) and reports whether
// any chain member declares a signature-compatible method, and whether the
// chain names a superclass that is not indexed (external base: coverage
// cannot be decided). Caller must hold g.mu.
func (g *CodeGraph) implementationCoverage(typeID, methodName string, declParams []string, wildcards map[string]bool, contractIDs map[string]bool) (covered, escapesIndex bool) {
	visited := map[string]bool{}
	frontier := []string{typeID}
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		if visited[node] {
			continue
		}
		visited[node] = true
		for _, m := range g.containedMethods([]string{node}, methodName) {
			// The contract declaration itself never counts as coverage — a
			// concrete body on the seed is the inherited default the
			// DefaultProvided flag reports, not an implementation the
			// subtype supplies.
			if contractIDs[m.ID] {
				continue
			}
			if signatureCompatible(declParams, paramTypesOf(&m), wildcards) && providesImplementation(&m) {
				return true, false
			}
		}
		t, ok := g.symbols[node]
		if !ok {
			continue
		}
		// Go struct-embeds-interface: the embedded interface's method set is
		// promoted onto the struct (delegation through the embedded field),
		// so reaching an interface that declares the member IS coverage —
		// gin's interceptedWriter{ResponseWriter} has Status() without
		// declaring it, and the program compiles. Go-only: in nominal
		// languages a supertype interface declares the obligation, never
		// satisfies it.
		if node != typeID && t.Language == "go" && t.Kind == core.KindInterface &&
			typeDeclaresMember(&t, methodName) {
			return true, false
		}
		// Python bases all grant implementation inheritance; for Java/TS/JS
		// only the extends position does, and extends edges encode exactly
		// that. An extends-clause superclass with no indexed resolution means
		// the chain leaves the project.
		resolvedSupers := map[string]bool{}
		for _, ei := range g.outbound[node] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeExtends {
				continue
			}
			if sup, ok := g.symbols[edge.To]; ok {
				resolvedSupers[sup.Name] = true
				frontier = append(frontier, edge.To)
			}
		}
		for _, name := range classExtendsNames(&t) {
			// matchNameList keeps dotted qualifiers; this check wants the
			// simple name ("ValueInstantiator.Base" → "Base").
			if j := strings.LastIndexByte(name, '.'); j >= 0 {
				name = name[j+1:]
			}
			if !resolvedSupers[name] && !g.hasIndexedType(name) && !knownEmptyBases[name] {
				escapesIndex = true
			}
		}
	}
	return false, escapesIndex
}

// classExtendsNames returns the names in the implementation-granting position
// of a class declaration: the extends clause for Java/TS/JS classes, every
// base for Python. Interfaces return nil — extending an interface grants no
// implementation.
func classExtendsNames(t *core.SymbolRecord) []string {
	if t.Kind != core.KindClass && t.Kind != core.KindStruct && t.Kind != core.KindEnum {
		return nil
	}
	switch t.Language {
	case "typescript", "tsx", "javascript", "java":
		text := t.Signature
		if text == "" {
			text = firstLine(t.RawText)
		}
		return matchNameList(extendsRe, stripAngleBrackets(text))
	case "csharp":
		// The base class (first non-interface entry) grants implementation
		// inheritance; interfaces do not. Without a C# case here, a C#
		// subtype with an unresolved/external base could never set
		// escapesIndex and would be wrongly bucketed Missing rather than
		// Unverifiable.
		text := t.Signature
		if text == "" {
			text = firstLine(t.RawText)
		}
		names := csharpBaseNames(text)
		if len(names) > 0 && !strings.HasPrefix(names[0], "I") {
			return names[:1]
		}
		return nil
	case "python":
		return declaredSuperNames(t)
	}
	return nil
}

// contractProvidesBody reports whether the contract declaration itself
// supplies an implementation every subtype inherits: a Java interface default
// method, or a concrete (non-abstract) method on a class seed.
func contractProvidesBody(contract []core.SymbolRecord, seedKinds map[string]core.SymbolKind) bool {
	for _, c := range contract {
		if hasModifier(&c, "default") || strings.Contains(c.Signature, "default ") {
			return true
		}
		if hasModifier(&c, "abstract") {
			continue
		}
		// A concrete method on a class/abstract-class seed is inherited via
		// extends. On an interface, a signature alone provides nothing. A
		// Python ABC member (@abstractmethod / NotImplementedError body) is a
		// contract, not an implementation.
		if c.Language == "python" &&
			(strings.Contains(c.RawText, "abstractmethod") ||
				strings.Contains(c.RawText, "NotImplementedError")) {
			continue
		}
		for _, k := range seedKinds {
			if k == core.KindClass || k == core.KindStruct {
				if strings.Contains(c.RawText, "{") || c.Language == "python" {
					return true
				}
			}
		}
	}
	return false
}

// knownEmptyBases are external base types that cannot provide a user-defined
// contract member: an unresolved extends on one of these does not make
// coverage unverifiable. Deliberately tiny — absence here only means "treated
// as escaping the index", never a wrong Missing claim.
var knownEmptyBases = map[string]bool{
	"ABC":      true, // python abc.ABC
	"ABCMeta":  true,
	"object":   true,
	"Object":   true, // java.lang.Object in an explicit extends clause
	"Protocol": true, // typing.Protocol
	"Generic":  true, // typing.Generic
}

// providesImplementation reports whether a method found on an inheritance
// chain is a real body a subtype inherits — not a re-statement of the
// contract (abstract method, Python @abstractmethod, or a
// raise-NotImplementedError stub, which fails the contract at runtime).
func providesImplementation(m *core.SymbolRecord) bool {
	if hasModifier(m, "abstract") || strings.Contains(m.Signature, "abstract ") {
		return false
	}
	if m.Language == "python" &&
		(strings.Contains(m.RawText, "abstractmethod") ||
			strings.Contains(m.RawText, "NotImplementedError")) {
		return false
	}
	return true
}

func isAbstractType(t *core.SymbolRecord) bool {
	return hasModifier(t, "abstract") || strings.Contains(t.Signature, "abstract class ")
}

func hasModifier(s *core.SymbolRecord, mod string) bool {
	for _, m := range s.Modifiers {
		if m == mod {
			return true
		}
	}
	return false
}
