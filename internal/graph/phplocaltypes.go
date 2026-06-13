package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// PHP local type inference, same shallow altitude as the C#/Java passes.
// Modern PHP declares types on parameters (`Foo $x`), properties
// (`private Bar $y;`), constructor-promoted properties
// (`public function __construct(private Repo $repo)`), and `new Foo()`
// locals — enough to resolve most `$var->method()` receivers. Variable
// names are keyed without their `$` sigil, matching the qualifiers astkit
// emits ($repo->save → qualifier "repo").

var (
	// $x = new Foo(  /  $x = new \Ns\Foo(
	phpNewLocalRe = regexp.MustCompile(`\$(\w+)\s*=\s*new\s+\\?([A-Za-z_][\w\\]*)`)
	// property: [modifiers] Type $name ;|=  (Type is class-like, ?nullable)
	phpPropertyRe = regexp.MustCompile(`(?m)(?:public|private|protected|readonly|static)\s+(?:(?:public|private|protected|readonly|static)\s+)*\??([A-Za-z_][\w\\]*)\s+\$(\w+)`)
	// return new Foo(  /  return new \Ns\Foo(
	phpReturnNewRe = regexp.MustCompile(`return\s+new\s+\\?([A-Za-z_][\w\\]*)`)
	// return $this; (fluent builder)
	phpReturnThisRe = regexp.MustCompile(`return\s+\$this\b`)
)

// phpLocalTypes infers identifier → bare class name for one PHP callable.
func phpLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Properties of the class and its ancestors (lowest precedence).
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
					case core.KindClass, core.KindInterface, core.KindTrait, core.KindEnum:
					default:
						continue
					}
					for _, m := range phpPropertyRe.FindAllStringSubmatch(cls.RawText, -1) {
						if t := phpBareType(m[1]); t != "" {
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

	// Parameters, including constructor-promoted properties.
	for name, typ := range phpParamTypes(symbol.Signature, symbol.RawText) {
		out[name] = typ
	}

	// Body locals (highest precedence): $x = new Foo().
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range phpNewLocalRe.FindAllStringSubmatch(body, -1) {
			if t := phpBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
	}
	delete(out, "this")
	return out
}

// phpCallResultType resolves the class produced by a call-result receiver in a
// fluent chain ("createInterfaceBuilder()" → Interface_) by inferring the
// return type of the named method/function in scope. Mirrors javaCallResultType:
// returns "" when no candidate resolves or candidates disagree, so an ambiguous
// fluent self-return (many "addStmt(): $this" across builder classes) drops
// rather than fanning out to every same-named downstream method.
func phpCallResultType(idx *edgeIndex, qualifier string, scope map[string]struct{}) string {
	name := strings.TrimSuffix(qualifier, "()")
	ret := ""
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name {
			continue
		}
		if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		r := phpReturnType(cand)
		if r == "" {
			return ""
		}
		if ret == "" {
			ret = r
		} else if ret != r {
			return ""
		}
	}
	return ret
}

// phpReturnType infers a callable's return class: a declared `: \Ns\Type`, else
// `return new X`, else `return $this` (fluent → the method's own class).
func phpReturnType(s *core.SymbolRecord) string {
	if i := strings.LastIndexByte(s.Signature, ')'); i >= 0 {
		tail := s.Signature[i+1:]
		if c := strings.IndexByte(tail, ':'); c >= 0 {
			if t := phpBareType(strings.TrimSpace(tail[c+1:])); t != "" {
				return t
			}
			// `: self`/`: static` reduce to "" in phpBareType → fall through to
			// the body, where `return $this` pins the concrete class.
		}
	}
	body := stripCommentsAndStrings(s.RawText)
	if m := phpReturnNewRe.FindStringSubmatch(body); m != nil {
		if t := phpBareType(m[1]); t != "" {
			return t
		}
	}
	if s.ParentSymbol != "" && phpReturnThisRe.MatchString(body) {
		return s.ParentSymbol
	}
	return ""
}

// phpParamTypes parses "function f(Foo $a, ?Bar $b, private Repo $c)" into
// {a: Foo, b: Bar, c: Repo}. Promoted-property modifiers and nullable/
// reference/variadic markers are stripped; untyped and union params skip.
func phpParamTypes(signature, rawText string) map[string]string {
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
		dollar := strings.IndexByte(g, '$')
		if dollar <= 0 {
			continue // untyped or no name
		}
		typePart := strings.TrimSpace(g[:dollar])
		// Drop promotion/visibility modifiers, keep the trailing type token.
		fields := strings.Fields(typePart)
		if len(fields) == 0 {
			continue
		}
		typeTok := fields[len(fields)-1]
		name := g[dollar+1:]
		// name may carry a default ("$x = 1") or be variadic ("...$x").
		if i := strings.IndexAny(name, " =)"); i >= 0 {
			name = name[:i]
		}
		name = strings.TrimSpace(name)
		if t := phpBareType(typeTok); t != "" && name != "" {
			out[name] = t
		}
	}
	return out
}

// phpBareType reduces a PHP type token to an indexable class name:
// "?Foo" → "Foo", "\Ns\Foo" → "Foo", "Foo&" → "Foo". Built-ins, unions,
// and intersections return "".
func phpBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "?")
	t = strings.TrimSuffix(t, "&")
	if strings.ContainsAny(t, "|&") {
		return "" // union/intersection: ambiguous
	}
	if i := strings.LastIndexByte(t, '\\'); i >= 0 {
		t = t[i+1:]
	}
	if t == "" {
		return ""
	}
	switch strings.ToLower(t) {
	case "int", "float", "string", "bool", "array", "void", "mixed", "object",
		"callable", "iterable", "null", "false", "true", "never", "self", "static", "parent":
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
