package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// C/C++ local type inference. C calls are mostly plain global functions
// (receiver qualifiers empty), so this matters most for C++ method calls:
// parameters (`const Foo& x`, `Bar* y`), `auto x = new Foo()` / `Foo x;`
// locals, and the enclosing class's fields. Pointers, references, and cv/
// struct qualifiers are stripped to the bare class name.

var (
	// Foo x = new Foo(  /  auto x = new Foo(
	cppNewLocalRe = regexp.MustCompile(`(?m)\b(?:auto|[A-Za-z_][\w:]*\s*[*&]?)\s+(\w+)\s*=\s*new\s+([A-Za-z_][\w:]*)`)
	// Type var;  /  Type *var;  (class-like Type, uppercase or struct-tagged)
	cppLocalDeclRe = regexp.MustCompile(`(?m)(?:^|[;{}]\s*)\s*(?:const\s+)?(?:struct\s+|class\s+)?([A-Za-z_][\w:]*)\s*[*&]?\s+(\w+)\s*[;=]`)
)

// cFamilyLocalTypes infers identifier → bare class name for one C/C++ callable.
func cFamilyLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Fields of the enclosing class and its bases (C++).
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
					case core.KindClass, core.KindStruct:
					default:
						continue
					}
					body := cls.RawText
					if i := strings.IndexByte(body, '{'); i >= 0 {
						body = body[i+1:]
					}
					for _, m := range cppLocalDeclRe.FindAllStringSubmatch(body, -1) {
						if t := cFamilyBareType(m[1]); t != "" {
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

	// Parameters.
	for name, typ := range cFamilyParamTypes(symbol.Signature, symbol.RawText) {
		out[name] = typ
	}

	// Body locals (highest precedence): new-expressions.
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range cppNewLocalRe.FindAllStringSubmatch(body, -1) {
			if t := cFamilyBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
	}
	delete(out, "this")
	return out
}

// cFamilyParamTypes parses "(const Foo& a, Bar* b)" into {a: Foo, b: Bar}.
func cFamilyParamTypes(signature, rawText string) map[string]string {
	out := map[string]string{}
	src := signature
	if !strings.Contains(src, "(") {
		src = rawText
	}
	open := strings.IndexByte(src, '(')
	if open < 0 {
		return out
	}
	depth, end := 0, -1
	for i := open; i < len(src); i++ {
		switch src[i] {
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
	for _, g := range splitTopLevel(src[open+1:end], ',') {
		g = strings.TrimSpace(g)
		// The parameter name is the last identifier; the type is what
		// precedes it (after stripping pointer/ref/cv markers).
		g = strings.TrimRight(g, " \t")
		nameStart := len(g)
		for nameStart > 0 {
			c := g[nameStart-1]
			if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' {
				nameStart--
			} else {
				break
			}
		}
		name := g[nameStart:]
		typePart := strings.TrimSpace(g[:nameStart])
		if name == "" || typePart == "" {
			continue
		}
		fields := strings.Fields(typePart)
		if len(fields) == 0 {
			continue
		}
		if t := cFamilyBareType(fields[len(fields)-1]); t != "" {
			out[name] = t
		}
	}
	return out
}

// cFamilyBareType reduces a C/C++ type token to a bare class name: "Foo*" →
// "Foo", "Ns::Bar&" → "Bar", "const Baz" handled by the caller's field
// split. Primitives and lowercase-leading tokens return "".
func cFamilyBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "const ")
	t = strings.TrimPrefix(t, "struct ")
	t = strings.TrimPrefix(t, "class ")
	t = strings.TrimRight(t, "*& \t")
	if i := strings.IndexByte(t, '<'); i >= 0 {
		t = t[:i]
	}
	if i := strings.LastIndex(t, "::"); i >= 0 {
		t = t[i+2:]
	}
	if t == "" {
		return ""
	}
	switch t {
	case "void", "int", "char", "bool", "float", "double", "long", "short",
		"unsigned", "signed", "size_t", "auto", "wchar_t", "int8_t", "int16_t",
		"int32_t", "int64_t", "uint8_t", "uint16_t", "uint32_t", "uint64_t":
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	return t
}
