package graph

// TS/JS receivers typed through the property name alone.
//
// `inst._zod.parse(payload, ctx)` inside `core.$constructor<$ZodType>("$ZodType",
// (inst, def) => {...})`: inst is a callback parameter typed only through a
// generic, so the receiver chain dies at its first hop and the call bound to
// nothing (zod _$ZodTypeInternals.parse: 0 of 61 sites, 2026-10-16). The
// property itself is better evidence than the variable: every declaration of
// `_zod` in the project is typed, and every declared type that has a `parse`
// member at all reaches the same declaration of it.
//
// The rule, precision first. For `X.prop.member` with X untyped, collect every
// declaration of a property named prop in the project (class/interface
// fields, and properties of object types written in interfaces and type
// aliases). Each declared type must resolve to an indexed class or interface
// (a generic parameter of the declaring type resolves to its constraint, else
// its default); an untyped, unresolvable, external, union or primitive
// declaration — or any non-property symbol of that name — stops the rule.
// Declared types that cannot have the member (neither they nor their
// ancestors declare it, all ancestors indexed) are not receivers of
// `.member` and drop out; an inline object type that does not mention the
// member drops out the same way. Every remaining type must reach the same
// nearest declaration(s) of member — one family — or nothing is decided.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
)

// tsPropDecl is one declaration of a property: the declared type's text and
// the type (class, interface, or type alias) that declares it.
type tsPropDecl struct {
	file  string
	owner *core.SymbolRecord
	typ   string
}

var (
	tsIdentRe       = regexp.MustCompile(`^[A-Za-z_$][\w$]*$`)
	tsFieldHeadRe   = regexp.MustCompile(`^\s*(?:(?:public|private|protected|readonly|static|declare|abstract|override|accessor)\s+)*["']?([A-Za-z_$][\w$]*)["']?\s*[?!]?\s*:`)
	tsMemberReCache sync.Map
)

// tsMemberDeclRe matches a declaration-shaped mention of name inside type
// text: `name:`, `name?:`, `name(`, `name<`.
func tsMemberDeclRe(name string) *regexp.Regexp {
	if re, ok := tsMemberReCache.Load(name); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`(?:^|[^\w$.])["']?` + regexp.QuoteMeta(name) + `["']?\s*[?!]?\s*[:(<]`)
	tsMemberReCache.Store(name, re)
	return re
}

// tsPropertyMemberTargets returns the declaration(s) of member reached by
// `<untyped>.prop.member`, or nil when the property's declarations do not
// pin one family (see the file comment). The answer depends only on the
// index, so it is memoized per (prop, member).
func tsPropertyMemberTargets(idx *edgeIndex, prop, member string) []*core.SymbolRecord {
	key := "m\x00" + prop + "\x00" + member
	if v, ok := idx.tsPropMembers.Load(key); ok {
		return v.([]*core.SymbolRecord)
	}
	out := computeTSPropertyMemberTargets(idx, prop, member)
	idx.tsPropMembers.Store(key, out)
	return out
}

func computeTSPropertyMemberTargets(idx *edgeIndex, prop, member string) []*core.SymbolRecord {
	decls, ok := tsPropertyDecls(idx, prop)
	if !ok || len(decls) == 0 {
		return nil
	}
	var target []*core.SymbolRecord
	targetKey := ""
	for _, d := range decls {
		typ := strings.TrimSpace(d.typ)
		if strings.HasPrefix(typ, "{") {
			// An inline object type: a receiver of `.member` only if it
			// declares member, and then its member is no indexed symbol.
			if tsMemberDeclRe(member).MatchString(typ) {
				return nil
			}
			continue
		}
		chosen := tsResolveDeclaredType(idx, d, typ, 0)
		if chosen == nil {
			return nil
		}
		found, complete := tsNearestMembers(idx, chosen, member, prop)
		if len(found) == 0 {
			if !complete {
				return nil // an ancestor outside the index may declare it
			}
			continue // this type cannot be the receiver of .member
		}
		ids := make([]string, len(found))
		for i, f := range found {
			ids[i] = f.ID
		}
		sort.Strings(ids)
		k := strings.Join(ids, "\x00")
		if target == nil {
			target, targetKey = found, k
		} else if k != targetKey {
			return nil // two families: the property does not decide
		}
	}
	for _, t := range target {
		if t.Kind != core.KindMethod && !graphCallableSymbol(t) {
			// A data property named member (or an enum const): not a
			// call/assignment target this rule binds.
			return nil
		}
	}
	return target
}

// tsPropertyDecls lists every declaration of a property named prop in the
// TS/JS part of the index. ok is false when prop names something the rule
// cannot model: an untyped field, a method or accessor, a module-level
// value (`ns.prop` would be that value), or a class.
func tsPropertyDecls(idx *edgeIndex, prop string) ([]tsPropDecl, bool) {
	type cached struct {
		decls []tsPropDecl
		ok    bool
	}
	key := "d\x00" + prop
	if v, ok := idx.tsPropMembers.Load(key); ok {
		c := v.(cached)
		return c.decls, c.ok
	}
	decls, ok := computeTSPropertyDecls(idx, prop)
	idx.tsPropMembers.Store(key, cached{decls, ok})
	return decls, ok
}

func computeTSPropertyDecls(idx *edgeIndex, prop string) ([]tsPropDecl, bool) {
	var out []tsPropDecl
	seen := map[string]bool{} // file:line already declared by a field symbol
	anyField := false
	for _, s := range idx.byName[strings.ToLower(prop)] {
		if s.Name != prop || !tsFamilyLang(s.Language) {
			continue
		}
		if s.Kind != core.KindField || s.ParentSymbol == "" {
			return nil, false
		}
		anyField = true
		owner := tsFieldOwner(idx, s)
		if owner == nil {
			// An object literal's member (`const api = { prop: Foo }`): a
			// value, not a declared type.
			return nil, false
		}
		text := s.RawText
		if text == "" {
			text = s.Signature
		}
		text = maskCode(s.Language, text)
		loc := tsFieldHeadRe.FindStringSubmatchIndex(text)
		if loc == nil || text[loc[2]:loc[3]] != prop {
			return nil, false // `prop = value`: no declared type
		}
		typ := tsTypeTextAt(text, loc[1])
		if typ == "" {
			return nil, false
		}
		out = append(out, tsPropDecl{file: s.FilePath, owner: owner, typ: typ})
		seen[s.FilePath+":"+strconv.Itoa(s.Span.Start)] = true
	}
	if !anyField {
		return nil, false
	}
	// Properties of object types written inside interfaces and type aliases
	// (`type SomeType = { _zod: _$ZodTypeInternals }`, a method parameter's
	// `{ _zod: X }`) are not indexed as fields; every such mention is a
	// type-position declaration.
	re := tsMemberDeclRe(prop)
	for _, s := range idx.tsTypeContainers() {
		if !strings.Contains(s.RawText, prop) {
			continue
		}
		masked := maskCode(s.Language, s.RawText)
		for _, m := range re.FindAllStringIndex(masked, -1) {
			end := m[1]
			if masked[end-1] != ':' {
				// `prop(` / `prop<`: a method signature of that name.
				return nil, false
			}
			line := s.Span.Start + strings.Count(masked[:m[0]], "\n")
			if m[0] < len(masked) && masked[m[0]] == '\n' {
				line++
			}
			if seen[s.FilePath+":"+strconv.Itoa(line)] {
				continue
			}
			typ := tsTypeTextAt(masked, end)
			if typ == "" {
				return nil, false
			}
			out = append(out, tsPropDecl{file: s.FilePath, owner: s, typ: typ})
		}
	}
	return out, true
}

// tsTypeContainers lists the TS/JS interfaces and type aliases, whose bodies
// hold only type positions (unlike a class body, where `{ prop: x }` is a
// value). Built once; the edge index is read-only afterwards.
func (idx *edgeIndex) tsTypeContainers() []*core.SymbolRecord {
	idx.tsTypeContainersOnce.Do(func() {
		files := make([]string, 0, len(idx.byFile))
		for f := range idx.byFile {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			for _, s := range idx.byFile[f] {
				if tsFamilyLang(s.Language) && (s.Kind == core.KindInterface || s.Kind == core.KindType) && s.RawText != "" {
					idx.tsTypeContainersList = append(idx.tsTypeContainersList, s)
				}
			}
		}
	})
	return idx.tsTypeContainersList
}

// tsFieldOwner is the class/interface declaring field s.
func tsFieldOwner(idx *edgeIndex, s *core.SymbolRecord) *core.SymbolRecord {
	var fallback *core.SymbolRecord
	for _, o := range idx.byFile[s.FilePath] {
		if o.Name != s.ParentSymbol {
			continue
		}
		switch o.Kind {
		case core.KindClass, core.KindInterface, core.KindType:
		default:
			continue
		}
		if o.Span.Start <= s.Span.Start && (o.Span.End == 0 || s.Span.Start <= o.Span.End) {
			return o
		}
		if fallback == nil {
			fallback = o
		}
	}
	return fallback
}

// tsTypeTextAt reads one type annotation starting at text[i]: up to a
// top-level `;`, `,`, `=` (not `=>`), newline, or closing bracket.
func tsTypeTextAt(text string, i int) string {
	depth := 0
	start := i
	for ; i < len(text); i++ {
		c := text[i]
		switch c {
		case '=':
			if i+1 < len(text) && text[i+1] == '>' {
				i++
				continue
			}
			if depth == 0 {
				return strings.TrimSpace(text[start:i])
			}
		case '(', '[', '{', '<':
			depth++
		case ')', ']', '}', '>':
			if depth == 0 {
				return strings.TrimSpace(text[start:i])
			}
			depth--
		case ';', ',':
			if depth == 0 {
				return strings.TrimSpace(text[start:i])
			}
		case '\n':
			if depth == 0 && strings.TrimSpace(text[start:i]) != "" {
				return strings.TrimSpace(text[start:i])
			}
		}
	}
	return strings.TrimSpace(text[start:])
}

// tsPlainTypeRef reduces `ns.Name<Args>` to its dotted head; ok is false for
// anything else (unions, arrays, functions, literals, keyof, ...).
func tsPlainTypeRef(typ string) (string, bool) {
	typ = strings.TrimSpace(typ)
	if i := strings.IndexByte(typ, '<'); i >= 0 {
		depth := 0
		end := -1
		for j := i; j < len(typ); j++ {
			switch typ[j] {
			case '<':
				depth++
			case '>':
				depth--
				if depth == 0 {
					end = j
				}
			}
			if end >= 0 {
				break
			}
		}
		if end != len(typ)-1 {
			return "", false // `Foo<X>[]`, `Foo<X> | Bar`, unbalanced
		}
		typ = strings.TrimSpace(typ[:i])
	}
	for _, part := range strings.Split(typ, ".") {
		if !tsIdentRe.MatchString(part) {
			return "", false
		}
	}
	return typ, true
}

// tsResolveDeclaredType resolves a declaration's type text to the indexed
// class or interface it names, or nil.
func tsResolveDeclaredType(idx *edgeIndex, d tsPropDecl, typ string, depth int) *core.SymbolRecord {
	if depth > 4 {
		return nil
	}
	ref, ok := tsPlainTypeRef(typ)
	if !ok {
		return nil
	}
	if !strings.Contains(ref, ".") && d.owner != nil {
		if bound, isParam := tsGenericParamBound(d.owner, ref); isParam {
			if bound == "" {
				return nil // unconstrained type parameter
			}
			return tsResolveDeclaredType(idx, d, bound, depth+1)
		}
	}
	head, leaf := ref, ref
	if i := strings.IndexByte(ref, '.'); i >= 0 {
		head, leaf = ref[:i], ref[strings.LastIndexByte(ref, '.')+1:]
	}
	switch leaf {
	case "any", "unknown", "object", "string", "number", "boolean", "void", "never", "symbol", "bigint", "undefined", "null", "this":
		return nil
	}
	if idx.tsBaseFromPackage(d.file, head) {
		return nil // an npm package's type, not a same-named one of ours
	}
	chosen := tsChosenTypeDecl(idx, leaf, dirOf(d.file))
	if chosen == nil {
		// `type X = Y<A>`: follow a plain alias.
		if alias, rhs := tsTypeAliasRHS(idx, leaf, dirOf(d.file)); alias != nil {
			return tsResolveDeclaredType(idx, tsPropDecl{file: alias.FilePath, owner: alias}, rhs, depth+1)
		}
		return nil
	}
	if !tsFamilyLang(chosen.Language) {
		return nil
	}
	return chosen
}

// tsTypeAliasRHS finds the TS type alias named name (same directory as
// preferDir first) and returns it with its right-hand side.
func tsTypeAliasRHS(idx *edgeIndex, name, preferDir string) (*core.SymbolRecord, string) {
	var chosen *core.SymbolRecord
	for _, c := range namedSymbols(idx, name) {
		if c.Kind != core.KindType || !tsFamilyLang(c.Language) || c.RawText == "" {
			continue
		}
		if chosen == nil || dirOf(c.FilePath) == preferDir && dirOf(chosen.FilePath) != preferDir {
			chosen = c
		}
	}
	if chosen == nil {
		return nil, ""
	}
	// Positions come from the masked text (a string literal holds no
	// syntax); the slice is the raw text, where `T["prop"]` keeps its key.
	raw := stripLeadingGenericParams(chosen.RawText)
	masked := maskCode(chosen.Language, raw)
	eq := topLevelIndex(masked, '=')
	if eq < 0 || len(masked) != len(raw) {
		return nil, ""
	}
	rhs := strings.TrimSpace(raw[eq+1:])
	return chosen, strings.TrimSpace(strings.TrimSuffix(rhs, ";"))
}

// tsIsPropIndexedAlias reports whether base names a project alias of the
// form `T["prop"]` with T a project type: the type of some declaration of
// prop, which the property rule already weighs on its own.
func tsIsPropIndexedAlias(idx *edgeIndex, base, prop, file string) bool {
	ref, ok := tsPlainTypeRef(base)
	if !ok || prop == "" {
		return false
	}
	leaf := ref[strings.LastIndexByte(ref, '.')+1:]
	alias, rhs := tsTypeAliasRHS(idx, leaf, dirOf(file))
	if alias == nil {
		return false
	}
	m := tsIndexedAccessRe.FindStringSubmatch(rhs)
	if m == nil || m[2] != prop {
		return false
	}
	head, ok := tsPlainTypeRef(m[1])
	if !ok {
		return false
	}
	headLeaf := head[strings.LastIndexByte(head, '.')+1:]
	if tsResolveDeclaredType(idx, tsPropDecl{file: alias.FilePath, owner: alias}, m[1], 0) != nil {
		return true
	}
	a, _ := tsTypeAliasRHS(idx, headLeaf, dirOf(alias.FilePath))
	return a != nil && !idx.tsBaseFromPackage(alias.FilePath, head[:strings.IndexByte(head+".", '.')])
}

var tsIndexedAccessRe = regexp.MustCompile(`^(.+?)\s*\[\s*["']([A-Za-z_$][\w$]*)["']\s*\]$`)

// tsGenericParamBound looks param up in owner's own type-parameter list:
// isParam reports whether it is one, bound is its constraint (else its
// default, else "").
func tsGenericParamBound(owner *core.SymbolRecord, param string) (bound string, isParam bool) {
	sig := owner.Signature
	if sig == "" {
		sig = owner.RawText
	}
	at := -1
	for i := 0; i+len(owner.Name) <= len(sig); i++ {
		if wordAt(sig, i, owner.Name) {
			at = i + len(owner.Name)
			break
		}
	}
	if at < 0 {
		return "", false
	}
	rest := strings.TrimLeft(sig[at:], " \t\n")
	if !strings.HasPrefix(rest, "<") {
		return "", false
	}
	depth, end := 0, -1
	for j := 0; j < len(rest) && end < 0; j++ {
		switch rest[j] {
		case '<', '(', '[', '{':
			depth++
		case '>':
			if j > 0 && rest[j-1] == '=' {
				continue
			}
			depth--
			if depth == 0 {
				end = j
			}
		case ')', ']', '}':
			depth--
		}
	}
	if end < 0 {
		return "", false
	}
	for _, p := range splitTopLevel(rest[1:end], ',') {
		p = strings.TrimSpace(p)
		for _, mod := range []string{"in ", "out ", "const "} {
			p = strings.TrimSpace(strings.TrimPrefix(p, mod))
		}
		name := p
		if i := strings.IndexAny(p, " \t\n=<"); i >= 0 {
			name = p[:i]
		}
		if name != param {
			continue
		}
		p = strings.TrimSpace(p[len(name):])
		if strings.HasPrefix(p, "extends") {
			c := strings.TrimSpace(p[len("extends"):])
			if eq := topLevelIndex(c, '='); eq >= 0 {
				c = c[:eq]
			}
			return strings.TrimSpace(c), true
		}
		if strings.HasPrefix(p, "=") {
			return strings.TrimSpace(p[1:]), true
		}
		return "", true
	}
	return "", false
}

// topLevelIndex is the index of the first c in s outside brackets (an `=`
// of `=>` excluded), or -1.
func topLevelIndex(s string, c byte) int {
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<', '(', '[', '{':
			depth++
		case '>':
			if i > 0 && s[i-1] == '=' {
				continue
			}
			depth--
		case ')', ']', '}':
			depth--
		default:
			if s[i] == c && depth == 0 && !(c == '=' && i+1 < len(s) && s[i+1] == '>') {
				return i
			}
		}
	}
	return -1
}

// tsNearestMembers walks typ and its ancestors breadth-first and returns the
// nearest level's declarations of member. complete is false when an
// ancestor is outside the index (an npm base, an unresolvable name), so a
// miss is not proof the type lacks the member.
func tsNearestMembers(idx *edgeIndex, typ *core.SymbolRecord, member, prop string) (found []*core.SymbolRecord, complete bool) {
	complete = true
	seen := map[string]bool{}
	level := []*core.SymbolRecord{typ}
	memberRe := tsMemberDeclRe(member)
	for depth := 0; depth < 12 && len(level) > 0; depth++ {
		var next []*core.SymbolRecord
		for _, t := range level {
			if seen[t.ID] {
				continue
			}
			seen[t.ID] = true
			own := tsOwnMemberDecls(idx, t, member)
			if len(own) > 0 {
				found = append(found, own...)
				continue
			}
			if memberRe.MatchString(tsMaskedBody(idx, t)) {
				// The body mentions the member in declaration shape but no
				// symbol of it is indexed: not something to bind.
				return nil, false
			}
			for _, base := range tsDeclBases(idx, t, prop) {
				if base == nil {
					complete = false
					continue
				}
				next = append(next, base)
			}
		}
		if len(found) > 0 {
			sort.Slice(found, func(i, j int) bool { return found[i].ID < found[j].ID })
			out := found[:0]
			for i, f := range found {
				if i == 0 || f.ID != found[i-1].ID {
					out = append(out, f)
				}
			}
			return out, true
		}
		level = next
	}
	if len(level) > 0 {
		complete = false
	}
	return nil, complete
}

// tsMaskedBody is a type declaration's masked text from its first '{' on
// (the heritage clause's generic arguments are not members), memoized.
func tsMaskedBody(idx *edgeIndex, t *core.SymbolRecord) string {
	key := "b\x00" + t.ID
	if v, ok := idx.tsPropMembers.Load(key); ok {
		return v.(string)
	}
	body := ""
	sig := stripLeadingGenericParams(t.RawText)
	if i := strings.IndexByte(sig, '{'); i >= 0 {
		body = maskCode(t.Language, sig[i:])
	}
	idx.tsPropMembers.Store(key, body)
	return body
}

// tsOwnMemberDecls lists t's own indexed members named member (any kind).
func tsOwnMemberDecls(idx *edgeIndex, t *core.SymbolRecord, member string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, m := range idx.byName[strings.ToLower(member)] {
		if m.Name == member && m.ParentSymbol == t.Name && m.FilePath == t.FilePath && tsFamilyLang(m.Language) {
			out = append(out, m)
		}
	}
	return out
}

// tsDeclBases resolves t's extends/implements clauses to indexed types; a
// nil entry stands for a base outside the index. A base aliasing
// `T["prop"]` (prop being the property under analysis) is left out: it is
// the type of another declaration of prop, already weighed.
func tsDeclBases(idx *edgeIndex, t *core.SymbolRecord, prop string) []*core.SymbolRecord {
	sig := stripLeadingGenericParams(t.Signature)
	var parts []string
	if c, ok := inheritanceClauseText(sig, "extends", "implements"); ok {
		parts = append(parts, splitTopLevel(c, ',')...)
	}
	if c, ok := inheritanceClauseText(sig, "implements"); ok {
		parts = append(parts, splitTopLevel(c, ',')...)
	}
	var out []*core.SymbolRecord
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		ref, ok := tsPlainTypeRef(p)
		if !ok {
			out = append(out, nil)
			continue
		}
		base := tsResolveDeclaredType(idx, tsPropDecl{file: t.FilePath}, ref, 0)
		if base == nil && tsIsPropIndexedAlias(idx, ref, prop, t.FilePath) {
			continue // `T["prop"]`: weighed as that declaration itself
		}
		out = append(out, base)
	}
	return out
}

// tsUntypedPropertyChainTargets applies the property rule to a receiver
// chain `X.prop` (or a longer chain ending in `.prop`) whose prefix has no
// type: the targets of `.member` on it, or nil. A prefix that does resolve,
// or that names an import or a type (`ns.prop`, `Cls.prop` are values of
// their own), keeps its own answer.
func tsUntypedPropertyChainTargets(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, chain, member string) []*core.SymbolRecord {
	parts := strings.Split(chain, ".")
	for len(parts) > 0 && (parts[0] == "this" || parts[0] == "self") {
		parts = parts[1:]
	}
	if len(parts) < 2 {
		return nil
	}
	for _, p := range parts {
		if !tsIdentRe.MatchString(p) {
			return nil
		}
	}
	head := parts[0]
	if _, _, ok := idx.jsImportAlias(symbol.FilePath, head); ok {
		return nil
	}
	if _, ok := idx.jsPackageImports[symbol.FilePath][head]; ok {
		return nil
	}
	if typeSymbolExists(idx, head) {
		return nil
	}
	if prefix := strings.Join(parts[:len(parts)-1], "."); tsResolveReceiverChain(idx, localTypes, prefix, symbol.FilePath, symbol.Language) != "" {
		return nil
	}
	if t, ok := localTypes[head]; ok && len(parts) == 2 && t != "" {
		return nil // typed head: the field walk owns the answer
	}
	targets := tsPropertyMemberTargets(idx, parts[len(parts)-1], member)
	for _, t := range targets {
		if (hasModifier(t, "private") || hasModifier(t, "protected") || strings.HasPrefix(t.Name, "#")) &&
			t.ParentSymbol != symbol.ParentSymbol {
			// Outside its class a private member is reachable only through
			// an `any` receiver (socket.io's adapter calling
			// `socket.client.writeToEngine` on an untyped callback
			// parameter): the declared type is not what the receiver has.
			return nil
		}
	}
	return targets
}

// tsDottedChainRe finds dotted member chains (`inst._zod.parse`).
var tsDottedChainRe = regexp.MustCompile(`[A-Za-z_$][\w$]*(?:[ \t]*\.[ \t]*[A-Za-z_$][\w$]*)+`)

// tsMemberRefEdges records non-call references to a method member through a
// receiver chain as reads/writes edges to the member's declaration:
// `inst._zod.parse = (payload, ctx) => {...}` (write) and
// `const base = inst._zod.parse` (read). A rename or signature change of the
// member breaks them exactly as it breaks a call. The receiver resolves like
// a call's (field-typed chain, else the property rule). Each line belongs to
// the innermost callable symbol containing it, so nested symbols do not
// double-report.
func tsMemberRefEdges(idx *edgeIndex, symbol *core.SymbolRecord) []core.Edge {
	if !tsFamilyLang(symbol.Language) || (symbol.Kind != core.KindFunction && symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor) {
		return nil
	}
	lines := maskedSymbolLines(idx, symbol)
	var nested []core.LineRange
	for _, o := range idx.byFile[symbol.FilePath] {
		if o.ID == symbol.ID || (o.Kind != core.KindFunction && o.Kind != core.KindMethod && o.Kind != core.KindConstructor) {
			continue
		}
		if o.Span.Start >= symbol.Span.Start && o.Span.End <= symbol.Span.End &&
			(o.Span.Start > symbol.Span.Start || o.Span.End < symbol.Span.End) {
			nested = append(nested, o.Span)
		}
	}
	var localTypes map[string]string
	var edges []core.Edge
	at := map[string]int{}
	add := func(t *core.SymbolRecord, typ core.EdgeType, pos string) {
		key := string(typ) + "\x00" + t.ID
		if j, ok := at[key]; ok {
			edges[j].Lines += "," + pos
			return
		}
		at[key] = len(edges)
		edges = append(edges, core.Edge{From: symbol.ID, To: t.ID, Type: typ,
			Confidence: 0.8, Source: core.EvidenceSourceHeuristic, Reason: core.ReasonMemberRef, Lines: pos})
	}
	for i, line := range lines {
		if !strings.Contains(line, ".") {
			continue
		}
		abs := symbol.Span.Start + i
		inner := false
		for _, r := range nested {
			if abs >= r.Start && abs <= r.End {
				inner = true
				break
			}
		}
		if inner {
			continue
		}
		for _, m := range tsDottedChainRe.FindAllStringIndex(line, -1) {
			if m[0] > 0 && (line[m[0]-1] == '.' || line[m[0]-1] == '?') {
				continue // the tail of a longer expression (`f().a.b`, `a?.b.c`)
			}
			text := line[m[0]:m[1]]
			// Segment boundaries inside the chain.
			var segs []string
			var ends []int // end offset (in line) of each segment
			off := m[0]
			for _, raw := range strings.Split(text, ".") {
				lead := len(raw) - len(strings.TrimLeft(raw, " \t"))
				seg := strings.TrimSpace(raw)
				segs = append(segs, seg)
				ends = append(ends, off+lead+len(seg))
				off += len(raw) + 1
			}
			for k := 1; k < len(segs); k++ {
				name := segs[k]
				if !tsHasMethodNamed(idx, name) {
					continue
				}
				rest := strings.TrimLeft(line[ends[k]:], " \t")
				typ := core.EdgeReads
				switch {
				case k == len(segs)-1 && (strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "<") || strings.HasPrefix(rest, "`")):
					continue // a call: the call-site pass owns it
				case k == len(segs)-1 && strings.HasPrefix(rest, "=") && !strings.HasPrefix(rest, "==") && !strings.HasPrefix(rest, "=>"):
					typ = core.EdgeWrites
				case k == len(segs)-1 && rest == "" && i+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i+1]), "("):
					continue // a call wrapped onto the next line
				}
				if localTypes == nil {
					localTypes = tsLocalTypes(idx, symbol)
				}
				chain := strings.Join(segs[:k], ".")
				col := ends[k] - len(name) + 1
				if i == 0 && symbol.Span.Start > 0 {
					// The first raw line starts mid-line; the column is
					// only reliable from the second line on.
					col = 0
				}
				pos := strconv.Itoa(abs)
				if col > 0 {
					pos += ":" + strconv.Itoa(col)
				}
				for _, t := range tsChainMemberTargets(idx, symbol, localTypes, chain, name) {
					if t.ID != symbol.ID {
						add(t, typ, pos)
					}
				}
			}
		}
	}
	return edges
}

// tsHasMethodNamed reports whether any TS/JS method or callable member of
// that name is indexed (a cheap pre-filter for the write scan).
func tsHasMethodNamed(idx *edgeIndex, name string) bool {
	for _, s := range idx.byName[strings.ToLower(name)] {
		if s.Name == name && s.ParentSymbol != "" && tsFamilyLang(s.Language) &&
			(s.Kind == core.KindMethod || graphCallableSymbol(s)) {
			return true
		}
	}
	return false
}

// tsChainMemberTargets resolves `<chain>.member` to member declarations: a
// field-typed chain first (the type's own member), else the property rule.
func tsChainMemberTargets(idx *edgeIndex, symbol *core.SymbolRecord, localTypes map[string]string, chain, member string) []*core.SymbolRecord {
	if typ := strings.TrimPrefix(tsResolveReceiverChain(idx, localTypes, chain, symbol.FilePath, symbol.Language), "class:"); typ != "" {
		if strings.HasPrefix(typ, "extern:") {
			return nil
		}
		chosen := tsChosenTypeDecl(idx, typ, dirOf(symbol.FilePath))
		if chosen == nil || !tsFamilyLang(chosen.Language) {
			return nil
		}
		found, _ := tsNearestMembers(idx, chosen, member, "")
		for _, f := range found {
			if f.Kind != core.KindMethod && !graphCallableSymbol(f) {
				return nil
			}
		}
		return found
	}
	return tsUntypedPropertyChainTargets(idx, symbol, localTypes, chain, member)
}
