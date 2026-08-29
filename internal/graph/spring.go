package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Framework reference edges (SPRING-MEASURE.md, gaps B and D; generalized
// past Java 2026-08-29 once the same template-binding gap was measured on
// real Angular/Flask apps — angular.io alone: 678 interpolations, 267
// property bindings, 302 structural directives, all in template files
// separate from the class they bind to). Both emit CALLS edges to the
// binding target because both ARE invocations/reads at runtime: a
// Thymeleaf/JSP EL or Angular template property reference reads the field
// (or calls the getter); a JPA derived-query method's name binds it to the
// field's accessor (rename the field and findByLoanId must change with
// it). Confidence and reason mark them as name-derived, never certain.

var (
	reJpaRepo = regexp.MustCompile(
		`extends\s+\w*(?:Jpa|Crud|PagingAndSorting|ListCrud|ListPagingAndSorting)Repository\s*<\s*(\w+)`)
	reDerived = regexp.MustCompile(
		`^(?:find|read|get|query|search|stream|count|exists|delete|remove)(?:All|First\d*|Top\d*|Distinct)?By([A-Z].*)$`)
	// Operator/suffix tokens stripped from a derived-query property expression.
	reDerivedOps = regexp.MustCompile(
		`(?:And|Or|OrderBy.*|IgnoreCase|Not|In|NotIn|Between|LessThan\w*|GreaterThan\w*|Like|NotLike|Containing|Contains|StartingWith|StartsWith|EndingWith|EndsWith|IsNull|IsNotNull|Null|NotNull|True|False|After|Before|Desc|Asc)$`)
	// EL expressions: th:field="*{name}", ${obj.name} (Thymeleaf/JSP). The
	// whole expression is captured and every identifier inside considered:
	// ${vet.firstName + ' ' + vet.lastName} references THREE properties and
	// a first-token-only capture missed two of them (measured: one of four
	// gold template files dropped). #{...} skipped — message keys.
	reELExpr = regexp.MustCompile(`[$*]\{([^}]*)\}`)
	// {{ expr }} interpolation — Angular AND Jinja share this delimiter.
	reBraceExpr = regexp.MustCompile(`\{\{([^}]*)\}\}`)
	// Angular property/event bindings: [prop]="expr", (event)="expr".
	reAngularBind = regexp.MustCompile(`[\[(][a-zA-Z][\w.-]*[\])]\s*=\s*"([^"]*)"`)
	// Includes underscore: Python/Jinja identifiers are snake_case, and
	// without it "full_name" tokenized as "full" + "name" and missed the
	// real property (caught by TestBuildFrameworkEdges_PythonJinja).
	reELIdent = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
)

func buildFrameworkEdges(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	// accessor lookup: lowercase property -> binding-target symbols. Two
	// shapes, because "what a template binds to" differs by convention:
	//   - Java: get*/is* METHODS only — the field itself is private and
	//     never the binding target; the accessor's name determines the
	//     property name (getLoanId -> loanId).
	//   - TypeScript/JavaScript, Python: FIELDS bind directly (Angular
	//     template interpolation targets a class property like
	//     `currentUser$`, not a getLoanId()-style method; Jinja/Flask
	//     templates read model attributes the same way) — keep the field's
	//     OWN name, no prefix stripping. Python @property-decorated methods
	//     read like fields at the call site, so they join the same set.
	type acc struct{ sym *core.SymbolRecord }
	accessors := map[string][]acc{}
	for i := range symbols {
		s := &symbols[i]
		switch s.Language {
		case "java":
			if s.Kind != core.KindMethod {
				continue
			}
			n := s.Name
			var prop string
			switch {
			case strings.HasPrefix(n, "get") && len(n) > 3:
				prop = n[3:]
			case strings.HasPrefix(n, "is") && len(n) > 2:
				prop = n[2:]
			default:
				continue
			}
			accessors[strings.ToLower(prop)] = append(accessors[strings.ToLower(prop)], acc{s})
		case "typescript", "tsx", "javascript":
			if s.Kind != core.KindField {
				continue
			}
			accessors[strings.ToLower(s.Name)] = append(accessors[strings.ToLower(s.Name)], acc{s})
		case "python":
			isProp := s.Kind == core.KindMethod && hasPropertyDecorator(s.Annotations)
			if s.Kind != core.KindField && !isProp {
				continue
			}
			accessors[strings.ToLower(s.Name)] = append(accessors[strings.ToLower(s.Name)], acc{s})
		}
	}
	if len(accessors) == 0 {
		return nil
	}

	var edges []core.Edge
	seen := map[string]bool{}
	add := func(from, to string, conf float64) {
		key := from + "::" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: from, To: to,
			Type: core.EdgeCalls, Confidence: conf,
			Source: core.EvidenceSourceHeuristic, Reason: core.ReasonCrossArtifact,
		})
	}

	// D: JPA derived-query methods -> entity accessors, scoped to the
	// repository's own entity type so same-named properties on other
	// entities never match.
	entityOf := map[string]string{} // interface qualified name -> entity name
	for i := range symbols {
		s := &symbols[i]
		if s.Language == "java" && s.Kind == core.KindInterface {
			if m := reJpaRepo.FindStringSubmatch(s.Signature); m != nil {
				entityOf[s.Name] = m[1]
			}
		}
	}
	if len(entityOf) > 0 {
		for i := range symbols {
			s := &symbols[i]
			if s.Language != "java" || s.Kind != core.KindMethod {
				continue
			}
			entity, ok := entityOf[s.ParentSymbol]
			if !ok {
				continue
			}
			m := reDerived.FindStringSubmatch(s.Name)
			if m == nil {
				continue
			}
			for _, raw := range splitDerivedProps(m[1]) {
				prop := strings.ToLower(reDerivedOps.ReplaceAllString(raw, ""))
				for _, a := range accessors[prop] {
					if a.sym.ParentSymbol == entity {
						add(s.ID, a.sym.ID, 0.7)
					}
				}
			}
		}
	}

	// B: template property references -> accessors. Estate-wide by property
	// name at low confidence: templates carry no type context, and a missed
	// binding is the expensive failure (rename ships, view breaks at
	// runtime). Plaintext template records carry the file's content. Three
	// delimiter shapes cover Thymeleaf/JSP EL, Angular, and Jinja; running
	// all three against every template is cheap and each is a no-op where
	// its syntax does not occur.
	scanExpr := func(templateID, expr string) {
		for _, ident := range reELIdent.FindAllString(expr, -1) {
			for _, a := range accessors[strings.ToLower(ident)] {
				add(templateID, a.sym.ID, 0.6)
			}
		}
	}
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "plaintext" || s.RawText == "" {
			continue
		}
		fp := strings.ToLower(s.FilePath)
		if !strings.HasSuffix(fp, ".html") && !strings.HasSuffix(fp, ".htm") &&
			!strings.HasSuffix(fp, ".jsp") && !strings.HasSuffix(fp, ".ftl") {
			continue
		}
		for _, m := range reELExpr.FindAllStringSubmatch(s.RawText, -1) {
			scanExpr(s.ID, m[1])
		}
		for _, m := range reBraceExpr.FindAllStringSubmatch(s.RawText, -1) {
			scanExpr(s.ID, m[1])
		}
		for _, m := range reAngularBind.FindAllStringSubmatch(s.RawText, -1) {
			scanExpr(s.ID, m[1])
		}
	}
	return edges
}

// hasPropertyDecorator reports whether a Python method's decorator list
// includes @property (astkit strips the leading "@"). A sibling @x.setter
// method is intentionally NOT matched: templates READ properties, never
// write them, so the getter alone is the binding target that matters here.
func hasPropertyDecorator(annotations []string) bool {
	for _, a := range annotations {
		if a == "property" {
			return true
		}
	}
	return false
}

// splitDerivedProps splits a derived-query property expression on And/Or
// boundaries: "LoanIdAndStatus" -> ["LoanId", "Status"]. Manual scan — RE2
// has no lookahead, and a MustCompile panic here surfaced as an internal
// error on the first change-impact query of any Spring repo.
func splitDerivedProps(expr string) []string {
	var out []string
	start := 0
	for i := 0; i < len(expr)-2; i++ {
		if expr[i] == 'A' && i+3 < len(expr) && expr[i+1] == 'n' && expr[i+2] == 'd' &&
			expr[i+3] >= 'A' && expr[i+3] <= 'Z' && i > start {
			out = append(out, expr[start:i])
			start = i + 3
			i += 2
		} else if expr[i] == 'O' && i+2 < len(expr) && expr[i+1] == 'r' &&
			expr[i+2] >= 'A' && expr[i+2] <= 'Z' && i > start {
			out = append(out, expr[start:i])
			start = i + 2
			i++
		}
	}
	if start < len(expr) {
		out = append(out, expr[start:])
	}
	return out
}

// fieldImpactLocked answers change-impact for a FIELD anchor (a class
// property, not a method) — the query shape a template-binding or ORM
// attribute reference needs, which containedMethods (method/function only)
// never resolved. Returns nil when no field of that name exists, or when a
// METHOD of that name exists on the type (methods keep priority; a bare
// property is checked only as a fallback the ordinary path did not already
// answer). Callers-only completeness, same as a free function: a field has
// no override family to close over.
func (g *CodeGraph) fieldImpactLocked(query string) *ChangeImpactResult {
	typeName, fieldName, _, err := parseChangeImpactQuery(query)
	if err != nil {
		return nil
	}
	var typeIDs []string
	for _, id := range g.idsNamed(typeName) {
		switch g.symbols[id].Kind {
		case core.KindClass, core.KindInterface, core.KindType, core.KindStruct, core.KindTrait, core.KindEnum:
			typeIDs = append(typeIDs, id)
		}
	}
	if len(typeIDs) == 0 {
		return nil
	}
	if len(g.containedMethods(typeIDs, fieldName)) > 0 {
		return nil // a method of this name exists — let the normal path answer
	}
	var decls []core.SymbolRecord
	declIDs := map[string]bool{}
	for _, tid := range typeIDs {
		for _, ei := range g.outbound[tid] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeContains {
				continue
			}
			s, ok := g.symbols[edge.To]
			if !ok || s.Name != fieldName || s.Kind != core.KindField {
				continue
			}
			decls = append(decls, s)
			declIDs[s.ID] = true
		}
	}
	if len(decls) == 0 {
		return nil
	}
	heuristicCallers := false
	seen := map[string]bool{}
	var callers []core.SymbolRecord
	for id := range declIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeCalls || declIDs[edge.From] || seen[edge.From] {
				continue
			}
			seen[edge.From] = true
			if edge.Source == core.EvidenceSourceHeuristic || edge.Source == core.EvidenceSourceRegex {
				heuristicCallers = true
			}
			if s, ok := g.symbols[edge.From]; ok {
				callers = append(callers, s)
			}
		}
	}
	sortSymbols(decls)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query: query, Declarations: decls, Callers: callers,
		Completeness: "callers-only", HasHeuristicRefs: heuristicCallers,
	}
}
