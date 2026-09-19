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

var (
	// let x: Type = ... / var x: Type
	swiftLetTypedRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*:\s*([^=;\n{]+)`)
	// let x = Type(...) / var x = Mod.Type(...) — Swift's plain call-syntax
	// initializer, indistinguishable at the source level from a function
	// call, so only a Capitalized callee is treated as a constructor.
	swiftLetCtorRe = regexp.MustCompile(`(?m)\b(?:let|var)\s+([a-z_]\w*)\s*=\s*(?:[A-Za-z_]\w*\.)*([A-Z]\w*)\s*\(`)
	// func f(name: Type, _ other: Type) — parameter list entries.
	swiftParamRe = regexp.MustCompile(`(?:[A-Za-z_]\w*|_)\s+([a-z_]\w*)\s*:\s*([^,)=]+)`)
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

	for name, typ := range swiftParamTypes(symbol.Signature) {
		out[name] = typ
	}

	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range swiftLetTypedRe.FindAllStringSubmatch(body, -1) {
			if typ := swiftBareType(m[2]); typ != "" {
				out[m[1]] = typ
			}
		}
		for _, m := range swiftLetCtorRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
		}
	}

	if symbol.ParentSymbol != "" {
		out["self"] = symbol.ParentSymbol
	}
	return out
}

// swiftParamTypes parses "func greet(name: String, age: Int) -> String" into
// {name: String, age: Int}. `self` is handled by receiver narrowing, not here.
func swiftParamTypes(signature string) map[string]string {
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
		m := swiftParamRe.FindStringSubmatch(strings.TrimSpace(g))
		if m == nil {
			continue
		}
		if typ := swiftBareType(m[2]); typ != "" {
			out[m[1]] = typ
		}
	}
	return out
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
