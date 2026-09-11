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
	cppLocalDeclRe = regexp.MustCompile(`(?m)(?:^|[;{}]\s*)\s*(?:const\s+)?(?:struct\s+|class\s+)?([A-Za-z_][\w:]*(?:<[^;={}()]+>)?)\s*[*&]?\s+(\w+)\s*[;=]`)
)

// cFamilyLocalTypes infers identifier → bare class name for one C/C++ callable.
func cFamilyLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}
	record := func(name, typ string) {
		if name == "" || typ == "" {
			return
		}
		out[name] = cFamilyTypeInContext(idx, symbol, typ)
	}

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
				for _, cls := range namedSymbols(idx, className) {
					if cls.RawText == "" {
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
								record(m[2], t)
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
		record(name, typ)
	}

	// Body locals (highest precedence): new-expressions.
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range cppLocalDeclRe.FindAllStringSubmatch(body, -1) {
			if t := cFamilyBareType(m[1]); t != "" {
				record(m[2], t)
			}
		}
		for _, m := range cppNewLocalRe.FindAllStringSubmatch(body, -1) {
			if t := cFamilyBareType(m[2]); t != "" {
				record(m[1], t)
			}
		}
	}
	delete(out, "this")
	return out
}

func cFamilyTypeInContext(idx *edgeIndex, symbol *core.SymbolRecord, typ string) string {
	if typ == "" {
		return typ
	}
	if split := strings.Index(typ, "::"); split >= 0 {
		prefix := typ[:split]
		for _, annotation := range symbol.Annotations {
			if local, target, ok := core.ParseCppNamespaceAlias(annotation); ok && local == prefix {
				candidate := target + typ[split:]
				if len(namedSymbols(idx, candidate)) > 0 {
					return candidate
				}
			}
		}
		return typ
	}
	qualified := symbol.QualifiedName
	if strings.Contains(symbol.ParentSymbol, "::") {
		qualified = symbol.ParentSymbol
	}
	if split := strings.LastIndex(qualified, "::"); split >= 0 {
		candidate := qualified[:split] + "::" + typ
		if len(namedSymbols(idx, candidate)) > 0 {
			return candidate
		}
	}
	for _, annotation := range symbol.Annotations {
		if local, target, ok := core.ParseCppUsingType(annotation); ok && local == typ && len(namedSymbols(idx, target)) > 0 {
			return target
		}
	}
	matched := ""
	for _, annotation := range symbol.Annotations {
		namespace, ok := core.ParseCppUsingNamespace(annotation)
		if !ok {
			continue
		}
		candidate := namespace + "::" + typ
		if len(namedSymbols(idx, candidate)) == 0 {
			continue
		}
		if matched != "" && matched != candidate {
			return typ
		}
		matched = candidate
	}
	if matched != "" {
		return matched
	}
	return typ
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
		outer := strings.TrimSpace(t[:i])
		inner := ""
		if strings.HasSuffix(t, ">") {
			inner = strings.TrimSpace(t[i+1 : len(t)-1])
		}
		leaf := outer
		if j := strings.LastIndex(leaf, "::"); j >= 0 {
			leaf = leaf[j+2:]
		}
		switch leaf {
		case "shared_ptr", "unique_ptr", "weak_ptr", "intrusive_ptr":
			if !strings.Contains(inner, ",") {
				return cFamilyBareType(inner)
			}
		}
		t = outer
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
	for _, segment := range strings.Split(t, "::") {
		if segment == "" {
			return ""
		}
		for i := 0; i < len(segment); i++ {
			c := segment[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
				return ""
			}
		}
	}
	return t
}
