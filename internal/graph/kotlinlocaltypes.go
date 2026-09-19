package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Kotlin local type inference: signature parameters, typed val/var bindings,
// and the `val x = Type(...)` constructor convention — the same altitude as
// swiftLocalTypes/rustLocalTypes.

var (
	// val x: Type = ... / var x: Type
	kotlinValTypedRe = regexp.MustCompile(`(?m)\b(?:val|var)\s+([a-z_]\w*)\s*:\s*([^=;\n{]+)`)
	// val x = Type(...) / var x = pkg.Type(...) — Kotlin's plain call-syntax
	// constructor invocation, indistinguishable at the source level from a
	// function call, so only a Capitalized callee is treated as one.
	kotlinValCtorRe = regexp.MustCompile(`(?m)\b(?:val|var)\s+([a-z_]\w*)\s*=\s*(?:[a-z_]\w*\.)*([A-Z]\w*)\s*\(`)
	// fun f(name: Type, other: Type = default) — parameter list entries.
	kotlinParamRe = regexp.MustCompile(`([a-z_]\w*)\s*:\s*([^,)=]+)`)
)

// kotlinPrimitives are lowercase/PascalCase-but-builtin tokens that look
// like types but can never resolve to an indexed declaration's methods.
var kotlinPrimitives = map[string]bool{
	"int": true, "long": true, "short": true, "byte": true,
	"double": true, "float": true, "boolean": true, "char": true, "string": true,
	"any": true, "unit": true, "nothing": true,
}

// kotlinLocalTypes infers identifier -> bare type name for one callable symbol.
func kotlinLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	for name, typ := range kotlinParamTypes(symbol.Signature) {
		out[name] = typ
	}

	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range kotlinValTypedRe.FindAllStringSubmatch(body, -1) {
			if typ := kotlinBareType(m[2]); typ != "" {
				out[m[1]] = typ
			}
		}
		for _, m := range kotlinValCtorRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
	}

	if symbol.ParentSymbol != "" {
		out["this"] = symbol.ParentSymbol
		// Properties of the enclosing class are receivers in every
		// method body (`arguments + other` inside Command). Explicit
		// bindings above shadow them.
		for _, cand := range idx.byFile[symbol.FilePath] {
			if cand.Kind != core.KindField || cand.ParentSymbol != symbol.ParentSymbol {
				continue
			}
			if _, shadowed := out[cand.Name]; shadowed {
				continue
			}
			if typ := kotlinBareType(kotlinFieldType(idx, symbol.ParentSymbol, cand.Name)); typ != "" {
				out[cand.Name] = typ
			}
		}
	}
	return out
}

// kotlinParamTypes parses "fun greet(name: String, age: Int): String" into
// {name: String, age: Int}. `this` is handled by receiver narrowing, not here.
func kotlinParamTypes(signature string) map[string]string {
	out := map[string]string{}
	start := kotlinParamListStart(signature)
	if start < 0 {
		return out
	}
	depth, end := 0, -1
	for i := start; i < len(signature); i++ {
		switch signature[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return out
	}
	for _, g := range splitTopLevel(signature[start+1:end], ',') {
		g = strings.TrimSpace(g)
		// Strip a leading val/var (constructor-property parameters).
		g = strings.TrimPrefix(g, "val ")
		g = strings.TrimPrefix(g, "var ")
		m := kotlinParamRe.FindStringSubmatch(g)
		if m == nil {
			continue
		}
		if typ := kotlinBareType(m[2]); typ != "" {
			out[m[1]] = typ
		}
	}
	return out
}

// kotlinBaseClasses returns the superclass a Kotlin class declaration names:
// the delegation-specifier entry written as a constructor call (`Base()`);
// plain names are interfaces. Feeds constructorBaseClasses for `super(...)`.
func kotlinBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	decl := tsChosenTypeDecl(idx, className, preferDir)
	if decl == nil {
		return nil
	}
	text := decl.Signature
	if text == "" {
		text = firstLine(decl.RawText)
	}
	for _, raw := range kotlinBaseNames(text) {
		if name, isCtor := kotlinBaseNameAndCtor(raw); isCtor && name != "" {
			return []string{name}
		}
	}
	return nil
}

// kotlinFieldType returns the declared (or constructor-inferred) type of a
// property `name` of class `class`, or "" — `shell.run()` inside a class with
// `private val shell: ShellScript` is a typed receiver, not an unknown one.
func kotlinFieldType(idx *edgeIndex, class, name string) string {
	if class == "" || name == "" {
		return ""
	}
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Kind != core.KindField || cand.Name != name || cand.ParentSymbol != class || cand.Language != "kotlin" {
			continue
		}
		sig := cand.Signature
		if sig == "" {
			sig = firstLine(cand.RawText)
		}
		if m := kotlinValTypedRe.FindStringSubmatch(sig); m != nil {
			return kotlinShapeType(m[2])
		}
		if m := kotlinValCtorRe.FindStringSubmatch(sig); m != nil {
			return m[2]
		}
	}
	return ""
}

// kotlinTypeIndexed reports whether name is a type this index declares,
// enums included (they carry methods too). A Kotlin receiver whose type is
// known but not indexed — String, List, a library class — is external:
// nothing in the repo is its member.
func kotlinTypeIndexed(idx *edgeIndex, name string) bool {
	return swiftTypeIndexed(idx, name)
}

// kotlinShapeType normalizes a declared type for overload matching: the
// nullable marker, a `vararg` prefix (the call site passes elements), a
// default value, and a package qualifier drop; generics and whitespace
// collapse. Unlike kotlinBareType it keeps builtins (`String` is what tells
// `contains(String)` from `contains(Arguments)`).
func kotlinShapeType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '='); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	t = strings.TrimPrefix(t, "vararg ")
	t = strings.TrimSpace(strings.TrimSuffix(t, "?"))
	if strings.IndexByte(t, '<') < 0 {
		if j := strings.LastIndexByte(t, '.'); j >= 0 {
			t = t[j+1:]
		}
	}
	return strings.Join(strings.Fields(t), " ")
}

// kotlinArgShapes maps identifiers a callable's body may pass on to their
// shape-preserving types: parameters, typed val/var bindings, and
// constructor-initialized bindings.
func kotlinArgShapes(symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}
	if params, ok := kotlinParamGroups(symbol.Signature); ok {
		for _, g := range params {
			g = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(g), "val "), "var ")
			if m := kotlinParamRe.FindStringSubmatch(g); m != nil {
				if shape := kotlinShapeType(m[2]); shape != "" {
					out[m[1]] = shape
				}
			}
		}
	}
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range kotlinValTypedRe.FindAllStringSubmatch(body, -1) {
			if shape := kotlinShapeType(m[2]); shape != "" {
				out[m[1]] = shape
			}
		}
		for _, m := range kotlinValCtorRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
	}
	return out
}

// kotlinParamListStart returns the index of the '(' opening a declaration's
// parameter list: the first one after the `fun` keyword when there is one
// (an annotation's own parentheses — `@Suppress("x") fun f(...)` — come
// first in the signature), else the first one (constructors).
func kotlinParamListStart(signature string) int {
	from := 0
	if i := strings.Index(signature, "fun "); i >= 0 {
		from = i
	}
	start := strings.IndexByte(signature[from:], '(')
	if start < 0 {
		return -1
	}
	return from + start
}

// kotlinParamGroups returns the comma-split parameter groups of a
// declaration's parameter list.
func kotlinParamGroups(signature string) ([]string, bool) {
	start := kotlinParamListStart(signature)
	if start < 0 {
		return nil, false
	}
	depth := 0
	for i := start; i < len(signature); i++ {
		switch signature[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return splitTopLevel(signature[start+1:i], ','), true
			}
		}
	}
	return nil, false
}

// kotlinDeclParamShapes returns a candidate's parameter shapes by position
// and whether its last parameter is a vararg.
func kotlinDeclParamShapes(s *core.SymbolRecord) (shapes []string, variadic bool, ok bool) {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = firstLine(s.RawText)
	}
	groups, ok := kotlinParamGroups(src)
	if !ok {
		return nil, false, false
	}
	for _, g := range groups {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		i := strings.IndexByte(g, ':')
		if i < 0 {
			return nil, false, false
		}
		variadic = strings.Contains(g[:i], "vararg ")
		shapes = append(shapes, kotlinShapeType(g[i+1:]))
	}
	return shapes, variadic, true
}

// kotlinLiteralShape maps astkit's literal markers to the Kotlin type a bare
// literal defaults to.
func kotlinLiteralShape(tok string) string {
	switch tok {
	case "#int":
		return "Int"
	case "#long":
		return "Long"
	case "#String":
		return "String"
	case "#boolean":
		return "Boolean"
	case "#double":
		return "Double"
	case "#float":
		return "Float"
	}
	return ""
}

// kotlinStdlibResults are the kotlin-stdlib factory functions whose result
// type a call site's argument carries (`Arguments(listOf(x))` passes a
// List): the generic argument is unknown, so the shape is the erasure.
var kotlinStdlibResults = map[string]string{
	"listOf": "List", "emptyList": "List", "mutableListOf": "MutableList", "arrayListOf": "ArrayList",
	"setOf": "Set", "emptySet": "Set", "mutableSetOf": "MutableSet", "hashSetOf": "HashSet",
	"mapOf": "Map", "emptyMap": "Map", "mutableMapOf": "MutableMap", "hashMapOf": "HashMap",
	"arrayOf": "Array", "sequenceOf": "Sequence", "emptySequence": "Sequence",
}

// kotlinErase drops a type's generic arguments: List<String> → List.
func kotlinErase(t string) string {
	if i := strings.IndexByte(t, '<'); i >= 0 {
		return strings.TrimSpace(t[:i])
	}
	return t
}

// kotlinShapeMatch scores how a parameter shape accepts an argument shape:
// 2 for an exact (or erasure-equal, when either side lacks generic
// arguments) match, 1 for an `Any`/`Any?` parameter — kotlinc's
// most-specific-overload rule ranks a concrete match above the catch-all —
// and 0 for a mismatch.
func kotlinShapeMatch(param, arg string) int {
	switch {
	case param == arg:
		return 2
	case kotlinErase(param) == kotlinErase(arg) && (!strings.Contains(param, "<") || !strings.Contains(arg, "<")):
		return 2
	case param == "Any":
		return 1
	}
	return 0
}

// kotlinNarrowByArgShapes keeps, among same-arity candidates, those whose
// parameters best accept the arguments of known shape — kotlinc's overload
// resolution: `Arguments(url)` with `url: String` binds the
// `fun Arguments(vararg Any?)` factory, not the `Arguments(List<String>)`
// constructor; `Arguments(listOf(x))` the reverse. A candidate that
// rejects any known argument drops when a better one remains; with no
// evidence at all the set is left alone. A `call:Type` argument has that
// type when Type is an indexed or Capitalized (constructor) name, or a
// stdlib factory's erased result.
func kotlinNarrowByArgShapes(idx *edgeIndex, cands []*core.SymbolRecord, args []string, shapes map[string]string) []*core.SymbolRecord {
	if len(cands) < 2 || len(args) == 0 {
		return cands
	}
	argShape := func(a string) string {
		if shape := kotlinLiteralShape(a); shape != "" {
			return shape
		}
		if name, ok := strings.CutPrefix(a, "call:"); ok {
			if typeSymbolExists(idx, name) || (name != "" && name[0] >= 'A' && name[0] <= 'Z') {
				return name
			}
			return kotlinStdlibResults[name]
		}
		return shapes[a]
	}
	score := func(cand *core.SymbolRecord) int {
		pshapes, variadic, ok := kotlinDeclParamShapes(cand)
		if !ok {
			return -1
		}
		if len(args) > len(pshapes) && !variadic {
			return 0
		}
		best := -1
		for i, a := range args {
			shape := argShape(a)
			if shape == "" {
				continue
			}
			j := i
			if j >= len(pshapes) {
				j = len(pshapes) - 1
			}
			if j < 0 {
				return 0
			}
			m := kotlinShapeMatch(pshapes[j], shape)
			if m == 0 {
				return 0
			}
			if best < 0 || m < best {
				best = m
			}
		}
		return best
	}
	top := 0
	scores := make([]int, len(cands))
	for i, cand := range cands {
		scores[i] = score(cand)
		if scores[i] > top {
			top = scores[i]
		}
	}
	if top <= 0 {
		return cands // no evidence (or every candidate unparsed)
	}
	var out []*core.SymbolRecord
	for i, cand := range cands {
		if scores[i] == top || scores[i] == -1 {
			out = append(out, cand)
		}
	}
	return out
}

var kotlinExtensionRe = regexp.MustCompile(`\bfun\s+(?:<[^>]*>\s*)?([A-Za-z_][\w.]*(?:<[^>]*>)?\??)\.([A-Za-z_]\w*)\s*\(`)

// kotlinExtensionReceiver returns the erased receiver type of an extension
// function declaration (`fun Process.retrieveOutput()` → Process), or "".
func kotlinExtensionReceiver(signature string) string {
	m := kotlinExtensionRe.FindStringSubmatch(signature)
	if m == nil {
		return ""
	}
	return kotlinErase(kotlinShapeType(m[1]))
}

// kotlinExtensionCandidates keeps the candidates declared as extension
// functions applicable to a receiver of type typ ("" when unknown): the
// extension's own receiver type, or `Any`. An extension on an external type
// is in-repo code reached through a receiver the index cannot type
// (`process.retrieveOutput()` on a java.lang.Process), so an unknown
// receiver must not drop it.
func kotlinExtensionCandidates(cands []*core.SymbolRecord, typ string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range cands {
		recv := kotlinExtensionReceiver(cand.Signature)
		if recv == "" {
			continue
		}
		if typ == "" || recv == typ || recv == "Any" {
			out = append(out, cand)
		}
	}
	return out
}

// kotlinCallResultTypes resolves a `name()` receiver to the erased return
// types of the in-repo callables it can name: a constructor call yields
// the type itself, a function its declared `: Type`. Empty means the call
// is not ours (ProcessBuilder.start()) or declares no return type.
func kotlinCallResultTypes(idx *edgeIndex, qualifier string) map[string]bool {
	name := strings.TrimSuffix(qualifier, "()")
	out := map[string]bool{}
	if typeSymbolExists(idx, name) {
		out[name] = true
		return out
	}
	for _, cand := range namedSymbols(idx, name) {
		if cand.Language != "kotlin" {
			continue
		}
		switch cand.Kind {
		case core.KindFunction, core.KindMethod:
			if ret := kotlinReturnType(cand.Signature); ret != "" {
				out[ret] = true
			}
		case core.KindConstructor:
			out[cand.ParentSymbol] = true
		}
	}
	return out
}

// kotlinReturnType reads the declared return type of `fun f(...): T` (erased).
func kotlinReturnType(signature string) string {
	start := kotlinParamListStart(signature)
	if start < 0 {
		return ""
	}
	depth, end := 0, -1
	for i := start; i < len(signature) && end < 0; i++ {
		switch signature[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		return ""
	}
	rest := strings.TrimSpace(signature[end+1:])
	if !strings.HasPrefix(rest, ":") {
		return ""
	}
	rest = strings.TrimSpace(rest[1:])
	if i := strings.IndexAny(rest, "={"); i >= 0 {
		rest = rest[:i]
	}
	return kotlinErase(kotlinShapeType(rest))
}

// kotlinBareType reduces a Kotlin type expression to its final type
// identifier: "String?" -> "String", "List<Person>" -> "List", "pkg.Type" ->
// "Type". Primitives return "".
func kotlinBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimRight(t, ",; \t")
	t = strings.TrimSuffix(t, "?")
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	t = strings.TrimSpace(t)
	if t == "" || kotlinPrimitives[strings.ToLower(t)] {
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	if t[0] >= 'a' && t[0] <= 'z' {
		return ""
	}
	return t
}
