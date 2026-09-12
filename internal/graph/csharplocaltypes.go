package graph

import (
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
	// Type x = ... / Type x; / for (Type x : — uppercase-initial type
	csharpLocalDeclRe = regexp.MustCompile(`(?m)(?:^|[;{}()]\s*)\s*([A-Z]\w*)(?:<[^<>]*>)?(?:\[\])?\s+(\w+)\s*[=;)]`)
	// foreach (Type x in ...)
	csharpForeachRe = regexp.MustCompile(`\bforeach\s*\(\s*(?:var|([A-Z]\w*)(?:<[^<>]*>)?)\s+(\w+)\s+in\b`)
	// if (value is Type name) / while (... is Type name)
	csharpIsPatternRe = regexp.MustCompile(`\bis\s+([A-Z]\w*)(?:<[^<>]*>)?\s+([a-z_]\w*)\b`)
	// auto-property / field: "public Foo Bar { ..." or "Foo _bar ;|="
	csharpFieldRe = regexp.MustCompile(`(?m)^\s*(?:(?:public|private|protected|internal|static|readonly|virtual|override|sealed|abstract|new|volatile|const)\s+)*([A-Z]\w*)(?:<[^<>]*>)?(?:\[\])?\s+(\w+)\s*[{;=]`)
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
					if cls.RawText == "" {
						continue
					}
					body := cls.RawText
					if i := strings.IndexByte(body, '{'); i >= 0 {
						body = body[i+1:]
					}
					for _, m := range csharpFieldRe.FindAllStringSubmatch(body, -1) {
						if t := javaBareType(m[1]); t != "" && !csharpKeyword(t) {
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
	if params := tsDeclParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			fields := strings.Fields(strings.TrimSpace(g))
			// Drop C# parameter modifiers and attributes.
			for len(fields) > 2 || (len(fields) == 2 && csharpParamModifier(fields[0])) {
				fields = fields[1:]
			}
			if len(fields) != 2 {
				continue
			}
			if t := javaBareType(fields[0]); t != "" && !csharpKeyword(t) {
				out[fields[1]] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		if i := strings.IndexByte(body, '{'); i >= 0 {
			body = body[i+1:]
		}
		for _, m := range csharpForeachRe.FindAllStringSubmatch(body, -1) {
			if t := javaBareType(m[1]); t != "" && !csharpKeyword(t) {
				out[m[2]] = t
			}
		}
		for _, m := range csharpLocalDeclRe.FindAllStringSubmatch(body, -1) {
			if t := javaBareType(m[1]); t != "" && !csharpKeyword(t) {
				out[m[2]] = t
			}
		}
		for _, m := range csharpIsPatternRe.FindAllStringSubmatch(body, -1) {
			if t := javaBareType(m[1]); t != "" && !csharpKeyword(t) {
				out[m[2]] = t
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
	params := tsDeclParams(csDeclSource(s))
	if params == "" {
		return nil, false
	}
	for _, g := range splitTopLevel(params, ',') {
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
	if params := tsDeclParams(csDeclSource(symbol)); params != "" {
		for _, g := range splitTopLevel(params, ',') {
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
		body := stripCommentsAndStrings(symbol.RawText)
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
		"char":   {"char", "int", "uint", "long", "ulong", "float", "double", "decimal"},
		"bool":   {"bool"},
		"string": {"string"},
	}
	for _, ok := range compat[lit] {
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
				argType = csNormalizeType(argName[1:])
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
			if isLit {
				cost[cand] += csLiteralRank(argType, p)
			} else {
				cost[cand]++ // a typed argument binding a wider parameter (int → object, JValue → JToken)
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
			normalOn[c.ParentSymbol] = true
		}
	}
	var narrowed []*core.SymbolRecord
	for _, c := range kept {
		if _, v := csParamTypes(c); v && normalOn[c.ParentSymbol] {
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
		if cur, ok := best[c.ParentSymbol]; !ok || cost[c] < cur {
			best[c.ParentSymbol] = cost[c]
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
		b, ranked := best[c.ParentSymbol]
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

// csLiteralRank orders the implicit conversions a literal allows: the
// smaller, the better the overload (int literal: int 0, uint 1, long 2,
// ulong 3, float 4, double 5, decimal 6; object last).
func csLiteralRank(lit, param string) int {
	order := map[string][]string{
		"int":    {"int", "uint", "long", "ulong", "float", "double", "decimal", "object"},
		"long":   {"long", "ulong", "float", "double", "decimal", "object"},
		"float":  {"float", "double", "object"},
		"double": {"double", "object"},
		"char":   {"char", "ushort", "int", "uint", "long", "ulong", "float", "double", "decimal", "object"},
		"bool":   {"bool", "object"},
		"string": {"string", "object"},
	}
	for i, p := range order[lit] {
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
	if !csharpPartialRe.MatchString(first.Signature + "\n" + first.RawText) {
		return []*core.SymbolRecord{first}
	}
	// Partial declarations form one type even when source generators place
	// fragments in a different directory. Once the preferred declaration is
	// proven partial, collect every partial fragment of that name.
	out := make([]*core.SymbolRecord, 0, len(fallback))
	for _, cand := range fallback {
		if csharpPartialRe.MatchString(cand.Signature + "\n" + cand.RawText) {
			out = append(out, cand)
		}
	}
	return out
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
