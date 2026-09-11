package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

var (
	phpTraitUseRe     = regexp.MustCompile(`(?s)\buse[ \t]+([^;{\n]+)(?:;|\{([^}]*)\})`)
	phpTraitInsteadRe = regexp.MustCompile(`(?i)^\s*([\\\w]+)::(\w+)\s+insteadof\s+(.+?)\s*$`)
	phpTraitAliasRe   = regexp.MustCompile(`(?i)^\s*(?:([\\\w]+)::)?(\w+)\s+as\s+(?:(?:public|protected|private)\s+)?(\w+)?\s*$`)
)

type phpTraitAlias struct {
	trait  string
	method string
}

type phpTraitRules struct {
	used      map[string]bool
	preferred map[string]string
	excluded  map[string]map[string]bool
	aliases   map[string]phpTraitAlias
	traits    []string
}

func phpTraitRulesFor(rawText string) phpTraitRules {
	rules := phpTraitRules{
		used:      map[string]bool{},
		preferred: map[string]string{},
		excluded:  map[string]map[string]bool{},
		aliases:   map[string]phpTraitAlias{},
	}
	for _, match := range phpTraitUseRe.FindAllStringSubmatch(rawText, -1) {
		if len(match) < 2 || strings.ContainsAny(match[1], "($") {
			continue // closure `use ($x) { ... }`, not trait composition
		}
		var traits []string
		for _, raw := range strings.Split(match[1], ",") {
			name := phpSimpleTypeName(raw)
			if name == "" {
				continue
			}
			key := strings.ToLower(name)
			if !rules.used[key] {
				rules.traits = append(rules.traits, name)
			}
			rules.used[key] = true
			traits = append(traits, name)
		}
		if len(match) < 3 || match[2] == "" {
			continue
		}
		for _, statement := range strings.Split(match[2], ";") {
			if parts := phpTraitInsteadRe.FindStringSubmatch(statement); len(parts) == 4 {
				method := strings.ToLower(parts[2])
				rules.preferred[method] = strings.ToLower(phpSimpleTypeName(parts[1]))
				if rules.excluded[method] == nil {
					rules.excluded[method] = map[string]bool{}
				}
				for _, loser := range strings.Split(parts[3], ",") {
					rules.excluded[method][strings.ToLower(phpSimpleTypeName(loser))] = true
				}
				continue
			}
			if parts := phpTraitAliasRe.FindStringSubmatch(statement); len(parts) == 4 && parts[3] != "" {
				trait := phpSimpleTypeName(parts[1])
				if trait == "" && len(traits) == 1 {
					trait = traits[0]
				}
				rules.aliases[strings.ToLower(parts[3])] = phpTraitAlias{
					trait: strings.ToLower(trait), method: parts[2],
				}
			}
		}
	}
	return rules
}

func phpSimpleTypeName(raw string) string {
	raw = strings.Trim(strings.TrimSpace(raw), "\\")
	if i := strings.LastIndexByte(raw, '\\'); i >= 0 {
		raw = raw[i+1:]
	}
	return strings.TrimSpace(raw)
}

// phpTraitCallTargets applies PHP's trait adaptation block to a self call.
// It reports decided only when the enclosing class composes a trait method or
// alias with this name; callers can then bypass unrelated inheritance fanout.
func phpTraitCallTargets(idx *edgeIndex, caller *core.SymbolRecord, name string, initial []*core.SymbolRecord) ([]*core.SymbolRecord, bool) {
	var class *core.SymbolRecord
	for _, candidate := range namedSymbols(idx, caller.ParentSymbol) {
		if candidate.FilePath == caller.FilePath && candidate.Kind == core.KindClass {
			class = candidate
			break
		}
	}
	if class == nil {
		return initial, false
	}
	rules := phpTraitRulesFor(class.RawText)
	lookupName := name
	wantedTrait := ""
	aliased := false
	if alias, ok := rules.aliases[strings.ToLower(name)]; ok {
		lookupName = alias.method
		wantedTrait = alias.trait
		initial = namedSymbols(idx, lookupName)
		aliased = true
	}
	methodKey := strings.ToLower(lookupName)
	if wantedTrait == "" {
		wantedTrait = rules.preferred[methodKey]
	}
	var out []*core.SymbolRecord
	for _, candidate := range initial {
		traitKey := strings.ToLower(candidate.ParentSymbol)
		if candidate.Kind != core.KindMethod || !strings.EqualFold(candidate.Name, lookupName) || !rules.used[traitKey] {
			continue
		}
		if wantedTrait != "" && traitKey != wantedTrait {
			continue
		}
		if !aliased && rules.excluded[methodKey][traitKey] {
			continue
		}
		out = append(out, candidate)
	}
	return out, len(out) > 0
}

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

	// Body locals (highest precedence): $x = new Foo(), then
	// $x = Class::m() / $this->m() / $y->m() typed by m's return type
	// ($stmt = BuilderHelpers::normalizeNode($stmt) → Node). A `new` or
	// declared type already recorded wins.
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range phpNewLocalRe.FindAllStringSubmatch(body, -1) {
			if t := phpBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
		if idx != nil {
			var scope map[string]struct{}
			for _, m := range phpCallLocalRe.FindAllStringSubmatch(body, -1) {
				if _, done := out[m[1]]; done {
					continue
				}
				if scope == nil {
					scope = idx.importedFiles(symbol.FilePath)
				}
				if t := phpCallResultType(idx, m[2]+"()", scope); t != "" {
					out[m[1]] = t
				}
			}
		}
	}
	delete(out, "this")
	return out
}

// $x = [Class::|$this->|$y->]method(
var phpCallLocalRe = regexp.MustCompile(`\$(\w+)\s*=\s*(?:\\?[\w\\]+::|\$\w+->)?(\w+)\(`)

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
			declared := strings.TrimSpace(tail[c+1:])
			declared = strings.TrimRight(declared, "{; ")
			if (declared == "self" || declared == "static") && s.ParentSymbol != "" {
				return s.ParentSymbol
			}
			if t := phpBareType(declared); t != "" {
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

// phpNewQualifiedRe finds the class expression of a `new` at a call line:
// `new Stmt\ClassConst(`, `new \Foo\Bar(`, `new Const_(`.
var phpNewQualifiedRe = regexp.MustCompile(`\bnew\s+(\\?[A-Za-z_][\w\\]*)\s*[(;,)]`)

// phpNarrowNewByNamespace keeps the constructors whose file matches the
// namespace the `new` expression names. astkit emits only the last segment
// (`ClassConst`), and php-parser has Builder\ClassConst AND
// Node\Stmt\ClassConst: `new Stmt\ClassConst(` resolved to both. The
// qualified name resolves PHP-style — a leading backslash is absolute, a
// first segment matching a `use` import continues that import, anything
// else is relative to the caller's namespace (its directory under PSR-4)
// — and is matched as a path suffix. No match keeps every candidate.
func phpNarrowNewByNamespace(idx *edgeIndex, symbol *core.SymbolRecord, cs core.CallSite, name string, ctors []*core.SymbolRecord) []*core.SymbolRecord {
	if len(ctors) < 2 || cs.Line <= 0 || symbol.Span.Start <= 0 {
		return ctors
	}
	off := cs.Line - symbol.Span.Start
	lines := strings.Split(symbol.RawText, "\n")
	if off < 0 || off >= len(lines) {
		return ctors
	}
	var expr string
	for _, m := range phpNewQualifiedRe.FindAllStringSubmatch(lines[off], -1) {
		if segs := strings.Split(strings.TrimPrefix(m[1], "\\"), "\\"); segs[len(segs)-1] == name {
			expr = m[1]
			break
		}
	}
	if expr == "" {
		return ctors
	}
	var fqn string
	switch {
	case strings.HasPrefix(expr, "\\"):
		fqn = strings.TrimPrefix(expr, "\\")
	default:
		segs := strings.Split(expr, "\\")
		for imp := range idx.fileImports[symbol.FilePath] {
			if strings.HasSuffix(imp, "\\"+segs[0]) || imp == segs[0] {
				fqn = imp
				if len(segs) > 1 {
					fqn += "\\" + strings.Join(segs[1:], "\\")
				}
				break
			}
		}
		if fqn == "" {
			// Same namespace as the caller: its directory, PSR-4.
			want := strings.ToLower(dirOf(symbol.FilePath) + "/" + strings.ReplaceAll(expr, "\\", "/") + ".php")
			var out []*core.SymbolRecord
			for _, c := range ctors {
				if strings.ToLower(c.FilePath) == want {
					out = append(out, c)
				}
			}
			if len(out) > 0 {
				return out
			}
			return ctors
		}
	}
	suffix := strings.ToLower("/" + strings.ReplaceAll(fqn, "\\", "/") + ".php")
	var out []*core.SymbolRecord
	for _, c := range ctors {
		if strings.HasSuffix(strings.ToLower(c.FilePath), suffix) {
			out = append(out, c)
		}
	}
	if len(out) > 0 {
		return out
	}
	return ctors
}

// phpNarrowMethodsByImport disambiguates same-named classes through a file's
// use declarations. PHP graph scope is library-wide, so ParentSymbol alone
// cannot distinguish A\User::save from B\User::save.
func phpNarrowMethodsByImport(idx *edgeIndex, symbol *core.SymbolRecord, typ string, methods []*core.SymbolRecord) []*core.SymbolRecord {
	if len(methods) < 2 || symbol == nil {
		return methods
	}
	for imp := range idx.fileImports[symbol.FilePath] {
		clause := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(imp, "use "), ";"))
		target := clause
		alias := ""
		if i := strings.LastIndex(strings.ToLower(clause), " as "); i >= 0 {
			target = strings.TrimSpace(clause[:i])
			alias = strings.TrimSpace(clause[i+4:])
		}
		leaf := target
		if i := strings.LastIndexByte(leaf, '\\'); i >= 0 {
			leaf = leaf[i+1:]
		}
		if alias != typ && (alias != "" || leaf != typ) {
			continue
		}
		suffix := strings.ToLower("/" + strings.ReplaceAll(strings.Trim(target, "\\"), "\\", "/") + ".php")
		var out []*core.SymbolRecord
		for _, method := range methods {
			if strings.HasSuffix(strings.ToLower("/"+method.FilePath), suffix) {
				out = append(out, method)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return methods
}
