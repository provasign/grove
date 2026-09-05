package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
	"sort"
)

// interfaceSatisfaction records which concrete types satisfy which
// interfaces, derived without a type checker. For Go this is method-set
// inclusion by name and parameter signature: type T satisfies interface I
// when every method I declares has a compatible method on T. Return types are
// not yet compared, so derived edges still carry reduced confidence.
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

// tsIfaceMethodRe matches a TS/JS interface method signature line
// ("escape(name: string): string", optionally generic or optional:
// "clone?<T>(x: T): T"). The generic group allows ONE level of nested
// angle brackets so a bound like "save<T extends DeepPartial<Entity>>(...)"
// (TypeORM's Repository shape) still matches — a flat [^>]* stopped at the
// inner '>' and dropped the whole member. Property members ("driver: Driver")
// and function-typed properties ("handler: (x) => void") have ':' before any
// '(' and don't match.
var tsIfaceMethodRe = regexp.MustCompile(`(?m)^\s*([A-Za-z_$][\w$]*)\??\s*(?:<(?:[^<>\n]|<[^<>\n]*>)*>)?\s*\(`)

// phpIfaceMethodRe matches a PHP interface method ("public function
// getType(): string;", optionally static/by-ref).
var phpIfaceMethodRe = regexp.MustCompile(`(?m)^\s*(?:(?:public|static)\s+)*function\s+&?([A-Za-z_]\w*)\s*\(`)

// csIfaceMethodRe matches a C# interface member with a parameter list
// ("string GetType();", "void Write<T>(T value);", "Task<int> ReadAsync(");
// the name is the identifier immediately before the paren, after a
// return type.
var csIfaceMethodRe = regexp.MustCompile(`(?m)^\s*(?:[\w.<>\[\],?]+\s+)+([A-Za-z_]\w*)\s*(?:<[^<>\n]*>)?\s*\(`)

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
	if iface.RawText == "" {
		return nil
	}
	// Body-text fallback for languages whose parsers do not emit interface
	// members as symbols: Go (method specs) and TS/JS (method signatures).
	var re *regexp.Regexp
	switch iface.Language {
	case "go":
		re = goIfaceMethodRe
	case "typescript", "tsx", "javascript":
		re = tsIfaceMethodRe
	case "php":
		re = phpIfaceMethodRe
	case "csharp":
		re = csIfaceMethodRe
	default:
		return nil
	}
	body := stripCommentsAndStrings(iface.RawText)
	// Drop the declaration line so "type Render interface {" can't
	// contribute "interface(" style artifacts on unusual formatting.
	if i := strings.IndexByte(body, '{'); i >= 0 {
		body = body[i+1:]
	}
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(body, -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			names = append(names, m[1])
		}
	}
	return names
}

// interfaceMethodParamAnchors returns every indexed or source-declared
// parameter list for one interface member. A nil list is neutral evidence:
// callers retain the historical name-only behavior when a parser cannot
// recover a signature, but use types whenever they are available.
func interfaceMethodParamAnchors(iface *core.SymbolRecord, idx *edgeIndex, methodName string) [][]string {
	var anchors [][]string
	for _, cand := range idx.byFile[iface.FilePath] {
		if cand.Kind != core.KindMethod || cand.ParentSymbol != iface.Name || cand.Name != methodName {
			continue
		}
		anchors = append(anchors, paramTypesOf(cand))
	}
	if len(anchors) > 0 {
		return anchors
	}
	for _, signature := range interfaceMemberSignatures(iface, methodName) {
		anchors = append(anchors, paramTypesOf(&core.SymbolRecord{
			Language:  iface.Language,
			Kind:      core.KindMethod,
			Signature: signature,
		}))
	}
	return anchors
}

// interfaceMemberSignatures extracts complete method headers through the
// closing parameter parenthesis. It supports multiline and nested function
// parameters and is shared by interface satisfaction and synthesized
// change-impact declarations.
func interfaceMemberSignatures(iface *core.SymbolRecord, methodName string) []string {
	if iface.RawText == "" {
		return nil
	}
	var re *regexp.Regexp
	switch iface.Language {
	case "go":
		re = goIfaceMethodRe
	case "typescript", "tsx", "javascript":
		re = tsIfaceMethodRe
	default:
		return nil
	}
	body := stripCommentsAndStrings(iface.RawText)
	if i := strings.IndexByte(body, '{'); i >= 0 {
		body = body[i+1:]
	}
	var signatures []string
	for _, match := range re.FindAllStringSubmatchIndex(body, -1) {
		if len(match) < 4 || body[match[2]:match[3]] != methodName {
			continue
		}
		open := match[1] - 1 // both interface regexes end immediately after '('
		_, close, ok := parenthesizedAt(body, open)
		if !ok {
			continue
		}
		signatures = append(signatures, strings.TrimSpace(body[match[0]:close+1]))
	}
	return signatures
}

// nominalInterfaceLang reports whether a language declares interface
// implementation explicitly (implements/extends clauses, or Python base
// classes), so structural method-set matching must not synthesize satisfaction
// edges for it. Go (and languages without explicit clauses here) stay
// structural.
func nominalInterfaceLang(language string) bool {
	switch language {
	case "java", "typescript", "tsx", "javascript", "python", "php", "csharp":
		return true
	}
	return false
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
	// Every method per lowercase name, not one: Go types routinely pair an
	// exported method with an unexported case-fold twin (grafana's
	// WithDbSession / withDbSession(engine)), and a single-entry map made the
	// signature check read whichever one landed last — failing the whole
	// interface and severing every dispatch edge through it (the
	// grafana-bigblast 1.00→0.02 regression).
	methodsByType := map[typeKey]map[string][]*core.SymbolRecord{}
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
				methodsByType[key] = map[string][]*core.SymbolRecord{}
			}
			ln := strings.ToLower(s.Name)
			methodsByType[key][ln] = append(methodsByType[key][ln], s)
		case core.KindStruct, core.KindClass, core.KindType:
			typeSymbols[typeKey{dirOf(s.FilePath), s.Name}] = s
		}
	}

	// Deterministic iteration order: implementor slices feed first-match
	// type inference and dispatch fan-out downstream, so their order must
	// not depend on map iteration.
	sortedTypeKeys := make([]typeKey, 0, len(methodsByType))
	for k := range methodsByType {
		sortedTypeKeys = append(sortedTypeKeys, k)
	}
	sort.Slice(sortedTypeKeys, func(i, j int) bool {
		if sortedTypeKeys[i].dir != sortedTypeKeys[j].dir {
			return sortedTypeKeys[i].dir < sortedTypeKeys[j].dir
		}
		return sortedTypeKeys[i].name < sortedTypeKeys[j].name
	})

	var edges []core.Edge
	seen := map[string]bool{}
	addEdge := func(from, to string, t core.EdgeType) {
		key := from + "\x00" + string(t) + "\x00" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{From: from, To: to, Type: t, Confidence: 0.75, Source: core.EvidenceSourceHeuristic, Reason: core.ReasonMethodSet})
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
		paramAnchors := make(map[string][][]string, len(names))
		for j, n := range names {
			lower[j] = strings.ToLower(n)
			paramAnchors[lower[j]] = interfaceMethodParamAnchors(iface, idx, n)
		}
		for _, key := range sortedTypeKeys {
			methods := methodsByType[key]
			// The interface's own methods would trivially "satisfy" it.
			if key.name == iface.Name && key.dir == dirOf(iface.FilePath) {
				continue
			}
			// Per member: pick the implementing method among the case-fold
			// candidates — exact-name matches first, then any candidate
			// signature-compatible with an anchor (no anchors = name-only,
			// the historical behavior). The picked method, not an arbitrary
			// map entry, is what gets recorded as the implementor below.
			pick := func(cands []*core.SymbolRecord, anchors [][]string, exactName string) *core.SymbolRecord {
				ok := func(m *core.SymbolRecord) bool {
					if len(anchors) == 0 {
						return true
					}
					candidate := paramTypesOf(m)
					for _, anchor := range anchors {
						if signatureCompatible(anchor, candidate, nil) {
							return true
						}
					}
					return false
				}
				for _, m := range cands {
					if m.Name == exactName && ok(m) {
						return m
					}
				}
				for _, m := range cands {
					if ok(m) {
						return m
					}
				}
				return nil
			}
			chosen := make(map[string]*core.SymbolRecord, len(lower))
			satisfied := true
			for j, n := range lower {
				m := pick(methods[n], paramAnchors[n], names[j])
				if m == nil {
					satisfied = false
					break
				}
				chosen[n] = m
			}
			if !satisfied {
				continue
			}
			if sat.implementors[iface.ID] == nil {
				sat.implementors[iface.ID] = map[string][]*core.SymbolRecord{}
			}
			// Structural satisfaction over-approximates for NOMINAL languages
			// (Java/TS/JS/Python): any class with a same-named method would
			// otherwise "implement" the interface, polluting the subtype
			// closure (e.g. a QueryBuilder.escape wrapper landing in the
			// Driver.escape family). Those languages declare implements/extends
			// explicitly, and buildExtendsImplements emits the real edges — so
			// here we skip structural EDGE emission for them and keep the
			// structural index only for call-dispatch over-approximation.
			nominal := nominalInterfaceLang(iface.Language)
			for _, n := range lower {
				m := chosen[n]
				sat.implementors[iface.ID][n] = append(sat.implementors[iface.ID][n], m)
				if !nominal {
					addEdge(m.ID, iface.ID, core.EdgeOverrides)
				}
			}
			if t, ok := typeSymbols[key]; ok && !nominal {
				addEdge(t.ID, iface.ID, core.EdgeImplements)
			}
		}
		for _, n := range lower {
			sat.declaringIfaces[n] = append(sat.declaringIfaces[n], iface)
		}
	}
	return sat, edges
}

// implementorsFor returns the methods implementing calleeName for one
// specific interface. Deliberately NOT scoped to the caller's imports:
// dependency injection means implementations live in packages the interface
// consumer never imports — that's the whole point of the interface.
func (sat *interfaceSatisfaction) implementorsFor(iface *core.SymbolRecord, calleeName string) []*core.SymbolRecord {
	return sat.implementors[iface.ID][strings.ToLower(calleeName)]
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
