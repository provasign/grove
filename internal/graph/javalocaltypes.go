package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Java local type inference: parameter declarations ("final CharSequence
// seq" — type precedes name), typed locals, and field declarations from the
// class body. Same shallow, harness-bounded approach as the other languages.

var (
	// Type x = ... (typed local; also matches enhanced-for "for (Type x :")
	javaLocalDeclRe = regexp.MustCompile(`\b([A-Z]\w*)(?:<[^<>]*>)?(?:\[\])?\s+(\w+)\s*[=:)]`)
	// typed local including primitives and arrays, for overload matching:
	// anchored to statement starts so "return x =" / cast fragments can't
	// masquerade as declarations
	javaTypedLocalRe = regexp.MustCompile(`(?m)(?:^|[;{)]\s*)\s*(?:final\s+)?((?:boolean|byte|char|short|int|long|float|double|[A-Z][\w.]*)(?:<[^<>]*>)?(?:\[\])?)\s+(\w+)\s*=`)
	// field declaration line in a class body
	javaFieldRe = regexp.MustCompile(`(?m)^\s+(?:(?:public|private|protected|static|final|transient|volatile)\s+)*([A-Z]\w*)(?:<[^<>]*>)?(?:\[\])?\s+(\w+)\s*[;=]`)
)

// javaArgTypes infers identifier → raw type token (primitives and arrays
// preserved: "long[]", "int") for overload matching, from parameters and
// typed locals. Distinct from javaLocalTypes, which normalizes to indexable
// class names for receiver narrowing.
func javaArgTypes(symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}
	record := func(typ, name string) {
		typ = strings.TrimSpace(typ)
		if i := strings.IndexByte(typ, '<'); i > 0 {
			typ = typ[:i] + typ[strings.IndexByte(typ, '>')+1:]
		}
		typ = strings.ReplaceAll(typ, "...", "[]")
		if j := strings.LastIndexByte(typ, '.'); j >= 0 {
			arr := strings.HasSuffix(typ, "[]")
			typ = strings.TrimSuffix(typ[j+1:], "[]")
			if arr {
				typ += "[]"
			}
		}
		if typ != "" && name != "" {
			out[name] = typ
		}
	}
	if params := tsDeclParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			fields := strings.Fields(strings.TrimSpace(g))
			for len(fields) > 2 || (len(fields) == 2 && (fields[0] == "final" || strings.HasPrefix(fields[0], "@"))) {
				fields = fields[1:]
			}
			if len(fields) == 2 {
				record(fields[0], fields[1])
			}
		}
	}
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		if i := strings.IndexByte(body, '{'); i >= 0 {
			body = body[i+1:]
		}
		for _, m := range javaTypedLocalRe.FindAllStringSubmatch(body, -1) {
			record(m[1], m[2])
		}
	}
	return out
}

// javaParamTypes parses a candidate's declared parameter type tokens.
func javaParamTypes(s *core.SymbolRecord) []string {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = s.RawText
	}
	params := tsDeclParams(src)
	if params == "" {
		return nil
	}
	var out []string
	for _, g := range splitTopLevel(params, ',') {
		fields := strings.Fields(strings.TrimSpace(g))
		for len(fields) > 2 || (len(fields) == 2 && (fields[0] == "final" || strings.HasPrefix(fields[0], "@"))) {
			fields = fields[1:]
		}
		if len(fields) != 2 {
			return nil
		}
		typ := fields[0]
		if i := strings.IndexByte(typ, '<'); i > 0 && strings.Contains(typ, ">") {
			typ = typ[:i] + typ[strings.LastIndexByte(typ, '>')+1:]
		}
		typ = strings.ReplaceAll(typ, "...", "[]")
		if j := strings.LastIndexByte(strings.TrimSuffix(typ, "[]"), '.'); j >= 0 {
			arr := strings.HasSuffix(typ, "[]")
			typ = strings.TrimSuffix(typ, "[]")[j+1:]
			if arr {
				typ += "[]"
			}
		}
		out = append(out, typ)
	}
	return out
}

// narrowOverloadsByArgTypes drops candidates whose declared parameter types
// CONFLICT with known argument types. Candidates stay when evidence is
// neutral: unparseable params, varargs, type variables, and widening-prone
// supertypes (a String argument legally binds a CharSequence parameter).
// If everything conflicts, nothing is dropped — conflict evidence narrows,
// its absence never decides.
func narrowOverloadsByArgTypes(cands []*core.SymbolRecord, args []string, argTypes map[string]string) []*core.SymbolRecord {
	if len(cands) < 2 || len(args) == 0 || len(argTypes) == 0 {
		return cands
	}
	var kept []*core.SymbolRecord
	for _, cand := range cands {
		paramTypes := javaParamTypes(cand)
		if paramTypes == nil || len(paramTypes) != len(args) {
			kept = append(kept, cand) // varargs or unparseable: neutral
			continue
		}
		conflict := false
		for i, argName := range args {
			if argName == "" {
				continue
			}
			var argType string
			if argName[0] == '#' {
				argType = javaNormalizeTypeToken(argName[1:])
			} else if strings.HasPrefix(argName, "call:") {
				t, known := argTypes[argName] // pre-resolved return type
				if !known {
					continue
				}
				argType = t
			} else {
				t, known := argTypes[argName]
				if !known {
					continue
				}
				argType = t
			}
			if paramTypes[i] != argType && !javaWildcardParam(paramTypes[i], cand) &&
				!javaLiteralCompatible(argType, paramTypes[i], argName[0] == '#') {
				conflict = true
				break
			}
		}
		if !conflict {
			kept = append(kept, cand)
		}
	}
	if len(kept) == 0 {
		return cands
	}
	return kept
}

// javaNormalizeTypeToken reduces a cast/declared type token to the bare
// comparison form used by javaParamTypes (last dotted segment, arrays kept).
func javaNormalizeTypeToken(t string) string {
	t = strings.TrimSpace(t)
	arr := strings.HasSuffix(t, "[]")
	t = strings.TrimSuffix(t, "[]")
	if j := strings.LastIndexByte(t, '.'); j >= 0 {
		t = t[j+1:]
	}
	if arr {
		t += "[]"
	}
	return t
}

// javaResolveCallReturnTypes resolves "call:name" argument markers to the
// called function's declared return type, when the name resolves to
// declarations that all agree on it.
func javaResolveCallReturnTypes(idx *edgeIndex, args []string, scope map[string]struct{}, argTypes map[string]string) {
	for _, a := range args {
		if !strings.HasPrefix(a, "call:") {
			continue
		}
		if _, done := argTypes[a]; done {
			continue
		}
		name := a[len("call:"):]
		ret := ""
		agree := true
		for _, cand := range idx.byName[strings.ToLower(name)] {
			if cand.Name != name {
				continue
			}
			if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod {
				continue
			}
			if _, ok := scope[cand.FilePath]; !ok {
				continue
			}
			r := javaReturnType(cand)
			if r == "" {
				agree = false
				break
			}
			if ret == "" {
				ret = r
			} else if ret != r {
				agree = false
				break
			}
		}
		if agree && ret != "" {
			argTypes[a] = ret
		}
	}
}

// javaCallResultType resolves a "name()" qualifier to the named function's
// declared return type, when all in-scope declarations agree.
func javaCallResultType(idx *edgeIndex, qualifier string, scope map[string]struct{}) string {
	name := strings.TrimSuffix(qualifier, "()")
	ret := ""
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name {
			continue
		}
		if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		r := javaReturnType(cand)
		if r == "" {
			return ""
		}
		if ret == "" {
			ret = r
		} else if ret != r {
			return ""
		}
	}
	return ret
}

// javaReturnType parses the declared return type token from a method
// signature ("public static boolean[] clone(final boolean[] array)").
func javaReturnType(s *core.SymbolRecord) string {
	src := s.Signature
	if !strings.Contains(src, "(") {
		src = s.RawText
	}
	head := src
	if i := strings.IndexByte(head, '('); i >= 0 {
		head = head[:i]
	}
	fields := strings.Fields(head)
	if len(fields) < 2 {
		return ""
	}
	// last field is the method name; the one before it is the return type
	typ := fields[len(fields)-2]
	switch typ {
	case "public", "private", "protected", "static", "final", "void", "abstract", "synchronized", "native", "default":
		return ""
	}
	if strings.HasPrefix(typ, "<") || strings.HasPrefix(typ, "@") {
		return ""
	}
	if i := strings.IndexByte(typ, '<'); i > 0 {
		end := strings.LastIndexByte(typ, '>')
		if end > i {
			typ = typ[:i] + typ[end+1:]
		} else {
			return ""
		}
	}
	return javaNormalizeTypeToken(typ)
}

// javaLiteralCompatible reports whether a literal of type lit can bind a
// parameter of type param through Java's implicit conversions (widening,
// boxing). Identifier-typed args use exact matching; literals widen.
func javaLiteralCompatible(lit, param string, isLiteral bool) bool {
	if !isLiteral {
		return false
	}
	compat := map[string][]string{
		"int":     {"int", "long", "float", "double", "short", "byte", "Integer"},
		"long":    {"long", "float", "double", "Long"},
		"float":   {"float", "double", "Float"},
		"double":  {"double", "Double"},
		"char":    {"char", "int", "long", "float", "double", "Character"},
		"boolean": {"boolean", "Boolean"},
		"String":  {"String"},
	}
	for _, ok := range compat[lit] {
		if param == ok {
			return true
		}
	}
	return false
}

// javaWildcardParam reports whether a parameter type can legally bind many
// argument types: generic type variables and the supertypes overload sets
// commonly widen through.
func javaWildcardParam(paramType string, cand *core.SymbolRecord) bool {
	bare := strings.TrimSuffix(paramType, "[]")
	if len(bare) == 1 && bare[0] >= 'A' && bare[0] <= 'Z' {
		return true // type variable by convention (T, K, V, E...)
	}
	for _, tp := range cand.TypeParameters {
		if tp == bare {
			return true
		}
	}
	switch bare {
	case "Object", "CharSequence", "Number", "Comparable", "Iterable", "Collection", "Map":
		return true
	}
	return false
}

// javaBareType reduces a Java type token to an indexable class name.
func javaBareType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i > 0 {
		t = t[:i]
	}
	t = strings.TrimSuffix(t, "[]")
	t = strings.TrimSuffix(t, "...")
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	if t == "" || strings.ContainsAny(t, "<>[]() ,") || t[0] < 'A' || t[0] > 'Z' {
		return ""
	}
	return t
}

// javaLocalTypes infers identifier → type for one Java callable.
func javaLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Fields, own class then ancestors (lowest precedence).
	if symbol.ParentSymbol != "" {
		seen := map[string]bool{}
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, className := range classes {
				if className == "" || seen[className] {
					continue
				}
				seen[className] = true
				for _, cls := range idx.byName[strings.ToLower(className)] {
					if cls.Name != className || cls.RawText == "" {
						continue
					}
					switch cls.Kind {
					case core.KindClass, core.KindEnum, core.KindInterface:
					default:
						continue
					}
					for _, m := range javaFieldRe.FindAllStringSubmatch(cls.RawText, -1) {
						if t := javaBareType(m[1]); t != "" {
							if _, exists := out[m[2]]; !exists {
								out[m[2]] = t
							}
						}
					}
					break
				}
				next = append(next, tsBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}

	// Parameters: "Type name" pairs from the declaration's paren group.
	if params := tsDeclParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			fields := strings.Fields(strings.TrimSpace(g))
			// Drop modifiers and annotations: "final @Nullable CharSequence seq"
			for len(fields) > 2 || (len(fields) == 2 && (fields[0] == "final" || strings.HasPrefix(fields[0], "@"))) {
				fields = fields[1:]
			}
			if len(fields) != 2 {
				continue
			}
			if t := javaBareType(fields[0]); t != "" {
				out[fields[1]] = t
			}
		}
	}

	// Typed locals in the body (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		// Skip the declaration header so parameters aren't re-parsed with
		// the wrong regex.
		if i := strings.IndexByte(body, '{'); i >= 0 {
			body = body[i+1:]
		}
		for _, m := range javaLocalDeclRe.FindAllStringSubmatch(body, -1) {
			if t := javaBareType(m[1]); t != "" {
				out[m[2]] = t
			}
		}
	}
	delete(out, "this")
	return out
}
