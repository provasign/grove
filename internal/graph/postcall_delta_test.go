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

// Post-call builders (implicit super calls, framework/template edges) are
// global functions of the symbol set. The delta path used to carry their
// edges only for unaffected owners and never regenerate them, so an edit
// that re-resolved an owner silently dropped its implicit super() edge, and
// any edit dropped every template edge (templates are always re-resolved).
// Measured on real corpora: one guava body edit lost 2 edges, three django
// edits lost 521.
func TestDeltaKeepsPostCallBuilderEdges(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/base/Base.java":  "package base;\n\npublic class Base {\n  public Base() {}\n}\n",
		"src/util/U.java":     "package util;\n\npublic class U {\n  public static int f() { return 1; }\n}\n",
		"src/sub/Sub.java":    "package sub;\n\nimport base.Base;\nimport util.U;\n\npublic class Sub extends Base {\n  public Sub() {\n    U.f();\n  }\n}\n",
		"app/models.py":       "class Article:\n    headline = \"\"\n",
		"templates/page.html": "<p>{{ article.headline }}</p>\n",
	}
	// Unrelated packages keep the edit under the delta path's
	// affected-fraction limit.
	for i := 0; i < 40; i++ {
		pkg := "filler" + strconv.Itoa(i)
		files["src/"+pkg+"/F.java"] = "package " + pkg + ";\n\npublic class F {\n  public int a() { return b(); }\n  public int b() { return 1; }\n}\n"
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
		var out []core.SymbolRecord
		rels := make([]string, 0, len(files))
		for rel := range files {
			rels = append(rels, rel)
		}
		sort.Strings(rels)
		for _, rel := range rels {
			syms, err := eng.ExtractFile(filepath.Join(root, rel), root)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, syms...)
		}
		return out
	}
	hasReason := func(edges []core.Edge, reason core.EdgeReason, from string) bool {
		for _, e := range edges {
			if e.Type == core.EdgeCalls && e.Reason == reason && filepath.Dir(e.From) != "" && len(e.From) >= len(from) && e.From[:len(from)] == from {
				return true
			}
		}
		return false
	}

	prevSymbols := extract()
	prevEdges := graph.BuildEdges(prevSymbols)
	if !hasReason(prevEdges, core.ReasonConstructor, "src/sub/Sub.java::") || !hasReason(prevEdges, core.ReasonCrossArtifact, "templates/page.html") {
		t.Fatalf("fixture produces no implicit-super or template edge; test would be vacuous")
	}

	// A body edit in U.java re-resolves Sub (it calls U.f) and, like any
	// edit, the template.
	files["src/util/U.java"] = "package util;\n\npublic class U {\n  public static int f() { return 2; }\n}\n"
	write("src/util/U.java", files["src/util/U.java"])
	symbols := extract()
	got, meta := graph.BuildEdgesDeltaMeta(prevEdges, prevSymbols, symbols, map[string]bool{"src/util/U.java": true}, nil)
	if meta == nil {
		t.Fatal("delta fell back to a full rebuild; test would be vacuous")
	}
	want := graph.BuildEdges(symbols)
	key := func(e core.Edge) string {
		return e.From + "|" + string(e.Type) + "|" + e.To + "|" + string(e.Source) + "|" + string(e.Reason)
	}
	have := map[string]bool{}
	for _, e := range got {
		have[key(e)] = true
	}
	for _, e := range want {
		if !have[key(e)] {
			t.Errorf("delta lost %s", key(e))
		}
	}
	need := map[string]bool{}
	for _, e := range want {
		need[key(e)] = true
	}
	for _, e := range got {
		if !need[key(e)] {
			t.Errorf("delta has extra %s", key(e))
		}
	}
}
