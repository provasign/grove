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
	}
	return out
}

// kotlinParamTypes parses "fun greet(name: String, age: Int): String" into
// {name: String, age: Int}. `this` is handled by receiver narrowing, not here.
func kotlinParamTypes(signature string) map[string]string {
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
