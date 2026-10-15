package graph

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// C# local type inference, same shallow altitude as the Java/Rust passes:
// signature parameters (Type name), typed locals (Type x = / Type x;),
// var-with-constructor (var x = new Type()), foreach bindings, and the
// containing class's fields/properties. C# shares Java's "Type name" syntax,
// so the Java field/param/local regexes are reused; the C#-specific shapes
// (var x = new T, PascalCase auto-properties) get dedicated patterns.

var (
	// var x = new Foo(...) / var x = new Foo<T> { ... }
	csharpVarNewRe = regexp.MustCompile(`(?m)\bvar\s+(\w+)\s*=\s*new\s+([A-Z]\w*)`)
	// var o = (JObject)token;
	csharpVarCastRe = regexp.MustCompile(`(?m)\bvar\s+(\w+)\s*=\s*\(\s*([A-Z][\w.]*(?:<(?:[^<>]|<(?:[^<>]|<[^<>]*>)*>)*>)?)\??\s*\)\s*[\w(]`)
	// var o = token as JObject;
	csharpVarAsRe = regexp.MustCompile(`(?m)\bvar\s+(\w+)\s*=\s*[^;]*?\bas\s+([A-Z][\w.]*(?:<(?:[^<>]|<(?:[^<>]|<[^<>]*>)*>)*>)?)\??\s*;`)
	// var j = JObject.Parse(...); / var b = HexToBytes(...);
	csharpVarCallRe = regexp.MustCompile(`(?m)\bvar\s+(\w+)\s*=\s*(?:([A-Z]\w*)\.)?([A-Z]\w*)\s*(?:<[^<>]*>)?\s*\(`)
	// Type x = ... / Type x; / for (Type x : — uppercase-initial type
	csharpLocalDeclRe = regexp.MustCompile(`(?m)(?:^|[;{}()]\s*)\s*([A-Z]\w*)(?:<(?:[^<>]|<(?:[^<>]|<[^<>]*>)*>)*>)?\??(?:\[\])?\??\s+(\w+)\s*[=;)]`)
	// foreach (Type x in ...)
	csharpForeachRe = regexp.MustCompile(`\bforeach\s*\(\s*(?:var|([A-Z]\w*)(?:<(?:[^<>]|<(?:[^<>]|<[^<>]*>)*>)*>)?\??)\s+(\w+)\s+in\b`)
	// if (value is Type name) / while (... is Type name)
	csharpIsPatternRe = regexp.MustCompile(`\bis\s+([A-Z]\w*)(?:<[^<>]*>)?\s+([a-z_]\w*)\b`)
	// auto-property / field: "public Foo Bar { ..." or "Foo _bar ;|="
	csharpFieldRe = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|static|readonly|virtual|override|sealed|abstract|new|volatile|const|required)\s+)*([A-Z]\w*)(?:<(?:[^<>]|<(?:[^<>]|<[^<>]*>)*>)*>)?\??(?:\[\])?\??\s+(\w+)\s*[{;=]`)
)

// csharpLocalTypes infers identifier → indexable type name for one C#
// callable.
func csharpLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Fields/properties of the class and its ancestors (lowest precedence).
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
				for _, cls := range csharpTypeFragments(idx, className, dirOf(symbol.FilePath)) {
					// Top-level members only (classBody): a method's local
					// `Foo helper = ...` or a commented-out field is not one.
					for _, m := range csharpFieldRe.FindAllStringSubmatch(classBody(idx, cls), -1) {
						if t := csBareType(m[1]); t != "" && csTypedLocal(idx, t) {
							if _, exists := out[m[2]]; !exists {
								out[m[2]] = t
							}
						}
					}
				}
				next = append(next, csBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}

	// Parameters: "Type name" pairs from the declaration's paren group.
	if params := tsDeclParams(symbol.Language, symbol.RawText); params != "" {
		for _, g := range splitParams(symbol.Language, params) {
			fields := strings.Fields(strings.TrimSpace(g))
			// Drop C# parameter modifiers and attributes.
			for len(fields) > 2 || (len(fields) == 2 && csharpParamModifier(fields[0])) {
				fields = fields[1:]
			}
			if len(fields) != 2 {
				continue
			}
			if t := csBareType(fields[0]); t != "" && csTypedLocal(idx, t) {
				out[fields[1]] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := maskCode(symbol.Language, symbol.RawText)
		if i := strings.IndexByte(body, '{'); i >= 0 {
			body = body[i+1:]
		}
		for _, m := range csharpForeachRe.FindAllStringSubmatch(body, -1) {
			if t := csBareType(m[1]); t != "" && csTypedLocal(idx, t) {
				out[m[2]] = t
			}
		}
		for _, m := range csharpLocalDeclRe.FindAllStringSubmatch(body, -1) {
			if t := csBareType(m[1]); t != "" && csTypedLocal(idx, t) {
				out[m[2]] = t
			}
		}
		for _, m := range csharpIsPatternRe.FindAllStringSubmatch(body, -1) {
			if t := csBareType(m[1]); t != "" && csTypedLocal(idx, t) {
				out[m[2]] = t
			}
		}
		// var x = (T)y / y as T / T.Make(...) / Make(...): the cast type, or
		// our method's declared return type (JObject.Parse -> JObject).
		for _, m := range csharpVarCastRe.FindAllStringSubmatch(body, -1) {
			if t := csNormalizeType(m[2]); typeSymbolExists(idx, t) {
				out[m[1]] = t
			}
		}
		for _, m := range csharpVarAsRe.FindAllStringSubmatch(body, -1) {
			if t := csNormalizeType(m[2]); typeSymbolExists(idx, t) {
				out[m[1]] = t
			}
		}
		for _, m := range csharpVarCallRe.FindAllStringSubmatch(body, -1) {
			name := m[3]
			var rets []string
			if owner := m[2]; owner != "" && typeSymbolExists(idx, owner) {
				rets = csOwnerMethodReturnTypes(idx, owner, name)
			} else if owner == "" {
				rets, _ = csProducerReturnTypes(idx, name)
			}
			if len(rets) == 1 && typeSymbolExists(idx, rets[0]) {
				out[m[1]] = rets[0]
			}
		}
		// var x = new Type() overrides any spurious local-decl capture.
		for _, m := range csharpVarNewRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
	}
	aliases := map[string]string{}
	for _, imp := range symbol.Imports {
		left, right, ok := strings.Cut(imp, "=")
		if !ok {
			continue
		}
		alias := strings.TrimSpace(left)
		target := csNormalizeType(strings.TrimSpace(right))
		if alias != "" && target != "" {
			aliases[alias] = target
		}
	}
	for name, typ := range out {
		if target := aliases[typ]; target != "" {
			out[name] = target
		}
	}
	delete(out, "this")
	delete(out, "_")
	return out
}

// csharpParamModifier reports C# parameter-list modifier keywords.
func csharpParamModifier(s string) bool {
	switch s {
	case "ref", "out", "in", "params", "this", "readonly", "scoped":
		return true
	}
	return strings.HasPrefix(s, "[") // attribute
}

// csOwnerMethodReturnTypes is csProducerReturnTypes restricted to the
// methods declared by owner.
func csOwnerMethodReturnTypes(idx *edgeIndex, owner, name string) []string {
	seen := map[string]bool{}
	var out []string
	for _, f := range idx.byName[strings.ToLower(name)] {
		if f.Name != name || f.Language != "csharp" || f.ParentSymbol != owner {
			continue
		}
		ret := csDeclaredReturnType(f.Signature, name)
		if ret == "" || ret == "void" || len(ret) <= 2 && strings.ToUpper(ret) == ret {
			return nil
		}
		if !seen[ret] {
			seen[ret] = true
			out = append(out, ret)
		}
	}
	return out
}

// csTypedLocal: a declared local/field type worth recording. A BCL type is
// recorded when no type of ours shares its name, so extension methods on
// it resolve (t.IsEnum() on a Type); collection interfaces stay out: LINQ
// look-alike extension sets (LinqBridge) cannot be told apart by arity.
func csTypedLocal(idx *edgeIndex, t string) bool {
	if !csharpKeyword(t) {
		return true
	}
	switch t {
	case "IEnumerable", "IList", "ICollection", "List", "Dictionary", "IDictionary", "Func", "Action", "Task", "ValueTask", "Nullable":
		return false
	}
	return !typeSymbolExists(idx, t)
}

// csharpKeyword rejects C# contextual/builtin tokens that look like
// PascalCase types but never resolve to an indexed declaration.
func csharpKeyword(t string) bool {
	switch t {
	case "Task", "ValueTask", "Action", "Func", "List", "Dictionary",
		"IEnumerable", "IList", "ICollection", "IDictionary", "Nullable",
		"Object", "String", "Boolean", "Int32", "Int64", "Type":
		// BCL types: not in our index, name collisions are noise.
		return true
	}
	return false
}

// ── Overload narrowing by argument types (C# edition of the Java pass) ──

var (
	// typed local with builtins: "string s =", "int? n;", "Foo[] xs ="
	csharpTypedLocalRe = regexp.MustCompile(`(?m)(?:^|[;{}()]\s*)\s*(?:readonly\s+)?((?:bool|byte|sbyte|char|short|ushort|int|uint|long|ulong|float|double|decimal|string|object|[A-Z][\w.]*)(?:<[^<>]*(?:<[^<>]*>[^<>]*)*>)?\??(?:\[\])?)\s+(\w+)\s*[=;)]`)
	// var s = "..." / var n = 1 / var x = new T(...)
	csharpVarLiteralRe = regexp.MustCompile(`(?m)\bvar\s+(\w+)\s*=\s*(new\s+([A-Z][\w.]*)|"|@"|\$"|'|(\d)|true\b|false\b)`)
	// field/property with builtins, raw type token kept
	csharpFieldArgRe = regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|readonly|virtual|override|sealed|abstract|new|volatile|const)\s+)*((?:bool|byte|sbyte|char|short|ushort|int|uint|long|ulong|float|double|decimal|string|object|[A-Z][\w.]*)(?:<[^<>]*(?:<[^<>]*>[^<>]*)*>)?\??(?:\[\])?)\s+(\w+)\s*[{;=]`)
)

// csNormalizeType reduces a C# type token to the comparison form: generics
// and nullable markers stripped, namespace dropped, BCL aliases folded onto
// the keyword (String → string, Int32 → int) so literal markers and
// declarations agree.
func csNormalizeType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i > 0 {
		end := strings.LastIndexByte(t, '>')
		if end > i {
			t = t[:i] + t[end+1:]
		} else {
			t = t[:i]
		}
	}
	arr := strings.HasSuffix(t, "[]")
	t = strings.TrimSuffix(t, "[]")
	t = strings.TrimSuffix(t, "?")
	if j := strings.LastIndexByte(t, '.'); j >= 0 {
		t = t[j+1:]
	}
	switch t {
	case "String":
		t = "string"
	case "Boolean", "boolean":
		t = "bool"
	case "Int32":
		t = "int"
	case "Int64":
		t = "long"
	case "Int16":
		t = "short"
	case "Byte":
		t = "byte"
	case "Char":
		t = "char"
	case "Double":
		t = "double"
	case "Single":
		t = "float"
	case "Decimal":
		t = "decimal"
	case "Object":
		t = "object"
	}
	if arr {
		t += "[]"
	}
	return t
}

// csDeclSource is javaDeclSource for C#: leading attributes ([Test],
// [TestCase(1)]) and comments removed so the first paren group is the
// parameter list.
func csDeclSource(s *core.SymbolRecord) string {
	src := s.RawText
	if src == "" {
		src = s.Signature
	}
	for {
		trimmed := strings.TrimLeft(src, " \t\r\n")
		switch {
		case strings.HasPrefix(trimmed, "/*"):
			end := strings.Index(trimmed, "*/")
			if end < 0 {
				return trimmed
			}
			src = trimmed[end+2:]
		case strings.HasPrefix(trimmed, "//"):
			nl := strings.IndexByte(trimmed, '\n')
			if nl < 0 {
				return trimmed
			}
			src = trimmed[nl+1:]
		case strings.HasPrefix(trimmed, "["):
			depth := 0
			k := 0
			for ; k < len(trimmed); k++ {
				if trimmed[k] == '[' {
					depth++
				} else if trimmed[k] == ']' {
					depth--
					if depth == 0 {
						break
					}
				}
			}
			if k >= len(trimmed) {
				return trimmed
			}
			src = trimmed[k+1:]
		default:
			return trimmed
		}
	}
}

// csParamTypes parses a C# callable's parameter type tokens (normalized)
// and whether the last one is a `params` array.
func csParamTypes(s *core.SymbolRecord) (types []string, variadic bool) {
	params := tsDeclParams("csharp", csDeclSource(s))
	if params == "" {
		return nil, false
	}
	for _, g := range splitParams("csharp", params) {
		g = strings.TrimSpace(g)
		if eq := strings.Index(g, "="); eq >= 0 {
			g = g[:eq] // default value
		}
		fields := strings.Fields(g)
		isParams, isThis := false, false
		for len(fields) > 2 || (len(fields) == 2 && csharpParamModifier(fields[0])) {
			if fields[0] == "params" {
				isParams = true
			}
			if fields[0] == "this" {
				isThis = true
			}
			fields = fields[1:]
		}
		if len(fields) != 2 {
			return nil, false
		}
		if isThis {
			continue // extension-method receiver: not an argument slot
		}
		types = append(types, csOverloadType(fields[0]))
		variadic = isParams
	}
	return types, variadic
}

func csOverloadType(typ string) string {
	typ = strings.TrimSpace(typ)
	nullable := strings.HasSuffix(typ, "?")
	typ = csNormalizeType(strings.TrimSuffix(typ, "?"))
	if nullable && typ != "" {
		typ += "?"
	}
	return typ
}

// csharpArgTypes infers identifier → normalized type token for overload
// matching: parameters, typed locals (builtins included), var-literal and
// var-new locals, then the class's fields and properties from the index.
func csharpArgTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}
	record := func(typ, name string) {
		if typ = csNormalizeType(typ); typ != "" && name != "" {
			out[name] = typ
		}
	}
	if params := tsDeclParams("csharp", csDeclSource(symbol)); params != "" {
		for _, g := range splitParams("csharp", params) {
			g = strings.TrimSpace(g)
			if eq := strings.Index(g, "="); eq >= 0 {
				g = g[:eq]
			}
			fields := strings.Fields(g)
			for len(fields) > 2 || (len(fields) == 2 && csharpParamModifier(fields[0])) {
				fields = fields[1:]
			}
			if len(fields) == 2 {
				record(fields[0], fields[1])
			}
		}
	}
	if symbol.RawText != "" {
		body := maskCode(symbol.Language, symbol.RawText)
		if i := strings.IndexByte(body, '{'); i >= 0 {
			body = body[i+1:]
		}
		for _, m := range csharpTypedLocalRe.FindAllStringSubmatch(body, -1) {
			if m[1] != "var" && m[1] != "return" && m[1] != "new" {
				record(m[1], m[2])
			}
		}
		for _, m := range csharpVarLiteralRe.FindAllStringSubmatch(body, -1) {
			switch {
			case m[3] != "":
				record(m[3], m[1])
			case strings.HasSuffix(m[2], "\""):
				record("string", m[1])
			case m[2] == "'":
				record("char", m[1])
			case m[4] != "":
				record("int", m[1])
			case m[2] == "true" || m[2] == "false":
				record("bool", m[1])
			}
		}
	}
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
				for _, cls := range csharpTypeFragments(idx, className, dirOf(symbol.FilePath)) {
					for _, f := range idx.byFile[cls.FilePath] {
						if f.Kind != core.KindField || f.ParentSymbol != className {
							continue
						}
						if _, exists := out[f.Name]; exists {
							continue
						}
						if m := csharpFieldArgRe.FindStringSubmatch(strings.TrimSpace(f.RawText)); m != nil && m[2] == f.Name {
							record(m[1], m[2])
						}
					}
				}
				next = append(next, csBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}
	delete(out, "this")
	return out
}

func csIsPrimitive(t string) bool {
	switch t {
	case "bool", "byte", "sbyte", "char", "short", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal":
		return true
	}
	return false
}

// csWildcardParam: object/dynamic, a type variable (declared, or the T-
// prefixed convention: T, TSource, TResult), or an interface (IEnumerable,
// IList — arrays and collections all bind those).
func csWildcardParam(p string, cand *core.SymbolRecord) bool {
	bare := strings.TrimSuffix(p, "[]")
	if bare == "object" || bare == "dynamic" {
		return true
	}
	if csTypeVariable(bare, cand) {
		return true
	}
	return len(bare) > 1 && bare[0] == 'I' && bare[1] >= 'A' && bare[1] <= 'Z'
}

func csTypeVariable(t string, cand *core.SymbolRecord) bool {
	for _, tp := range cand.TypeParameters {
		if tp == t {
			return true
		}
	}
	if len(t) == 1 && t[0] >= 'A' && t[0] <= 'Z' {
		return true
	}
	return len(t) > 1 && t[0] == 'T' && t[1] >= 'A' && t[1] <= 'Z'
}

// csLiteralCompatible: implicit conversions a literal marker allows.
func csLiteralCompatible(lit, param string) bool {
	compat := map[string][]string{
		"int":    {"int", "uint", "long", "ulong", "short", "ushort", "byte", "sbyte", "float", "double", "decimal"},
		"long":   {"long", "ulong", "float", "double", "decimal"},
		"float":  {"float", "double"},
		"double": {"double"},
		"char":   {"char", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal"},
		"bool":   {"bool"},
		"string": {"string"},
	}
	list, ok := compat[lit]
	if !ok {
		list = csNumericOrder[lit] // a cast marker: (byte)x is a typed byte
	}
	for _, ok := range list {
		if param == ok {
			return true
		}
	}
	return false
}

// csNarrowOverloads keeps the candidates whose parameter types do not
// conflict with the known argument types; an overload matching every known
// argument exactly beats its compatible siblings on the same declaring type
// (Roslyn's better-conversion rule, approximated). Mirrors
// narrowOverloadsByArgTypes; never zeroes the set.
func csNarrowOverloads(idx *edgeIndex, cands []*core.SymbolRecord, args []string, argTypes map[string]string) []*core.SymbolRecord {
	return csNarrowOverloadsGrouped(idx, cands, args, argTypes, true)
}

// csNarrowOverloadsGrouped is csNarrowOverloads; perParent=false ranks all
// candidates as one overload set (a receiver's members across its type
// hierarchy) instead of each declaring type separately.
func csNarrowOverloadsGrouped(idx *edgeIndex, cands []*core.SymbolRecord, args []string, argTypes map[string]string, perParent bool) []*core.SymbolRecord {
	group := func(c *core.SymbolRecord) string {
		if perParent {
			return c.ParentSymbol
		}
		return ""
	}
	if len(cands) < 2 {
		return cands
	}
	if len(args) == 0 {
		// `new JObject()`: the parameterless overload is the normal form
		// and beats a `params` sibling applied with zero elements.
		var plain []*core.SymbolRecord
		for _, c := range cands {
			if pt, v := csParamTypes(c); !v && len(pt) == 0 {
				plain = append(plain, c)
			}
		}
		if len(plain) > 0 {
			return plain
		}
		return cands
	}
	var kept, exact, unparsed []*core.SymbolRecord
	fullyKnown := map[*core.SymbolRecord]bool{} // every argument typed and compatible
	cost := map[*core.SymbolRecord]int{}        // literal-conversion distance (0 = exact)
	for _, cand := range cands {
		paramTypes, variadic := csParamTypes(cand)
		if paramTypes == nil {
			kept = append(kept, cand)
			unparsed = append(unparsed, cand)
			continue
		}
		if len(paramTypes) != len(args) && !(variadic && len(args) >= len(paramTypes)-1) {
			kept = append(kept, cand) // optional params let argc through: neutral
			continue
		}
		conflict := false
		allExact := !variadic
		allKnown := true
		for i, argName := range args {
			pi := i
			if pi >= len(paramTypes) {
				pi = len(paramTypes) - 1
			}
			if argName == "" {
				allExact = false
				allKnown = false
				continue
			}
			var argType string
			isLit := argName[0] == '#'
			switch {
			case isLit:
				argType = csOverloadType(argName[1:]) // keeps int? nullable
			case argName[0] == '%':
				// Formatting.Indented: typed only when Formatting is an
				// indexed enum; a static member's type is unknown.
				if t := argName[1:]; csIsEnum(idx, t) {
					argType = t
				} else {
					allExact = false
					allKnown = false
					continue
				}
			case strings.HasPrefix(argName, "call:"):
				// HexToBytes("4A") as an argument: typed by our method's
				// declared return type when it names exactly one.
				rets, _ := csProducerReturnTypes(idx, argName[len("call:"):])
				if len(rets) != 1 {
					allExact = false
					allKnown = false
					continue
				}
				argType = rets[0]
			default:
				t, known := argTypes[argName]
				if !known {
					allExact = false
					allKnown = false
					continue
				}
				argType = t
			}
			p := paramTypes[pi]
			if p == argType {
				continue
			}
			pNullable := strings.HasSuffix(p, "?")
			p = strings.TrimSuffix(p, "?")
			argNullable := strings.HasSuffix(argType, "?")
			argType = strings.TrimSuffix(argType, "?")
			if p == argType {
				allExact = false
				if argNullable && !pNullable {
					conflict = true
					break
				}
				// Nullable annotations on reference types do not change their
				// overload identity. Value types do: T is a better conversion
				// than lifting a non-null T to T?.
				if pNullable && csValueType(idx, p) {
					cost[cand]++
				}
				continue
			}
			// A `#Name` marker for a nested `new Name(...)` is a typed
			// object, not a literal: it binds like a typed identifier.
			if isLit && len(argType) > 0 && argType[0] >= 'A' && argType[0] <= 'Z' && !csIsPrimitive(argType) {
				isLit = false
			}
			if argNullable && !pNullable && p != "object" && csValueType(idx, argType) && !csWildcardParam(p, cand) {
				// int? converts to object (boxing) and to interfaces, never
				// to a non-nullable value type: JValue(int?) is not
				// JValue(long).
				conflict = true
				break
			}
			if csIsPrimitive(argType) && csIsPrimitive(p) {
				// Implicit numeric conversions (C# §10.2.3), ranked by the
				// better-conversion-target rule: JValue((uint)x) binds
				// JValue(long), JValue(b) for a byte binds JValue(long).
				r := csNumericRank(argType, p, isLit && !argNullable)
				if r < 0 {
					conflict = true
					break
				}
				cost[cand] += r
				allExact = false
				continue
			}
			switch {
			case isLit:
				cost[cand] += csLiteralRank(argType, p)
			case p == "object":
				// Boxing a value or widening to object is the worst
				// conversion: a base class or interface beats it
				// (JValue → JToken over JValue → object).
				cost[cand] += 20
			default:
				cost[cand]++ // a typed argument binding a wider parameter (JValue → JToken)
			}
			if variadic && pi == len(paramTypes)-1 && strings.HasSuffix(p, "[]") {
				// A lone element binds the params slot by exact type or
				// assignability (new BinaryConverter() into params
				// JsonConverter[]).
				if elem := strings.TrimSuffix(p, "[]"); elem == argType || csAssignable(idx, argType, elem) || csWildcardParam(elem, cand) || (isLit && csLiteralCompatible(argType, elem)) {
					allExact = false
					continue
				}
			}
			allExact = false
			argArr := strings.HasSuffix(argType, "[]")
			parArr := strings.HasSuffix(p, "[]")
			switch {
			case argType == "lambda":
				if parArr || csIsPrimitive(p) || p == "string" {
					conflict = true
				}
			case argArr && !parArr:
				if !csWildcardParam(p, cand) {
					conflict = true
				}
			case !argArr && parArr && !isLit:
				if !csTypeVariable(argType, cand) {
					conflict = true
				}
			case isLit:
				if !csLiteralCompatible(argType, p) && !csWildcardParam(p, cand) {
					conflict = true
				}
			default:
				if !csWildcardParam(p, cand) && !(csIsPrimitive(argType) && p == "object") && !csAssignable(idx, argType, p) {
					conflict = true
				}
			}
			if conflict {
				break
			}
		}
		if !conflict {
			kept = append(kept, cand)
			if allExact {
				exact = append(exact, cand)
			}
			if allKnown && !variadic {
				fullyKnown[cand] = true
			}
		}
	}
	if len(kept) == 0 {
		return cands
	}
	// A `params` overload is applicable only in expanded form: when a
	// normal-form sibling on the same type is applicable WITH EVERY
	// ARGUMENT KNOWN, it wins (JProperty(string, object) over
	// JProperty(string, params object[])). An untyped argument leaves both
	// — SerializeObject(x, converter) must keep the params overload.
	normalOn := map[string]bool{}
	for _, c := range kept {
		if fullyKnown[c] {
			normalOn[group(c)] = true
		}
	}
	var narrowed []*core.SymbolRecord
	for _, c := range kept {
		pt, v := csParamTypes(c)
		if v && normalOn[group(c)] {
			continue
		}
		if v && len(args) == len(pt)-1 && csNormalFormSibling(kept, c, pt[:len(pt)-1]) {
			// Zero params elements: DeserializeObject<T>(value) is
			// applicable to both DeserializeObject<T>(string) and
			// DeserializeObject<T>(string, params JsonConverter[]); C#
			// prefers the normal form whenever the fixed prefix matches
			// (§12.6.4.3), regardless of how well the arguments are typed.
			continue
		}
		narrowed = append(narrowed, c)
	}
	kept = narrowed
	// Better-conversion ranking (C# §12.6.4.5, approximated): among the
	// fully-typed applicable overloads on one type, the one whose literal
	// arguments convert least (exact = 0; int literal: long before ulong
	// before float...) wins. Unparsed and partially-typed candidates ride.
	best := map[string]int{}
	for _, c := range kept {
		if !fullyKnown[c] {
			continue
		}
		if cur, ok := best[group(c)]; !ok || cost[c] < cur {
			best[group(c)] = cost[c]
		}
	}
	if len(best) == 0 {
		return kept
	}
	isUnparsed := map[*core.SymbolRecord]bool{}
	for _, c := range unparsed {
		isUnparsed[c] = true
	}
	var out []*core.SymbolRecord
	for _, c := range kept {
		b, ranked := best[group(c)]
		if isUnparsed[c] || !ranked || (fullyKnown[c] && cost[c] == b) || (!fullyKnown[c] && b > 0) {
			out = append(out, c)
		}
	}
	_ = exact
	return out
}

func csValueType(idx *edgeIndex, typ string) bool {
	if csIsPrimitive(typ) {
		return true
	}
	switch typ {
	case "DateTime", "DateTimeOffset", "Guid", "TimeSpan", "BigInteger":
		return true
	}
	if idx == nil {
		return false
	}
	for _, symbol := range idx.byName[strings.ToLower(typ)] {
		if symbol.Language == "csharp" && symbol.Name == typ && (symbol.Kind == core.KindStruct || symbol.Kind == core.KindEnum) {
			return true
		}
	}
	return false
}

// csIsEnum reports whether an indexed C# enum named t exists.
func csIsEnum(idx *edgeIndex, t string) bool {
	if idx == nil {
		return false
	}
	for _, c := range idx.byName[strings.ToLower(t)] {
		if c.Name == t && c.Kind == core.KindEnum && c.Language == "csharp" {
			return true
		}
	}
	return false
}

// csNumericOrder lists, per numeric source type, the types it implicitly
// converts to in better-conversion order (exact first; signed before
// unsigned; narrower before wider).
var csNumericOrder = map[string][]string{
	"sbyte":   {"sbyte", "short", "int", "long", "float", "double", "decimal"},
	"byte":    {"byte", "short", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal"},
	"short":   {"short", "int", "long", "float", "double", "decimal"},
	"ushort":  {"ushort", "int", "uint", "long", "ulong", "float", "double", "decimal"},
	"int":     {"int", "long", "float", "double", "decimal"},
	"uint":    {"uint", "long", "ulong", "float", "double", "decimal"},
	"long":    {"long", "float", "double", "decimal"},
	"ulong":   {"ulong", "float", "double", "decimal"},
	"char":    {"char", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal"},
	"float":   {"float", "double"},
	"double":  {"double"},
	"decimal": {"decimal"},
	"bool":    {"bool"},
}

// csNumericRank is the conversion distance from a primitive argument type
// to a primitive parameter type, or -1 when no implicit conversion exists.
// A literal (constant) follows csLiteralRank, which also admits the
// constant conversions (an int literal binds uint).
func csNumericRank(arg, param string, literal bool) int {
	if literal {
		if _, ok := csLiteralOrder[arg]; ok {
			if !csLiteralCompatible(arg, param) {
				return -1
			}
			return csLiteralRank(arg, param)
		}
	}
	for i, t := range csNumericOrder[arg] {
		if t == param {
			return i
		}
	}
	return -1
}

// csLiteralRank orders the implicit conversions a literal allows: the
// smaller, the better the overload (int literal: int 0, uint 1, long 2,
// ulong 3, float 4, double 5, decimal 6; object last).
var csLiteralOrder = map[string][]string{
	"int":    {"int", "uint", "long", "ulong", "float", "double", "decimal", "object"},
	"long":   {"long", "ulong", "float", "double", "decimal", "object"},
	"float":  {"float", "double", "object"},
	"double": {"double", "object"},
	"char":   {"char", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal", "object"},
	"bool":   {"bool", "object"},
	"string": {"string", "object"},
}

func csLiteralRank(lit, param string) int {
	for i, p := range csLiteralOrder[lit] {
		if p == param {
			return i
		}
	}
	return 9
}

// csAssignable reports whether an argument of indexed class argType can
// bind a parameter of type paramType through inheritance (JValue → JToken):
// walks the argument class's base list up to four levels.
func csAssignable(idx *edgeIndex, argType, paramType string) bool {
	if csBclAssignable(argType, paramType) {
		return true
	}
	if idx == nil {
		return false
	}
	arr := strings.HasSuffix(argType, "[]")
	if arr != strings.HasSuffix(paramType, "[]") {
		return false
	}
	argType, paramType = strings.TrimSuffix(argType, "[]"), strings.TrimSuffix(paramType, "[]")
	seen := map[string]bool{argType: true}
	frontier := []string{argType}
	for level := 0; level < 4 && len(frontier) > 0; level++ {
		var next []string
		for _, cls := range frontier {
			for _, base := range csBaseClasses(idx, cls, "") {
				if base == paramType {
					return true
				}
				if !seen[base] {
					seen[base] = true
					next = append(next, base)
				}
			}
		}
		frontier = next
	}
	return false
}

// csBaseClasses parses the base list of a C# class declaration signature:
// "public class BsonWriter : JsonWriter, IDisposable" → [JsonWriter,
// IDisposable]. Generic arguments and `where` constraints are dropped.
// Interfaces ride along: assignability and dispatch both need them.
func csBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	fragments := csharpTypeFragments(idx, className, preferDir)
	if len(fragments) == 0 {
		return nil
	}
	var bases []string
	seen := map[string]bool{}
	for _, fragment := range fragments {
		for _, b := range csharpBaseList(csDeclSource(fragment)) {
			b = csNormalizeType(strings.TrimSpace(b))
			if b != "" && b != "object" && !seen[b] {
				seen[b] = true
				bases = append(bases, b)
			}
		}
	}
	return bases
}

var csharpTypeDeclHeadRe = regexp.MustCompile(`\b(?:class|struct|interface|record(?:\s+(?:class|struct))?)\s+[A-Za-z_][A-Za-z0-9_]*`)
var csharpPartialRe = regexp.MustCompile(`\bpartial\b`)

func csharpTypeFragments(idx *edgeIndex, className, preferDir string) []*core.SymbolRecord {
	var preferred, fallback []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name != className {
			continue
		}
		switch cand.Kind {
		case core.KindClass, core.KindStruct, core.KindEnum, core.KindInterface:
		default:
			continue
		}
		fallback = append(fallback, cand)
		if dirOf(cand.FilePath) == preferDir {
			preferred = append(preferred, cand)
		}
	}
	candidates := fallback
	if len(preferred) > 0 {
		candidates = preferred
	}
	if len(candidates) == 0 {
		return nil
	}
	first := candidates[0]
	if !csharpIsPartial(first) {
		return []*core.SymbolRecord{first}
	}
	// Partial declarations form one type even when source generators place
	// fragments in a different directory. Once the preferred declaration is
	// proven partial, collect every partial fragment of that name.
	out := make([]*core.SymbolRecord, 0, len(fallback))
	for _, cand := range fallback {
		if csharpIsPartial(cand) {
			out = append(out, cand)
		}
	}
	return out
}

// csharpIsPartial reports whether a type declaration is `partial`. Two
// matches, never a concatenation: `Signature + "\n" + RawText` copied the
// whole class body per call, and this runs per symbol per local type —
// 42% of the resolve-phase heap on a 4,800-file C# corpus.
func csharpIsPartial(s *core.SymbolRecord) bool {
	return csharpPartialRe.MatchString(s.Signature) || csharpPartialRe.MatchString(s.RawText)
}

func csharpBaseList(text string) []string {
	loc := csharpTypeDeclHeadRe.FindStringIndex(text)
	if loc == nil {
		return nil
	}
	tail := strings.TrimLeft(text[loc[1]:], " \t\r\n")
	for _, pair := range [][2]byte{{'<', '>'}, {'(', ')'}} {
		if n := csharpBalancedPrefix(tail, pair[0], pair[1]); n > 0 {
			tail = strings.TrimLeft(tail[n:], " \t\r\n")
		}
	}
	if !strings.HasPrefix(tail, ":") {
		return nil
	}
	tail = strings.TrimLeft(tail[1:], " \t")
	if i := strings.Index(tail, " where "); i >= 0 {
		tail = tail[:i]
	}
	if i := strings.IndexByte(tail, '{'); i >= 0 {
		tail = tail[:i]
	}
	if i := strings.IndexByte(tail, ';'); i >= 0 {
		tail = tail[:i]
	}
	var out []string
	start, depth := 0, 0
	for i := 0; i <= len(tail); i++ {
		if i < len(tail) {
			switch tail[i] {
			case '<', '(', '[':
				depth++
			case '>', ')', ']':
				if depth > 0 {
					depth--
				}
			}
		}
		if i < len(tail) && (tail[i] != ',' || depth != 0) {
			continue
		}
		part := strings.TrimSpace(tail[start:i])
		start = i + 1
		if j := strings.IndexByte(part, '('); j >= 0 {
			part = strings.TrimSpace(part[:j])
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func csharpBalancedPrefix(text string, open, close byte) int {
	if text == "" || text[0] != open {
		return 0
	}
	depth := 0
	for i := 0; i < len(text); i++ {
		if text[i] == open {
			depth++
		} else if text[i] == close {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

// csBclBases lists base types of common BCL classes the index cannot see
// (new BsonWriter(memoryStream) binds BsonWriter(Stream), not
// BsonWriter(BinaryWriter)).
var csBclBases = map[string][]string{
	"MemoryStream":              {"Stream"},
	"FileStream":                {"Stream"},
	"BufferedStream":            {"Stream"},
	"StreamWriter":              {"TextWriter"},
	"StringWriter":              {"TextWriter"},
	"StreamReader":              {"TextReader"},
	"StringReader":              {"TextReader"},
	"BinaryWriter":              {"IDisposable"},
	"BinaryReader":              {"IDisposable"},
	"List":                      {"IList", "ICollection", "IEnumerable"},
	"Dictionary":                {"IDictionary", "ICollection", "IEnumerable"},
	"HashSet":                   {"ISet", "ICollection", "IEnumerable"},
	"StringBuilder":             {"object"},
	"ArgumentException":         {"Exception"},
	"InvalidOperationException": {"Exception"},
	"JsonException":             {"Exception"},
}

func csBclAssignable(argType, paramType string) bool {
	a, p := strings.TrimSuffix(argType, "[]"), strings.TrimSuffix(paramType, "[]")
	for _, b := range csBclBases[a] {
		if b == p {
			return true
		}
	}
	return false
}

// csRecoverArgs re-derives argument markers from the call's own source line
// when the extractor reported none (every argument was a complex
// expression). Two shapes matter for overload binding and are language
// semantics rather than library knowledge:
//
//   - `x.Value` / `x.GetValueOrDefault()` on a local whose declared type is
//     a value type: Nullable<T> unwrapping, typed T. TraceJsonWriter's
//     `base.WriteValue(value.GetValueOrDefault())` inside
//     `WriteValue(DateTime? value)` binds WriteValue(DateTime), not all 35
//     overloads.
//   - `(T)expr`: an explicit cast, typed T.
//
// Anything else stays "" (unknown). Returns nil when nothing is recovered
// or the argument list does not match cs.Argc.
func csRecoverArgs(idx *edgeIndex, symbol *core.SymbolRecord, cs core.CallSite, argTypes map[string]string) []string {
	if cs.Argc == 0 || cs.Line < symbol.Span.Start || symbol.RawText == "" {
		return nil
	}
	// Masked: `Log("Foo(1, 2)"); Foo(x)` located the call inside the string.
	lines := maskedSymbolLines(idx, symbol)
	off := cs.Line - symbol.Span.Start
	if off < 0 || off >= len(lines) {
		return nil
	}
	line := lines[off]
	callee := cs.Callee
	if i := strings.LastIndexByte(callee, '.'); i >= 0 {
		callee = callee[i+1:]
	}
	if callee == "" {
		return nil
	}
	// Locate `callee(` at a word boundary; a generic call is `callee<...>(`.
	start := -1
	for from := 0; from < len(line); {
		i := strings.Index(line[from:], callee)
		if i < 0 {
			break
		}
		i += from
		before := i == 0 || !csIdentByte(line[i-1])
		j := i + len(callee)
		if j < len(line) && line[j] == '<' {
			depth := 0
			for ; j < len(line); j++ {
				if line[j] == '<' {
					depth++
				} else if line[j] == '>' {
					depth--
					if depth == 0 {
						j++
						break
					}
				}
			}
		}
		if before && j < len(line) && line[j] == '(' {
			start = j + 1
			break
		}
		from = i + len(callee)
	}
	if start < 0 {
		return nil
	}
	depth, end := 1, -1
	for k := start; k < len(line); k++ {
		switch line[k] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				end = k
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	groups := splitTopLevel(line[start:end], ',')
	if len(groups) != cs.Argc {
		return nil
	}
	out := make([]string, len(groups))
	known := false
	for i, g := range groups {
		g = strings.TrimSpace(g)
		if strings.HasPrefix(g, "typeof(") {
			out[i] = "#Type"
			known = true
			continue
		}
		if strings.HasPrefix(g, "(") {
			// (T)expr cast.
			if close := strings.IndexByte(g, ')'); close > 1 {
				if t := csNormalizeType(g[1:close]); t != "" && csSimpleTypeName(t) && close+1 < len(g) {
					out[i] = "#" + t
					known = true
				}
			}
			continue
		}
		if csIdentString(g) {
			out[i] = g
			known = true
			continue
		}
		recv, member, ok := strings.Cut(g, ".")
		if !ok || !csIdentString(recv) {
			continue
		}
		if member != "Value" && member != "GetValueOrDefault()" {
			continue
		}
		t, typed := argTypes[recv]
		if !typed || t == "" || strings.HasSuffix(t, "[]") {
			continue
		}
		if csIsPrimitive(t) || csValueType(idx, t) {
			out[i] = "#" + t
			known = true
		}
	}
	if !known {
		return nil
	}
	return out
}

func csIdentByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func csIdentString(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !csIdentByte(s[i]) {
			return false
		}
	}
	return true
}

func csSimpleTypeName(t string) bool {
	return csIdentString(strings.TrimSuffix(t, "[]"))
}

// csNormalFormSibling reports whether kept holds a non-params overload on
// the same type whose parameter list equals prefix exactly.
func csNormalFormSibling(kept []*core.SymbolRecord, params *core.SymbolRecord, prefix []string) bool {
	for _, c := range kept {
		if c == params || c.ParentSymbol != params.ParentSymbol {
			continue
		}
		pt, v := csParamTypes(c)
		if v || len(pt) != len(prefix) {
			continue
		}
		same := true
		for i := range pt {
			if pt[i] != prefix[i] {
				same = false
				break
			}
		}
		if same {
			return true
		}
	}
	return false
}

// csTypeChain returns typ and its base classes and interfaces (indexed, and
// the BCL bases csBclBases knows), each with its inheritance depth (typ is
// 0). Five levels bound the walk.
func csTypeChain(idx *edgeIndex, typ, preferDir string) map[string]int {
	depth := map[string]int{typ: 0}
	frontier := []string{typ}
	for level := 1; level <= 5 && len(frontier) > 0; level++ {
		var next []string
		for _, cls := range frontier {
			bases := append([]string(nil), csBaseClasses(idx, cls, preferDir)...)
			bases = append(bases, csBclBases[cls]...)
			for _, b := range bases {
				if _, seen := depth[b]; !seen {
					depth[b] = level
					next = append(next, b)
				}
			}
		}
		frontier = next
	}
	return depth
}

// csArityApplicable reports whether a call with argc arguments can bind
// cand by count: required parameters (no default, not params) at most argc,
// and argc at most the total unless the last is `params`. Unparsable
// declarations are applicable.
func csArityApplicable(cand *core.SymbolRecord, argc int) bool {
	params := tsDeclParams("csharp", csDeclSource(cand))
	if params == "" {
		return argc == 0 || !strings.Contains(csDeclSource(cand), "()")
	}
	required, total, variadic := 0, 0, false
	for _, g := range splitParams("csharp", params) {
		g = strings.TrimSpace(g)
		if g == "" || strings.HasPrefix(g, "this ") {
			continue
		}
		total++
		switch {
		case strings.HasPrefix(g, "params "):
			variadic = true
		case strings.Contains(g, "="):
		default:
			required++
		}
	}
	return argc >= required && (argc <= total || variadic)
}

// csMemberTargets resolves `recv.name(args)` for a receiver of static type
// typ, following C# member lookup: the same-named methods of typ and every
// base share one overload set, an override counts once (at its most-derived
// declaration), and the best overload is chosen across the whole set — a
// JsonTextWriter receiver calling WriteValue((char?)null) runs JsonWriter's
// WriteValue(char?) when JsonTextWriter does not override it. Only when no
// instance member applies by arity do extension methods apply: those whose
// `this` parameter is typ, any base or interface of it, or a type variable
// (person.IsValid(schema) on a JObject runs Extensions.IsValid(this JToken
// ...)) — when the caller's file imports, or sits in, the extension
// class's namespace. instance=false (a type name qualifier: static access) never
// considers extensions. Returns nil when nothing in typ's hierarchy fits.
func csMemberTargets(idx *edgeIndex, caller *core.SymbolRecord, cands []*core.SymbolRecord, typ string, cs core.CallSite, instance bool, argTypes map[string]string) []*core.SymbolRecord {
	chain := csTypeChain(idx, typ, dirOf(caller.FilePath))
	best := map[string]*core.SymbolRecord{}
	var order []string
	for _, cand := range cands {
		d, ok := chain[cand.ParentSymbol]
		if !ok || !graphCallableSymbol(cand) {
			continue
		}
		key := cand.ID
		if pt, v := csParamTypes(cand); pt != nil || csArityApplicable(cand, 0) && tsDeclParams("csharp", csDeclSource(cand)) == "" {
			// Same parameter types: an override (Read() on JsonTextReader
			// and JsonReader) is one member.
			key = fmt.Sprintf("%s(%s|%v)", cand.Name, strings.Join(pt, ","), v)
		}
		cur, seen := best[key]
		if !seen {
			order = append(order, key)
			best[key] = cand
		} else if chain[cur.ParentSymbol] > d {
			best[key] = cand // the override nearer the receiver type
		}
	}
	var members []*core.SymbolRecord
	for _, k := range order {
		members = append(members, best[k])
	}
	argc := cs.Argc
	if argc == 0 {
		argc = len(cs.Args)
	}
	if instance {
		applicable := false
		for _, m := range members {
			if csArityApplicable(m, argc) {
				applicable = true
				break
			}
		}
		if !applicable {
			var ext []*core.SymbolRecord
			for _, cand := range cands {
				if et := csExtensionThisType(cand); et != "" && idx.csExtensionVisible(caller, cand) {
					if _, ok := chain[et]; ok || csTypeVariable(et, cand) {
						if csArityApplicable(cand, argc) {
							ext = append(ext, cand)
						}
					}
				}
			}
			if len(ext) > 0 {
				members = ext
			}
		}
	}
	if len(members) > 1 {
		members = csNarrowOverloadsGrouped(idx, members, cs.Args, argTypes, false)
	}
	return members
}

// csExtensionThisType is the normalized type of an extension method's
// `this` parameter, or "" for an ordinary method.
func csExtensionThisType(cand *core.SymbolRecord) string {
	params := tsDeclParams(cand.Language, cand.Signature)
	if params == "" {
		params = tsDeclParams(cand.Language, cand.RawText)
	}
	first := strings.TrimSpace(strings.SplitN(params, ",", 2)[0])
	fields := strings.Fields(first)
	if len(fields) >= 3 && fields[0] == "this" {
		return csNormalizeType(fields[1])
	}
	return ""
}

// csExtensionVisible reports whether an extension method is in scope for
// caller: C# finds extension methods only in the caller's enclosing
// namespaces and its file's `using` namespaces. newtonsoft's LinqBridge
// (namespace Newtonsoft.Json.Utilities.LinqBridge) is not what `.Where()`
// means in a file that imports only System.Linq.
func (idx *edgeIndex) csExtensionVisible(caller, cand *core.SymbolRecord) bool {
	return idx.csVisibleNamespaces(caller)[idx.csNamespaceOf(cand)]
}

// csBareType is javaBareType for C#: a nullable annotation (`ITraceWriter?`)
// does not change the type a member lookup uses.
func csBareType(t string) string {
	return javaBareType(strings.TrimSuffix(strings.TrimSpace(t), "?"))
}

// csChainType types a dotted C# receiver chain hop by hop: the head is a
// typed local/field/parameter, or a type name (static access:
// ExpressionReflectionDelegateFactory.Instance), and every later hop is a
// field or property declared on the type reached so far or its bases.
// "" when the chain has a single segment or any hop is unknown.
func csChainType(idx *edgeIndex, caller *core.SymbolRecord, localTypes map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	for len(parts) > 0 && (parts[0] == "this" || parts[0] == "base") {
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return ""
	}
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, "()[]") {
			return ""
		}
	}
	lt := localTypes
	if _, ok := localTypes[parts[0]]; !ok {
		if !typeSymbolExists(idx, parts[0]) {
			return ""
		}
		lt = map[string]string{parts[0]: parts[0]}
	}
	return tsResolveReceiverChain(idx, lt, strings.Join(parts, "."), caller.FilePath, "csharp")
}

// csReceiverChainAt recovers the dotted receiver written before the called
// name at cs.Line:cs.Col in the caller's source — astkit keeps only the last
// receiver segment ("Instance.CreateSet" for
// `ExpressionReflectionDelegateFactory.Instance.CreateSet<object>(...)`).
// Only plain identifier hops (with ?. and !.) count; "" otherwise.
func csReceiverChainAt(caller *core.SymbolRecord, cs core.CallSite, method string) string {
	if cs.Line <= 0 || cs.Col <= 0 || caller.RawText == "" {
		return ""
	}
	lines := strings.Split(caller.RawText, "\n")
	i := cs.Line - caller.Span.Start
	if i < 0 || i >= len(lines) {
		return ""
	}
	line := []rune(lines[i])
	at := cs.Col - 1
	if at <= 0 || at+len([]rune(method)) > len(line) || string(line[at:at+len([]rune(method))]) != method {
		return ""
	}
	j := at - 1
	skip := func() {
		for j >= 0 && (line[j] == ' ' || line[j] == '\t') {
			j--
		}
	}
	var segs []string
	for {
		skip()
		if j < 0 || line[j] != '.' {
			break
		}
		j--
		if j >= 0 && (line[j] == '?' || line[j] == '!') {
			j--
		}
		skip()
		end := j + 1
		for j >= 0 && (line[j] == '_' || line[j] >= 'a' && line[j] <= 'z' || line[j] >= 'A' && line[j] <= 'Z' || line[j] >= '0' && line[j] <= '9') {
			j--
		}
		if end == j+1 {
			return "" // a call, index or parenthesized receiver
		}
		segs = append([]string{string(line[j+1 : end])}, segs...)
	}
	if j >= 0 && (line[j] == ')' || line[j] == ']' || line[j] == '>') {
		return ""
	}
	if len(segs) < 2 {
		return ""
	}
	return strings.Join(segs, ".")
}
