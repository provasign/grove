package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Comment/string-as-code audit (2026-10-10), parser layer. Files with a
// syntax error take the regex recovery path (extractSymbols merges names
// the AST missed); text inside comments, strings, heredocs, inline HTML,
// JSX text, and inactive #if branches must not become symbols, must not
// stretch or cut recovered spans, and must not bleed into docstrings.
// Index-level twins of the same findings: internal/invariance.

func extractFor(t *testing.T, path, src string) []core.SymbolRecord {
	t.Helper()
	syms, err := NewEngine().ExtractContent(path, []byte(src))
	if err != nil {
		t.Fatalf("extract %s: %v", path, err)
	}
	return syms
}

func describe(syms []core.SymbolRecord) string {
	var b strings.Builder
	for _, s := range syms {
		fmt.Fprintf(&b, "\n  %s %s parent=%q span=%d-%d lang=%s ann=%v", s.Kind, s.QualifiedName, s.ParentSymbol, s.Span.Start, s.Span.End, s.Language, s.Annotations)
	}
	return b.String()
}

func hasSymbolNamed(syms []core.SymbolRecord, name string) bool {
	parent, leaf, qualified := "", name, strings.Contains(name, ".")
	if qualified {
		i := strings.LastIndex(name, ".")
		parent, leaf = name[:i], name[i+1:]
	}
	for _, s := range syms {
		if s.Name != leaf && s.QualifiedName != name {
			continue
		}
		if !qualified || s.QualifiedName == name || strings.HasSuffix(s.ParentSymbol, parent) ||
			strings.ReplaceAll(s.QualifiedName, "::", ".") == name {
			return true
		}
	}
	return false
}

func TestCommentStringRecovery_NoPhantomSymbols(t *testing.T) {
	cases := []struct {
		id, path, src string
		absent        []string
		present       []string
	}{
		{"P6-go", "a.go", "package p\n\n/*\nfunc Phantom() {}\ntype Ghost struct{}\n*/\nvar s = `\nfunc Phantom2() {}\ntype Ghost2 struct{}\n`\n\nfunc Real() int { return 1 }\n\nfunc Broken( {\n",
			[]string{"Phantom", "Ghost", "Phantom2", "Ghost2"}, []string{"Real"}},
		{"P7-java-text-block", "A.java", "class A {\n  String s = \"\"\"\n      class Phantom { void ghost() {} }\n      \"\"\";\n  void real() {}\n  void broken( {\n}\n",
			[]string{"Phantom", "ghost"}, []string{"real"}},
		{"P8-csharp-raw-verbatim", "A.cs", "class A {\n  string s = \"\"\"\n    class Phantom { }\n    \"\"\";\n  string v = @\"\nclass Phantom2 { }\n\";\n  void Real() {}\n  void Broken( {\n}\n",
			[]string{"Phantom", "Phantom2"}, []string{"Real"}},
		{"P9-ts-template", "a.ts", "const tpl = `\nclass Phantom {\nfunction ghost(a) {\n`;\nexport function real() { return 1; }\nexport function broken( {\n",
			[]string{"Phantom", "ghost"}, []string{"real"}},
		{"P9-tsx-jsx-text", "v.tsx", "export function View() {\n  return <div>\n    class names are applied\n  </div>;\n}\nexport function broken( {\n",
			[]string{"names"}, []string{"View"}},
		{"P10-php-heredoc-html", "a.php", "<?php\n$s = <<<EOT\nclass Phantom {\nfunction ghost($a) {\nEOT;\nfunction real() { return 1; }\nfunction broken( {\n?>\n<p>\n  class names here\n</p>\n",
			[]string{"Phantom", "ghost", "names"}, []string{"real"}},
		{"P11-rust-raw-string", "lib.rs", "const S: &str = r#\"\nfn ghost() {}\nstruct Phantom;\n\"#;\npub fn real() -> i32 { 1 }\npub fn broken( {\n",
			[]string{"ghost", "Phantom"}, []string{"real"}},
		{"P12-python-continued-string", "a.py", "x = \"abc \\\nclass Phantom:\"\n\n\ndef real():\n    return 1\n\n\ndef broken(:\n    pass\n",
			[]string{"Phantom"}, []string{"real"}},
		{"P13-c-if0", "a.c", "#if 0\nint ghost(int a) {\n#endif\nint real(void) { return 0; }\nint broken( {\n",
			[]string{"ghost"}, []string{"real"}},
		{"P13-cpp-if0-namespace", "a.cpp", "#if 0\nnamespace legacy {\nint ghost(int a) {\n#endif\nint real(void) { return 0; }\nint broken( {\n",
			[]string{"legacy", "ghost", "legacy.real"}, []string{"real"}},
		{"P13-csharp-if-false", "A.cs", "#if false\npublic class Legacy {\n#endif\npublic class Real { public void Go() {} }\npublic class Broken { void X( { }\n",
			[]string{"Legacy", "Legacy.Real"}, []string{"Real", "Real.Go"}},
		{"P14-cpp-comment-qualified-name", "a.cpp", "int broken( {\nvoid helper(int x) { // see Widget::draw(x)\n  (void)x;\n}\n",
			[]string{"Widget.draw", "draw"}, []string{"helper"}},
		{"P2-cpp-raw-string", "a.cpp", "const char* kSrc = R\"(\nint phantom(int x) {\n  return x;\n}\n)\";\nint real(int y) { return y; }\n",
			[]string{"phantom"}, []string{"real"}},
		{"P3-c-multiline-define", "a.c", "#define DECL(n) \\\n  static int helper(void) { \\\n    return n; \\\n  }\nint real(void) { return 0; }\n",
			[]string{"helper"}, []string{"real"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			syms := extractFor(t, tc.path, tc.src)
			for _, name := range tc.absent {
				if hasSymbolNamed(syms, name) {
					t.Errorf("phantom symbol %q from comment/string/inactive text%s", name, describe(syms))
				}
			}
			for _, name := range tc.present {
				if !hasSymbolNamed(syms, name) {
					t.Errorf("real symbol %q missing%s", name, describe(syms))
				}
			}
		})
	}
}

// P15: recovered symbols (after the syntax error, so the AST lost them) end
// where their body ends, whatever braces/dedents hide in comments/strings.
func TestCommentStringRecovery_Spans(t *testing.T) {
	cases := []struct {
		id, path, src, name string
		start, end          int
	}{
		{"P15-java-block-comment-brace", "A.java",
			"class A {\n  void broken( {\n  void real() {\n    int a = 0; /* } */\n    a++;\n  }\n  void after() {}\n}\n", "real", 3, 6},
		{"P15-ts-template-brace", "a.ts",
			"export function broken( {\nexport function real() {\n  const s = `\n}\n`;\n  return s;\n}\nexport function after() {}\n", "real", 2, 7},
		{"P15-python-docstring-col0", "a.py",
			"x = foo(\n\n\ndef real():\n    \"\"\"Doc.\nend\n\"\"\"\n    return 1\n\n\ndef after():\n    return 2\n", "real", 4, 8},
		// P16: a call statement above a function is not an overload
		// signature; the span starts at the function.
		{"P16-ts-overload-widening", "a.ts", "foo(1);\nfunction foo(x: number) {\n  return x;\n}\n", "foo", 2, 4},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			syms := extractFor(t, tc.path, tc.src)
			for _, s := range syms {
				if s.Name == tc.name {
					// End may include trailing blank lines (existing
					// convention); it must reach the last body line.
					if s.Span.Start != tc.start || s.Span.End < tc.end {
						t.Errorf("%s span = %d-%d, want %d-%d (end >= %d)%s", tc.name, s.Span.Start, s.Span.End, tc.start, tc.end, tc.end, describe(syms))
					}
					return
				}
			}
			t.Fatalf("symbol %s not found%s", tc.name, describe(syms))
		})
	}
}

// P1: precedingCommentBlock must not take code (or a string holding "/**")
// for a doc comment.
func TestCommentStringRecovery_PrecedingCommentDocstring(t *testing.T) {
	cases := []struct {
		id, path, src, name string
		mustNot             []string
	}{
		{"P1-java-trailing-block-comment", "A.java",
			"/** The A class. */\nclass A {\n  int x; /* trailing */\n  void b() {}\n}\n", "b", []string{"int x", "class A", "trailing"}},
		{"P1-js-glob-string", "a.js",
			"const glob = \"src/**\";\n/* helper */\nfunction f() {}\n", "f", []string{"const glob", "src/"}},
		{"P1-go-trailing-block-comment", "a.go",
			"/** Package p. */\npackage p\n\nvar x = 1 /* trailing */\nfunc F() {}\n", "F", []string{"var x", "package", "trailing"}},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			syms := extractFor(t, tc.path, tc.src)
			for _, s := range syms {
				if s.Name != tc.name {
					continue
				}
				for _, bad := range tc.mustNot {
					if strings.Contains(s.Docstring, bad) {
						t.Errorf("%s docstring %q contains code %q", tc.name, s.Docstring, bad)
					}
				}
				return
			}
			t.Fatalf("symbol %s not found%s", tc.name, describe(syms))
		})
	}
}

// P17/A9: a block-comment line in a .h file that starts with a C++ keyword
// must not flip the header's language to C++.
func TestCommentStringRecovery_HeaderLanguageSniff(t *testing.T) {
	for _, tc := range []struct{ id, src string }{
		{"P17-namespace-comment-line", "/*\nnamespace handling is not supported here\n */\nint real(void);\n"},
		{"A9-class-comment-line", "/*\n class handling for the C API\n */\nint real(void);\n"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			if got := DetectLanguageContent("a.h", []byte(tc.src)); got != "c" {
				t.Errorf("language = %q, want c", got)
			}
		})
	}
}

func TestImportPathsDropComments(t *testing.T) {
	clean := "pub use crate::{\n    error::{Error, ErrorKind},\n};\nuse std::{\n    io,\n};\n"
	decoy := "pub use crate::{\n    // decoy: use Fake; don't 'quote\n    error::{Error, ErrorKind}, // trailing\n};\nuse std::{\n    /* block */ io,\n};\n"
	want, ok := extractImportsFromAST("rust", []byte(clean))
	if !ok {
		t.Fatal("clean imports not extracted")
	}
	got, ok := extractImportsFromAST("rust", []byte(decoy))
	if !ok {
		t.Fatal("decoy imports not extracted")
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("comments leaked into import paths:\n got %q\nwant %q", got, want)
	}
}
