package graph_test

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

// Python function-value markers are emitted by the call pass, per owner.
// The delta path carried only unaffected owners' calls, so an edit
// anywhere dropped every other file's markers (fuzz 2026-10-17: ~290 on
// flask); and it carried the calls the install-time join derives from
// markers, which fed the decorator builder a call a full build never sees
// (Reg.run -> setupmethod).
func TestDeltaKeepsPythonValueFlowMarkers(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"app.py": "def setupmethod(f):\n    return f\n\n\n@setupmethod\ndef handler(state):\n    pass\n\n\n" +
			"class Reg:\n    def __init__(self):\n        self.funcs = [handler]\n\n    def run(self, state):\n        for f in self.funcs:\n            f(state)\n",
	}
	for i := 0; i < 40; i++ {
		files["pkg/filler"+strconv.Itoa(i)+".py"] = "def a():\n    return b()\n\n\ndef b():\n    return 1\n"
	}
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range files {
		write(rel, body)
	}
	eng := parser.NewEngine()
	extract := func() []core.SymbolRecord {
		t.Helper()
		rels := make([]string, 0, len(files))
		for rel := range files {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		var out []core.SymbolRecord
		for _, rel := range rels {
			syms, err := eng.ExtractFile(filepath.Join(root, rel), root)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, syms...)
		}
		return out
	}
	installed := func(symbols []core.SymbolRecord, base []core.Edge) map[string]bool {
		g := graph.New()
		if base == nil {
			g.Replace(symbols, len(files))
		} else {
			g.ReplaceWithBaseEdges(symbols, base, nil, len(files))
		}
		out := map[string]bool{}
		for _, e := range g.EdgesSnapshot() {
			out[e.From+"|"+string(e.Type)+"|"+e.To+"|"+string(e.Reason)+"|"+e.Lines] = true
		}
		return out
	}

	prevSymbols := extract()
	g := graph.New()
	g.Replace(prevSymbols, len(files))
	prevEdges := g.EdgesSnapshot()
	joined := false
	for _, e := range prevEdges {
		if e.Type == core.EdgeCalls && e.Reason == core.ReasonFuncField {
			joined = true
		}
	}
	if !joined {
		t.Fatalf("fixture produces no joined function-value call; test would be vacuous")
	}

	files["pkg/filler0.py"] = "def a():\n    return b()\n\n\ndef b():\n    return 2\n"
	write("pkg/filler0.py", files["pkg/filler0.py"])
	symbols := extract()
	got, meta := graph.BuildEdgesDeltaMeta(prevEdges, prevSymbols, symbols, map[string]bool{"pkg/filler0.py": true}, nil)
	if meta == nil {
		t.Fatal("delta fell back to a full rebuild; test would be vacuous")
	}
	have, want := installed(symbols, got), installed(symbols, nil)
	for k := range want {
		if !have[k] {
			t.Errorf("delta lost %s", k)
		}
	}
	for k := range have {
		if !want[k] {
			t.Errorf("delta has extra %s", k)
		}
	}
}
