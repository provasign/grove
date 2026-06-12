package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// TS/JS local type inference: typed class fields (already indexed as field
// symbols with "public transport: Transport" signatures), constructor
// parameter properties, parameter annotations, and new-expression
// assignments. Same shallow, harness-bounded philosophy as Go and Python.

var (
	// field signature: "public transport: Transport = default"
	tsFieldSigRe = regexp.MustCompile(`(\w+)\??\s*:\s*([^=;]+?)\s*(?:=|;|$)`)
	// constructor parameter property: "private readonly opts: Options"
	tsCtorPropRe = regexp.MustCompile(`(?:public|private|protected)\s+(?:readonly\s+)?(\w+)\??\s*:\s*([A-Za-z_][\w.<>\[\]| ]*)`)
	// x = new Type(...) / this.x = new pkg.Type(...)
	tsNewAssignRe = regexp.MustCompile(`(?:this\.)?(\w+)\s*=\s*new\s+(?:\w+\.)?([A-Z]\w*)`)
	// const x: Type = ... / let x: Type = ...
	tsVarAnnRe = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*:\s*([^=\n]+?)\s*=`)
	// this.x = y (plain field assignment from a constructor parameter)
	tsSelfAssignRe = regexp.MustCompile(`this\.(\w+)\s*=\s*(\w+)\s*;`)
)

// tsBareType reduces a TS annotation to one indexable class name.
// Arrays, generics with arguments, unions, and primitives return "".
func tsBareType(ann string) string {
	ann = strings.TrimSpace(ann)
	// Strip a generic argument list: Map<K,V> narrows nothing, but
	// Transport<...> would still be a Transport — keep the head only when
	// the whole annotation is one generic application.
	if i := strings.IndexByte(ann, '<'); i > 0 && strings.HasSuffix(ann, ">") {
		ann = ann[:i]
	}
	if strings.ContainsAny(ann, "<>[]|&(){}, ") {
		return ""
	}
	if i := strings.LastIndexByte(ann, '.'); i >= 0 {
		ann = ann[i+1:]
	}
	if ann == "" || ann[0] < 'A' || ann[0] > 'Z' {
		return ""
	}
	return ann
}

// tsBaseClasses parses base classes from "class X extends Y implements Z {".
func tsBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	var chosen *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name != className || cand.Kind != core.KindClass {
			continue
		}
		if dirOf(cand.FilePath) == preferDir {
			chosen = cand
			break
		}
		if chosen == nil {
			chosen = cand
		}
	}
	if chosen == nil {
		return nil
	}
	sig := chosen.Signature
	i := strings.Index(sig, "extends ")
	if i < 0 {
		return nil
	}
	rest := sig[i+len("extends "):]
	for _, stop := range []string{" implements ", "{"} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}
	base := tsBareType(rest)
	if base == "" {
		return nil
	}
	return []string{base}
}

// baseClassesFor dispatches base-class parsing per language.
func baseClassesFor(idx *edgeIndex, language, className, preferDir string) []string {
	switch language {
	case "python":
		return pyBaseClasses(idx, className, preferDir)
	case "typescript", "javascript", "java":
		return tsBaseClasses(idx, className, preferDir)
	}
	return nil
}

// tsLocalTypes infers identifier → type name for one TS/JS callable.
func tsLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Class fields, own class then ancestors (lowest precedence).
	if symbol.Kind == core.KindMethod || symbol.Kind == core.KindConstructor {
		seen := map[string]bool{}
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, className := range classes {
				if className == "" || seen[className] {
					continue
				}
				seen[className] = true
				tsClassFieldTypes(idx, className, out)
				next = append(next, tsBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}

	// Parameter annotations from the declaration's own parens.
	if params := tsDeclParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			g = strings.TrimSpace(g)
			name, ann, ok := strings.Cut(g, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimLeft(name, ". "), "?"))
			name = strings.TrimSpace(strings.TrimPrefix(name, "readonly "))
			for _, mod := range []string{"public ", "private ", "protected "} {
				name = strings.TrimPrefix(name, mod)
			}
			if eq := strings.Index(ann, "="); eq >= 0 {
				ann = ann[:eq]
			}
			if t := tsBareType(ann); t != "" && name != "" && !strings.ContainsAny(name, "{[ ") {
				out[name] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range tsVarAnnRe.FindAllStringSubmatch(body, -1) {
			if t := tsBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
		for _, m := range tsNewAssignRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				out[m[1]] = m[2]
			}
		}
	}
	delete(out, "this")
	delete(out, "_")
	return out
}

// tsClassFieldTypes records one class's field types from indexed field
// symbols, its constructor's parameter properties, and this.x = new T()
// assignments. Existing entries win (closer classes shadow ancestors).
func tsClassFieldTypes(idx *edgeIndex, className string, out map[string]string) {
	record := func(name, typ string) {
		if _, exists := out[name]; !exists && typ != "" {
			out[name] = typ
		}
	}
	// No byParent index exists; class members live in the class's file.
	var classFile string
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name == className && cand.Kind == core.KindClass {
			classFile = cand.FilePath
			break
		}
	}
	if classFile == "" {
		return
	}
	for _, member := range idx.byFile[classFile] {
		if member.ParentSymbol != className {
			continue
		}
		switch member.Kind {
		case core.KindField:
			if m := tsFieldSigRe.FindStringSubmatch(member.Signature); m != nil && m[1] == member.Name {
				record(member.Name, tsBareType(m[2]))
			}
		case core.KindConstructor:
			body := stripCommentsAndStrings(member.RawText)
			for _, m := range tsCtorPropRe.FindAllStringSubmatch(body, -1) {
				record(m[1], tsBareType(m[2]))
			}
			for _, m := range tsNewAssignRe.FindAllStringSubmatch(body, -1) {
				if typeSymbolExists(idx, m[2]) {
					record(m[1], m[2])
				}
			}
			// this.transport = transport — a plain assignment from a typed
			// constructor parameter carries the parameter's annotation.
			ctorParams := map[string]string{}
			if params := tsDeclParams(member.RawText); params != "" {
				for _, g := range splitTopLevel(params, ',') {
					name, ann, ok := strings.Cut(strings.TrimSpace(g), ":")
					if !ok {
						continue
					}
					if eq := strings.Index(ann, "="); eq >= 0 {
						ann = ann[:eq]
					}
					if t := tsBareType(ann); t != "" {
						ctorParams[strings.TrimSpace(name)] = t
					}
				}
			}
			for _, m := range tsSelfAssignRe.FindAllStringSubmatch(body, -1) {
				if t, ok := ctorParams[m[2]]; ok {
					record(m[1], t)
				}
			}
		}
	}
}

// tsDeclParams extracts the parameter list from a TS/JS declaration's raw
// text by scanning the first balanced paren group.
func tsDeclParams(rawText string) string {
	start := strings.IndexByte(rawText, '(')
	if start < 0 {
		return ""
	}
	depth := 0
	for k := start; k < len(rawText); k++ {
		switch rawText[k] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return rawText[start+1 : k]
			}
		}
	}
	return ""
}
