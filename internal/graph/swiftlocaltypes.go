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
	text := codeSignature(decl)
	if text == "" {
		text = maskCode(decl.Language, firstLine(decl.RawText))
	}
	names := swiftBaseNames(text)
	if len(names) == 0 || !namedSymbolIsClass(idx, names[0]) {
		return nil
	}
	return names[:1]
}

// swiftConformances lists every indexed supertype a Swift type declares:
// its superclass and the protocols it conforms to.
func swiftConformances(idx *edgeIndex, typeName, preferDir string) []string {
	decl := tsChosenTypeDecl(idx, typeName, preferDir)
	if decl == nil {
		return nil
	}
	text := codeSignature(decl)
	if text == "" {
		text = maskCode(decl.Language, firstLine(decl.RawText))
	}
	var out []string
	for _, n := range swiftBaseNames(text) {
		if typeSymbolExists(idx, n) {
			out = append(out, n)
		}
	}
	return out
}

var (
	// let x: Type = ... / var x: Type
	swiftLetTypedRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*:\s*([^=;\n{]+)`)
	// let x = Type(...) / var x = Mod.Type(...) — Swift's plain call-syntax
	// initializer, indistinguishable at the source level from a function
	// call, so only a Capitalized callee is treated as a constructor.
	swiftLetCtorRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*=\s*(?:try[?!]?\s+)?(?:await\s+)?(?:[A-Za-z_]\w*\.)*([A-Z]\w*)\s*\(`)
	// let file = try folder.createFile(named: "x"): typed by the method's
	// declared result.
	swiftLetCallRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*=\s*(?:try[?!]?\s+)?(?:await\s+)?([a-z_]\w*)\.([a-z_]\w*)\s*\(`)
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
	for name, typ := range swiftParamShapes(codeSignature(symbol)) {
		out[name] = typ
	}

	if symbol.RawText != "" {
		body := maskCode(symbol.Language, symbol.RawText)
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
			if m := swiftLetTypedRe.FindStringSubmatch(codeSignature(cand)); m != nil {
				if typ := swiftShapeType(m[2]); typ != "" {
					out[cand.Name] = typ
				}
			} else if m := swiftLetCtorRe.FindStringSubmatch(codeSignature(cand)); m != nil {
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
		if typ == "Iterator" && !swiftTypeIndexed(idx, typ) {
			if it := swiftSequenceIterator(idx, symbol); it != "" {
				out[name] = it
			}
		}
	}
	if symbol.RawText != "" {
		body := maskCode(symbol.Language, symbol.RawText)
		for _, m := range swiftLetCallRe.FindAllStringSubmatch(body, -1) {
			if _, done := out[m[1]]; done {
				continue
			}
			recv := out[m[2]]
			if recv == "" || recv[0] == '[' {
				continue
			}
			if ret := swiftMethodReturnType(idx, recv, m[3], dirOf(symbol.FilePath)); ret != "" {
				out[m[1]] = ret
			}
		}
		// `forEach { try $0.delete() }` inside ChildSequence<Child: Location>:
		// $0 is the element (Child), a generic placeholder standing for its
		// constraint (Location). Typed only when every such closure in the
		// body agrees.
		dollar := ""
		consistent := true
		for _, m := range swiftClosureIterRe.FindAllStringSubmatch(body, -1) {
			recv := symbol.ParentSymbol
			if m[1] != "" && m[1] != "self" {
				recv = out[m[1]]
			}
			elem := ""
			if recv != "" && recv[0] != '[' {
				elem = strings.TrimRight(swiftElementType(idx, recv), "?!")
				elem = swiftGenericConstraint(idx, recv, elem)
			}
			if elem == "" || strings.HasPrefix(elem, "(") || dollar != "" && dollar != elem {
				consistent = false
				break
			}
			dollar = elem
		}
		if consistent && dollar != "" {
			out["$0"] = dollar
		}
		// for (key, _) in other: with other a JSON (a Collection whose
		// subscript(position:) returns (String, JSON)), key is a String.
		for _, m := range swiftForInRe.FindAllStringSubmatch(body, -1) {
			coll := out[m[3]]
			if coll == "" || coll[0] == '[' {
				continue
			}
			elem := swiftElementType(idx, coll)
			if elem == "" {
				continue
			}
			if m[2] == "" {
				if !strings.HasPrefix(elem, "(") {
					out[m[1]] = elem
				}
				continue
			}
			names := splitTopLevel(m[2], ',')
			if !strings.HasPrefix(elem, "(") || !strings.HasSuffix(elem, ")") {
				continue
			}
			parts := splitTopLevel(elem[1:len(elem)-1], ',')
			if len(parts) != len(names) {
				continue
			}
			for i, n := range names {
				n = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(n), "let "))
				if n == "" || n == "_" {
					continue
				}
				if t := swiftShapeType(parts[i]); t != "" {
					out[n] = t
				}
			}
		}
	}
	return out
}

// for x in coll / for (a, b) in coll / for case let ...
var swiftForInRe = regexp.MustCompile(`(?m)\bfor\s+(?:case\s+)?(?:let\s+|var\s+)?(?:([a-z_]\w*)|\(([^()]*)\))\s+in\s+([a-z_]\w*)\s*[{\n]`)

// swiftElementType is the element an in-repo Sequence/Collection type
// yields: its subscript(position:) result, else its iterator's next()
// result (optional dropped). "(String, JSON)" for SwiftyJSON's JSON.
func swiftElementType(idx *edgeIndex, typ string) string {
	for _, f := range idx.byName["subscript"] {
		if f.Language != "swift" || f.ParentSymbol != typ {
			continue
		}
		sig := codeSignature(f)
		if !strings.Contains(sig, "(position:") {
			continue
		}
		if i := strings.LastIndex(sig, "->"); i >= 0 {
			t := strings.TrimSpace(sig[i+2:])
			if j := strings.IndexByte(t, '{'); j >= 0 {
				t = strings.TrimSpace(t[:j])
			}
			return t
		}
	}
	for _, m := range idx.byName["makeiterator"] {
		if m.Language != "swift" || m.ParentSymbol != typ {
			continue
		}
		it := swiftReturnTypeName(codeSignature(m))
		if it == "" {
			return ""
		}
		for _, n := range idx.byName["next"] {
			if n.Language == "swift" && n.ParentSymbol == it {
				return swiftReturnTypeName(codeSignature(n))
			}
		}
	}
	return ""
}

// swiftReturnTypeName reads the bare result type of a Swift function
// signature (`func createFile(named: String) throws -> File` -> File), ""
// for none, a container or a type parameter.
func swiftReturnTypeName(sig string) string {
	i := strings.LastIndex(sig, "->")
	if i < 0 {
		return ""
	}
	t := strings.TrimSpace(sig[i+2:])
	if j := strings.IndexByte(t, '{'); j >= 0 {
		t = strings.TrimSpace(t[:j])
	}
	if j := strings.Index(t, " where "); j >= 0 {
		t = t[:j]
	}
	t = strings.TrimRight(strings.TrimSpace(t), "?!")
	if k := strings.IndexByte(t, '<'); k > 0 {
		t = t[:k]
	}
	if k := strings.LastIndexByte(t, '.'); k >= 0 {
		t = t[k+1:]
	}
	if t == "" || t[0] < 'A' || t[0] > 'Z' || strings.ContainsAny(t, "[]() ,") {
		return ""
	}
	return t
}

// swiftMethodReturnType is the declared result of typ's (or a conformed
// protocol's / extension's) method name, when exactly one type results.
func swiftMethodReturnType(idx *edgeIndex, typ, name, preferDir string) string {
	owners := append([]string{typ}, swiftConformances(idx, typ, preferDir)...)
	ret := ""
	for _, o := range owners {
		for _, f := range idx.byName[strings.ToLower(name)] {
			if f.Name != name || f.Language != "swift" || f.ParentSymbol != o || !graphCallableSymbol(f) {
				continue
			}
			r := swiftReturnTypeName(codeSignature(f))
			if r == "Self" {
				r = typ
			}
			if r == "" || ret != "" && ret != r {
				return ""
			}
			ret = r
		}
		if ret != "" {
			return ret
		}
	}
	return ret
}

// swiftSequenceIterator names `Iterator` inside a Sequence type: the
// associated type its makeIterator() returns (Files' ChildSequence:
// ChildIterator).
func swiftSequenceIterator(idx *edgeIndex, symbol *core.SymbolRecord) string {
	if symbol.ParentSymbol == "" {
		return ""
	}
	for _, m := range idx.byName["makeiterator"] {
		if m.Language == "swift" && m.ParentSymbol == symbol.ParentSymbol {
			if r := swiftReturnTypeName(codeSignature(m)); r != "" {
				return r
			}
		}
	}
	return ""
}

// swiftChainType types a dotted receiver chain (`Folder.home`) hop by hop:
// the head is a typed local or a type name, each later hop a property
// declared on the type reached so far (its extensions included).
func swiftChainType(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	for len(parts) > 0 && parts[0] == "self" {
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return ""
	}
	cur, ok := localTypes[parts[0]]
	if !ok {
		if !swiftTypeIndexed(idx, parts[0]) {
			return ""
		}
		cur = parts[0]
	}
	for _, seg := range parts[1:] {
		next := ""
		for _, f := range idx.byName[strings.ToLower(seg)] {
			if f.Name != seg || f.Language != "swift" || f.Kind != core.KindField || f.ParentSymbol != cur {
				continue
			}
			if m := swiftLetTypedRe.FindStringSubmatch(codeSignature(f)); m != nil {
				next = swiftShapeType(m[2])
			}
			break
		}
		if i := strings.IndexByte(next, '<'); i > 0 {
			next = next[:i]
		}
		if next == "Self" {
			next = cur
		}
		if next == "" || next[0] == '[' {
			return ""
		}
		cur = next
	}
	return cur
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
		m := swiftTypealiasRe.FindStringSubmatch(codeSignature(cand))
		if m == nil {
			m = swiftTypealiasRe.FindStringSubmatch(maskCode(cand.Language, firstLine(cand.RawText)))
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
	src := codeSignature(s)
	if !strings.Contains(src, ")") {
		src = maskCode(s.Language, firstLine(s.RawText))
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
	src := codeSignature(s)
	if !strings.Contains(src, ")") {
		src = maskCode(s.Language, firstLine(s.RawText))
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
func swiftNarrowCall(idx *edgeIndex, cands []*core.SymbolRecord, cs core.CallSite, caller *core.SymbolRecord, localTypes map[string]string) (out []*core.SymbolRecord, decided bool) {
	if out, decided = swiftNarrowByLabels(cands, cs.Args); decided {
		shapes := swiftParamShapes(codeSignature(caller))
		for name, typ := range localTypes {
			if _, ok := shapes[name]; !ok && name != "self" {
				shapes[name] = typ
			}
		}
		out = swiftNarrowByArgShapes(out, cs.Args, shapes, caller)
		return swiftDropIncompatible(idx, out, cs.Args, shapes), true
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

// swiftDropIncompatible removes the candidates a typed argument cannot bind:
// a String into an Index or an array parameter. A parameter accepts its own
// type, a protocol the argument's type conforms to, Any/AnyObject, or a
// generic placeholder; a variadic parameter accepts one element. Untyped
// arguments and unparsable declarations decide nothing; the set is never
// emptied.
func swiftDropIncompatible(idx *edgeIndex, cands []*core.SymbolRecord, args []string, shapes map[string]string) []*core.SymbolRecord {
	if len(cands) < 2 {
		return cands
	}
	callLabels, ok := swiftCallLabels(args)
	if !ok {
		return cands
	}
	var kept []*core.SymbolRecord
	for _, cand := range cands {
		params, ok := swiftDeclParams(cand)
		pshapes := swiftDeclParamShapes(cand)
		bind, ok2 := swiftBindLabels(params, callLabels)
		if !ok || !ok2 || len(pshapes) != len(params) {
			kept = append(kept, cand)
			continue
		}
		fits := true
		for i, a := range args {
			v := a[strings.IndexByte(a, ':')+1:]
			shape := swiftLiteralShape(v)
			if shape == "" {
				shape = shapes[v]
			}
			if shape == "" || strings.HasPrefix(shape, "(") {
				continue
			}
			p := pshapes[bind[i]]
			if params[bind[i]].variadic {
				p = strings.TrimSuffix(strings.TrimPrefix(p, "["), "]")
			}
			if !swiftAccepts(idx, p, shape, cand) {
				fits = false
				break
			}
		}
		if fits {
			kept = append(kept, cand)
		}
	}
	if len(kept) == 0 {
		return cands
	}
	return kept
}

func swiftAccepts(idx *edgeIndex, param, arg string, cand *core.SymbolRecord) bool {
	if param == "" || param == arg || param == "Any" || param == "AnyObject" {
		return true
	}
	if i := strings.IndexByte(param, '<'); i > 0 {
		param = param[:i]
	}
	if param == arg {
		return true
	}
	pArr, aArr := strings.HasPrefix(param, "["), strings.HasPrefix(arg, "[")
	if pArr != aArr {
		return false
	}
	if pArr {
		return true // element types are not compared
	}
	// A generic placeholder declared by the candidate (or a lone capital).
	if len(param) == 1 || strings.Contains(cand.Signature, "<"+param) || strings.Contains(cand.Signature, ", "+param+":") {
		return true
	}
	for _, c := range swiftConformances(idx, arg, "") {
		if c == param {
			return true
		}
	}
	if swiftWitnesses(idx, arg, param) {
		return true
	}
	return !swiftTypeIndexed(idx, param) && !swiftTypeIndexed(idx, arg) && !swiftPrimitives[strings.ToLower(param)] && !swiftPrimitives[strings.ToLower(arg)]
}

// swiftWitnesses reports whether the repository makes typ conform to the
// indexed protocol proto by extension: `extension String: JSONSubscriptType`
// is not itself indexed (String is the standard library's), but its members
// are (String.jsonKey). typ conforms when it has a member for every one
// proto declares.
func swiftWitnesses(idx *edgeIndex, typ, proto string) bool {
	if idx == nil || swiftTypeIndexed(idx, typ) {
		return false
	}
	var files []string
	for _, p := range namedSymbols(idx, proto) {
		if p.Language == "swift" && p.Kind == core.KindInterface {
			files = append(files, p.FilePath) // requirements live in the protocol body
		}
	}
	if len(files) == 0 {
		return false
	}
	required := 0
	for _, f := range files {
		for _, m := range idx.byFile[f] {
			if m.Language != "swift" || m.ParentSymbol != proto || m.Name == "" {
				continue
			}
			required++
			found := false
			for _, w := range idx.byName[strings.ToLower(m.Name)] {
				if w.Name == m.Name && w.ParentSymbol == typ && w.Language == "swift" {
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
	}
	return required > 0
}

var swiftClosureIterRe = regexp.MustCompile(`(?:\b([a-z_]\w*)\.|(?:^|[^.\w]))(?:forEach|map|compactMap|flatMap|filter)\s*\{`)

// swiftGenericConstraint maps a generic placeholder of owner
// (`struct ChildSequence<Child: Location>`) to its constraint (Location);
// any other type comes back unchanged.
func swiftGenericConstraint(idx *edgeIndex, owner, typ string) string {
	if typ == "" {
		return ""
	}
	for _, d := range namedSymbols(idx, owner) {
		if d.Language != "swift" {
			continue
		}
		sig := codeSignature(d)
		i := strings.IndexByte(sig, '<')
		if i < 0 {
			continue
		}
		j := strings.IndexByte(sig[i:], '>')
		if j < 0 {
			continue
		}
		for _, g := range splitTopLevel(sig[i+1:i+j], ',') {
			name, constraint, ok := strings.Cut(strings.TrimSpace(g), ":")
			if ok && strings.TrimSpace(name) == typ {
				return swiftShapeType(constraint)
			}
		}
	}
	return typ
}
