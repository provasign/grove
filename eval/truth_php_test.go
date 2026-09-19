package eval

import (
	"os"
	"path/filepath"
	"testing"
)

// A closure frame attributes to the function whose body defines it — the
// reduce callbacks Php8::initReduceCallbacks builds run from
// ParserAbstract::doParse, and the calls inside them are
// initReduceCallbacks's (what Grove records), not doParse's.
func TestParsePHPTrace_ClosureAttributesToDefiningFunction(t *testing.T) {
	root := t.TempDir()
	refl := map[string]phpReflEntry{
		"P->initCbs":        {File: "src/P.php", Line: 4, Name: "P.initCbs"},
		"P->run":            {File: "src/P.php", Line: 9, Name: "P.run"},
		"Node->__construct": {File: "src/Node.php", Line: 2, Name: "Node.__construct"},
	}
	closure := "P->{closure:" + filepath.Join(root, "src", "P.php") + ":5-7}"
	trace := "Version: 3.5.3\nFile format: 4\n" +
		"1\t0\t0\t0.1\t100\t{main}\t1\t\n" +
		"2\t1\t0\t0.1\t100\tP->initCbs\t1\t\n" +
		"2\t1\t1\t0.1\t100\n" +
		"2\t2\t0\t0.1\t100\tP->run\t1\t\n" +
		"3\t3\t0\t0.1\t100\t" + closure + "\t1\t\n" +
		"4\t4\t0\t0.1\t100\tNode->__construct\t1\t\n"
	path := filepath.Join(root, "trace.xt")
	if err := os.WriteFile(path, []byte(trace), 0o644); err != nil {
		t.Fatal(err)
	}
	edges, err := parsePHPTrace(path, refl, root)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range edges {
		got = append(got, e.Caller.Name+"->"+e.Callee.Name)
	}
	want := map[string]bool{"P.initCbs->Node.__construct": true}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected edge %s (all: %v)", g, got)
		}
		delete(want, g)
	}
	if len(want) > 0 {
		t.Fatalf("missing edges %v (got %v)", want, got)
	}
}
