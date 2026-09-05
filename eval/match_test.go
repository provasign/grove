package eval

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A method and a same-named function nested inside it are two oracle
// declarations landing on one grove symbol (grove does not index the nested
// one). The declaration on the span's first line owns the symbol, whatever
// order the refs arrive in; the nested decl stays unmatched.
func TestMatchDecls_NestedSameNameIsDeterministic(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "socket.ts::Socket.run", FilePath: "socket.ts", Name: "run", Kind: core.KindMethod, Span: core.LineRange{Start: 871, End: 890}},
	}
	method := FuncRef{File: "socket.ts", Line: 871, Name: "Socket.run"}
	nested := FuncRef{File: "socket.ts", Line: 876, Name: "run"}
	for _, refs := range []map[string]FuncRef{
		{"a" + method.funcKey(): method, "b" + nested.funcKey(): nested},
		{"z" + method.funcKey(): method, "a" + nested.funcKey(): nested},
	} {
		m := matchDecls(syms, refs)
		var methodKey, nestedKey string
		for k, r := range refs {
			if r.Line == 871 {
				methodKey = k
			} else {
				nestedKey = k
			}
		}
		if m.keyToID[methodKey] != "socket.ts::Socket.run" {
			t.Fatalf("method decl must own the symbol, got %q", m.keyToID[methodKey])
		}
		if _, ok := m.keyToID[nestedKey]; ok {
			t.Fatalf("nested decl must stay unmatched")
		}
		if m.idToKey["socket.ts::Socket.run"] != methodKey {
			t.Fatalf("idToKey points at %q", m.idToKey["socket.ts::Socket.run"])
		}
	}
}
