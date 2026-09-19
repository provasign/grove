package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Swift local type inference: signature parameters, typed let/var bindings,
// and the `let x = Type(...)` constructor convention — the same altitude as
// rustLocalTypes. Swift's own value types (structs) and classes share one
// inference path since both are constructed with plain call syntax.

// swiftBaseClasses returns className's superclass, if any — used to resolve
// `super.init(...)` to the right type's constructors. Only the first base
// name is eligible (Swift requires a superclass, if present, to be listed
// first) and only when it actually names an indexed class, mirroring the
// same disambiguation buildExtendsImplements's "swift" case already uses.
func swiftBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	decl := tsChosenTypeDecl(idx, className, preferDir)
	if decl == nil {
		return nil
	}
	text := decl.Signature
	if text == "" {
		text = firstLine(decl.RawText)
	}
	names := swiftBaseNames(text)
	if len(names) == 0 || !namedSymbolIsClass(idx, names[0]) {
		return nil
	}
	return names[:1]
}

var (
	// let x: Type = ... / var x: Type
	swiftLetTypedRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*:\s*([^=;\n{]+)`)
	// let x = Type(...) / var x = Mod.Type(...) — Swift's plain call-syntax
	// initializer, indistinguishable at the source level from a function
	// call, so only a Capitalized callee is treated as a constructor.
	swiftLetCtorRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*=\s*(?:[A-Za-z_]\w*\.)*([A-Z]\w*)\s*\(`)
	// func f(name: Type, _ other: Type, ext int: Type) — parameter list
	// entries; the internal name is the token right before the colon.
	swiftParamRe = regexp.MustCompile(`(?:(?:[A-Za-z_]\w*|_)\s+)?([a-z_]\w*)\s*:\s*([^,)=]+)`)
	// let x = self / var copy = self — a self alias carries the enclosing
	// type. The statement must END there: `var next = self[sub: p]` is a
	// subscript result, not an alias (a bare \b after self matched the `[`).
	swiftSelfAliasRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*=\s*self[ \t]*(?:;|$)`)
)

// swiftPrimitives are lowercase-initial tokens that look like types but can
// never resolve to an indexed declaration's methods.
var swiftPrimitives = map[string]bool{
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"double": true, "float": true, "bool": true, "string": true, "character": true,
}

// swiftLocalTypes infers identifier -> bare type name for one callable symbol.
func swiftLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Shapes, not bare element types: `path: [JSONSubscriptType]` is an
	// Array receiver whose subscript/methods are the standard library's,
	// never JSONSubscriptType's — unwrapping it misattributed every
	// `path[0]` to the in-repo type's own subscripts.
	for name, typ := range swiftParamShapes(symbol.Signature) {
		out[name] = typ
	}

	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range swiftLetTypedRe.FindAllStringSubmatch(body, -1) {
			if typ := swiftShapeType(m[2]); typ != "" {
				out[m[1]] = typ
			}
		}
		for _, m := range swiftLetCtorRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
		if symbol.ParentSymbol != "" {
			for _, m := range swiftSelfAliasRe.FindAllStringSubmatch(body, -1) {
				out[m[1]] = symbol.ParentSymbol
			}
		}
	}

	if symbol.ParentSymbol != "" {
		out["self"] = symbol.ParentSymbol
		// Properties of the enclosing type — declared in the type, in an
		// extension, or as a protocol requirement (`var storage:
		// Storage<Self> { get }` in `protocol Location`, used from
		// `extension Location`) — are receivers in every member body.
		for _, cand := range idx.byFile[symbol.FilePath] {
			if cand.Kind != core.KindField || cand.ParentSymbol != symbol.ParentSymbol || cand.Language != "swift" {
				continue
			}
			if _, shadowed := out[cand.Name]; shadowed {
				continue
			}
			if m := swiftLetTypedRe.FindStringSubmatch(cand.Signature); m != nil {
				if typ := swiftShapeType(m[2]); typ != "" {
					out[cand.Name] = typ
				}
			} else if m := swiftLetCtorRe.FindStringSubmatch(cand.Signature); m != nil {
				out[cand.Name] = m[2]
			}
		}
	}
	// A generic instantiation (`Storage<Self>`, `Result<T, E>`) receives
	// the generic type's members: drop the arguments. Container shapes
	// (`[T]`) stay, per the note above.
	for name, typ := range out {
		if i := strings.IndexByte(typ, '<'); i > 0 && typ[0] != '[' {
			out[name] = typ[:i]
		}
	}
	return out
}

// swiftShapeType normalizes a declared type for overload matching, keeping
// the shape a Swift overload set is told apart by: `[T]` stays an array,
// `T...` (a variadic parameter is an array inside its body) becomes `[T]`,
// optionals/inout/module qualifiers drop. Unlike swiftBareType it never
// unwraps a container.
func swiftShapeType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.IndexByte(t, '='); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	for _, kw := range []string{"inout ", "some ", "any ", "borrowing ", "consuming "} {
		t = strings.TrimPrefix(t, kw)
	}
	t = strings.TrimSpace(strings.TrimRight(t, "?!"))
	if strings.HasSuffix(t, "...") {
		return "[" + swiftShapeType(strings.TrimSuffix(t, "...")) + "]"
	}
	if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
		inner := t[1 : len(t)-1]
		if strings.ContainsRune(inner, ':') {
			return "[" + strings.Join(strings.Fields(inner), " ") + "]"
		}
		return "[" + swiftShapeType(inner) + "]"
	}
	if i := strings.IndexByte(t, '<'); i < 0 {
		if j := strings.LastIndexByte(t, '.'); j >= 0 {
			t = t[j+1:]
		}
	}
	return strings.Join(strings.Fields(t), " ")
}

var swiftTypealiasRe = regexp.MustCompile(`\btypealias\s+([A-Za-z_]\w*)\s*(?:<[^=]*>)?\s*=\s*([A-Za-z_][\w.]*)`)

// swiftTypealiasTarget resolves a Swift `typealias Name = Target<...>`
// declared in the index to Target's bare name, or "".
func swiftTypealiasTarget(idx *edgeIndex, name string) string {
	for _, cand := range namedSymbols(idx, name) {
		if cand.Language != "swift" || cand.Kind != core.KindType {
			continue
		}
		m := swiftTypealiasRe.FindStringSubmatch(cand.Signature)
		if m == nil {
			m = swiftTypealiasRe.FindStringSubmatch(firstLine(cand.RawText))
		}
		if m != nil && m[1] == name {
			target := m[2]
			if i := strings.LastIndexByte(target, '.'); i >= 0 {
				target = target[i+1:]
			}
			if target != name {
				return target
			}
		}
	}
	return ""
}

// swiftTypeIndexed reports whether name is a type this index declares
// (typeSymbolExists plus enums, which Swift gives methods too). A Swift
// receiver whose type is known but not indexed — Array, String, a
// Foundation class — is external: nothing in the repo is its member.
func swiftTypeIndexed(idx *edgeIndex, name string) bool {
	if typeSymbolExists(idx, name) {
		return true
	}
	for _, cand := range namedSymbols(idx, name) {
		if cand.Kind == core.KindEnum {
			return true
		}
	}
	return false
}

// swiftParamShapes maps a callable's internal parameter names to their
// shape-preserving types, for typing the identifiers its body passes on.
func swiftParamShapes(signature string) map[string]string {
	out := map[string]string{}
	params, ok := swiftParamList(signature)
	if !ok {
		return out
	}
	for _, g := range splitTopLevel(params, ',') {
		m := swiftParamRe.FindStringSubmatch(strings.TrimSpace(g))
		if m == nil {
			continue
		}
		if shape := swiftShapeType(m[2]); shape != "" {
			out[m[1]] = shape
		}
	}
	return out
}

// swiftDeclParamShapes returns a candidate's parameter shapes by position.
func swiftDeclParamShapes(s *core.SymbolRecord) []string {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = firstLine(s.RawText)
	}
	list, ok := swiftParamList(src)
	if !ok {
		return nil
	}
	var out []string
	for _, g := range splitTopLevel(list, ',') {
		i := strings.IndexByte(g, ':')
		if i < 0 {
			return nil
		}
		out = append(out, swiftShapeType(g[i+1:]))
	}
	return out
}

// swiftLiteralShape maps astkit's literal markers to the Swift type a bare
// literal defaults to.
func swiftLiteralShape(tok string) string {
	switch tok {
	case "#int", "#long":
		return "Int"
	case "#String":
		return "String"
	case "#boolean":
		return "Bool"
	case "#double", "#float":
		return "Double"
	}
	return ""
}

// swiftNarrowByArgShapes keeps, among label-applicable candidates, those
// whose parameter shape equals a known argument's shape — swiftc's exact
// match beating a protocol- or supertype-typed sibling (`self[path]` with
// `path: JSONSubscriptType...` binds `subscript(path: [JSONSubscriptType])`,
// not `subscript(position: Index)`). Positive evidence only: when no
// candidate matches exactly the argument may well be a conforming value,
// and the set is left alone — unless the caller itself, a same-name
// sibling the candidate lookup excludes, is the exact match: then the call
// is the recursion the direct-recursion rule records separately, and none
// of the inexact siblings is its target.
func swiftNarrowByArgShapes(cands []*core.SymbolRecord, args []string, shapes map[string]string, caller *core.SymbolRecord) []*core.SymbolRecord {
	if len(cands) == 0 || len(args) == 0 {
		return cands
	}
	callLabels, ok := swiftCallLabels(args)
	if !ok {
		return cands
	}
	exactMatch := func(cand *core.SymbolRecord) (matched bool) {
		params, ok := swiftDeclParams(cand)
		if !ok {
			return false
		}
		bind, ok := swiftBindLabels(params, callLabels)
		if !ok {
			return false
		}
		pshapes := swiftDeclParamShapes(cand)
		if len(pshapes) != len(params) {
			return false
		}
		for i, a := range args {
			v := a[strings.IndexByte(a, ':')+1:]
			shape := swiftLiteralShape(v)
			if shape == "" {
				shape = shapes[v]
			}
			if shape == "" {
				continue
			}
			p := pshapes[bind[i]]
			if params[bind[i]].variadic {
				// A single element binds a variadic slot by element type.
				p = strings.TrimSuffix(strings.TrimPrefix(p, "["), "]")
			}
			if p != shape {
				return false
			}
			matched = true
		}
		return matched
	}
	var exact []*core.SymbolRecord
	for _, cand := range cands {
		if exactMatch(cand) {
			exact = append(exact, cand)
		}
	}
	if len(exact) == 0 {
		if caller != nil && caller.Name == cands[0].Name && caller.ParentSymbol == cands[0].ParentSymbol && exactMatch(caller) {
			return nil
		}
		return cands
	}
	return exact
}

// swiftParamList returns the text inside a declaration's first balanced
// paren group — its parameter list — and whether one was found.
func swiftParamList(signature string) (string, bool) {
	start := strings.IndexByte(signature, '(')
	if start < 0 {
		return "", false
	}
	depth := 0
	for i := start; i < len(signature); i++ {
		switch signature[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return signature[start+1 : i], true
			}
		}
	}
	return "", false
}

// swiftParam is one declared parameter's call-site-visible shape: its
// external label ("_" when unlabeled), whether a caller may omit it (it has
// a default), and whether it is variadic.
type swiftParam struct {
	label    string
	optional bool
	variadic bool
	closure  bool // function-typed: may be passed as a trailing closure
}

// swiftDeclParams parses a Swift callable's declared parameter labels from
// its Signature. Swift's overload identity is its labels, not its arity or
// types: `init(_ object: Any)`, `init(parseJSON jsonString: String)` and
// `init(stringLiteral value: StringLiteralType)` are three different
// functions the compiler tells apart by exactly this — and they are what a
// same-arity, same-shape overload set (which arity and value-type
// narrowing cannot split) collapses to. ok is false when the signature has
// no parseable parameter list, which callers treat as neutral.
func swiftDeclParams(s *core.SymbolRecord) (params []swiftParam, ok bool) {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = firstLine(s.RawText)
	}
	list, found := swiftParamList(src)
	if !found {
		return nil, false
	}
	if strings.TrimSpace(list) == "" {
		return nil, true
	}
	for _, g := range splitTopLevel(list, ',') {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		p := swiftParam{variadic: strings.Contains(g, "...")}
		if i := strings.IndexByte(g, '='); i >= 0 {
			p.optional = true
			g = g[:i]
		}
		head := g
		if i := strings.IndexByte(head, ':'); i >= 0 {
			head = head[:i]
			p.closure = strings.Contains(g[i:], "->")
		}
		fields := strings.Fields(head)
		if len(fields) == 0 {
			return nil, false
		}
		// `external internal: T` labels by the external name; `name: T`
		// uses name for both; `_ name: T` is unlabeled. Subscripts invert
		// the one-name case: `subscript(path: T)` is called as `x[p]`, its
		// parameter unlabeled unless an explicit external name is given.
		p.label = fields[0]
		if s.Name == "subscript" && len(fields) == 1 {
			p.label = "_"
		}
		params = append(params, p)
	}
	return params, true
}

// swiftCallLabels reads the labels astkit records for a Swift call
// ("label:value" per argument, "_" when unlabeled). ok is false for a call
// whose arguments were not recorded in that form.
func swiftCallLabels(args []string) (labels []string, ok bool) {
	for _, a := range args {
		i := strings.IndexByte(a, ':')
		if i < 0 {
			return nil, false
		}
		labels = append(labels, a[:i])
	}
	return labels, true
}

// swiftNarrowByLabels keeps the candidates whose declared parameter labels
// admit the call's argument labels — defaulted parameters may be omitted, a
// variadic parameter absorbs any run of matching labels, and a trailing
// function-typed parameter may be passed as a trailing closure (which the
// call's argument list does not record) — the same applicability rule
// swiftc uses before it ever considers types. Unlike arity narrowing this
// MAY empty the set, and applies even to a lone candidate: a label mismatch
// is the compiler's own "not this function", not an extraction gap
// (merge(with:typecheck:) calling merge(with:) is exactly what a
// same-name sibling with different labels looks like, and keeping it
// invented an edge). Reports decided=false, leaving the caller to fall back
// to arity, only when the call carries no label information at all.
func swiftNarrowByLabels(cands []*core.SymbolRecord, args []string) (out []*core.SymbolRecord, decided bool) {
	if len(cands) == 0 {
		return cands, false
	}
	callLabels, ok := swiftCallLabels(args)
	if !ok {
		return cands, false
	}
	var kept []*core.SymbolRecord
	for _, cand := range cands {
		params, ok := swiftDeclParams(cand)
		if !ok {
			kept = append(kept, cand) // unparseable: neutral
			continue
		}
		if swiftLabelsApplicable(params, callLabels) {
			kept = append(kept, cand)
		}
	}
	return kept, true
}

// swiftNarrowCall is the overload narrowing every Swift call gets: by
// argument labels when the call recorded them (then by the shapes of the
// caller's own parameters it passes along), else by arity.
//
// decided reports that labels were available and judged the set: a decided
// empty result means nothing in the repo fits this call (Swift scope is the
// whole repo, so no later inheritance fallback can know better), and the
// caller must stop resolving the call site rather than let those fallbacks
// re-source the same-name members it just ruled out.
func swiftNarrowCall(cands []*core.SymbolRecord, cs core.CallSite, caller *core.SymbolRecord) (out []*core.SymbolRecord, decided bool) {
	if out, decided = swiftNarrowByLabels(cands, cs.Args); decided {
		return swiftNarrowByArgShapes(out, cs.Args, swiftParamShapes(caller.Signature), caller), true
	}
	return filterByArgc(cands, cs.Argc), false
}

// swiftLabelsApplicable reports whether the declared parameters admit the
// call's labels in order.
func swiftLabelsApplicable(params []swiftParam, call []string) bool {
	_, ok := swiftBindLabels(params, call)
	return ok
}

// swiftBindLabels maps each call argument to the index of the declared
// parameter it binds, or reports that the labels do not fit.
func swiftBindLabels(params []swiftParam, call []string) ([]int, bool) {
	bind := make([]int, 0, len(call))
	ci := 0
	for i, p := range params {
		if p.variadic {
			for ci < len(call) && call[ci] == p.label {
				bind = append(bind, i)
				ci++
			}
			continue
		}
		if ci < len(call) && call[ci] == p.label {
			bind = append(bind, i)
			ci++
			continue
		}
		if p.optional || (p.closure && i == len(params)-1) {
			continue
		}
		return nil, false
	}
	if ci != len(call) {
		return nil, false
	}
	return bind, true
}

// swiftBareType reduces a Swift type expression to its final type
// identifier: "String?" -> "String", "[Person]" -> "Person", "Person!" ->
// "Person", "some Greeter" -> "Greeter". Primitives return "".
func swiftBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimRight(t, ",; \t")
	for {
		switch {
		case strings.HasPrefix(t, "some "):
			t = strings.TrimSpace(t[5:])
		case strings.HasPrefix(t, "any "):
			t = strings.TrimSpace(t[4:])
		case strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]"):
			inner := t[1 : len(t)-1]
			if colon := strings.IndexByte(inner, ':'); colon >= 0 {
				// [Key: Value] dictionary — the value type is what carries
				// methods a caller would invoke off an element.
				inner = inner[colon+1:]
			}
			t = strings.TrimSpace(inner)
		default:
			goto unwrapped
		}
	}
unwrapped:
	t = strings.TrimRight(t, "?!")
	t = strings.TrimSpace(t)
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	if t == "" || swiftPrimitives[strings.ToLower(t)] {
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
