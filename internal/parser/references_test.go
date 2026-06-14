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

func TestReferences_SkipsVendorAndCacheDirs(t *testing.T) {
	dir := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// One real use in the user's code; the rest are in dirs the indexer ignores
	// and must NOT be reported (they'd be vendored/downloaded noise).
	mk("app.go", "package p\n\nfunc run() { var x Widget; _ = x }\n")
	mk("vendor/lib.go", "package lib\n\ntype Widget struct{}\n")
	mk("node_modules/pkg/m.go", "package m\n\nvar Widget = 1\n")
	mk(".cache/home/go/pkg/mod/dep/d.go", "package d\n\ntype Widget struct{}\n")

	res, err := NewEngine().References(dir, "Widget")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Refs {
		if r.File != "app.go" {
			t.Errorf("reference from ignored dir leaked into results: %s:%d", r.File, r.Line)
		}
	}
	if len(res.Refs) != 1 {
		t.Fatalf("expected exactly 1 reference (app.go), got %d (%+v)", len(res.Refs), res.Refs)
	}
}

func TestReferences_QualifiedNameMatchesLeaf(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "svc.go"), []byte(
		"package p\n\ntype Service struct{}\n\nfunc (Service) ValidateToken(s string) bool { return s != \"\" }\n\nfunc use(s Service) { _ = s.ValidateToken(\"x\"); _ = s.ValidateToken(\"y\") }\n"),
		0o644)

	bare, err := NewEngine().References(dir, "ValidateToken")
	if err != nil {
		t.Fatal(err)
	}
	// A qualified input must resolve to the same bare-leaf references, not zero.
	qual, err := NewEngine().References(dir, "auth.Service.ValidateToken")
	if err != nil {
		t.Fatal(err)
	}
	if len(qual.Refs) == 0 {
		t.Fatalf("qualified name returned 0 references (regression: qualifier not stripped)")
	}
	if len(qual.Refs) != len(bare.Refs) {
		t.Fatalf("qualified (%d) and bare (%d) reference counts differ", len(qual.Refs), len(bare.Refs))
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
