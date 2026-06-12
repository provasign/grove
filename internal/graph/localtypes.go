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
	}
	delete(out, "_")
	return out
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
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.Name != name {
			continue
		}
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
	for _, cand := range idx.byName[strings.ToLower(symbol.ParentSymbol)] {
		if cand.Name != symbol.ParentSymbol {
			continue
		}
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
func narrowByLocalType(idx *edgeIndex, sat *interfaceSatisfaction, localTypes map[string]string, qualifier, calleeName string, cands []*core.SymbolRecord, scope map[string]struct{}) (kept, dispatch []*core.SymbolRecord, decided bool) {
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
	return nil, nil, true
}
