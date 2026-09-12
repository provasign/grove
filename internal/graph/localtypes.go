package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Local type inference, Go-only and deliberately shallow: it learns variable
// types from the three places that are cheap and reliable without a type
// checker — signature parameters, explicit declarations in the body, and the
// receiver type's struct fields. Reassignment and shadowing are ignored; a
// wrong guess is bounded by the harness numbers, not by hope.

var (
	// x := Type{...} / x := &Type{...} / x := pkg.Type{...}
	goCompositeLitRe = regexp.MustCompile(`(?m)\b([a-zA-Z_]\w*)\s*:=\s*&?(?:\w+\.)?([A-Za-z_]\w*)\{`)
	// x := NewType(...) / x, err := pkg.NewType(...)
	goNewCtorRe = regexp.MustCompile(`(?m)\b([a-zA-Z_]\w*)(?:\s*,\s*\w+)?\s*:=\s*(?:\w+\.)?New([A-Z]\w*)\(`)
	// var x Type / var x *Type / var x []Type / var x pkg.Type
	goVarDeclRe = regexp.MustCompile(`(?m)\bvar\s+([a-zA-Z_]\w*)\s+\*?(?:\[\])?(?:\w+\.)?([A-Za-z_]\w*)`)

	// goConvRe: `w := ResponseWriter(writer)` — a type CONVERSION binds the
	// variable to the named type. Textually identical to a function call, so
	// the match is gated on the captured name resolving to an INDEXED TYPE
	// (typeSymbolExists), exactly like the New-ctor heuristic below.
	goConvRe = regexp.MustCompile(`(?m)\b([a-zA-Z_]\w*)\s*:?=\s*(?:\w+\.)?([A-Z]\w*)\(`)

	// goCallResultRe: `c, _ := CreateTestContext(w)` — the FIRST variable of
	// a call-result assignment binds to the called function's first return
	// type, when that function is indexed and its return parses.
	goCallResultRe = regexp.MustCompile(`(?m)\b([a-zA-Z_]\w*)(?:\s*,\s*[a-zA-Z_]\w*)*\s*:=\s*(?:\w+\.)?([A-Za-z_]\w*)\(`)

	// goClosureParamRe: `func(c *Context) {` — closure-literal parameter
	// lists. Scoped narrower than the whole function, so these bind at the
	// LOWEST precedence and never overwrite an existing entry.
	goClosureParamRe = regexp.MustCompile(`func\(([^()]*)\)`)
	// struct field line: "Name Type" (embedded fields are single-token and
	// don't match; func/map/chan/interface types are rejected below)
	goStructFieldRe = regexp.MustCompile(`(?m)^\s*([A-Za-z_]\w*)\s+\*?(?:\[\])?(?:\w+\.)?([A-Za-z_]\w*)`)
)

// goTypeBlocklist rejects pseudo-type tokens the regexes can capture.
var goTypeBlocklist = map[string]bool{
	"func": true, "map": true, "chan": true, "interface": true,
	"struct": true, "range": true, "return": true,
}

// goLocalTypes infers identifier → bare type name for one callable symbol.
func goLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Receiver struct fields (lowest precedence: locals shadow fields).
	if symbol.Kind == core.KindMethod && symbol.ParentSymbol != "" {
		if t := findTypeSymbol(idx, symbol); t != nil && t.RawText != "" {
			body := t.RawText
			if i := strings.IndexByte(body, '{'); i >= 0 {
				body = body[i+1:]
			}
			for _, m := range goStructFieldRe.FindAllStringSubmatch(body, -1) {
				if typ := m[2]; !goTypeBlocklist[typ] && !goTypeBlocklist[m[1]] {
					out[m[1]] = typ
				}
			}
		}
	}

	// Signature parameters.
	for name, typ := range goParamTypes(symbol.Signature) {
		out[name] = typ
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, re := range []*regexp.Regexp{goVarDeclRe, goCompositeLitRe} {
			for _, m := range re.FindAllStringSubmatch(body, -1) {
				if typ := m[2]; !goTypeBlocklist[typ] {
					out[m[1]] = typ
				}
			}
		}
		// Constructor names are a convention, not a declaration: NewHandler
		// returns a Handler, but NewHandlerWithLedger also returns a Handler.
		// Record the guess only when it resolves to a type we actually index
		// (longest camel-case prefix wins) — a wrong guess here would turn
		// into a confident wrong drop downstream.
		for _, m := range goNewCtorRe.FindAllStringSubmatch(body, -1) {
			if typ := resolveCtorType(idx, m[2]); typ != "" {
				out[m[1]] = typ
			}
		}
		// Type conversions: only when the name IS an indexed type — a plain
		// function call never binds here.
		for _, m := range goConvRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				out[m[1]] = m[2]
			}
		}
		// Call results: first variable takes the called function's first
		// return type, when the callee is indexed and its return parses.
		for _, m := range goCallResultRe.FindAllStringSubmatch(body, -1) {
			if _, exists := out[m[1]]; exists {
				continue
			}
			if typ := goFirstReturnType(idx, m[2]); typ != "" {
				out[m[1]] = typ
			}
		}
		// Closure params, lowest precedence: never overwrite.
		for _, m := range goClosureParamRe.FindAllStringSubmatch(body, -1) {
			// goParamList requires the named form ("func name(params)") —
			// a bare closure literal reads its params as a receiver.
			for name, typ := range goParamTypes("func closure(" + m[1] + ")") {
				if _, exists := out[name]; !exists {
					out[name] = typ
				}
			}
		}
	}
	delete(out, "_")
	return out
}

// goFirstReturnType resolves an indexed Go FUNCTION's first return type to a
// bare, indexed type name; "" when the callee is unknown, not a function, or
// the return does not parse to an indexed type.
func goFirstReturnType(idx *edgeIndex, fnName string) string {
	for _, f := range idx.byName[strings.ToLower(fnName)] {
		if f.Name != fnName || f.Language != "go" {
			continue
		}
		switch f.Kind {
		case core.KindFunction, core.KindMethod:
		default:
			continue
		}
		sig := f.Signature
		close := strings.IndexByte(sig, ')')
		if close < 0 {
			continue
		}
		// Skip a leading receiver's parens for methods.
		rest := strings.TrimSpace(sig[close+1:])
		if strings.HasPrefix(rest, "(") {
			rest = strings.TrimPrefix(rest, "(")
		}
		rest = strings.TrimSpace(rest)
		if rest == "" || rest == "{" {
			continue
		}
		first := rest
		if i := strings.IndexAny(first, ",)"); i >= 0 {
			first = first[:i]
		}
		// "c *Context" or "*Context" — take the last token, strip decorations.
		fields := strings.Fields(first)
		if len(fields) == 0 {
			continue
		}
		tok := fields[len(fields)-1]
		tok = strings.TrimLeft(tok, "*&[]")
		if i := strings.LastIndexByte(tok, '.'); i >= 0 {
			tok = tok[i+1:]
		}
		tok = strings.TrimRight(tok, "{")
		if tok != "" && typeSymbolExists(idx, tok) {
			return tok
		}
	}
	return ""
}

// resolveCtorType maps a New<X> constructor suffix to an indexed type name:
// exact match first, then progressively shorter camel-case prefixes
// ("HandlerWithLedger" → "HandlerWith" → "Handler").
func resolveCtorType(idx *edgeIndex, captured string) string {
	if typeSymbolExists(idx, captured) {
		return captured
	}
	for i := len(captured) - 1; i > 0; i-- {
		if captured[i] >= 'A' && captured[i] <= 'Z' {
			if prefix := captured[:i]; typeSymbolExists(idx, prefix) {
				return prefix
			}
		}
	}
	return ""
}

func typeSymbolExists(idx *edgeIndex, name string) bool {
	for _, cand := range namedSymbols(idx, name) {
		switch cand.Kind {
		case core.KindStruct, core.KindClass, core.KindType, core.KindInterface:
			return true
		}
	}
	return false
}

// goParamTypes parses "func (recv) Name(a, b Type, c *pkg.Other) ..." into
// {a: Type, b: Type, c: Other}. Parameter groups share the type of the next
// typed group ("a, b Type"). Function-typed and other composite parameters
// are skipped.
func goParamTypes(signature string) map[string]string {
	out := map[string]string{}
	params, ok := goParamList(signature)
	if !ok {
		return out
	}
	groups := splitTopLevel(params, ',')
	pendingNames := []string{}
	for _, g := range groups {
		fields := strings.Fields(strings.TrimSpace(g))
		if len(fields) == 0 {
			continue
		}
		if len(fields) == 1 {
			// Either an unnamed type or a name sharing a later group's type.
			pendingNames = append(pendingNames, fields[0])
			continue
		}
		name := fields[0]
		typ := bareTypeName(strings.Join(fields[1:], " "))
		if typ == "" {
			pendingNames = nil
			continue
		}
		out[name] = typ
		for _, p := range pendingNames {
			out[p] = typ
		}
		pendingNames = nil
	}
	return out
}

// goParamList extracts the parameter list of the declared function itself,
// skipping a method's receiver parens.
func goParamList(signature string) (string, bool) {
	rest, found := strings.CutPrefix(signature, "func ")
	if !found {
		return "", false
	}
	if strings.HasPrefix(rest, "(") {
		// Receiver — skip its balanced parens.
		depth, i := 0, 0
		for ; i < len(rest); i++ {
			if rest[i] == '(' {
				depth++
			} else if rest[i] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if i >= len(rest) {
			return "", false
		}
		rest = rest[i+1:]
	}
	start := strings.IndexByte(rest, '(')
	if start < 0 {
		return "", false
	}
	depth := 0
	for i := start; i < len(rest); i++ {
		if rest[i] == '(' {
			depth++
		} else if rest[i] == ')' {
			depth--
			if depth == 0 {
				return rest[start+1 : i], true
			}
		}
	}
	return "", false
}

// splitTopLevel splits on sep outside any (), [], {} nesting.
func splitTopLevel(s string, sep byte) []string {
	var out []string
	depth, last := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[last:i])
				last = i + 1
			}
		}
	}
	out = append(out, s[last:])
	return out
}

// bareTypeName reduces a type expression to its final identifier: "*pkg.Type"
// → "Type", "[]Foo" → "Foo", "...string" → "string". Composite types (func,
// map, chan, interface literals) return "".
func bareTypeName(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "...")
	for strings.HasPrefix(t, "*") || strings.HasPrefix(t, "[]") {
		t = strings.TrimPrefix(t, "*")
		t = strings.TrimPrefix(t, "[]")
	}
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	if t == "" || strings.ContainsAny(t, "([{ )") || goTypeBlocklist[t] {
		return ""
	}
	return t
}

// findTypeSymbol locates the type declaration for a method's receiver type
// in the same package directory.
func findTypeSymbol(idx *edgeIndex, symbol *core.SymbolRecord) *core.SymbolRecord {
	dir := dirOf(symbol.FilePath)
	for _, cand := range namedSymbols(idx, symbol.ParentSymbol) {
		switch cand.Kind {
		case core.KindStruct, core.KindClass, core.KindType:
			if dirOf(cand.FilePath) == dir {
				return cand
			}
		}
	}
	return nil
}

// narrowByLocalType resolves a qualified call through the inferred type of
// its receiver variable. Three outcomes:
//
//   - methods on the inferred type exist among candidates → keep only those
//   - the inferred type is an interface → return its implementors as
//     dispatch targets (reduced confidence, decided by the caller)
//   - the type is known but no candidate belongs to it → drop everything;
//     the call targets a type we don't index
//
// An unknown qualifier leaves candidates untouched.
func narrowByLocalType(idx *edgeIndex, sat *interfaceSatisfaction, caller *core.SymbolRecord, localTypes map[string]string, qualifier, calleeName string, cands []*core.SymbolRecord, scope map[string]struct{}) (kept, dispatch []*core.SymbolRecord, decided bool) {
	if qualifier == "" || strings.HasSuffix(qualifier, "()") {
		return cands, nil, false
	}
	typ, ok := localTypes[qualifier]
	if !ok {
		return cands, nil, false
	}
	// A variable holding a class still narrows method calls to that class
	// (classmethods, attribute access through the class object).
	typ = strings.TrimPrefix(typ, "class:")
	if strings.HasPrefix(typ, "extern:") {
		// `private _xhr: any` — nothing of ours runs through it.
		return nil, nil, true
	}
	pool := cands
	if len(pool) == 0 {
		pool = globalCallableCandidates(idx, caller, calleeName)
	}
	byType := filterByParent(pool, typ)
	resolvedTypeFile := ""
	if caller != nil && tsFamilyLang(caller.Language) {
		resolvedTypeFile = tsResolveClassFile(idx, typ, caller.FilePath)
		byType = filterCandidatesByFile(byType, resolvedTypeFile)
	}
	if caller != nil && caller.Language == "java" {
		// A single-type import shadows a same-package type with the same
		// simple name. Scope intentionally contains both packages, so pin the
		// already type-matched methods to the explicit import here.
		byType = narrowByExplicitImport(idx, caller, typ, byType)
	}
	if caller != nil && caller.Language == "php" {
		byType = phpNarrowMethodsByImport(idx, caller, typ, byType)
	}
	if len(byType) == 0 && caller != nil && caller.Language == "csharp" {
		byType = csharpExtensionTargets(cands, typ)
	}
	// Class-hierarchy dispatch: a receiver typed by a class or interface
	// runs whichever subtype's override the instance carries. The declared
	// method (byType) stays; the overrides and implementors join as
	// dispatch edges. Static declaration-binding oracles (javac, tsc,
	// Roslyn) cannot see these; the scorer skips reason=dispatch for
	// them, dynamic oracles (pytest, xdebug) count them.
	var lang string
	for _, c := range cands {
		lang = c.Language
		break
	}
	if lang == "" && caller != nil {
		lang = caller.Language
	}
	var targets []*core.SymbolRecord
	seenD := map[string]bool{}
	for _, m := range subclassOverrides(idx, lang, typ, calleeName, "") {
		if !seenD[m.ID] {
			seenD[m.ID] = true
			targets = append(targets, m)
		}
	}
	if sat != nil {
		for _, iface := range idx.byName[strings.ToLower(typ)] {
			if iface.Kind != core.KindInterface || iface.Name != typ ||
				(caller != nil && !callLanguagesCompatible(caller.Language, iface.Language)) {
				continue
			}
			for _, m := range sat.implementorsFor(iface, calleeName) {
				if !seenD[m.ID] {
					seenD[m.ID] = true
					targets = append(targets, m)
				}
			}
		}
	}
	if len(targets) > maxDispatchFanout {
		targets = nil
	}
	if len(byType) == 0 {
		// The method may be inherited: a receiver typed FlaskProxy (a stub
		// subclass of Flask) calling make_response runs Flask's. Walk the
		// base classes; the nearest declaring ancestor wins.
		bases := baseClassesFor(idx, lang, typ, "")
		for level := 0; level < 4 && len(bases) > 0 && len(byType) == 0; level++ {
			var next []string
			for _, b := range bases {
				matches := filterByParent(pool, b)
				if caller != nil && tsFamilyLang(caller.Language) {
					baseFile := tsResolveClassFile(idx, b, resolvedTypeFile)
					matches = filterCandidatesByFile(matches, baseFile)
					if baseFile != "" {
						resolvedTypeFile = baseFile
					}
				}
				byType = append(byType, matches...)
				next = append(next, baseClassesFor(idx, lang, b, dirOf(resolvedTypeFile))...)
			}
			bases = next
		}
	}
	if len(byType) > 0 || len(targets) > 0 {
		return byType, targets, true
	}
	return nil, nil, true
}

func globalCallableCandidates(idx *edgeIndex, caller *core.SymbolRecord, name string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, candidate := range idx.byName[strings.ToLower(name)] {
		if candidate.Name != name || candidate.ID == caller.ID ||
			!callLanguagesCompatible(caller.Language, candidate.Language) || !graphCallableSymbol(candidate) {
			continue
		}
		if hasModifier(candidate, "private") && candidate.ParentSymbol != caller.ParentSymbol {
			continue
		}
		out = append(out, candidate)
	}
	return out
}

func filterCandidatesByFile(cands []*core.SymbolRecord, file string) []*core.SymbolRecord {
	if file == "" {
		return cands
	}
	out := cands[:0]
	for _, candidate := range cands {
		if candidate.FilePath == file {
			out = append(out, candidate)
		}
	}
	return out
}

func typeOrInheritedMethodTargets(idx *edgeIndex, caller *core.SymbolRecord, typ, name string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	pool := cands
	if len(pool) == 0 {
		pool = globalCallableCandidates(idx, caller, name)
	}
	typeFile := ""
	if tsFamilyLang(caller.Language) {
		typeFile = tsResolveClassFile(idx, typ, caller.FilePath)
	}
	if own := filterCandidatesByFile(filterByParent(pool, typ), typeFile); len(own) > 0 {
		return own
	}
	bases := baseClassesFor(idx, caller.Language, typ, dirOf(typeFile))
	seen := map[string]bool{}
	for len(bases) > 0 {
		var next []string
		for _, base := range bases {
			if base == "" || seen[base] {
				continue
			}
			seen[base] = true
			baseFile := ""
			if tsFamilyLang(caller.Language) {
				baseFile = tsResolveClassFile(idx, base, typeFile)
			}
			if inherited := filterCandidatesByFile(filterByParent(pool, base), baseFile); len(inherited) > 0 {
				return inherited
			}
			next = append(next, baseClassesFor(idx, caller.Language, base, dirOf(baseFile))...)
			if baseFile != "" {
				typeFile = baseFile
			}
		}
		bases = next
	}
	return nil
}

func csharpExtensionTargets(cands []*core.SymbolRecord, receiverType string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range cands {
		params := tsDeclParams(cand.Signature)
		if params == "" {
			params = tsDeclParams(cand.RawText)
		}
		first := strings.TrimSpace(strings.SplitN(params, ",", 2)[0])
		fields := strings.Fields(first)
		if len(fields) >= 3 && fields[0] == "this" && csNormalizeType(fields[1]) == receiverType {
			out = append(out, cand)
		}
	}
	return out
}

// maxDispatchFanout bounds class-hierarchy dispatch through one typed
// receiver; beyond it the hierarchy is a framework root (every visitor,
// every node) and the edges say nothing about this call.
const maxDispatchFanout = 64
