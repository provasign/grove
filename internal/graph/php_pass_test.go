package graph_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

// PHP pass 2026-10-17 (php-parser P .918→.922 R .649→.683, csv P .889→.947
// R .707→.837): call shapes the dynamic oracles showed grove missing or
// over-binding.
func TestPHPCallShapes(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"lib/Node.php":       "<?php\nnamespace P;\ninterface Node { public function getType(): string; }\n",
		"lib/Stmt/Echo_.php": "<?php\nnamespace P\\Stmt;\nuse P\\Node;\nclass Echo_ implements Node { public function getType(): string { return 'e'; } }\n",
		"lib/Expr/Echo_.php": "<?php\nnamespace P\\Expr;\nclass Echo_ { public function __construct($a) {} }\n",
		"lib/Builder/Class_.php": "<?php\nnamespace P\\Builder;\nclass Class_ extends Declaration {\n  public function __construct($n) {}\n  public function getNode() { return 1; }\n}\n",
		"lib/Builder/Declaration.php": "<?php\nnamespace P\\Builder;\nabstract class Declaration {\n  public function setDocComment($c) { return $this; }\n}\n",
		"lib/Other/Class_.php": "<?php\nnamespace P\\Other;\nclass Class_ { public function getNode() { return 2; } public function setDocComment($c) { return $this; } }\n",
		"lib/Reader.php": "<?php\nnamespace P;\nclass Reader implements \\IteratorAggregate {\n  public static function from($filename, string $mode = 'r', $context = null): static { return new static(); }\n  public function first(): array { return []; }\n  public function getIterator(): \\Iterator { return new \\ArrayIterator([]); }\n  public function chain(): ResultSet { return ResultSet::from($this); }\n}\n",
		"lib/ResultSet.php": "<?php\nnamespace P;\nclass ResultSet {\n  public static function from($rs): self { return new self(); }\n}\n",
		"lib/Row.php": "<?php\nnamespace P;\nclass Row {\n  public static function from(mixed $v): Row { return new Row(); }\n}\n",
		"test/bootstrap.php": "<?php\nfunction canonicalize($s) { return $s; }\n",
		"test/Builder/ClassTest.php": "<?php\nnamespace P\\Builder;\nuse P\\Reader;\nclass ClassTest {\n" +
			"  protected function make($n) { return new Class_($n); }\n" +
			"  public function canonicalize($s) { return $s; }\n" +
			"  public function run($node, $xs) {\n" +
			"    if ($node instanceof \\P\\Node) {\n" +
			"      $t = $node->getType();\n" +
			"    }\n" +
			"    $c = $this->make('x')\n" +
			"      ->setDocComment('d')\n" +
			"      ->getNode();\n" +
			"    $r = Reader::from('f.csv');\n" +
			"    $r->first();\n" +
			"    foreach ($r as $row) {}\n" +
			"    canonicalize('a');\n" +
			"    $xs[0]->getNode();\n" +
			"  }\n}\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	eng := parser.NewEngine()
	var syms []core.SymbolRecord
	for _, rel := range rels {
		s, err := eng.ExtractFile(filepath.Join(root, rel), root)
		if err != nil {
			t.Fatal(err)
		}
		syms = append(syms, s...)
	}
	idOf := map[string]string{}
	for _, s := range syms {
		idOf[s.FilePath+":"+s.QualifiedName] = s.ID
	}
	g := graph.New()
	g.Replace(syms, len(files))
	calls := map[string]bool{}
	for _, e := range g.EdgesSnapshot() {
		if e.Type == core.EdgeCalls {
			calls[e.From+"|"+e.To] = true
		}
	}
	edge := func(from, to string) bool {
		f, tt := idOf[from], idOf[to]
		if f == "" || tt == "" {
			t.Fatalf("no symbol %q or %q", from, to)
		}
		return calls[f+"|"+tt]
	}
	run := "test/Builder/ClassTest.php:ClassTest.run"
	for _, c := range []struct {
		to   string
		want bool
		why  string
	}{
		{"lib/Node.php:Node.getType", true, "instanceof narrows $node to Node"},
		{"lib/Builder/Declaration.php:Declaration.setDocComment", true, "chain starts at make(): Builder\\Class_ resolved by namespace"},
		{"lib/Builder/Class_.php:Class_.getNode", true, "setDocComment returns $this: the Builder Class_"},
		{"lib/Other/Class_.php:Class_.getNode", false, "the other Class_ is another namespace's"},
		{"lib/Other/Class_.php:Class_.setDocComment", false, "the other Class_ is another namespace's"},
		{"lib/Reader.php:Reader.from", true, "from('f.csv') fits from($filename, $mode = 'r', ...)"},
		{"lib/Row.php:Row.from", false, "Reader::from names Reader"},
		{"lib/Reader.php:Reader.first", true, "Reader::from returns static: Reader"},
		{"lib/Reader.php:Reader.getIterator", true, "foreach over a Reader runs getIterator"},
		{"test/bootstrap.php:canonicalize", true, "a bare call is the function"},
		{"test/Builder/ClassTest.php:ClassTest.canonicalize", false, "a method needs $this->"},
	} {
		if got := edge(run, c.to); got != c.want {
			t.Errorf("run -> %s = %v, want %v (%s)", c.to, got, c.want, c.why)
		}
	}
	if !edge("lib/Reader.php:Reader.chain", "lib/ResultSet.php:ResultSet.from") || edge("lib/Reader.php:Reader.chain", "lib/Reader.php:Reader.from") {
		t.Errorf("ResultSet::from inside Reader is ResultSet's, not Reader's own from")
	}
}
