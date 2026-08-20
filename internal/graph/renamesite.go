package graph

// Per-site receiver classification for rename plans.
//
// The failure this replaces (measured live on gin, 2026-08-20): rename-plan
// ambiguity was CALLER-granular — one hasNonFamilySameName(caller) flag
// bucketed every matching line in that caller. A test function calling both
// c.Writer.Status() (family: Writer is the renamed interface's own type) and
// c.Status(200) (Context.Status, non-family) got ALL its lines marked
// ambiguous, including the ones whose receiver type is declared right in the
// struct definition. Result on gin's ResponseWriter.Status: 3 confirmed +
// 21 ambiguous, where per-site resolution yields ~14 confirmed and drops the
// 10 Context.Status lines that were never rename sites at all. Those 21
// ambiguous edits are what drove a live agent to over-rename, break the
// build, and invert the whole rename to get green back.
//
// This file resolves EACH call site's receiver chain with the same
// local-type machinery the edge builder uses, and classifies:
//
//	confirm  — receiver resolves to a family type (the renamed contract or
//	           any member's declaring type). An interface-typed receiver is
//	           the MOST certain case for renaming that interface's member:
//	           whichever implementor dispatches, it is family.
//	exclude  — receiver confidently resolves to a non-family type that
//	           declares the same-named member (Context.Status): not a site.
//	           Static-typed languages only — a dynamic receiver that "looks"
//	           non-family could still be family at runtime, so dynamic
//	           languages never exclude, they stay ambiguous.
//	ambiguous — everything the resolver cannot decide. Never worse than the
//	           caller-level rule this replaces.

import (
	"regexp"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
)

type siteVerdict int

const (
	siteAmbiguous siteVerdict = iota // resolver could not decide — keep old behavior
	siteConfirm                      // receiver is family: a certain rename site
	siteExclude                      // receiver is a non-family declarer: not a site
)

// excludeSafeLanguages are the statically-typed languages where a resolved
// non-family receiver is trustworthy enough to DROP the line from the plan.
var excludeSafeLanguages = map[string]bool{
	"go": true, "java": true, "typescript": true, "tsx": true, "csharp": true,
}

// localTypesForSymbol dispatches to the per-language local-type extractor —
// the same coverage the edge builder uses (edges.go keeps its own inline
// switch because its python case also derives positional-param data).
func localTypesForSymbol(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	switch symbol.Language {
	case "go":
		return goLocalTypes(idx, symbol)
	case "python":
		return pyLocalTypes(idx, symbol)
	case "typescript", "tsx", "javascript":
		return tsLocalTypes(idx, symbol)
	case "java":
		return javaLocalTypes(idx, symbol)
	case "rust":
		return rustLocalTypes(idx, symbol)
	case "csharp":
		return csharpLocalTypes(idx, symbol)
	case "php":
		return phpLocalTypes(idx, symbol)
	case "c", "cpp":
		return cFamilyLocalTypes(idx, symbol)
	}
	return nil
}

// goResolveReceiverChain walks a Go receiver chain ("c.Writer") through
// struct-field declarations, mirroring tsResolveReceiverChain: the first
// segment resolves via localTypes, each further segment via the current
// type's field list (goStructFieldRe over its RawText).
func goResolveReceiverChain(idx *edgeIndex, localTypes map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	if len(parts) == 0 {
		return ""
	}
	curType, ok := localTypes[parts[0]]
	if !ok {
		return ""
	}
	for _, seg := range parts[1:] {
		next := ""
		for _, t := range idx.byName[strings.ToLower(curType)] {
			if t.Name != curType || t.RawText == "" {
				continue
			}
			switch t.Kind {
			case core.KindClass, core.KindStruct, core.KindInterface, core.KindType:
			default:
				continue
			}
			body := t.RawText
			if i := strings.IndexByte(body, '{'); i >= 0 {
				body = body[i+1:]
			}
			for _, m := range goStructFieldRe.FindAllStringSubmatch(body, -1) {
				if m[1] == seg {
					next = m[2]
					break
				}
			}
			if next != "" {
				break
			}
		}
		if next == "" {
			return ""
		}
		curType = next
	}
	return curType
}

// resolveReceiverType resolves a call site's receiver chain to a bare type
// name, per language. Empty string = unresolved.
func resolveReceiverType(idx *edgeIndex, caller *core.SymbolRecord, localTypes map[string]string, chain string) string {
	if chain == "" || localTypes == nil {
		return ""
	}
	switch caller.Language {
	case "go":
		return goResolveReceiverChain(idx, localTypes, chain)
	case "typescript", "tsx", "javascript", "java":
		return strings.TrimPrefix(tsResolveReceiverChain(idx, localTypes, chain, caller.FilePath, caller.Language), "class:")
	default:
		// Single-segment only for the remaining languages: localTypes maps
		// the identifier directly; chains would need per-language field
		// walks that do not exist yet.
		if !strings.Contains(chain, ".") {
			return strings.TrimPrefix(localTypes[chain], "class:")
		}
		parts := strings.Split(chain, ".")
		if len(parts) == 2 && (parts[0] == "self" || parts[0] == "this") {
			return strings.TrimPrefix(localTypes[parts[1]], "class:")
		}
	}
	return ""
}

// typeDeclaresMethod reports whether an indexed type named typeName declares
// a member methodName — the "this receiver belongs to the OTHER contract"
// check behind exclusion.
func typeDeclaresMethod(g *CodeGraph, typeName, methodName string) bool {
	want := typeName + "." + methodName
	for _, s := range g.symbols {
		if s.QualifiedName == want {
			return true
		}
	}
	return false
}

// chainFromLine recovers the FULL receiver chain from the source line —
// astkit's CallSite.Callee keeps only the last receiver segment
// ("c.Writer.Status" arrives as "Writer.Status"), which loses exactly the
// hops field-type resolution needs. One chain per line only: two same-named
// calls on one line cannot be attributed textually, so recovery declines
// (the rawN>1 rule in editLine already forces those ambiguous).
func chainFromLine(caller *core.SymbolRecord, line int, methodName string) string {
	if caller.RawText == "" || line < caller.Span.Start {
		return ""
	}
	lines := strings.Split(caller.RawText, "\n")
	idx := line - caller.Span.Start
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	re := chainRe(methodName)
	ms := re.FindAllStringSubmatch(stripCommentsAndStrings(lines[idx]), -1)
	if len(ms) != 1 {
		return ""
	}
	return strings.TrimSuffix(ms[0][1], ".")
}

var chainReCache sync.Map // methodName -> *regexp.Regexp

func chainRe(methodName string) *regexp.Regexp {
	if v, ok := chainReCache.Load(methodName); ok {
		return v.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*)\.` +
		regexp.QuoteMeta(methodName) + `\s*\(`)
	chainReCache.Store(methodName, re)
	return re
}

// classifySite classifies one call site of the renamed member inside caller.
// familyTypes is the set of declaring-type names of every family member
// (contract + implementors + supers).
func classifySite(g *CodeGraph, idx *edgeIndex, caller *core.SymbolRecord,
	localTypes map[string]string, cs core.CallSite, methodName string,
	familyTypes map[string]bool) siteVerdict {

	// Prefer the source line's full chain; fall back to the (truncated)
	// Callee prefix when line recovery declines.
	chain := chainFromLine(caller, cs.Line, methodName)
	if chain == "" {
		if i := strings.LastIndexByte(cs.Callee, '.'); i >= 0 {
			chain = cs.Callee[:i]
		}
	}
	if chain == "" || chain == "this" || chain == "self" {
		return siteAmbiguous // receiver-less / self calls: keep the old rule
	}
	t := resolveReceiverType(idx, caller, localTypes, chain)
	if t == "" {
		return siteAmbiguous
	}
	if familyTypes[t] {
		return siteConfirm
	}
	if excludeSafeLanguages[caller.Language] && typeDeclaresMethod(g, t, methodName) {
		return siteExclude
	}
	return siteAmbiguous
}
