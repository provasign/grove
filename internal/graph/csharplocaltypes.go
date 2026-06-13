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
				for _, cls := range idx.byName[strings.ToLower(className)] {
					if cls.Name != className || cls.RawText == "" {
						continue
					}
					switch cls.Kind {
					case core.KindClass, core.KindStruct, core.KindEnum, core.KindInterface:
					default:
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
					break
				}
				next = append(next, tsBaseClasses(idx, className, dirOf(symbol.FilePath))...)
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
		// var x = new Type() overrides any spurious local-decl capture.
		for _, m := range csharpVarNewRe.FindAllStringSubmatch(body, -1) {
			out[m[1]] = m[2]
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
