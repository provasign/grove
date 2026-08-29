package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Framework reference edges (SPRING-MEASURE.md, gaps B and D). Both emit
// CALLS edges to accessor methods because both ARE invocations at runtime:
// a Thymeleaf/JSP EL property access calls the getter; a JPA derived-query
// method's name binds it to the field's accessor (rename the field and
// findByLoanId must change with it). Confidence and reason mark them as
// name-derived, never certain.

var (
	reJpaRepo = regexp.MustCompile(
		`extends\s+\w*(?:Jpa|Crud|PagingAndSorting|ListCrud|ListPagingAndSorting)Repository\s*<\s*(\w+)`)
	reDerived = regexp.MustCompile(
		`^(?:find|read|get|query|search|stream|count|exists|delete|remove)(?:All|First\d*|Top\d*|Distinct)?By([A-Z].*)$`)
	// Operator/suffix tokens stripped from a derived-query property expression.
	reDerivedOps = regexp.MustCompile(
		`(?:And|Or|OrderBy.*|IgnoreCase|Not|In|NotIn|Between|LessThan\w*|GreaterThan\w*|Like|NotLike|Containing|Contains|StartingWith|StartsWith|EndingWith|EndsWith|IsNull|IsNotNull|Null|NotNull|True|False|After|Before|Desc|Asc)$`)
	// EL expressions in templates: th:field="*{name}", ${obj.name}. The
	// whole expression is captured and every identifier inside considered:
	// ${vet.firstName + ' ' + vet.lastName} references THREE properties and
	// a first-token-only capture missed two of them (measured: one of four
	// gold template files dropped). #{...} skipped — message keys.
	reELExpr  = regexp.MustCompile(`[$*]\{([^}]*)\}`)
	reELIdent = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]*`)
)

func buildFrameworkEdges(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	// accessor lookup: lowercase property -> getter/setter method symbols.
	type acc struct{ sym *core.SymbolRecord }
	accessors := map[string][]acc{}
	javaSeen := false
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "java" {
			continue
		}
		javaSeen = true
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
	}
	if !javaSeen {
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

	// B: template EL property references -> accessors. Estate-wide by
	// property name at low confidence: templates carry no type context, and
	// a missed binding is the expensive failure (rename ships, view breaks
	// at runtime). Plaintext template records carry the file's content.
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
			for _, ident := range reELIdent.FindAllString(m[1], -1) {
				for _, a := range accessors[strings.ToLower(ident)] {
					add(s.ID, a.sym.ID, 0.6)
				}
			}
		}
	}
	return edges
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
