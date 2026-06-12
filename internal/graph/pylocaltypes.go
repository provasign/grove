package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Python local type inference from the annotations the language already
// carries: parameter annotations, annotated assignments, class attribute
// annotations, and self.x = Type(...) constructor assignments. Same
// philosophy as the Go version: shallow, declaration-driven, and every
// guess bounded by the harness numbers.
//
// A type recorded as "class:X" means the variable holds the CLASS X itself
// (null_session_class = NullSession), so calling it constructs an X.

var (
	// x: Type = ... (annotated assignment in a body)
	pyAnnAssignRe = regexp.MustCompile(`(?m)^\s*(\w+)\s*:\s*([^=\n]+?)\s*=`)
	// x = Type(...) (constructor call; verified against indexed types)
	pyCtorAssignRe = regexp.MustCompile(`(?m)\b(\w+)\s*=\s*(?:\w+\.)?([A-Z]\w*)\(`)
	// self.x = Type(...) inside __init__
	pySelfCtorRe = regexp.MustCompile(`self\.(\w+)\s*=\s*(?:\w+\.)?([A-Z]\w*)\(`)
	// self.x: Type = ... inside __init__
	pySelfAnnRe = regexp.MustCompile(`self\.(\w+)\s*:\s*([^=\n]+?)\s*=`)
	// class-body attribute annotation: "name: Type" / "name: Type = default"
	pyClassAnnRe = regexp.MustCompile(`(?m)^\s+(\w+)\s*:\s*([^=\n]+?)\s*(?:=|$)`)
	// class-body attribute holding a class reference: "name = SomeClass"
	pyClassRefRe = regexp.MustCompile(`(?m)^\s+(\w+)\s*=\s*(?:\w+\.)?([A-Z]\w*)\s*$`)
)

// pyBareType reduces a Python annotation to one indexable class name.
// Conservative: containers and unions other than Optional return "".
func pyBareType(ann string) string {
	ann = strings.TrimSpace(strings.Trim(strings.TrimSpace(ann), `"'`))
	// X | None / None | X
	if parts := strings.Split(ann, "|"); len(parts) == 2 {
		a, b := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if a == "None" {
			ann = b
		} else if b == "None" {
			ann = a
		}
	}
	// Optional[X] / t.Optional[X]
	for _, prefix := range []string{"Optional[", "t.Optional[", "typing.Optional["} {
		if strings.HasPrefix(ann, prefix) && strings.HasSuffix(ann, "]") {
			return pyBareType(ann[len(prefix) : len(ann)-1])
		}
	}
	// type[X] / Type[X] — holds the class itself
	for _, prefix := range []string{"type[", "Type[", "t.Type[", "typing.Type["} {
		if strings.HasPrefix(ann, prefix) && strings.HasSuffix(ann, "]") {
			if inner := pyBareType(ann[len(prefix) : len(ann)-1]); inner != "" {
				return "class:" + inner
			}
			return ""
		}
	}
	if i := strings.LastIndexByte(ann, '.'); i >= 0 {
		ann = ann[i+1:]
	}
	if ann == "" || strings.ContainsAny(ann, "[]() ,|") {
		return ""
	}
	// Uppercase-first by convention: "int", "str", "bool" carry no
	// narrowing value for our graph.
	if ann[0] < 'A' || ann[0] > 'Z' {
		return ""
	}
	return ann
}

// pyDefParams extracts the parameter list of a def from its raw text,
// scanning balanced parens (signatures are routinely multi-line, so the
// stored first-line Signature is unusable).
func pyDefParams(rawText string) string {
	i := strings.Index(rawText, "def ")
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(rawText[i:], '(')
	if j < 0 {
		return ""
	}
	start := i + j
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

// pyLocalTypes infers identifier → type name for one Python callable.
func pyLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	// Class attribute types, own class first then ancestors (inherited
	// attributes resolve through the hierarchy; ancestors never overwrite
	// closer definitions). Lowest precedence overall.
	if symbol.Kind == core.KindMethod && symbol.ParentSymbol != "" {
		seen := map[string]bool{}
		classes := []string{symbol.ParentSymbol}
		for level := 0; level < 4 && len(classes) > 0; level++ {
			var next []string
			for _, className := range classes {
				if seen[className] {
					continue
				}
				seen[className] = true
				pyClassAttrTypes(idx, symbol, className, out)
				next = append(next, pyBaseClasses(idx, className, dirOf(symbol.FilePath))...)
			}
			classes = next
		}
	}

	// Parameter annotations.
	if params := pyDefParams(symbol.RawText); params != "" {
		for _, g := range splitTopLevel(params, ',') {
			g = strings.TrimSpace(g)
			name, ann, ok := strings.Cut(g, ":")
			if !ok {
				continue
			}
			name = strings.TrimSpace(strings.TrimLeft(name, "*"))
			if eq := strings.Index(ann, "="); eq >= 0 {
				ann = ann[:eq]
			}
			if t := pyBareType(ann); t != "" && name != "" {
				out[name] = t
			}
		}
	}

	// Body declarations (highest precedence).
	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range pyAnnAssignRe.FindAllStringSubmatch(body, -1) {
			if t := pyBareType(m[2]); t != "" {
				out[m[1]] = t
			}
		}
		for _, m := range pyCtorAssignRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				out[m[1]] = m[2]
			}
		}
	}
	delete(out, "self")
	delete(out, "cls")
	delete(out, "_")
	return out
}

// pyClassAttrTypes collects attribute types declared on one class — body
// annotations, class-attribute class references, and self.x assignments in
// its __init__ — without overwriting names already inferred.
func pyClassAttrTypes(idx *edgeIndex, symbol *core.SymbolRecord, className string, out map[string]string) {
	record := func(name, typ string) {
		if _, exists := out[name]; !exists && typ != "" {
			out[name] = typ
		}
	}
	for _, cls := range idx.byName[strings.ToLower(className)] {
		if cls.Name != className || cls.Kind != core.KindClass || cls.RawText == "" {
			continue
		}
		for _, m := range pyClassAnnRe.FindAllStringSubmatch(cls.RawText, -1) {
			record(m[1], pyBareType(m[2]))
		}
		for _, m := range pyClassRefRe.FindAllStringSubmatch(cls.RawText, -1) {
			if typeSymbolExists(idx, m[2]) {
				record(m[1], "class:"+m[2])
			}
		}
		break
	}
	for _, cand := range idx.byName["__init__"] {
		if cand.ParentSymbol != className {
			continue
		}
		body := stripCommentsAndStrings(cand.RawText)
		for _, m := range pySelfAnnRe.FindAllStringSubmatch(body, -1) {
			record(m[1], pyBareType(m[2]))
		}
		for _, m := range pySelfCtorRe.FindAllStringSubmatch(body, -1) {
			if typeSymbolExists(idx, m[2]) {
				record(m[1], m[2])
			}
		}
		break
	}
}

// pyBaseClasses parses the base-class names from a class declaration
// signature: "class Blueprint(Scaffold):" → [Scaffold]. Keyword arguments
// (metaclass=...) and subscripted bases (Generic[T]) are skipped.
func pyBaseClasses(idx *edgeIndex, className, preferDir string) []string {
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
	if chosen != nil {
		sig := chosen.Signature
		open := strings.IndexByte(sig, '(')
		closeIdx := strings.LastIndexByte(sig, ')')
		if open < 0 || closeIdx <= open {
			return nil
		}
		var bases []string
		for _, b := range splitTopLevel(sig[open+1:closeIdx], ',') {
			b = strings.TrimSpace(b)
			if b == "" || strings.ContainsAny(b, "=[") {
				continue
			}
			if i := strings.LastIndexByte(b, '.'); i >= 0 {
				b = b[i+1:]
			}
			if b != "" && b != "object" {
				bases = append(bases, b)
			}
		}
		return bases
	}
	return nil
}

// inheritedTargets resolves self.method() / self.property to members of the
// caller's ancestor classes, regardless of file import scope: inheritance
// reaches through files the subclass module never imports directly
// (app.py's Flask never imports scaffold.py, yet self.has_static_folder
// lives there). propertyOnly restricts to property-annotated methods for
// attribute reads.
func inheritedTargets(idx *edgeIndex, symbol *core.SymbolRecord, calleeName string, propertyOnly bool) []*core.SymbolRecord {
	if symbol.ParentSymbol == "" {
		return nil
	}
	var all []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(calleeName)] {
		if cand.Name != calleeName || cand.Kind != core.KindMethod {
			continue
		}
		if propertyOnly && !hasPropertyAnnotation(cand) {
			continue
		}
		all = append(all, cand)
	}
	if len(all) == 0 {
		return nil
	}
	bases := baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath))
	for level := 0; level < 4 && len(bases) > 0; level++ {
		var matched []*core.SymbolRecord
		for _, base := range bases {
			matched = append(matched, filterByParent(all, base)...)
		}
		if len(matched) > 0 {
			return matched
		}
		var next []string
		for _, base := range bases {
			next = append(next, baseClassesFor(idx, symbol.Language, base, dirOf(symbol.FilePath))...)
		}
		bases = next
	}
	return nil
}

// narrowBySuper resolves super().method() candidates to methods on the
// caller's base classes (walking up to three levels of the hierarchy).
func narrowBySuper(idx *edgeIndex, symbol *core.SymbolRecord, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if symbol.ParentSymbol == "" || len(cands) == 0 {
		return nil
	}
	bases := baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath))
	for level := 0; level < 3 && len(bases) > 0; level++ {
		var matched []*core.SymbolRecord
		for _, base := range bases {
			matched = append(matched, filterByParent(cands, base)...)
		}
		if len(matched) > 0 {
			return matched
		}
		var next []string
		for _, base := range bases {
			next = append(next, baseClassesFor(idx, symbol.Language, base, dirOf(symbol.FilePath))...)
		}
		bases = next
	}
	return nil
}
