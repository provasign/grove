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
	// The generic group tolerates one nesting level (JsonDeserializer<Enum<?>>,
	// Map<String, List<T>>) — regexes can't balance arbitrarily, one level
	// covers real declarations.
	javaLocalDeclRe = regexp.MustCompile(`\b([A-Z]\w*)(?:<[^<>]*(?:<[^<>]*>[^<>]*)*>)?(?:\[\])?\s+(\w+)\s*[=:)]`)
	// typed local including primitives and arrays, for overload matching:
	// anchored to statement starts so "return x =" / cast fragments can't
	// masquerade as declarations
	javaTypedLocalRe = regexp.MustCompile(`(?m)(?:^|[;{)]\s*)\s*(?:final\s+)?((?:boolean|byte|char|short|int|long|float|double|[A-Z][\w.]*)(?:<[^<>]*>)?(?:\[\])?)\s+(\w+)\s*=`)
	// field declaration line in a class body
	javaFieldRe = regexp.MustCompile(`(?m)^\s+(?:(?:public|private|protected|static|final|transient|volatile)\s+)*([A-Z]\w*)(?:<[^<>]*(?:<[^<>]*>[^<>]*)*>)?(?:\[\])?\s+(\w+)\s*[;=]`)
	// field declaration line, primitives included, raw type token kept —
	// for overload matching (AT_SIGN is a char; javaFieldRe skips it)
	javaFieldArgRe = regexp.MustCompile(`(?m)^\s+(?:(?:public|private|protected|static|final|transient|volatile)\s+)*((?:boolean|byte|char|short|int|long|float|double|[A-Z]\w*)(?:<[^<>]*(?:<[^<>]*>[^<>]*)*>)?(?:\[\])?)\s+(\w+)\s*[;=]`)
)

// javaArgTypes infers identifier → raw type token (primitives and arrays
// preserved: "long[]", "int") for overload matching, from parameters and
// typed locals. Distinct from javaLocalTypes, which normalizes to indexable
// class names for receiver narrowing.
func javaArgTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
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
	if params := tsDeclParams(javaDeclSource(symbol)); params != "" {
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
	// Fields of the enclosing class (then its ancestors), lowest
	// precedence: `.append(AT_SIGN)` where `private static final char
	// AT_SIGN` picks append(char) over the other dozen overloads.
	if idx != nil && symbol.ParentSymbol != "" {
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
					if cls.Name != className {
						continue
					}
					switch cls.Kind {
					case core.KindClass, core.KindEnum, core.KindInterface:
					default:
						continue
					}
					// Indexed field symbols, not a regex over the class
					// text: that also matched method locals and typed a
					// caller's `ctor` from an unrelated method's local.
					for _, f := range idx.byFile[cls.FilePath] {
						if f.Kind != core.KindField || f.ParentSymbol != className {
							continue
						}
						if _, exists := out[f.Name]; exists {
							continue
						}
						if m := javaFieldArgRe.FindStringSubmatch(" " + strings.TrimSpace(f.RawText)); m != nil && m[2] == f.Name {
							record(m[1], m[2])
						}
					}
					break
				}
				next = append(next, tsBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}
	return out
}

// javaDeclSource returns the text to parse a Java declaration's head from,
// with leading annotations removed. `@SuppressWarnings("unchecked")` on the
// line before the method made the annotation's parens the "parameter
// list" (and the Signature — the first line — was the annotation alone),
// so every such method parsed as ("unchecked") and slipped through
// overload narrowing as neutral. Prefers the raw text; falls back to the
// signature when the raw text is absent.
func javaDeclSource(s *core.SymbolRecord) string {
	src := s.RawText
	if src == "" {
		src = s.Signature
	}
	for {
		trimmed := strings.TrimLeft(src, " \t\r\n")
		// Leading comments (a Javadoc "(may be {@code null})" has parens
		// too) go the same way as annotations.
		if strings.HasPrefix(trimmed, "/*") {
			end := strings.Index(trimmed, "*/")
			if end < 0 {
				return trimmed
			}
			src = trimmed[end+2:]
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			nl := strings.IndexByte(trimmed, '\n')
			if nl < 0 {
				return trimmed
			}
			src = trimmed[nl+1:]
			continue
		}
		if !strings.HasPrefix(trimmed, "@") {
			return trimmed
		}
		i := 1
		for i < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == '_' || trimmed[i] == '$' ||
			(trimmed[i] >= 'a' && trimmed[i] <= 'z') || (trimmed[i] >= 'A' && trimmed[i] <= 'Z') || (trimmed[i] >= '0' && trimmed[i] <= '9')) {
			i++
		}
		j := i
		for j < len(trimmed) && (trimmed[j] == ' ' || trimmed[j] == '\t') {
			j++
		}
		if j < len(trimmed) && trimmed[j] == '(' {
			depth := 0
			var quote byte
			k := j
			for ; k < len(trimmed); k++ {
				c := trimmed[k]
				if quote != 0 {
					if c == '\\' {
						k++
					} else if c == quote {
						quote = 0
					}
					continue
				}
				switch c {
				case '"', '\'':
					quote = c
				case '(':
					depth++
				case ')':
					depth--
				}
				if depth == 0 && c == ')' {
					break
				}
			}
			if k >= len(trimmed) {
				return trimmed
			}
			i = k + 1
		}
		src = trimmed[i:]
	}
}

// javaParamTypes parses a candidate's declared parameter type tokens.
func javaParamTypes(s *core.SymbolRecord) []string {
	src := javaDeclSource(s)
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
	var kept, exact, unparsed []*core.SymbolRecord
	for _, cand := range cands {
		paramTypes := javaParamTypes(cand)
		if paramTypes == nil {
			kept = append(kept, cand) // unparseable: neutral
			unparsed = append(unparsed, cand)
			continue
		}
		variadic := javaVariadic(cand)
		if len(paramTypes) != len(args) && !(variadic && len(args) >= len(paramTypes)-1) {
			kept = append(kept, cand) // arity mismatch only argc lets through: neutral
			continue
		}
		conflict := false
		allExact := !variadic // javac binds varargs last (phase 3): never "exact"
		for i, argName := range args {
			pi := i
			if pi >= len(paramTypes) {
				pi = len(paramTypes) - 1 // extra varargs elements bind the last param
			}
			if argName == "" {
				allExact = false
				continue
			}
			var argType string
			if argName[0] == '#' {
				argType = javaNormalizeTypeToken(argName[1:])
			} else if strings.HasPrefix(argName, "call:") {
				t, known := argTypes[argName] // pre-resolved return type
				if !known {
					allExact = false
					continue
				}
				argType = t
			} else {
				t, known := argTypes[argName]
				if !known {
					allExact = false
					continue
				}
				argType = t
			}
			if paramTypes[pi] == argType {
				continue
			}
			if javaBoxingPair(argType, paramTypes[pi]) {
				allExact = false // Integer → int binds, but only after phase 1
				continue
			}
			if variadic && pi == len(paramTypes)-1 && paramTypes[pi] == argType+"[]" {
				// insert(0, array, element) binds insert(int, byte[], byte...):
				// a lone element is a legal varargs call, not a conflict.
				continue
			}
			allExact = false
			if javaShapeMismatch(argType, paramTypes[pi], cand) ||
				(argType != "lambda" && !javaWildcardParam(paramTypes[pi], cand) &&
					!javaLiteralCompatible(argType, paramTypes[pi], argName[0] == '#')) {
				conflict = true
				break
			}
		}
		if !conflict {
			kept = append(kept, cand)
			if allExact {
				exact = append(exact, cand)
			}
		}
	}
	if len(kept) == 0 {
		return cands
	}
	// javac's first applicability phase (no boxing, no varargs) wins when it
	// finds a match: an overload whose every parameter equals the known
	// argument types beats the wildcard/boxing-compatible siblings ON THE
	// SAME TYPE (clone(float[]) over clone(T[]); append(String, boolean)
	// over append(String, Object)). Overloads live per declaring type and
	// receiver narrowing runs after this, so an exact match on Strings must
	// not evict StringUtils.isEmpty(CharSequence). Unparseable candidates
	// ride along — they might be the exact one.
	if len(exact) == 0 {
		return kept
	}
	exactOn := map[string]bool{}
	for _, c := range exact {
		exactOn[c.ParentSymbol] = true
	}
	isExact := map[*core.SymbolRecord]bool{}
	for _, c := range exact {
		isExact[c] = true
	}
	isUnparsed := map[*core.SymbolRecord]bool{}
	for _, c := range unparsed {
		isUnparsed[c] = true
	}
	var out []*core.SymbolRecord
	for _, c := range kept {
		if isExact[c] || isUnparsed[c] || !exactOn[c.ParentSymbol] {
			out = append(out, c)
		}
	}
	return out
}

// javaPrimitiveArrayMismatch reports the one widening that never happens:
// a primitive array (int[]) is not an Object[] and cannot instantiate a
// type-variable array (T[]), so it conflicts with any reference-array
// parameter — the wildcard rule would otherwise let clone(T[]) and
// indexOf(Object[], Object) absorb every primitive-array call.
// javaVariadic reports whether the callable's last parameter is declared
// with "..."; javaParamTypes flattens it to "[]" for comparison.
func javaVariadic(s *core.SymbolRecord) bool {
	return strings.Contains(tsDeclParams(javaDeclSource(s)), "...")
}

// javaShapeMismatch reports bindings the wildcard rule must not rescue:
// an array argument binds only an array, Object, or a type variable (never
// Collection/Iterable/CharSequence — String[] is not a Collection); a
// known non-array reference (Object, String, Set) never binds an array
// parameter; a lambda binds only a functional interface — no primitive,
// array, or String slot.
func javaShapeMismatch(argType, paramType string, cand *core.SymbolRecord) bool {
	if javaPrimitiveArrayMismatch(argType, paramType) {
		return true
	}
	argArr := strings.HasSuffix(argType, "[]")
	parArr := strings.HasSuffix(paramType, "[]")
	if argType == "lambda" {
		return parArr || javaIsPrimitive(paramType) || paramType == "String"
	}
	if argArr && !parArr {
		return paramType != "Object" && !javaTypeVariable(paramType, cand)
	}
	if !argArr && parArr && !javaIsPrimitive(argType) {
		return !javaTypeVariable(argType, cand) // a T argument may itself be an array
	}
	return false
}

// javaBoxingPair reports a primitive/wrapper pair (int/Integer) in either
// direction — legal through boxing, so never a conflict; not exact either.
func javaBoxingPair(a, b string) bool {
	box := map[string]string{"int": "Integer", "long": "Long", "short": "Short", "byte": "Byte",
		"char": "Character", "float": "Float", "double": "Double", "boolean": "Boolean"}
	return box[a] == b || box[b] == a
}

// javaTypeVariable reports whether t is a type variable: declared on the
// candidate, or a bare single capital by convention.
func javaTypeVariable(t string, cand *core.SymbolRecord) bool {
	bare := strings.TrimSuffix(t, "[]")
	if len(bare) == 1 && bare[0] >= 'A' && bare[0] <= 'Z' {
		return true
	}
	for _, tp := range cand.TypeParameters {
		if tp == bare {
			return true
		}
	}
	return false
}

func javaPrimitiveArrayMismatch(argType, paramType string) bool {
	if !strings.HasSuffix(argType, "[]") || !strings.HasSuffix(paramType, "[]") {
		return false
	}
	if !javaIsPrimitive(strings.TrimSuffix(argType, "[]")) {
		return false
	}
	return !javaIsPrimitive(strings.TrimSuffix(paramType, "[]"))
}

func javaIsPrimitive(t string) bool {
	switch t {
	case "int", "long", "short", "byte", "char", "float", "double", "boolean":
		return true
	}
	return false
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
		} else if ret == "" && agree {
			// No in-repo declaration: a handful of JDK methods have one
			// return type everywhere (getName/toString → String, size →
			// int, invoke/get → Object). Only names whose type is the same
			// on every JDK class are listed.
			if t, ok := jdkReturnTypes[name]; ok {
				argTypes[a] = t
			}
		}
	}
}

var jdkReturnTypes = map[string]string{
	"getName": "String", "toString": "String", "getSimpleName": "String", "getMessage": "String",
	"toLowerCase": "String", "toUpperCase": "String", "trim": "String", "substring": "String",
	"name": "String", "getClass": "Class", "size": "int", "length": "int", "hashCode": "int",
	"indexOf": "int", "ordinal": "int", "invoke": "Object", "getKey": "Object", "getValue": "Object",
	"isEmpty": "boolean", "equals": "boolean", "contains": "boolean", "startsWith": "boolean",
	"endsWith": "boolean", "matches": "boolean", "hasNext": "boolean", "charAt": "char",
	"intValue": "int", "longValue": "long", "doubleValue": "double", "booleanValue": "boolean",
	"getBytes": "byte[]", "toCharArray": "char[]", "getType": "Class", "getReturnType": "Class",
	"getParameterTypes": "Class[]", "getDeclaringClass": "Class", "getModifiers": "int",
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
	src := javaDeclSource(s)
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
	if params := tsDeclParams(javaDeclSource(symbol)); params != "" {
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
