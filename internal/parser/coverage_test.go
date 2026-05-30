package parser

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

func TestFileBlobSHA(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	if err := os.WriteFile(p, []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sha, err := FileBlobSHA(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(sha) != 40 {
		t.Errorf("unexpected sha len %d", len(sha))
	}
	if _, err := FileBlobSHA(filepath.Join(dir, "missing")); err == nil {
		t.Error("expected error on missing file")
	}
}

func TestEstimateTokensAndIsExported(t *testing.T) {
	if estimateTokens("") != 0 {
		t.Error("empty")
	}
	if estimateTokens("abcdefgh") != 3 {
		t.Errorf("got %d", estimateTokens("abcdefgh"))
	}
	cases := []struct {
		lang, name, line string
		want             bool
	}{
		{"go", "Foo", "", true},
		{"go", "foo", "", false},
		{"python", "_priv", "", false},
		{"python", "pub", "", true},
		{"javascript", "x", "export const x = 1", true},
		{"javascript", "x", "const x = 1", false},
		{"java", "x", "public void x", true},
		{"rust", "x", "pub fn x", true},
		{"unknown", "x", "", false},
	}
	for _, c := range cases {
		if got := isExported(c.lang, c.name, c.line); got != c.want {
			t.Errorf("isExported(%s,%s,%q)=%v want %v", c.lang, c.name, c.line, got, c.want)
		}
	}
}

func TestExtractSymbolsRegex_BrokenGo(t *testing.T) {
	// Intentionally truncated; tree-sitter would report ERROR and regex
	// fallback is supplemented.
	src := `package main
func Hello() {
func Broken(`
	syms := extractSymbolsRegex("go", "x.go", "sha", src, nil)
	got := map[string]bool{}
	for _, s := range syms {
		got[s.Name] = true
	}
	for _, w := range []string{"Hello"} {
		if !got[w] {
			t.Errorf("regex missed %q in %v", w, got)
		}
	}
}

func TestExtractSymbolsRegex_PythonAndJS(t *testing.T) {
	py := `def hello():
    pass

class Greeter:
    def say(self):
        return 1
`
	syms := extractSymbolsRegex("python", "x.py", "sha", py, nil)
	if len(syms) == 0 {
		t.Error("python regex produced 0")
	}

	js := `export function helloJS() {}
class A {}
const arrow = () => 1;
`
	syms = extractSymbolsRegex("javascript", "x.js", "sha", js, nil)
	if len(syms) == 0 {
		t.Error("js regex produced 0")
	}
}

func TestExtractSymbolsRegex_Java(t *testing.T) {
	src := `public class Greeter {
  public void greet() {}
}
`
	syms := extractSymbolsRegex("java", "x.java", "sha", src, nil)
	if len(syms) == 0 {
		t.Error("java regex produced 0")
	}
}

func TestExtractSymbolsRegex_Rust(t *testing.T) {
	src := `pub fn hello() {}
pub struct Greeter { pub prefix: String }
`
	syms := extractSymbolsRegex("rust", "x.rs", "sha", src, nil)
	if len(syms) == 0 {
		t.Error("rust regex produced 0")
	}
}

func TestExtractIndentBody_Python(t *testing.T) {
	lines := []string{
		"def f():",
		"    a = 1",
		"    b = 2",
		"",
		"    c = 3",
		"def g():",
	}
	end, body := extractIndentBody(lines, 0)
	if end != 5 {
		t.Errorf("end=%d want 5", end)
	}
	if body == "" {
		t.Error("empty body")
	}
}

func TestExtractIndentBody_EOF(t *testing.T) {
	lines := []string{"def f():", "    pass"}
	_, body := extractIndentBody(lines, 0)
	if body == "" {
		t.Error("expected body at EOF")
	}
	_, body = extractIndentBody(lines, 99)
	if body != "" {
		t.Error("out-of-bounds should be empty")
	}
}

func TestExtractBody_Braces(t *testing.T) {
	lines := []string{"func f() {", "  return 1", "}", "func g(){}"}
	end, body := extractBody(lines, 0, "go")
	if end < 3 {
		t.Errorf("end=%d", end)
	}
	if body == "" {
		t.Error("empty body")
	}
}

func TestSymbolPatterns_AllLanguages(t *testing.T) {
	for _, lang := range []string{"go", "python", "javascript", "typescript", "tsx", "java", "rust"} {
		if len(symbolPatterns(lang)) == 0 {
			t.Errorf("no patterns for %s", lang)
		}
	}
	if pats := symbolPatterns("unknown"); pats != nil {
		t.Errorf("unknown language got %d patterns", len(pats))
	}
}

func TestSymbolRecordSanity(t *testing.T) {
	s := core.SymbolRecord{Name: "x", Kind: core.KindFunction}
	if s.Name != "x" {
		t.Error("struct broken")
	}
}
