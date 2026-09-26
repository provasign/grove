package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A method's receiver list closes with ")" before the parameter list does;
// reading the return type after the FIRST ")" took the last parameter's
// type instead ("uint16"), so `c := router.allocateContext(0)` never typed c.
func TestGoReturnTypeSkipsMethodReceiver(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "t", Language: "go", Kind: core.KindStruct, Name: "Context", QualifiedName: "Context", FilePath: "c.go"},
		{ID: "e", Language: "go", Kind: core.KindStruct, Name: "Engine", QualifiedName: "Engine", FilePath: "c.go"},
		{ID: "m", Language: "go", Kind: core.KindMethod, Name: "allocateContext", ParentSymbol: "Engine", FilePath: "c.go",
			Signature: "func (engine *Engine) allocateContext(maxParams uint16) *Context"},
		{ID: "p", Language: "go", Kind: core.KindMethod, Name: "pair", ParentSymbol: "Engine", FilePath: "c.go",
			Signature: "func (engine *Engine) pair() (c *Context, e *Engine)"},
		{ID: "f", Language: "go", Kind: core.KindFunction, Name: "New", FilePath: "c.go", Signature: "func New(opts ...Option) *Engine"},
	}
	idx := newEdgeIndex(syms)
	for id, want := range map[string]string{"m": "Context", "p": "Context", "f": "Engine"} {
		if got := goReturnType(idx, idx.byID[id]); got != want {
			t.Errorf("goReturnType(%s) = %q, want %q", id, got, want)
		}
	}
}
