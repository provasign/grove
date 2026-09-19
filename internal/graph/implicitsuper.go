package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

// buildImplicitSuperCalls adds the constructor call the language inserts
// for the programmer: a Java or C# constructor that opens with neither
// `super(...)`/`base(...)` nor `this(...)` calls the superclass's
// parameterless constructor. javac and Roslyn emit that invocation and the
// bytecode/semantic oracles record it — 89 of commons-io's 593 misses were
// `AgeFileFilter() → AbstractFileFilter()` edges no source line shows.
// Only an in-repo superclass with an indexed parameterless constructor
// gets an edge; a class with no explicit constructor at all has no symbol
// to be the caller and is outside the measured universe anyway.
// implicitSuperBases returns the superclass names of the constructor's own
// class, read from the class declaration in the constructor's file: a
// nested `Builder extends AbstractStreamBuilder<..>` shares its simple
// name with dozens of other Builders, and a directory-preferred lookup
// picked a sibling file's Builder (and its base) — 35 false
// `AbstractBuilder()` edges on commons-io.
func implicitSuperBases(idx *edgeIndex, ctor *core.SymbolRecord) []string {
	for _, cls := range idx.byFile[ctor.FilePath] {
		if cls.Kind != core.KindClass || cls.Name != ctor.ParentSymbol {
			continue
		}
		sig := cls.Signature
		if sig == "" {
			sig = firstLine(cls.RawText)
		}
		switch ctor.Language {
		case "java":
			return uniqueStrings(inheritanceClauseTypes(stripLeadingGenericParams(sig), "extends", "implements"))
		case "csharp":
			return csBaseClasses(idx, cls.Name, dirOf(cls.FilePath))
		}
	}
	return constructorBaseClasses(idx, ctor.Language, ctor.ParentSymbol, dirOf(ctor.FilePath))
}

// implicitSuperBaseDecl picks the class declaration a base name denotes
// from the constructor's point of view. Nested helper types reuse simple
// names across a package (every channel class nests its own
// `AbstractBuilder` and `Builder`): a qualified name (`FilterChannel.
// AbstractBuilder`) matches by qualified name; a simple one prefers the
// constructor's own file, then its directory, then the first declaration.
func implicitSuperBaseDecl(idx *edgeIndex, ctor *core.SymbolRecord, base string) *core.SymbolRecord {
	simple := base
	if i := strings.LastIndexByte(base, '.'); i >= 0 {
		simple = base[i+1:]
	}
	var sameDir, first *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(simple)] {
		if cand.Name != simple || cand.Kind != core.KindClass || cand.Language != ctor.Language {
			continue
		}
		if simple != base {
			if cand.QualifiedName == base || strings.HasSuffix(cand.QualifiedName, "."+base) {
				return cand
			}
			continue
		}
		if cand.FilePath == ctor.FilePath {
			return cand
		}
		if sameDir == nil && dirOf(cand.FilePath) == dirOf(ctor.FilePath) {
			sameDir = cand
		}
		if first == nil {
			first = cand
		}
	}
	if sameDir != nil {
		return sameDir
	}
	return first
}

func buildImplicitSuperCalls(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for i := range symbols {
		ctor := &symbols[i]
		if ctor.Kind != core.KindConstructor || ctor.ParentSymbol == "" {
			continue
		}
		if ctor.Language != "java" && ctor.Language != "csharp" {
			continue
		}
		explicit := false
		for _, cs := range ctor.CallSites {
			switch cs.Callee {
			case "super()", "this()", "base()", "super", "base":
				explicit = true
			}
			if explicit {
				break
			}
		}
		if explicit {
			continue
		}
		for _, base := range implicitSuperBases(idx, ctor) {
			baseCls := implicitSuperBaseDecl(idx, ctor, base)
			if baseCls == nil {
				continue
			}
			for _, target := range classConstructors(idx, baseCls.Name, baseCls.FilePath) {
				if n, _, ok := declParamCount(target); ok && n == 0 && target.ID != ctor.ID {
					edges = append(edges, core.Edge{
						From: ctor.ID, To: target.ID, Type: core.EdgeCalls, Confidence: 0.85,
						Source: core.EvidenceSourceHeuristic, Reason: core.ReasonConstructor,
					})
				}
			}
		}
	}
	return edges
}
