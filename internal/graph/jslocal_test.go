package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A nested JS/TS function declaration (astkit: modifier "local") binds the
// bare calls inside its declaring function only — never a call from another
// function or file that happens to use the same name.
func TestJSLocalFunctionScope(t *testing.T) {
	g := New()
	fn := func(id, file, name, qual string, start, end int, local bool, raw string, sites ...core.CallSite) core.SymbolRecord {
		s := core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "b", Language: "typescript", Kind: core.KindFunction,
			Name: name, QualifiedName: qual, Span: core.LineRange{Start: start, End: end}, RawText: raw, CallSites: sites}
		if local {
			s.Modifiers = []string{"local"}
		}
		return s
	}
	syms := []core.SymbolRecord{
		fn("a.ts::encodePayload@b", "a.ts", "encodePayload", "encodePayload", 1, 10, false,
			"function encodePayload(p) {\n  function encodeOne(x) {\n    encodePacket(x);\n  }\n  encodeOne(p);\n}",
			core.CallSite{Callee: "encodeOne", Line: 5}),
		fn("a.ts::encodePayload.encodeOne@b", "a.ts", "encodeOne", "encodePayload.encodeOne", 2, 4, true,
			"function encodeOne(x) {\n    encodePacket(x);\n  }", core.CallSite{Callee: "encodePacket", Line: 3}),
		fn("a.ts::encodePacket@b", "a.ts", "encodePacket", "encodePacket", 12, 14, false, "function encodePacket(x) {}"),
		fn("a.ts::other@b", "a.ts", "other", "other", 16, 18, false, "function other(p) {\n  encodeOne(p);\n}",
			core.CallSite{Callee: "encodeOne", Line: 17}),
		fn("b.ts::caller@b", "b.ts", "caller", "caller", 1, 3, false, "function caller(p) {\n  encodeOne(p);\n}",
			core.CallSite{Callee: "encodeOne", Line: 2}),
	}
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeCalls, "a.ts::encodePayload@b", "a.ts::encodePayload.encodeOne@b") {
		t.Fatalf("encodePayload's bare encodeOne() must bind its nested function")
	}
	if !hasEdge(g, core.EdgeCalls, "a.ts::encodePayload.encodeOne@b", "a.ts::encodePacket@b") {
		t.Fatalf("the nested function owns its own calls")
	}
	if hasEdge(g, core.EdgeCalls, "a.ts::encodePayload@b", "a.ts::encodePacket@b") {
		t.Fatalf("the nested function's call is not its encloser's")
	}
	for _, c := range []string{"a.ts::other@b", "b.ts::caller@b"} {
		if hasEdge(g, core.EdgeCalls, c, "a.ts::encodePayload.encodeOne@b") {
			t.Fatalf("%s is outside encodePayload and cannot see its local encodeOne", c)
		}
	}
}
