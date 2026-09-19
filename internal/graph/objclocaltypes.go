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
	}
	return out
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
