package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Objective-C local type inference: keyword-parameter types parsed off the
// method's own Signature, and pointer-typed local declarations
// (`Type *x = ...;`, the overwhelming majority of ObjC locals — almost
// every object is held by pointer) — the same altitude as
// swiftLocalTypes/kotlinLocalTypes.

var (
	// :(Type)name / :(Type *)name — one per keyword segment of a method
	// signature, e.g. "- (void)doThing:(int)x withOption:(NSString *)y".
	objcParamRe = regexp.MustCompile(`:\s*\(([^)]+)\)\s*([a-z_]\w*)`)
	// Type *x = ... ; — a pointer-typed local declaration. Non-pointer
	// locals (int, BOOL, and other primitives) can never receive a message
	// send, so they carry no useful type for call resolution.
	objcLocalDeclRe = regexp.MustCompile(`(?m)\b([A-Z][A-Za-z0-9_]*)\s*\*\s*([a-z_]\w*)\s*=`)
)

// objcLocalTypes infers identifier -> bare type name for one callable symbol.
func objcLocalTypes(idx *edgeIndex, symbol *core.SymbolRecord) map[string]string {
	out := map[string]string{}

	for _, m := range objcParamRe.FindAllStringSubmatch(symbol.Signature, -1) {
		if typ := objcBareType(m[1]); typ != "" {
			out[m[2]] = typ
		}
	}

	if symbol.RawText != "" {
		body := stripCommentsAndStrings(symbol.RawText)
		for _, m := range objcLocalDeclRe.FindAllStringSubmatch(body, -1) {
			if typ := objcBareType(m[1]); typ != "" {
				out[m[2]] = typ
			}
		}
	}

	if symbol.ParentSymbol != "" {
		out["self"] = symbol.ParentSymbol
		for name, typ := range objcIvarTypes(idx, symbol.ParentSymbol, symbol.FilePath) {
			if _, shadowed := out[name]; !shadowed {
				out[name] = typ
			}
		}
	}
	return out
}

// objcMemberDeclRe matches the single-declarator Signature astkit gives an
// instance variable or @property: `SBState *state;`,
// `__weak id<Del> _delegate;`, `@property (readonly) NSMutableArray *stack;`.
var objcMemberDeclRe = regexp.MustCompile(`^\s*(?:@property\s*(?:\([^)]*\))?\s*)?(?:(?:__weak|__strong|__unsafe_unretained|IBOutlet|const)\s+)*([A-Za-z_]\w*(?:<[^>]*>)?)\s*\*?\s*(_?[A-Za-z_]\w*)\s*;`)

// objcIvarTypes maps a class's instance variables and properties — under
// both spellings a method body uses, `name` (self.name, or a bare ivar) and
// `_name` (the synthesized backing ivar) — to their bare types, read off
// the field symbols every @interface, class extension and @implementation
// of the class declares: in the method's own file and in the files of the
// class's declarations (its header). A member typed `id<Protocol>` maps to
// the protocol, which the receiver rule treats as dynamic dispatch.
func objcIvarTypes(idx *edgeIndex, class, file string) map[string]string {
	files := map[string]bool{file: true}
	for _, decl := range namedSymbols(idx, class) {
		if decl.Language == "objc" && decl.Kind == core.KindClass {
			files[decl.FilePath] = true
		}
	}
	out := map[string]string{}
	for f := range files {
		for _, field := range idx.byFile[f] {
			if field.Kind != core.KindField || field.ParentSymbol != class || field.Language != "objc" {
				continue
			}
			m := objcMemberDeclRe.FindStringSubmatch(field.Signature)
			if m == nil {
				continue
			}
			typ := objcBareType(m[1])
			if typ == "" {
				continue
			}
			name := m[2]
			out[name] = typ
			if strings.HasPrefix(name, "_") {
				out[name[1:]] = typ
			} else {
				out["_"+name] = typ
			}
		}
	}
	return out
}

// objcReturnTypeRe captures a method declaration's return type:
// `- (SBJson5Writer *)writer` → "SBJson5Writer *".
var objcReturnTypeRe = regexp.MustCompile(`^\s*[-+]\s*\(([^)]+)\)`)

// objcCallResultClasses resolves a call-result receiver name: a class
// (`[[Type alloc] init]`), `self` (the enclosing class), or an in-repo
// method whose declared return type names a class. `id`/`instancetype`
// results and library methods yield nothing.
func objcCallResultClasses(idx *edgeIndex, name string, caller *core.SymbolRecord) []string {
	if name == "self" {
		if caller.ParentSymbol != "" {
			return []string{caller.ParentSymbol}
		}
		return nil
	}
	if namedSymbolIsClass(idx, name) {
		return []string{name}
	}
	var out []string
	seen := map[string]bool{}
	for _, cand := range namedSymbols(idx, name) {
		if cand.Language != "objc" || cand.Kind != core.KindMethod {
			continue
		}
		m := objcReturnTypeRe.FindStringSubmatch(cand.Signature)
		if m == nil {
			continue
		}
		if typ := objcBareType(m[1]); typ != "" && !seen[typ] && namedSymbolIsClass(idx, typ) {
			seen[typ] = true
			out = append(out, typ)
		}
	}
	return out
}

// objcCandidatesOfClassChain keeps the candidates declared on the first
// class, walking from the given classes up their superclass chains, that
// declares any of them — the method the runtime would find first.
func objcCandidatesOfClassChain(idx *edgeIndex, cands []*core.SymbolRecord, classes []string) []*core.SymbolRecord {
	for level := 0; level < 8 && len(classes) > 0; level++ {
		var next []string
		for _, cls := range classes {
			if byType := filterByParent(cands, cls); len(byType) > 0 {
				return byType
			}
			next = append(next, objcBaseClasses(idx, cls)...)
		}
		classes = next
	}
	return nil
}

// objcBaseClasses returns the superclass an Objective-C class's @interface
// names (`@interface Sub : Base`), looking across every declaration of the
// class since a class extension (`@interface Sub ()`) names none.
func objcBaseClasses(idx *edgeIndex, className string) []string {
	for _, decl := range namedSymbols(idx, className) {
		if decl.Language != "objc" || decl.Kind != core.KindClass {
			continue
		}
		text := decl.Signature
		if text == "" {
			text = firstLine(decl.RawText)
		}
		if m := objcSuperclassRe.FindStringSubmatch(text); len(m) == 2 {
			return []string{m[1]}
		}
	}
	return nil
}

// objcPrimitives are C/ObjC scalar type keywords that look like types but
// can never resolve to an indexed declaration's methods.
var objcPrimitives = map[string]bool{
	"int": true, "unsigned": true, "long": true, "short": true, "char": true,
	"float": true, "double": true, "bool": true, "void": true,
	"nsinteger": true, "nsuinteger": true, "cgfloat": true, "bool_t": true,
}

// objcBareType reduces an Objective-C type expression to its final type
// identifier: "NSString *" -> "NSString", "id<Greeter>" -> "Greeter",
// "instancetype" and primitives return "".
func objcBareType(t string) string {
	t = strings.TrimSpace(t)
	t = strings.TrimSuffix(t, "*")
	t = strings.TrimSpace(t)
	// id<Protocol> / NSObject<Protocol> — the protocol is what a caller's
	// subsequent message send actually needs resolved against.
	if i := strings.IndexByte(t, '<'); i >= 0 {
		if j := strings.IndexByte(t[i+1:], '>'); j >= 0 {
			t = strings.TrimSpace(t[i+1 : i+1+j])
		}
	}
	t = strings.TrimPrefix(t, "const ")
	t = strings.TrimSpace(t)
	if t == "" || t == "instancetype" || objcPrimitives[strings.ToLower(t)] {
		return ""
	}
	for i := 0; i < len(t); i++ {
		c := t[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return ""
		}
	}
	if t[0] >= 'a' && t[0] <= 'z' {
		return ""
	}
	return t
}
