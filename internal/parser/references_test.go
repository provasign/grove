package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// References is the name+scope reference layer: it finds every *code* occurrence
// of a symbol name (excluding comments/strings, unlike grep) and tiers the
// answer by how many definitions share the name. It answers "where is X used"
// for types/classes — which the resolved call graph cannot (a class is
// referenced, not called).
func TestReferences_FindsTypeUsesAcrossFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("util.go", "package p\n\ntype StringUtils struct{}\n\nfunc (StringUtils) IsEmpty(s string) bool { return s == \"\" }\n")
	// Two call sites + one type position, plus a comment + string that must NOT count.
	write("app.go", "package p\n\n// StringUtils is great\nfunc run() {\n\tvar u StringUtils\n\t_ = u.IsEmpty(\"x\")\n\t_ = StringUtils{}.IsEmpty(\"StringUtils\")\n}\n")

	e := NewEngine()
	res, err := e.References(dir, "StringUtils")
	if err != nil {
		t.Fatal(err)
	}
	// app.go has 2 code occurrences of StringUtils (var type + composite literal);
	// the comment and the "StringUtils" string literal must be excluded.
	inApp := 0
	for _, r := range res.Refs {
		if r.File == "app.go" {
			inApp++
			if r.Enclosing != "run" {
				t.Errorf("expected enclosing run, got %q", r.Enclosing)
			}
		}
	}
	if inApp != 2 {
		t.Fatalf("expected 2 code references in app.go (comment+string excluded), got %d (%+v)", inApp, res.Refs)
	}
}

func TestReferences_ExcludesCommentsAndStrings(t *testing.T) {
	dir := t.TempDir()
	// "Nowhere" appears only in a comment and a string — zero code references.
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("package p\n// Nowhere\nfunc f() { _ = \"Nowhere\" }\n"), 0o644)
	res, _ := NewEngine().References(dir, "Nowhere")
	if len(res.Refs) != 0 {
		t.Fatalf("comment/string occurrences must not count as references, got %+v", res.Refs)
	}
}
