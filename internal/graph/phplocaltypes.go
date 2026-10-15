package graph

import (
	"regexp"
	"sort"
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

// phpTraitRulesFor reads a class's trait composition from its raw text.
// Only `use` statements at class-body depth 1 of the masked text count: a
// comment ("We no longer use Loud;") or a closure's `use` inside a method
// is not trait composition. The adaptation block `{ ... }` is read from the
// masked text at the statement's offset.
func phpTraitRulesFor(rawText string) phpTraitRules {
	rules := phpTraitRules{
		used:      map[string]bool{},
		preferred: map[string]string{},
		excluded:  map[string]map[string]bool{},
		aliases:   map[string]phpTraitAlias{},
	}
	masked := maskCode("php", rawText)
	top := braceClassBodyTopLevel(masked)
	for _, loc := range phpTraitUseRe.FindAllStringSubmatchIndex(masked, -1) {
		if loc[0] >= len(top) || !strings.HasPrefix(top[loc[0]:], "use") {
			continue
		}
		match := make([]string, len(loc)/2)
		for i := range match {
			if loc[2*i] >= 0 {
				match[i] = masked[loc[2*i]:loc[2*i+1]]
			}
		}
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

// phpClassTraitRules is phpTraitRulesFor memoized per class symbol (asked
// once per self call).
func phpClassTraitRules(idx *edgeIndex, class *core.SymbolRecord) phpTraitRules {
	key := "php-traits\x00" + class.ID
	if v, ok := idx.classBodies.Load(key); ok {
		return v.(phpTraitRules)
	}
	rules := phpTraitRulesFor(class.RawText)
	idx.classBodies.Store(key, rules)
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
	rules := phpClassTraitRules(idx, class)
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
					// Top-level members only (classBody): a commented-out
					// property is not one.
					for _, m := range phpPropertyRe.FindAllStringSubmatch(classBody(idx, cls), -1) {
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
		body := maskCode(symbol.Language, symbol.RawText)
		for _, m := range phpNewLocalRe.FindAllStringSubmatch(body, -1) {
			if t := phpBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
		if idx != nil {
			var scope map[string]struct{}
			for _, m := range phpStaticCallLocalRe.FindAllStringSubmatch(body, -1) {
				// $r = Reader::fromString(..): late static binding makes a
				// `static` result the named class (Reader), a `self` result
				// the declaring one.
				if _, done := out[m[1]]; done {
					continue
				}
				if t := phpStaticCallResultType(idx, symbol, phpBareType(m[2]), m[3]); t != "" {
					out[m[1]] = t
				}
			}
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

// $x = Class::method(
var phpStaticCallLocalRe = regexp.MustCompile(`\$(\w+)\s*=\s*(\\?[A-Za-z_][\w\\]*)::(\w+)\(`)

// phpStaticCallResultType types Class::method()'s result: the method found
// on cls or its nearest ancestor declaring it; `static` is cls itself,
// `self` the declaring class, any other declared class as written.
func phpStaticCallResultType(idx *edgeIndex, symbol *core.SymbolRecord, cls, method string) string {
	if idx == nil || cls == "" || cls == "self" || cls == "static" || cls == "parent" || !typeSymbolExists(idx, cls) {
		return ""
	}
	classes := []string{cls}
	for level := 0; level < 5 && len(classes) > 0; level++ {
		var next []string
		for _, c := range classes {
			for _, m := range idx.byName[strings.ToLower(method)] {
				if m.Name != method || m.ParentSymbol != c || m.Language != "php" ||
					m.Kind != core.KindMethod && m.Kind != core.KindFunction {
					continue // a same-named property is no result type
				}
				if i := strings.LastIndexByte(m.Signature, ')'); i >= 0 {
					tail := m.Signature[i+1:]
					if k := strings.IndexByte(tail, ':'); k >= 0 {
						switch declared := strings.TrimRight(strings.TrimSpace(tail[k+1:]), "{; "); declared {
						case "static":
							return cls
						case "self":
							return c
						}
					}
				}
				return phpReturnType(m)
			}
			next = append(next, tsBaseClasses(idx, c, dirOf(symbol.FilePath))...)
		}
		classes = next
	}
	return ""
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
	body := maskCode(s.Language, s.RawText)
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
	// A single candidate is checked too: `new Scalar\MagicConst\Method()`
	// (no constructor of its own) had Builder\Method::__construct as its
	// only same-named constructor.
	if len(ctors) == 0 || cs.Line <= 0 || symbol.Span.Start <= 0 {
		return ctors
	}
	off := cs.Line - symbol.Span.Start
	lines := maskedSymbolLines(idx, symbol)
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
		// Imports come from a map (random order), and aliases are not kept
		// (`use X\Middleware as MiddlewareAttribute` is stored as
		// X\Middleware), so two imports can both end in the name. Picking the
		// first match made `new Middleware` resolve differently on every
		// index run (laravel: 263-345 edges changed between runs of one
		// binary, 2026-09-26). Narrow only on a single match.
		var matches []string
		for _, imp := range idx.sortedFileImports(symbol.FilePath) {
			if strings.HasSuffix(imp, "\\"+segs[0]) || imp == segs[0] {
				matches = append(matches, imp)
			}
		}
		if len(matches) > 1 {
			return ctors
		}
		if len(matches) == 1 {
			fqn = matches[0]
			if len(segs) > 1 {
				fqn += "\\" + strings.Join(segs[1:], "\\")
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
	// The named class declares no constructor of its own
	// (`new Scalar\MagicConst\Class_(...)` runs MagicConst::__construct):
	// resolve the class the namespace names and take the constructors it
	// inherits, instead of falling back to every same-named class.
	for _, cls := range idx.byName[strings.ToLower(name)] {
		if cls.Name != name || (cls.Kind != core.KindClass && cls.Kind != core.KindStruct) {
			continue
		}
		if !strings.HasSuffix(strings.ToLower(cls.FilePath), suffix) {
			continue
		}
		// The named class is ours: its inherited constructors, or none
		// (a class with no constructor anywhere up its chain).
		return inheritedConstructors(idx, cls)
	}
	return ctors
}

// inheritedConstructors walks cls's base classes (up to three levels) and
// returns the first level's constructors.
func inheritedConstructors(idx *edgeIndex, cls *core.SymbolRecord) []*core.SymbolRecord {
	bases := baseClassesFor(idx, languageOfFile(idx, cls.FilePath), cls.Name, dirOf(cls.FilePath))
	for level := 0; level < 3 && len(bases) > 0; level++ {
		var out []*core.SymbolRecord
		var next []string
		for _, base := range bases {
			for _, baseCls := range idx.byName[strings.ToLower(base)] {
				if baseCls.Name == base && baseCls.Kind == core.KindClass {
					out = append(out, classConstructors(idx, base, baseCls.FilePath)...)
					next = append(next, baseClassesFor(idx, languageOfFile(idx, baseCls.FilePath), base, dirOf(baseCls.FilePath))...)
					break
				}
			}
		}
		if len(out) > 0 {
			return out
		}
		bases = next
	}
	return nil
}

// phpNarrowMethodsByImport disambiguates same-named classes through a file's
// use declarations. PHP graph scope is library-wide, so ParentSymbol alone
// cannot distinguish A\User::save from B\User::save.
func phpNarrowMethodsByImport(idx *edgeIndex, symbol *core.SymbolRecord, typ string, methods []*core.SymbolRecord) []*core.SymbolRecord {
	if len(methods) < 2 || symbol == nil {
		return methods
	}
	for _, imp := range idx.sortedFileImports(symbol.FilePath) {
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

// phpInstanceofAt returns the class a `$name instanceof X` test proves for
// the call on line: the innermost enclosing `if (...)` / `elseif (...)`
// block whose condition requires it (NodeDumper: `if ($node instanceof
// Node) { $node->getType() }`), or a guard earlier on the call's own line
// (`$n instanceof Node && $n->getName()`). "" when no test applies, the
// condition is a disjunction or negation, or X is not indexed.
func phpInstanceofAt(idx *edgeIndex, symbol *core.SymbolRecord, line int, name string) string {
	if symbol.RawText == "" || name == "" || !strings.Contains(symbol.RawText, "instanceof") {
		return ""
	}
	body := maskCode("php", symbol.RawText)
	lines := strings.Split(body, "\n")
	at := line - symbol.Span.Start
	if at < 0 || at >= len(lines) {
		return ""
	}
	test := localFnRes.get(`(!\s*)?\$` + regexp.QuoteMeta(name) + `\s+instanceof\s+\\?([\w\\]+)`)
	resolve := func(raw string) string {
		t := phpBareType(raw)
		if t == "" || !typeSymbolExists(idx, t) {
			return ""
		}
		return t
	}
	// Same-line guard before the call: `$n instanceof X && $n->m()`.
	if m := test.FindStringSubmatchIndex(lines[at]); m != nil && m[2] < 0 {
		rest := lines[at][m[1]:]
		if r := strings.TrimSpace(rest); strings.HasPrefix(r, "&&") || strings.HasPrefix(r, "?") || strings.HasPrefix(r, ")") && strings.Contains(r, "&&") {
			if strings.Contains(rest, "$"+name+"->") {
				return resolve(lines[at][m[4]:m[5]])
			}
		}
	}
	// Byte offset of each line start.
	starts := make([]int, len(lines)+1)
	for i, l := range lines {
		starts[i+1] = starts[i] + len(l) + 1
	}
	lineOf := func(off int) int {
		i := sort.SearchInts(starts, off+1) - 1
		if i < 0 {
			i = 0
		}
		return i
	}
	header := localFnRes.get(`\b(?:if|elseif)\s*\(`)
	best, bestHead := "", -1
	for _, h := range header.FindAllStringIndex(body, -1) {
		hl := lineOf(h[0])
		if hl >= at || hl <= bestHead {
			continue
		}
		// The condition: up to the matching ')'.
		depth, end := 1, -1
		for k := h[1]; k < len(body) && end < 0; k++ {
			switch body[k] {
			case '(':
				depth++
			case ')':
				depth--
				if depth == 0 {
					end = k
				}
			}
		}
		if end < 0 {
			continue
		}
		cond := body[h[1]:end]
		m := test.FindStringSubmatch(cond)
		if m == nil || m[1] != "" || strings.Contains(cond, "||") {
			continue
		}
		// The block: `{` right after the condition, to its match.
		k := end + 1
		for k < len(body) && (body[k] == ' ' || body[k] == '\t' || body[k] == '\n' || body[k] == '\r') {
			k++
		}
		if k >= len(body) || body[k] != '{' {
			continue
		}
		depth, close := 0, -1
		for j := k; j < len(body) && close < 0; j++ {
			switch body[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					close = j
				}
			}
		}
		if close < 0 || lineOf(close) < at {
			continue
		}
		if t := resolve(m[2]); t != "" {
			best, bestHead = t, hl
		}
	}
	return best
}

// phpChainTypeAt types the receiver of the call to callee at line by
// walking its fluent chain back to a start the caller can type ($var,
// $this, Class::m(), new Class) and then forward one method result at a
// time: `$this->tabularDataWithHeader()->first()` takes the caller's own
// tabularDataWithHeader, `Statement::create()\n->offset(1)\n->limit(5)`
// starts at Statement. "" when any hop is unknown.
func phpChainClassAt(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, line, col int, callee string) *core.SymbolRecord {
	if symbol.RawText == "" || line < symbol.Span.Start {
		return nil
	}
	lines := maskedSymbolLines(idx, symbol)
	at := line - symbol.Span.Start
	if at < 0 || at >= len(lines) {
		return nil
	}
	re := localFnRes.get(`\??->\s*` + regexp.QuoteMeta(callee) + `\s*\(`)
	// A multi-line chain is reported on the line it starts on; the name
	// may be a few lines below (`$this->make('x')\n->setDocComment(..)`).
	if col <= 1 {
		for k := at; k < len(lines) && k < at+12; k++ {
			if re.MatchString(lines[k]) {
				at = k
				break
			}
		}
	}
	text := strings.Join(lines[:at], "\n")
	cur := lines[at]
	loc := re.FindStringIndex(cur)
	if loc == nil {
		// An implied call (foreach/unpacking iteration) has no name in the
		// text: it sits at col, right after the receiver expression.
		if col <= 1 || col-1 > len(cur) {
			return nil
		}
		loc = []int{col - 1, col - 1}
	}
	if text != "" {
		text += "\n"
	}
	s := strings.TrimRight(text+cur[:loc[0]], " \t\n\r")
	var methods []string
	for steps := 0; steps < 12; steps++ {
		s = strings.TrimRight(s, " \t\n\r")
		if !strings.HasSuffix(s, ")") {
			// $var / $this start.
			i := len(s)
			for i > 0 && phpIdentByte(s[i-1]) {
				i--
			}
			if i == 0 || s[i-1] != '$' || i == len(s) {
				return nil
			}
			v := s[i:]
			var start *core.SymbolRecord
			if v == "this" {
				start = phpOwnClass(idx, symbol)
			} else if t := strings.TrimPrefix(localTypes[v], "class:"); t != "" {
				start = phpResolveClassIn(idx, symbol.FilePath, t)
			}
			return phpChainForward(idx, start, methods)
		}
		// Matching '('.
		depth, open := 0, -1
		for k := len(s) - 1; k >= 0 && open < 0; k-- {
			switch s[k] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					open = k
				}
			}
		}
		if open < 0 {
			return nil
		}
		head := strings.TrimRight(s[:open], " \t\n\r")
		i := len(head)
		for i > 0 && (phpIdentByte(head[i-1]) || head[i-1] == '\\') {
			i--
		}
		name := head[i:]
		before := strings.TrimRight(head[:i], " \t\n\r")
		switch {
		case name == "":
			// (new Foo(...)) / (clone $x): unwrap.
			inner := strings.TrimSpace(s[open+1 : len(s)-1])
			if m := phpNewStartRe.FindStringSubmatch(inner); m != nil {
				return phpChainForward(idx, phpResolveClassIn(idx, symbol.FilePath, m[1]), methods)
			}
			if strings.HasPrefix(inner, "clone ") {
				s = strings.TrimSpace(strings.TrimPrefix(inner, "clone "))
				continue
			}
			return nil
		case strings.HasSuffix(before, "->"):
			methods = append([]string{name}, methods...)
			s = strings.TrimSuffix(strings.TrimSuffix(before, "->"), "?")
		case strings.HasSuffix(before, "::"):
			b := strings.TrimRight(strings.TrimSuffix(before, "::"), " \t")
			j := len(b)
			for j > 0 && (phpIdentByte(b[j-1]) || b[j-1] == '\\') {
				j--
			}
			var cls *core.SymbolRecord
			switch b[j:] {
			case "self", "static":
				cls = phpOwnClass(idx, symbol)
			case "parent":
				return nil
			default:
				cls = phpResolveClassIn(idx, symbol.FilePath, b[j:])
			}
			return phpChainForward(idx, cls, append([]string{name}, methods...))
		case strings.HasSuffix(before, "new"):
			return phpChainForward(idx, phpResolveClassIn(idx, symbol.FilePath, name), methods)
		default:
			return nil
		}
	}
	return nil
}

var phpNewStartRe = regexp.MustCompile(`^new\s+\\?([A-Za-z_][\w\\]*)\s*(?:\(|$)`)

func phpIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// phpChainForward applies each method's result class in turn from start.
func phpChainForward(idx *edgeIndex, start *core.SymbolRecord, methods []string) *core.SymbolRecord {
	cur := start
	for _, name := range methods {
		if cur == nil {
			return nil
		}
		m := phpMemberOn(idx, cur, name)
		if m == nil {
			return nil
		}
		cur = phpResultClass(idx, m, cur)
	}
	return cur
}

// phpOwnClass is the class declaring symbol.
func phpOwnClass(idx *edgeIndex, symbol *core.SymbolRecord) *core.SymbolRecord {
	for _, c := range idx.byFile[symbol.FilePath] {
		if c.Name == symbol.ParentSymbol {
			switch c.Kind {
			case core.KindClass, core.KindInterface, core.KindTrait, core.KindEnum:
				return c
			}
		}
	}
	return nil
}

// phpClassRef is a PHP class identified by declaration file: short names
// collide across namespaces (php-parser has Builder\Class_, Stmt\Class_ and
// MagicConst\Class_).
type phpClassRef struct {
	sym *core.SymbolRecord
}

// phpResolveClassIn resolves a class name as written in file: a `use`
// import ending in it (PSR-4 path), else the file's own directory (same
// namespace), else the only class of that name.
func phpResolveClassIn(idx *edgeIndex, file, written string) *core.SymbolRecord {
	written = strings.TrimSpace(written)
	abs := strings.HasPrefix(written, "\\")
	segs := strings.Split(strings.TrimPrefix(written, "\\"), "\\")
	short := segs[len(segs)-1]
	if short == "" {
		return nil
	}
	var classes []*core.SymbolRecord
	for _, c := range idx.byName[strings.ToLower(short)] {
		if c.Name == short && c.Language == "php" {
			switch c.Kind {
			case core.KindClass, core.KindInterface, core.KindTrait, core.KindEnum:
				classes = append(classes, c)
			}
		}
	}
	if len(classes) == 0 {
		return nil
	}
	if len(classes) == 1 {
		return classes[0]
	}
	bySuffix := func(path string) *core.SymbolRecord {
		var hit *core.SymbolRecord
		for _, c := range classes {
			if strings.HasSuffix(strings.ToLower(c.FilePath), strings.ToLower(path)) {
				if hit != nil {
					return nil
				}
				hit = c
			}
		}
		return hit
	}
	if abs {
		return bySuffix("/" + strings.Join(segs, "/") + ".php")
	}
	for _, imp := range idx.sortedFileImports(file) {
		if strings.HasSuffix(imp, "\\"+segs[0]) || imp == segs[0] {
			fqn := imp
			if len(segs) > 1 {
				fqn += "\\" + strings.Join(segs[1:], "\\")
			}
			parts := strings.Split(fqn, "\\")
			// PSR-4: match the longest tail of the namespace path.
			for k := 0; k < len(parts)-1; k++ {
				if c := bySuffix("/" + strings.Join(parts[k:], "/") + ".php"); c != nil {
					return c
				}
			}
		}
	}
	if c := bySuffix(dirOf(file) + "/" + strings.Join(segs, "/") + ".php"); c != nil {
		return c
	}
	// Same namespace, other source root (PSR-4: test/PhpParser/Builder and
	// lib/PhpParser/Builder are both PhpParser\Builder): the one class whose
	// directory shares the longest trailing path with file's.
	if len(segs) == 1 {
		own := strings.Split(dirOf(file), "/")
		best, bestN, tie := (*core.SymbolRecord)(nil), 0, false
		for _, c := range classes {
			cd := strings.Split(dirOf(c.FilePath), "/")
			n := 0
			for n < len(own) && n < len(cd) && own[len(own)-1-n] == cd[len(cd)-1-n] {
				n++
			}
			switch {
			case n > bestN:
				best, bestN, tie = c, n, false
			case n == bestN && n > 0:
				tie = true
			}
		}
		if best != nil && !tie {
			return best
		}
	}
	return nil
}

// phpMemberOn finds method name on cls or its nearest ancestor declaring it
// (extends, then implements), each base resolved in its declaring file.
func phpMemberOn(idx *edgeIndex, cls *core.SymbolRecord, name string) *core.SymbolRecord {
	seen := map[string]bool{}
	frontier := []*core.SymbolRecord{cls}
	for level := 0; level < 6 && len(frontier) > 0; level++ {
		var next []*core.SymbolRecord
		for _, c := range frontier {
			if seen[c.ID] {
				continue
			}
			seen[c.ID] = true
			for _, m := range idx.byName[strings.ToLower(name)] {
				if m.Name == name && m.ParentSymbol == c.Name && m.FilePath == c.FilePath &&
					(m.Kind == core.KindMethod || m.Kind == core.KindFunction) {
					return m
				}
			}
			for _, b := range phpDeclaredBases(c) {
				if bc := phpResolveClassIn(idx, c.FilePath, b); bc != nil {
					next = append(next, bc)
				}
			}
		}
		frontier = next
	}
	return nil
}

var phpExtendsRe = regexp.MustCompile(`(?is)^[^{]*?\b(?:extends|implements)\s+([^{]+)`)

// phpDeclaredBases lists the names a class header extends or implements,
// as written.
func phpDeclaredBases(cls *core.SymbolRecord) []string {
	header := cls.Signature
	if header == "" || !strings.Contains(header, "extends") && !strings.Contains(header, "implements") {
		header = cls.RawText
		if i := strings.IndexByte(header, '{'); i >= 0 {
			header = header[:i]
		}
	}
	var out []string
	for _, part := range regexp.MustCompile(`\b(?:extends|implements)\b`).Split(header, -1)[1:] {
		for _, n := range strings.Split(part, ",") {
			if n = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(n), "{")); n != "" {
				out = append(out, strings.Fields(n)[0])
			}
		}
	}
	return out
}

// phpResultClass is m's result class when called on recv: `static` is the
// receiver, `self` the declaring class, a written class resolved in m's
// file, else what phpReturnType infers from the body (`return new X`,
// `return $this`).
func phpResultClass(idx *edgeIndex, m, recv *core.SymbolRecord) *core.SymbolRecord {
	if i := strings.LastIndexByte(m.Signature, ')'); i >= 0 {
		if k := strings.IndexByte(m.Signature[i+1:], ':'); k >= 0 {
			declared := strings.TrimLeft(strings.TrimRight(strings.TrimSpace(m.Signature[i+1+k+1:]), "{; "), "?")
			switch declared {
			case "static", "$this":
				return recv
			case "self":
				return phpResolveClassIn(idx, m.FilePath, m.ParentSymbol)
			}
			if declared != "" && !strings.ContainsAny(declared, "|&") {
				if c := phpResolveClassIn(idx, m.FilePath, declared); c != nil {
					return c
				}
			}
		}
	}
	body := maskCode("php", m.RawText)
	if mm := phpReturnNewWrittenRe.FindStringSubmatch(body); mm != nil {
		return phpResolveClassIn(idx, m.FilePath, mm[1])
	}
	if strings.Contains(body, "return $this;") {
		return recv
	}
	return nil
}

var phpReturnNewWrittenRe = regexp.MustCompile(`return\s+new\s+(\\?[A-Za-z_][\w\\]*)`)
