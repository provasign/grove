package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// JS/TS data-like symbols added 2026-09-26 (enum members, interface
// properties, parameter properties, this.x fields) must add no uses-type
// edges; class fields and methods indexed before keep theirs.
func TestDeclarationOnlyJSTSMembers(t *testing.T) {
	cases := []struct {
		lang string
		kind core.SymbolKind
		mods []string
		want bool
	}{
		{"typescript", core.KindConst, []string{"member-value"}, true},                  // enum member
		{"typescript", core.KindField, []string{"member-value"}, true},                  // interface property / parameter property
		{"javascript", core.KindField, []string{"member-value", "this-assigned"}, true}, // this.x
		{"tsx", core.KindVariable, []string{"member-value"}, true},                      // Vue `methods: {}` container
		{"javascript", core.KindVariable, []string{"module-value"}, true},
		{"typescript", core.KindField, []string{"private"}, false}, // declared class field
		{"typescript", core.KindMethod, nil, false},                // interface method signature
		{"javascript", core.KindVariable, nil, false},              // const api = {...}
	}
	for _, c := range cases {
		s := &core.SymbolRecord{Language: c.lang, Kind: c.kind, Modifiers: c.mods}
		if got := declarationOnlySymbol(s); got != c.want {
			t.Errorf("%s %s %v: declarationOnly=%v, want %v", c.lang, c.kind, c.mods, got, c.want)
		}
	}
}
