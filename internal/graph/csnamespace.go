package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

// C# namespace visibility. A simple name resolves in the caller's own
// namespace, its enclosing namespaces, and the namespaces its file imports
// with `using` — never in an unrelated one. newtonsoft's tests declare a
// dozen `Person` classes across namespaces; `new Person()` in a file that
// imports Newtonsoft.Json.Tests.TestObjects means that one, and Roslyn
// binds it there even when other Persons declare explicit constructors and
// it does not. Whole-repo scope (see importedFiles) makes every namespace
// reachable, so this filter restores the language's lookup order on top:
// when any candidate is namespace-visible, only those survive.

// csNamespaceOf returns the namespace enclosing a symbol: the innermost
// namespace declaration in its file whose span contains it, "" for the
// global namespace or a non-C# file.
func (idx *edgeIndex) csNamespaceOf(s *core.SymbolRecord) string {
	if s == nil || s.Language != "csharp" {
		return ""
	}
	if s.Kind == core.KindNamespace {
		return s.QualifiedName
	}
	best := ""
	bestSize := -1
	for _, cand := range idx.byFile[s.FilePath] {
		if cand.Kind != core.KindNamespace {
			continue
		}
		if cand.Span.Start > s.Span.Start || cand.Span.End < s.Span.End {
			// File-scoped `namespace X;` spans only its own line; treat it
			// as enclosing everything after it.
			if !(cand.Span.Start == cand.Span.End && cand.Span.Start <= s.Span.Start) {
				continue
			}
		}
		size := cand.Span.End - cand.Span.Start
		if bestSize < 0 || size < bestSize {
			best, bestSize = cand.QualifiedName, size
		}
	}
	return best
}

// csVisibleNamespaces is the set of namespaces a simple name in caller
// resolves against: its own namespace and every enclosing one, plus the
// file's `using` directives.
func (idx *edgeIndex) csVisibleNamespaces(caller *core.SymbolRecord) map[string]bool {
	out := map[string]bool{"": true}
	ns := idx.csNamespaceOf(caller)
	for ns != "" {
		out[ns] = true
		if i := strings.LastIndexByte(ns, '.'); i >= 0 {
			ns = ns[:i]
		} else {
			ns = ""
		}
	}
	for imp := range idx.fileImports[caller.FilePath] {
		imp = strings.TrimSpace(imp)
		if i := strings.Index(imp, " = "); i >= 0 {
			imp = strings.TrimSpace(imp[i+3:]) // using Alias = Some.Namespace;
		}
		imp = strings.TrimPrefix(imp, "static ")
		out[imp] = true
	}
	return out
}

// csNamespaceVisible reports whether cand's declaring namespace is one the
// caller can name without qualification. A candidate nested in the
// caller's own type is always visible.
func (idx *edgeIndex) csNamespaceVisible(visible map[string]bool, caller, cand *core.SymbolRecord) bool {
	return idx.csScopeRank(visible, caller, cand) < csRankInvisible
}

// C# simple-name lookup order, innermost first: a type nested in the
// caller's own type (or the type itself), the caller's own namespace, an
// enclosing namespace or a `using` import.
const (
	csRankOwnType = iota
	csRankOwnNamespace
	csRankImported
	csRankInvisible
)

// csScopeRank places a candidate in the caller's lookup order. The
// declaring type of a constructor or method is its ParentSymbol; a nested
// type's QualifiedName carries the enclosing type.
func (idx *edgeIndex) csScopeRank(visible map[string]bool, caller, cand *core.SymbolRecord) int {
	owner := cand.QualifiedName
	if cand.Kind == core.KindConstructor || cand.Kind == core.KindMethod || cand.Kind == core.KindField {
		if i := strings.LastIndexByte(owner, '.'); i >= 0 {
			owner = owner[:i]
		}
	}
	if caller.ParentSymbol != "" && cand.FilePath == caller.FilePath {
		// Nested in the caller's type: Outer.Person for a caller in Outer.
		if strings.HasPrefix(owner, caller.ParentSymbol+".") || owner == caller.ParentSymbol {
			return csRankOwnType
		}
		// Enclosing type of a nested caller sees its siblings.
		if i := strings.LastIndexByte(caller.ParentSymbol, '.'); i >= 0 && strings.HasPrefix(owner, caller.ParentSymbol[:i]+".") {
			return csRankOwnType
		}
	}
	ns := idx.csNamespaceOf(cand)
	if ns == idx.csNamespaceOf(caller) {
		return csRankOwnNamespace
	}
	if visible[ns] {
		return csRankImported
	}
	return csRankInvisible
}

// csNarrowByNamespace keeps the candidates at the innermost lookup rank
// that has any; with none visible the set is left alone (a fully
// qualified or `global::` call the extractor reduced to its last segment).
func csNarrowByNamespace(idx *edgeIndex, caller *core.SymbolRecord, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if len(cands) < 2 {
		return cands
	}
	visible := idx.csVisibleNamespaces(caller)
	best := csRankInvisible
	ranks := make([]int, len(cands))
	for i, cand := range cands {
		ranks[i] = idx.csScopeRank(visible, caller, cand)
		if ranks[i] < best {
			best = ranks[i]
		}
	}
	if best == csRankInvisible {
		return cands
	}
	var kept []*core.SymbolRecord
	for i, cand := range cands {
		if ranks[i] == best {
			kept = append(kept, cand)
		}
	}
	return kept
}

// csElementReceiverType types an element-access receiver `base[]`: the
// element type of an array-typed base, else the declared type of the
// base type's indexer (`public JToken this[string key]`, walking bases),
// else "". astkit names an indexer symbol "this[]".
func csElementReceiverType(idx *edgeIndex, caller *core.SymbolRecord, localTypes map[string]string, base string) string {
	typ, ok := localTypes[base]
	if !ok || typ == "" {
		return ""
	}
	if strings.HasSuffix(typ, "[]") {
		return strings.TrimSuffix(typ, "[]")
	}
	classes := []string{typ}
	for level := 0; level < 5 && len(classes) > 0; level++ {
		var next []string
		for _, cls := range classes {
			for _, cand := range namedSymbols(idx, "this[]") {
				if cand.Language != "csharp" || cand.ParentSymbol != cls {
					continue
				}
				if elem := csIndexerType(cand.Signature); elem != "" {
					return elem
				}
			}
			next = append(next, baseClassesFor(idx, "csharp", cls, dirOf(caller.FilePath))...)
		}
		classes = next
	}
	return ""
}

// csElementChainType types a receiver written with one "[]" per
// element-access level (`rss["channel"]["item"]` → "rss[][]"): each
// level's element type is the next level's base.
func csElementChainType(idx *edgeIndex, caller *core.SymbolRecord, localTypes map[string]string, qualifier string) string {
	base := qualifier
	levels := 0
	for strings.HasSuffix(base, "[]") {
		base = strings.TrimSuffix(base, "[]")
		levels++
	}
	if levels == 0 {
		return ""
	}
	typ := csElementReceiverType(idx, caller, localTypes, base)
	for i := 1; i < levels && typ != ""; i++ {
		typ = csElementReceiverType(idx, caller, map[string]string{base: typ}, base)
	}
	return typ
}

// csIndexerType reads the element type off an indexer signature: the
// token before `this` in `public JToken this[string key]`.
func csIndexerType(sig string) string {
	fields := strings.Fields(sig)
	for i, f := range fields {
		if f == "this" || strings.HasPrefix(f, "this[") {
			if i > 0 {
				return csNormalizeType(fields[i-1])
			}
			return ""
		}
	}
	return ""
}

// csExplicitInterfaceImpl reports whether a C# member is an explicit
// interface implementation (`void IList.Add(object)`, `void
// ICollection<T>.CopyTo(...)`): callable only through the interface, so a
// call on any other receiver binds elsewhere and Roslyn binds an
// interface-typed receiver to the interface member itself, never here.
func csExplicitInterfaceImpl(s *core.SymbolRecord) bool {
	if s.Language != "csharp" || s.Kind != core.KindMethod {
		return false
	}
	sig := s.Signature
	if i := strings.IndexByte(sig, '('); i >= 0 {
		sig = sig[:i]
	}
	return strings.HasSuffix(sig, "."+s.Name) || strings.HasSuffix(sig, ">."+s.Name)
}

// csNarrowBareCall restricts a bare (receiver-less, non-constructor) C#
// call to what the language can bind it to: a member of the caller's own
// type or its bases (implicit this), or a local/static function declared
// in the same file. C# has no free functions and imports no methods by
// name, so a same-named method on an unrelated class is never the target
// — `Equals(a, b)` with no own-class Equals is object.Equals.
func csNarrowBareCall(idx *edgeIndex, caller *core.SymbolRecord, cands []*core.SymbolRecord) []*core.SymbolRecord {
	var kept []*core.SymbolRecord
	for _, cand := range cands {
		if cand.Kind == core.KindFunction && cand.FilePath == caller.FilePath {
			kept = append(kept, cand)
		}
		// A receiver the extractor could not name (a call-chain element,
		// a query expression) still reaches an in-repo extension method
		// by name: `rss[..].Children()["category"].Values<string>()`.
		if csIsExtensionMethod(cand) {
			kept = append(kept, cand)
		}
	}
	if caller.ParentSymbol != "" {
		classes := []string{caller.ParentSymbol}
		for level := 0; level < 6 && len(classes) > 0; level++ {
			var next []string
			for _, cls := range classes {
				kept = append(kept, filterByParent(cands, cls)...)
				next = append(next, baseClassesFor(idx, "csharp", cls, dirOf(caller.FilePath))...)
			}
			classes = next
		}
		// A nested type's members see the enclosing type's static members.
		if i := strings.LastIndexByte(caller.ParentSymbol, '.'); i >= 0 {
			kept = append(kept, filterByParent(cands, caller.ParentSymbol[:i])...)
		}
	}
	return kept
}

// csIsExtensionMethod reports whether a C# method's first parameter is a
// `this` receiver.
func csIsExtensionMethod(s *core.SymbolRecord) bool {
	if s.Language != "csharp" || s.Kind != core.KindMethod {
		return false
	}
	params := tsDeclParams(s.Signature)
	if params == "" {
		params = tsDeclParams(s.RawText)
	}
	first := strings.TrimSpace(strings.SplitN(params, ",", 2)[0])
	return strings.HasPrefix(first, "this ")
}

// csImplicitConstructorShadows reports whether a bare `Name(...)` call
// names a namespace-visible C# class that declares no constructor while
// every constructor candidate belongs to a class the caller cannot see:
// the call is the visible class's implicit default constructor, which has
// no symbol, so the candidates are all wrong.
func csImplicitConstructorShadows(idx *edgeIndex, caller *core.SymbolRecord, calleeName string, cands []*core.SymbolRecord) bool {
	if len(cands) == 0 {
		return false
	}
	visible := idx.csVisibleNamespaces(caller)
	bestCtor := csRankInvisible
	for _, cand := range cands {
		if cand.Kind != core.KindConstructor {
			return false
		}
		if r := idx.csScopeRank(visible, caller, cand); r < bestCtor {
			bestCtor = r
		}
	}
	if bestCtor == csRankInvisible {
		// No same-named class in any namespace the caller can see: the
		// type is the runtime's or a dependency's (`new StreamWriter(..)`
		// beside a test that also declares a StreamWriter elsewhere).
		return true
	}
	for _, cls := range namedSymbols(idx, calleeName) {
		if cls.Language != "csharp" || (cls.Kind != core.KindClass && cls.Kind != core.KindStruct) {
			continue
		}
		// A class the lookup order reaches BEFORE any constructor
		// candidate's class, declaring no constructor of its own, is the
		// target — its implicit default constructor has no symbol.
		if idx.csScopeRank(visible, caller, cls) < bestCtor && len(classConstructors(idx, cls.Name, cls.FilePath)) == 0 {
			return true
		}
	}
	return false
}
