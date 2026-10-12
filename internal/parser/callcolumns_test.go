package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// Call sites keep astkit's called-name column, so a name-matched edit list
// names one token.
func TestCallSitesKeepTheirColumn(t *testing.T) {
	root := t.TempDir()
	src := "def f(w):\n    w.value(1); w.value(2)\n"
	if err := os.WriteFile(filepath.Join(root, "m.py"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	syms, err := NewEngine().ExtractFile(filepath.Join(root, "m.py"), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range syms {
		if s.Name != "f" {
			continue
		}
		var cols []int
		for _, cs := range s.CallSites {
			cols = append(cols, cs.Col)
		}
		if len(cols) != 2 || cols[0] != 7 || cols[1] != 19 {
			t.Fatalf("call columns %v, want [7 19]", cols)
		}
		return
	}
	t.Fatal("f not extracted")
}
