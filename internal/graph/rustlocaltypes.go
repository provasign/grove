package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Rust local type inference, same altitude as the Go version: signature
// parameters, typed let bindings, constructor-convention lets, and struct
// fields — the places Rust's annotation-heavy syntax makes cheap and
// reliable without a type checker. References and smart pointers deref
// transparently (&T, &mut T, Box<T>); a type that resolves to something we
// don't index is still recorded, because "known type, no candidate" is a
// correct drop (the Java unknown-receiver lesson: in a statically typed
// language, an unknown receiver type means the callee lives outside the
// repo).

var (
	// let x: Type = ... / let mut x: Type;
	rustLetTypedRe = regexp.MustCompile(`(?m)\blet\s+(?:mut\s+)?([a-z_]\w*)\s*:\s*([^=;{]+)`)
	// let x = Type::new(...) / Builder::default() / Foo::with_capacity(n) /
	// Foo::from(y) — the conventional Self-returning constructors.
	rustLetCtorRe = regexp.MustCompile(`(?m)\blet\s+(?:mut\s+)?([a-z_]\w*)\s*=\s*(?:[A-Za-z_]\w*::)*([A-Z]\w*)::(?:new|default|from|with_\w+)\s*[(<]`)
	// |x: Type, ...| — typed closure parameters.
	rustClosureParamRe = regexp.MustCompile(`\|([^|{}\n]{1,160})\|`)
	// let x = some_function(...) — typed through the callee's return type.
	rustLetCallRe = regexp.MustCompile(`(?m)\blet\s+(?:mut\s+)?([a-z_]\w*)\s*=\s*(?:[\w]+::)*([a-z_]\w*)\(`)
	// let x = SearcherBuilder::new()....build() — the builder convention:
	// the chain yields the built type, not the builder.
	rustBuilderChainRe = regexp.MustCompile(`(?ms)\blet\s+(?:mut\s+)?([a-z_]\w*)\s*=\s*(?:[A-Za-z_]\w*::)*([A-Z]\w*?)Builder::(?:new|default)\b[^;]*?\.build\(`)
)

// rustPrimitives are lowercase tokens that look like types but can never
// resolve to an indexed declaration's methods.
var rustPrimitives = map[string]bool{
	"i8": true, "i16": true, "i32": true, "i64": true, "i128": true, "isize": true,
	"u8": true, "u16": true, "u32": true, "u64": true, "u128": true, "usize": true,
	"f32": true, "f64": true, "bool": true, "char": true, "str": true,
}

// rustLocalTypes infers identifier → bare type name for one callable symbol.
func rustLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Fields of the impl type (lowest precedence: locals shadow fields).
	// Keyed by field name because qualifiers reduce to their last segment:
	// self.field.method() and args.field.method() both query "field".
	if symbol.ParentSymbol != "" {
		for name, typ := range rustFieldTypes(idx, symbol.ParentSymbol, symbol.FilePath) {
			out[name] = typ
		}
	}

	params := rustParamTypes(symbol.Signature)
	for name, typ := range params {
		out[name] = typ
	}

	lets := map[string]string{}
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		// Function-result lets carry the lowest confidence of the let
		// family — explicit annotations and constructor conventions
		// overwrite them below.
		var callScope map[string]struct{}
		for _, m := range rustLetCallRe.FindAllStringSubmatch(body, -1) {
			if callScope == nil {
				callScope = idx.importedFiles(symbol.FilePath)
			}
			if rets := rustCallResultTypes(idx, m[2]+"()", symbol, callScope); len(rets) == 1 {
				for typ := range rets {
					lets[m[1]] = typ
				}
			}
		}
		for _, m := range rustLetTypedRe.FindAllStringSubmatch(body, -1) {
			if typ := rustBareType(m[2]); typ != "" {
				lets[m[1]] = typ
			}
		}
		for _, m := range rustLetCtorRe.FindAllStringSubmatch(body, -1) {
			lets[m[1]] = m[2]
		}
		for _, m := range rustBuilderChainRe.FindAllStringSubmatch(body, -1) {
			if typ := m[2]; typeSymbolExists(idx, typ) {
				lets[m[1]] = typ
			}
		}
		for _, m := range rustClosureParamRe.FindAllStringSubmatch(body, -1) {
			for _, g := range splitTopLevel(m[1], ',') {
				colon := strings.IndexByte(g, ':')
				if colon < 0 {
					continue
				}
				name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(g[:colon]), "mut "))
				if name == "" || !isRustIdent(name) {
					continue
				}
				if typ := rustBareType(g[colon+1:]); typ != "" {
					lets[name] = typ
				}
			}
		}
	}
	for name, typ := range lets {
		out[name] = typ
	}

	// Generic parameters resolve to their trait bound: a call through
	// m: M where M: Matcher dispatches via the Matcher trait's methods,
	// which is exactly the declaration the typed oracle records.
	if bounds := rustGenericBounds(symbol.TypeParameters); len(bounds) > 0 {
		for name, typ := range out {
			if bound, ok := bounds[typ]; ok {
				out[name] = bound
			}
		}
	}

	// One hop through fields of in-repo parameter/local types: args.mode
	// reduces to qualifier "mode", which only field knowledge can type.
	// Conflicting field names across two in-scope types stay untyped.
	hop := map[string]string{}
	conflict := map[string]bool{}
	for _, typ := range out {
		for fname, ftype := range rustFieldTypes(idx, typ, symbol.FilePath) {
			if prev, ok := hop[fname]; ok && prev != ftype {
				conflict[fname] = true
				continue
			}
			hop[fname] = ftype
		}
	}
	for fname, ftype := range hop {
		if _, taken := out[fname]; !taken && !conflict[fname] {
			out[fname] = ftype
		}
	}

	// Self::method() resolves on the impl type, like the receiver does.
	if symbol.ParentSymbol != "" {
		out["Self"] = symbol.ParentSymbol
	}
	delete(out, "_")
	return out
}

// rustCallResultTypes resolves a "name()" qualifier through the return
// types of the named in-repo functions or methods, the Rust edition of
// javaCallResultType. Builder method names collide across crates
// (SearcherBuilder and StandardBuilder both declare line_number), so
// candidate declarations are first filtered to types the caller's body
// actually mentions; the surviving distinct return types are all plausible
// receivers and the caller narrows against the set. Any candidate with an
// unparseable return type makes the result unknown — guessing here turns
// into confident wrong drops downstream.
func rustCallResultTypes(idx *edgeIndex, qualifier string, symbol *core.SymbolRecord, scope map[string]struct{}) map[string]bool {
	name := strings.TrimSuffix(qualifier, "()")
	if name == "" {
		return nil
	}
	var all, mentioned []string
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name || cand.Language != "rust" {
			continue
		}
		switch cand.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
		default:
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		r := rustReturnType(cand.Signature)
		if r == "Self" {
			r = cand.ParentSymbol
		}
		if r == "" {
			return nil
		}
		all = append(all, r)
		if cand.ParentSymbol == "" || rustMentionsType(symbol.RawText, cand.ParentSymbol) {
			mentioned = append(mentioned, r)
		}
	}
	if len(mentioned) > 0 {
		all = mentioned
	}
	if len(all) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, r := range all {
		out[r] = true
	}
	return out
}

// rustMentionsType reports whether the body text contains the type name as
// a whole identifier — plain Contains would let Searcher claim a body that
// only ever names SearcherTester.
func rustMentionsType(body, name string) bool {
	if body == "" || name == "" {
		return false
	}
	for at := 0; ; {
		i := strings.Index(body[at:], name)
		if i < 0 {
			return false
		}
		i += at
		before := byte(0)
		if i > 0 {
			before = body[i-1]
		}
		after := byte(0)
		if j := i + len(name); j < len(body) {
			after = body[j]
		}
		if !isIdentByte(before) && !isIdentByte(after) {
			return true
		}
		at = i + len(name)
	}
}

func isIdentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}

// rustImplTrait reads the trait recorded by astkit on methods declared in
// an "impl Trait for Type" block.
func rustImplTrait(symbol *core.SymbolRecord) string {
	for _, ann := range symbol.Annotations {
		if t, ok := strings.CutPrefix(ann, "impl_trait:"); ok {
			return t
		}
	}
	return ""
}

// rustReturnType extracts the bare return type from "fn x(...) -> Type".
func rustReturnType(signature string) string {
	i := strings.LastIndex(signature, "->")
	if i < 0 {
		return ""
	}
	t := strings.TrimSpace(signature[i+2:])
	t = strings.TrimRight(t, "{ \t\n")
	if t == "Self" || strings.HasPrefix(t, "&mut Self") || strings.HasPrefix(t, "&Self") {
		return "Self"
	}
	return rustBareType(t)
}

// rustGenericBounds maps a type-parameter name to the bare name of its
// first trait bound: "M: Matcher" → {M: Matcher}, "S: Sink + Send" →
// {S: Sink}. Unbounded parameters and lifetimes contribute nothing.
func rustGenericBounds(typeParams []string) map[string]string {
	var out map[string]string
	for _, tp := range typeParams {
		colon := strings.IndexByte(tp, ':')
		if colon < 0 {
			continue
		}
		name := strings.TrimSpace(tp[:colon])
		if !isRustIdent(name) || strings.HasPrefix(name, "'") {
			continue
		}
		boundExpr := tp[colon+1:]
		if i := strings.IndexByte(boundExpr, '+'); i >= 0 {
			boundExpr = boundExpr[:i]
		}
		if bound := rustBareType(boundExpr); bound != "" {
			if out == nil {
				out = map[string]string{}
			}
			out[name] = bound
		}
	}
	return out
}

func isRustIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// rustFieldTypes maps field name → bare type for a named in-repo type.
// Same-file declarations win, then same-directory, then any.
func rustFieldTypes(idx *edgeIndex, typeName, preferFile string) map[string]string {
	var typeSym *core.SymbolRecord
	dir := dirOf(preferFile)
	for _, cand := range idx.byName[strings.ToLower(typeName)] {
		if cand.Name != typeName {
			continue
		}
		switch cand.Kind {
		case core.KindStruct, core.KindClass, core.KindEnum, core.KindType:
		default:
			continue
		}
		if typeSym == nil {
			typeSym = cand
		} else if cand.FilePath == preferFile && typeSym.FilePath != preferFile {
			typeSym = cand
		} else if dirOf(cand.FilePath) == dir && typeSym.FilePath != preferFile && dirOf(typeSym.FilePath) != dir {
			typeSym = cand
		}
	}
	if typeSym == nil {
		return nil
	}
	out := map[string]string{}
	for _, cand := range idx.byFile[typeSym.FilePath] {
		if cand.Kind != core.KindField || cand.ParentSymbol != typeName {
			continue
		}
		// Field signature: "pub mode: Mode," / "haystack: PathBuf,"
		sig := cand.Signature
		if i := strings.IndexByte(sig, ':'); i >= 0 {
			if typ := rustBareType(sig[i+1:]); typ != "" {
				out[cand.Name] = typ
			}
		}
	}
	return out
}

// rustParamTypes parses "fn update(&self, v: FlagValue, args: &mut LowArgs)
// -> Result<()>" into {v: FlagValue, args: LowArgs}. Self parameters are
// handled by receiver narrowing, not here.
func rustParamTypes(signature string) map[string]string {
	out := map[string]string{}
	start := strings.IndexByte(signature, '(')
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
		colon := strings.IndexByte(g, ':')
		if colon < 0 {
			continue // &self / mut self / unnamed
		}
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(g[:colon]), "mut "))
		if name == "" || name == "self" || strings.ContainsAny(name, "&( ") {
			continue
		}
		if typ := rustBareType(g[colon+1:]); typ != "" {
			out[name] = typ
		}
	}
	return out
}

// rustBareType reduces a Rust type expression to its final type identifier:
// "&mut LowArgs" → "LowArgs", "Box<dyn Flag>" → "Flag", "&'a Searcher" →
// "Searcher", "crate::flags::Mode" → "Mode". Transparent wrappers (references,
// Box/Rc/Arc, dyn/impl) peel away; containers like Vec/Option keep their own
// name — their methods belong to the container. Primitives, tuples, slices,
// and fn types return "".
func rustBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimRight(t, ",; \t")
	for {
		switch {
		case strings.HasPrefix(t, "&"):
			t = strings.TrimSpace(t[1:])
		case strings.HasPrefix(t, "'"): // lifetime
			if i := strings.IndexAny(t, " \t"); i >= 0 {
				t = strings.TrimSpace(t[i+1:])
			} else {
				return ""
			}
		case strings.HasPrefix(t, "mut "):
			t = strings.TrimSpace(t[4:])
		case strings.HasPrefix(t, "dyn "):
			t = strings.TrimSpace(t[4:])
		case strings.HasPrefix(t, "impl "):
			t = strings.TrimSpace(t[5:])
		default:
			goto unwrapped
		}
	}
unwrapped:
	for _, wrapper := range []string{"Box<", "Rc<", "Arc<", "RefCell<", "Cell<"} {
		if strings.HasPrefix(t, wrapper) && strings.HasSuffix(t, ">") {
			return rustBareType(t[len(wrapper) : len(t)-1])
		}
	}
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = strings.TrimSuffix(t[:i], "::")
	}
	if i := strings.LastIndex(t, "::"); i >= 0 {
		t = t[i+2:]
	}
	t = strings.TrimSpace(t)
	if t == "" || rustPrimitives[t] {
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	if t[0] >= 'a' && t[0] <= 'z' {
		// Lowercase-initial: a module segment or primitive, not a type.
		return ""
	}
	return t
}
