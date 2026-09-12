package parser_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

func TestJavaScriptTopLevelCallKeepsPrivateEntryLive(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "entry.ts")
	if err := os.WriteFile(file, []byte("function entry() {}\nfunction unused() {}\nentry();\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symbols, err := parser.NewEngine().ExtractFile(file, root)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	g.Replace(symbols, 1)
	result := g.DeadCode(nil)
	for _, symbol := range result.Dead {
		if symbol.Name == "entry" {
			t.Fatal("module-level entry() call must keep entry live")
		}
	}
	foundUnused := false
	for _, symbol := range result.Dead {
		foundUnused = foundUnused || symbol.Name == "unused"
	}
	if !foundUnused {
		t.Fatal("unreferenced private function should remain a dead-code candidate")
	}
}

func TestCSharpSyntaxRecoveryKeepsMethodOwnerAndCalls(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "Broken.cs")
	source := "class Broken {\n  void Bad() {\n    Work();\n  void Good() { Keep(); }\n  void Keep() {}\n}\n"
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	symbols, err := parser.NewEngine().ExtractFile(file, root)
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New()
	g.Replace(symbols, 1)
	result, err := g.ChangeImpact("Broken.Keep")
	if err != nil {
		t.Fatal(err)
	}
	foundGood := false
	foundBad := false
	for _, caller := range result.Callers {
		foundGood = foundGood || caller.QualifiedName == "Broken.Good"
		foundBad = foundBad || caller.QualifiedName == "Broken.Bad"
	}
	if !foundGood {
		t.Fatal("syntax-recovered Good method lost its Keep() call")
	}
	if foundBad {
		t.Fatal("enclosing broken method must not absorb Good's Keep() call")
	}
}
