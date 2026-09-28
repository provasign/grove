package index

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestSymbolRemapMatchesOverloadsBySignature(t *testing.T) {
	sym := func(id, qn, sig string) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: "a/Util.java", Kind: core.KindMethod, QualifiedName: qn, Signature: sig}
	}
	old := []core.SymbolRecord{
		sym("u::check@old", "Util.check", "int check(int x)"),
		sym("u::check@old#2", "Util.check", "int check(int x, int y)"),
		sym("u::gone@old", "Util.gone", "void gone()"),
		sym("u::solo@old", "Util.solo", "void solo()"),
	}
	// An overload inserted first shifts the positional #N suffixes; solo's
	// signature changed but it is the only declaration of its name.
	current := []core.SymbolRecord{
		sym("u::check@new", "Util.check", "int check(String s)"),
		sym("u::check@new#2", "Util.check", "int check(int x)"),
		sym("u::check@new#3", "Util.check", "int check(int x, int y)"),
		sym("u::solo@new", "Util.solo", "void solo(int n)"),
	}
	got := symbolRemap(old, current)
	want := map[string]string{
		"u::check@old":   "u::check@new#2",
		"u::check@old#2": "u::check@new#3",
		"u::solo@old":    "u::solo@new",
	}
	if len(got) != len(want) {
		t.Fatalf("remap = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("remap[%s] = %q, want %q", k, got[k], v)
		}
	}
}
