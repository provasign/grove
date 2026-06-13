package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Decorator call edges: applying @wrapper to a function means the wrapper
// invokes the wrapped function at runtime, and every caller of the wrapped
// function actually enters the wrapper first. Both are real executed edges
// (they dominate flask's measured recall gap) that pure call-site analysis
// can't see.

// decoratorLanguages gates the pass to languages with decorator semantics.
var decoratorLanguages = map[string]bool{"python": true, "typescript": true, "javascript": true}

// builtinDecorators never resolve to repo functions worth edging; skipping
// them avoids pointless scope lookups.
var builtinDecorators = map[string]bool{
	"property": true, "cached_property": true, "staticmethod": true,
	"classmethod": true, "abstractmethod": true, "override": true,
	"overload": true, "dataclass": true,
}

// buildDecoratorEdges emits wrapper→wrapped edges for every decoration that
// resolves to exactly one in-repo function, plus caller→wrapper supplements
// for existing call edges into the wrapped function.
func buildDecoratorEdges(idx *edgeIndex, symbols []core.SymbolRecord, callEdges []core.Edge) []core.Edge {
	var edges []core.Edge
	seen := map[string]bool{}
	addEdge := func(from, to string) {
		if from == to {
			return
		}
		key := from + "::calls::" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{From: from, To: to, Type: core.EdgeCalls, Confidence: 0.7, Source: core.EvidenceSourceHeuristic})
	}

	wrappersOf := map[string][]string{} // wrapped symbol ID → wrapper symbol IDs
	for i := range symbols {
		m := &symbols[i]
		if !decoratorLanguages[m.Language] || len(m.Annotations) == 0 {
			continue
		}
		switch m.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
		default:
			continue
		}
		var scope map[string]struct{}
		for _, ann := range m.Annotations {
			name := decoratorName(ann)
			if name == "" || builtinDecorators[name] {
				continue
			}
			if scope == nil {
				scope = idx.importedFiles(m.FilePath)
			}
			if wrapper := resolveDecorator(idx, name, scope); wrapper != nil {
				addEdge(wrapper.ID, m.ID)
				wrappersOf[m.ID] = append(wrappersOf[m.ID], wrapper.ID)
			}
		}
	}
	if len(wrappersOf) == 0 {
		return edges
	}
	for _, e := range callEdges {
		if e.Type != core.EdgeCalls {
			continue
		}
		for _, wrapperID := range wrappersOf[e.To] {
			addEdge(e.From, wrapperID)
		}
	}
	return edges
}

// decoratorName normalizes an annotation to a resolvable bare name:
// "@setupmethod" → "setupmethod", "route('/x')" → "route". Dotted
// decorators ("app.route", "x.setter") name a value, not a visible
// function — they return "" and are skipped.
func decoratorName(ann string) string {
	ann = strings.TrimPrefix(strings.TrimSpace(ann), "@")
	if i := strings.IndexByte(ann, '('); i >= 0 {
		ann = ann[:i]
	}
	if ann == "" || strings.ContainsAny(ann, ". \t") {
		return ""
	}
	return ann
}

// resolveDecorator finds the single in-scope function the name refers to;
// ambiguity (multiple same-named functions in scope) resolves to nothing.
func resolveDecorator(idx *edgeIndex, name string, scope map[string]struct{}) *core.SymbolRecord {
	var found *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name || cand.Kind != core.KindFunction {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		if found != nil {
			return nil
		}
		found = cand
	}
	return found
}
