package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// interfaceSatisfaction records which concrete types satisfy which
// interfaces, derived without a type checker. For Go this is method-set
// inclusion by name: type T satisfies interface I when every method I
// declares exists as a method on T. Name-only matching over-approximates
// (signatures are not compared), so derived edges carry reduced confidence.
type interfaceSatisfaction struct {
	// declaringIfaces maps a lowercase method name to the interface symbols
	// that declare it.
	declaringIfaces map[string][]*core.SymbolRecord
	// implementors maps interfaceID → lowercase method name → implementing
	// method symbols.
	implementors map[string]map[string][]*core.SymbolRecord
}

// goIfaceMethodRe matches a Go interface method spec line ("Render(w http...)
// error"). Embedded interfaces are bare identifiers without "(" and don't
// match; comment lines are stripped before matching.
var goIfaceMethodRe = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*)\s*\(`)

// interfaceMethodNames extracts the method names an interface declares.
// Child method symbols (parent set to the interface) win when present —
// some languages' parsers emit them; Go's does not, so Go falls back to
// parsing the interface body text.
func interfaceMethodNames(iface *core.SymbolRecord, idx *edgeIndex) []string {
	var names []string
	for _, cand := range idx.byFile[iface.FilePath] {
		if cand.Kind == core.KindMethod && cand.ParentSymbol == iface.Name {
			names = append(names, cand.Name)
		}
	}
	if len(names) > 0 {
		return names
	}
	if iface.Language != "go" || iface.RawText == "" {
		return nil
	}
	body := stripCommentsAndStrings(iface.RawText)
	// Drop the declaration line so "type Render interface {" can't
	// contribute "interface(" style artifacts on unusual formatting.
	if i := strings.IndexByte(body, '{'); i >= 0 {
		body = body[i+1:]
	}
	for _, m := range goIfaceMethodRe.FindAllStringSubmatch(body, -1) {
		names = append(names, m[1])
	}
	return names
}

// buildInterfaceSatisfaction computes satisfaction and returns the derived
// implements (type → interface) and overrides (method → interface) edges.
func buildInterfaceSatisfaction(idx *edgeIndex, symbols []core.SymbolRecord) (*interfaceSatisfaction, []core.Edge) {
	sat := &interfaceSatisfaction{
		declaringIfaces: map[string][]*core.SymbolRecord{},
		implementors:    map[string]map[string][]*core.SymbolRecord{},
	}

	// Concrete method sets, keyed by (package dir, type name) so same-named
	// types in different packages stay separate.
	type typeKey struct{ dir, name string }
	methodsByType := map[typeKey]map[string]*core.SymbolRecord{}
	typeSymbols := map[typeKey]*core.SymbolRecord{}
	for i := range symbols {
		s := &symbols[i]
		switch s.Kind {
		case core.KindMethod:
			if s.ParentSymbol == "" {
				continue
			}
			key := typeKey{dirOf(s.FilePath), s.ParentSymbol}
			if methodsByType[key] == nil {
				methodsByType[key] = map[string]*core.SymbolRecord{}
			}
			methodsByType[key][strings.ToLower(s.Name)] = s
		case core.KindStruct, core.KindClass, core.KindType:
			typeSymbols[typeKey{dirOf(s.FilePath), s.Name}] = s
		}
	}

	var edges []core.Edge
	seen := map[string]bool{}
	addEdge := func(from, to string, t core.EdgeType) {
		key := from + "\x00" + string(t) + "\x00" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{From: from, To: to, Type: t, Confidence: 0.75})
	}

	for i := range symbols {
		iface := &symbols[i]
		if iface.Kind != core.KindInterface {
			continue
		}
		names := interfaceMethodNames(iface, idx)
		if len(names) == 0 {
			continue
		}
		lower := make([]string, len(names))
		for j, n := range names {
			lower[j] = strings.ToLower(n)
		}
		for key, methods := range methodsByType {
			// The interface's own methods would trivially "satisfy" it.
			if key.name == iface.Name && key.dir == dirOf(iface.FilePath) {
				continue
			}
			satisfied := true
			for _, n := range lower {
				if _, ok := methods[n]; !ok {
					satisfied = false
					break
				}
			}
			if !satisfied {
				continue
			}
			if sat.implementors[iface.ID] == nil {
				sat.implementors[iface.ID] = map[string][]*core.SymbolRecord{}
			}
			for _, n := range lower {
				m := methods[n]
				sat.implementors[iface.ID][n] = append(sat.implementors[iface.ID][n], m)
				addEdge(m.ID, iface.ID, core.EdgeOverrides)
			}
			if t, ok := typeSymbols[key]; ok {
				addEdge(t.ID, iface.ID, core.EdgeImplements)
			}
		}
		for _, n := range lower {
			sat.declaringIfaces[n] = append(sat.declaringIfaces[n], iface)
		}
	}
	return sat, edges
}

// dispatchTargets returns the implementing methods reachable when a call to
// calleeName is interpreted as dynamic dispatch through an interface visible
// from the caller: the interface's file must be in the caller's import scope,
// and so must each implementing method's file.
func (sat *interfaceSatisfaction) dispatchTargets(calleeName string, scope map[string]struct{}) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	seen := map[string]bool{}
	for _, iface := range sat.declaringIfaces[strings.ToLower(calleeName)] {
		if _, ok := scope[iface.FilePath]; !ok {
			continue
		}
		for _, m := range sat.implementors[iface.ID][strings.ToLower(calleeName)] {
			if seen[m.ID] {
				continue
			}
			if _, ok := scope[m.FilePath]; !ok {
				continue
			}
			seen[m.ID] = true
			out = append(out, m)
		}
	}
	return out
}
