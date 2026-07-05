package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
	"sync"
)

// TS/JS local type inference: typed class fields (already indexed as field
// symbols with "public transport: Transport" signatures), constructor
// parameter properties, parameter annotations, and new-expression
// assignments. Same shallow, harness-bounded philosophy as Go and Python.

var (
	// field signature: "public transport: Transport = default"
	tsFieldSigRe = regexp.MustCompile(`(\w+)\??\s*:\s*([^=;]+?)\s*(?:=|;|$)`)
	// constructor parameter property: "private readonly opts: Options"
	tsCtorPropRe = regexp.MustCompile(`(?:public|private|protected)\s+(?:readonly\s+)?(\w+)\??\s*:\s*([A-Za-z_][\w.<>\[\]| ]*)`)
	// x = new Type(...) / this.x = new pkg.Type(...)
	tsNewAssignRe = regexp.MustCompile(`(?:this\.)?(\w+)\s*=\s*new\s+(?:\w+\.)?([A-Z]\w*)`)
	// const x: Type = ... / let x: Type = ...
	tsVarAnnRe = regexp.MustCompile(`(?:const|let|var)\s+(\w+)\s*:\s*([^=\n]+?)\s*=`)
	// this.x = y (plain field assignment from a constructor parameter)
	tsSelfAssignRe = regexp.MustCompile(`this\.(\w+)\s*=\s*(\w+)\s*;`)
	// class-body field declaration: "protected readonly driver: Driver" —
	// applied to one class-body statement already stripped of any initializer
	// and known not to contain '(' (which would make it a method).
	tsBodyFieldRe = regexp.MustCompile(`^(?:(?:public|private|protected|readonly|static|declare|abstract|override|async)\s+)*(\w+)\??\s*:\s*(.+)$`)
)

// tsBareType reduces a TS annotation to one indexable class name.
// Arrays, generics with arguments, unions, and primitives return "".
func tsBareType(ann string) string {
	ann = strings.TrimSpace(ann)
	// Strip a generic argument list: Map<K,V> narrows nothing, but
	// Transport<...> would still be a Transport — keep the head only when
	// the whole annotation is one generic application.
	if i := strings.IndexByte(ann, '<'); i > 0 && strings.HasSuffix(ann, ">") {
		ann = ann[:i]
	}
	if strings.ContainsAny(ann, "<>[]|&(){}, ") {
		return ""
	}
	if i := strings.LastIndexByte(ann, '.'); i >= 0 {
		ann = ann[i+1:]
	}
	if ann == "" || ann[0] < 'A' || ann[0] > 'Z' {
		return ""
	}
	return ann
}

// tsBaseClasses parses base classes from "class X extends Y implements Z {".
func tsBaseClasses(idx *edgeIndex, className, preferDir string) []string {
	var chosen *core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name != className || cand.Kind != core.KindClass {
			continue
		}
		if dirOf(cand.FilePath) == preferDir {
			chosen = cand
			break
		}
		if chosen == nil {
			chosen = cand
		}
	}
	if chosen == nil {
		return nil
	}
	sig := chosen.Signature
	i := strings.Index(sig, "extends ")
	if i < 0 {
		return nil
	}
	rest := sig[i+len("extends "):]
	for _, stop := range []string{" implements ", "{"} {
		if j := strings.Index(rest, stop); j >= 0 {
			rest = rest[:j]
		}
	}
	base := tsBareType(rest)
	if base == "" {
		return nil
	}
	return []string{base}
}

// baseClassesFor dispatches base-class parsing per language.
func baseClassesFor(idx *edgeIndex, language, className, preferDir string) []string {
	switch language {
	case "python":
		return pyBaseClasses(idx, className, preferDir)
	case "typescript", "javascript", "java":
		return tsBaseClasses(idx, className, preferDir)
	}
	return nil
}

// tsLocalTypes infers identifier → type name for one TS/JS callable.
func tsLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Class fields, own class then ancestors (lowest precedence).
	if symbol.Kind == core.KindMethod || symbol.Kind == core.KindConstructor {
		seen := map[string]bool{}
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, className := range classes {
				if className == "" || seen[className] {
					continue
				}
				seen[className] = true
				tsClassFieldTypes(idx, className, out)
				next = append(next, tsBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}

	// Parameter annotations from the declaration's own parens.
	if params := tsDeclParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			g = strings.TrimSpace(g)
			name, ann, ok := strings.Cut(g, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(strings.TrimSuffix(strings.TrimLeft(name, ". "), "?"))
			name = strings.TrimSpace(strings.TrimPrefix(name, "readonly "))
			for _, mod := range []string{"public ", "private ", "protected "} {
				name = strings.TrimPrefix(name, mod)
			}
			if eq := strings.Index(ann, "="); eq >= 0 {
				ann = ann[:eq]
			}
			if t := tsBareType(ann); t != "" && name != "" && !strings.ContainsAny(name, "{[ ") {
				out[name] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range tsVarAnnRe.FindAllStringSubmatch(body, -1) {
			if t := tsBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
		for _, m := range tsNewAssignRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				out[m[1]] = m[2]
			}
		}
	}
	delete(out, "this")
	delete(out, "_")
	return out
}

// tsClassFieldTypes records one class's field types from indexed field
// symbols, its constructor's parameter properties, and this.x = new T()
// assignments. Existing entries win (closer classes shadow ancestors).
func tsClassFieldTypes(idx *edgeIndex, className string, out map[string]string) {
	record := func(name, typ string) {
		if _, exists := out[name]; !exists && typ != "" {
			out[name] = typ
		}
	}
	// No byParent index exists; class members live in the class's file.
	// Interfaces participate too: a chain hop through an interface-typed
	// field (`this.queryRunner.connection.driver...` where queryRunner is a
	// QueryRunner interface) resolves through the interface's property
	// signatures, which parse exactly like class fields.
	var classFile string
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name == className && (cand.Kind == core.KindClass || cand.Kind == core.KindInterface) {
			classFile = cand.FilePath
			break
		}
	}
	if classFile == "" {
		return
	}
	// Class-body field declarations (`protected driver: Driver`). astkit does
	// not emit TS class fields as KindField symbols, so the member loop below
	// misses them; scan the class's own source instead. This is the common
	// case for interface-typed fields dispatched via `this.driver.escape(...)`.
	for _, cand := range idx.byName[strings.ToLower(className)] {
		if cand.Name == className && (cand.Kind == core.KindClass || cand.Kind == core.KindInterface) && cand.FilePath == classFile {
			tsClassBodyFieldTypes(cand.RawText, record)
			break
		}
	}
	for _, member := range idx.byFile[classFile] {
		if member.ParentSymbol != className {
			continue
		}
		switch member.Kind {
		case core.KindField:
			if m := tsFieldSigRe.FindStringSubmatch(member.Signature); m != nil && m[1] == member.Name {
				record(member.Name, tsBareType(m[2]))
			}
		case core.KindConstructor:
			body := stripCommentsAndStrings(member.RawText)
			for _, m := range tsCtorPropRe.FindAllStringSubmatch(body, -1) {
				record(m[1], tsBareType(m[2]))
			}
			for _, m := range tsNewAssignRe.FindAllStringSubmatch(body, -1) {
				if typeSymbolExists(idx, m[2]) {
					record(m[1], m[2])
				}
			}
			// this.transport = transport — a plain assignment from a typed
			// constructor parameter carries the parameter's annotation.
			ctorParams := map[string]string{}
			if params := tsDeclParams(member.RawText); params != "" {
				for _, g := range splitTopLevel(params, ',') {
					name, ann, ok := strings.Cut(strings.TrimSpace(g), ":")
					if !ok {
						continue
					}
					if eq := strings.Index(ann, "="); eq >= 0 {
						ann = ann[:eq]
					}
					if t := tsBareType(ann); t != "" {
						ctorParams[strings.TrimSpace(name)] = t
					}
				}
			}
			for _, m := range tsSelfAssignRe.FindAllStringSubmatch(body, -1) {
				if t, ok := ctorParams[m[2]]; ok {
					record(m[1], t)
				}
			}
		}
	}
}

// tsReceiverChainRe captures a dotted receiver chain immediately before a
// `.<method>(` call, e.g. "this.connection.driver" in
// "this.connection.driver.escape(".
var tsReceiverChainReCache sync.Map // method → *regexp.Regexp; concurrent-safe: buildCalls resolves callers in parallel

func tsReceiverChainRe(method string) *regexp.Regexp {
	if re, ok := tsReceiverChainReCache.Load(method); ok {
		return re.(*regexp.Regexp)
	}
	re := regexp.MustCompile(`([A-Za-z_$][\w$]*(?:\s*\.\s*[A-Za-z_$][\w$]*)*)\s*\.\s*` + regexp.QuoteMeta(method) + `\s*\(`)
	tsReceiverChainReCache.Store(method, re)
	return re
}

// tsReceiverChainAt recovers the full receiver chain of a `.<calleeName>(` call
// from the caller's source. relLine is 0-based into rawText; the call may wrap,
// so a small window around it is searched. Returns "" if no chain is found.
func tsReceiverChainAt(rawText string, relLine int, calleeName string) string {
	if rawText == "" {
		return ""
	}
	lines := strings.Split(rawText, "\n")
	if relLine < 0 || relLine >= len(lines) {
		// Line outside the captured body (astkit line vs Span drift): fall
		// back to scanning the whole body for the first matching call.
		relLine = -1
	}
	re := tsReceiverChainRe(calleeName)
	tryLines := func(s string) string {
		if m := re.FindStringSubmatch(s); m != nil {
			return strings.Join(strings.FieldsFunc(m[1], func(r rune) bool {
				return r == ' ' || r == '\t'
			}), "")
		}
		return ""
	}
	if relLine >= 0 {
		lo, hi := relLine-1, relLine+2
		if lo < 0 {
			lo = 0
		}
		if hi > len(lines) {
			hi = len(lines)
		}
		if chain := tryLines(strings.Join(lines[lo:hi], "")); chain != "" {
			return chain
		}
	}
	return tryLines(strings.Join(lines, ""))
}

// tsFamilyLang reports whether a language uses the TS/JS class-field model.
func tsFamilyLang(language string) bool {
	switch language {
	case "typescript", "tsx", "javascript":
		return true
	}
	return false
}

// tsResolveReceiverChain walks a dotted receiver chain (`this.connection.driver`)
// through field types, one hop per segment, and returns the receiver's type
// name. The first segment resolves against the caller's own local types; each
// later segment against the running type's class fields. "" if any hop fails.
func tsResolveReceiverChain(idx *edgeIndex, localTypes map[string]string, chain string) string {
	parts := strings.Split(chain, ".")
	for len(parts) > 0 && (parts[0] == "this" || parts[0] == "self") {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return ""
	}
	curType, ok := localTypes[parts[0]]
	if !ok {
		return ""
	}
	curType = strings.TrimPrefix(curType, "class:")
	for _, seg := range parts[1:] {
		fields := map[string]string{}
		tsClassFieldTypes(idx, curType, fields)
		next, ok := fields[seg]
		if !ok {
			return ""
		}
		curType = strings.TrimPrefix(next, "class:")
	}
	return curType
}

// narrowByChainType resolves a multi-hop TS/JS receiver chain to a type, then
// narrows candidates to that type or dispatches to its interface implementors.
// Mirrors narrowByLocalType's contract (kept, dispatch, decided).
func narrowByChainType(idx *edgeIndex, sat *interfaceSatisfaction, localTypes map[string]string, symbol *core.SymbolRecord, fullChain, calleeName string, cands []*core.SymbolRecord) (kept, dispatch []*core.SymbolRecord, decided bool) {
	typ := tsResolveReceiverChain(idx, localTypes, fullChain)
	if typ == "" {
		return cands, nil, false
	}
	if byType := filterByParent(cands, typ); len(byType) > 0 {
		return byType, nil, true
	}
	if sat != nil {
		for _, iface := range idx.byName[strings.ToLower(typ)] {
			if iface.Kind != core.KindInterface || iface.Name != typ {
				continue
			}
			if impls := sat.implementorsFor(iface, calleeName); len(impls) > 0 {
				return nil, impls, true
			}
		}
	}
	// The chain resolved to a type we know nothing more about (no candidate
	// on it, no satisfaction entry). Unlike narrowByLocalType's single-hop
	// contract, do NOT claim decided here: chain resolution is heuristic
	// (source-recovered), and a decided-drop would also suppress the blanket
	// dispatch rescue downstream.
	return cands, nil, false
}

// tsClassBodyFieldTypes scans a class's source for depth-1 field declarations
// (`name: Type`, with optional modifiers/initializer) and records name→type.
// Member method/constructor bodies (brace depth ≥ 1) and parameter lists are
// skipped, so a statement reaching the field regex has no '(' and is a field,
// not a method signature.
func tsClassBodyFieldTypes(rawText string, record func(name, typ string)) {
	body := stripCommentsAndStrings(rawText)
	open := strings.IndexByte(body, '{')
	if open < 0 {
		return
	}
	body = body[open+1:]

	var stmt strings.Builder
	braceDepth, parenDepth := 0, 0
	sawParen := false // a '(' before '=' → method signature, not a field
	sawEq := false    // past the initializer '=' → later parens are the value
	flush := func() {
		s := strings.TrimSpace(stmt.String())
		stmt.Reset()
		hadParen := sawParen
		sawParen, sawEq = false, false
		if s == "" || hadParen {
			return // a method/constructor signature, not a field
		}
		if eq := strings.IndexByte(s, '='); eq >= 0 {
			s = strings.TrimSpace(s[:eq])
		}
		if s == "" {
			return
		}
		if m := tsBodyFieldRe.FindStringSubmatch(s); m != nil {
			record(m[1], tsBareType(m[2]))
		}
	}
	for i := 0; i < len(body); i++ {
		switch c := body[i]; c {
		case '=':
			if braceDepth == 0 && parenDepth == 0 {
				sawEq = true
			}
			if braceDepth == 0 && parenDepth == 0 {
				stmt.WriteByte(c)
			}
		case '(':
			if braceDepth == 0 && !sawEq {
				sawParen = true
			}
			parenDepth++
		case ')':
			if parenDepth > 0 {
				parenDepth--
			}
		case '{':
			// Opening a member body: the accumulated signature is discarded
			// by flush (it contains '('), then its body is skipped.
			if braceDepth == 0 && parenDepth == 0 {
				flush()
			}
			braceDepth++
		case '}':
			if braceDepth == 0 {
				flush()
				return // end of class body
			}
			braceDepth--
		case ';', '\n':
			if braceDepth == 0 && parenDepth == 0 {
				flush()
			}
		default:
			if braceDepth == 0 && parenDepth == 0 {
				stmt.WriteByte(c)
			}
		}
	}
	flush()
}

// tsDeclParams extracts the parameter list from a TS/JS declaration's raw
// text by scanning the first balanced paren group.
func tsDeclParams(rawText string) string {
	start := strings.IndexByte(rawText, '(')
	if start < 0 {
		return ""
	}
	depth := 0
	for k := start; k < len(rawText); k++ {
		switch rawText[k] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return rawText[start+1 : k]
			}
		}
	}
	return ""
}
