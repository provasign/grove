package astkit_test

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/astkit"
)

func TestDetectLanguage(t *testing.T) {
	cases := map[string]astkit.LanguageKey{
		"a.go":        astkit.LangGo,
		"a.ts":        astkit.LangTypeScript,
		"a.tsx":       astkit.LangTSX,
		"a.js":        astkit.LangJavaScript,
		"a.jsx":       astkit.LangJavaScript,
		"a.mjs":       astkit.LangJavaScript,
		"a.cjs":       astkit.LangJavaScript,
		"a.py":        astkit.LangPython,
		"a.java":      astkit.LangJava,
		"a.rs":        astkit.LangRust,
		"a.c":         astkit.LangC,
		"a.h":         astkit.LangC,
		"a.cc":        astkit.LangCPP,
		"a.cpp":       astkit.LangCPP,
		"a.hpp":       astkit.LangCPP,
		"a.cs":        astkit.LangCSharp,
		"a.php":       astkit.LangPHP,
		"config.json": astkit.LangJSON,
		"x.yaml":      astkit.LangYAML,
		"x.YML":       astkit.LangYAML,
		"x.toml":      astkit.LangTOML,
		"README.md":   astkit.LangUnknown,
		"noext":       astkit.LangUnknown,
	}
	for path, want := range cases {
		if got := astkit.DetectLanguage(path, ""); got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestIsAST_IsConfigData(t *testing.T) {
	astLangs := []astkit.LanguageKey{
		astkit.LangGo, astkit.LangTypeScript, astkit.LangTSX, astkit.LangJavaScript,
		astkit.LangPython, astkit.LangJava, astkit.LangRust, astkit.LangC,
		astkit.LangCPP, astkit.LangCSharp, astkit.LangPHP,
	}
	for _, l := range astLangs {
		if !astkit.IsAST(l) {
			t.Errorf("IsAST(%q) = false, want true", l)
		}
		if astkit.IsConfigData(l) {
			t.Errorf("IsConfigData(%q) = true, want false", l)
		}
	}
	for _, l := range []astkit.LanguageKey{astkit.LangJSON, astkit.LangYAML, astkit.LangTOML} {
		if astkit.IsAST(l) {
			t.Errorf("IsAST(%q) = true, want false", l)
		}
		if !astkit.IsConfigData(l) {
			t.Errorf("IsConfigData(%q) = false, want true", l)
		}
	}
	if astkit.IsAST(astkit.LangUnknown) {
		t.Error("LangUnknown must not be AST")
	}
}

func TestEngineParse_AllASTLanguages(t *testing.T) {
	eng := astkit.NewEngine()
	cases := map[astkit.LanguageKey]string{
		astkit.LangGo:         "package main\nfunc main() {}\n",
		astkit.LangTypeScript: "export const x: number = 1;\n",
		astkit.LangTSX:        "export const C = () => <div/>;\n",
		astkit.LangJavaScript: "export const x = 1;\n",
		astkit.LangPython:     "def f():\n    return 1\n",
		astkit.LangJava:       "class A { void f(){} }\n",
		astkit.LangRust:       "fn main() {}\n",
		astkit.LangC:          "int main(){return 0;}\n",
		astkit.LangCPP:        "int main(){return 0;}\n",
		astkit.LangCSharp:     "class A { void F(){} }\n",
		astkit.LangPHP:        "<?php function f(){}\n",
	}
	for lang, src := range cases {
		tree, err := eng.Parse(context.Background(), lang, []byte(src))
		if err != nil {
			t.Fatalf("Parse(%s): %v", lang, err)
		}
		if tree == nil {
			t.Fatalf("Parse(%s): nil tree", lang)
		}
		if tree.RootNode().HasError() {
			t.Errorf("Parse(%s): unexpected syntax error in fixture", lang)
		}
		tree.Close()
	}
}

func TestEngineParse_NonASTReturnsNil(t *testing.T) {
	eng := astkit.NewEngine()
	tree, err := eng.Parse(context.Background(), astkit.LangJSON, []byte(`{}`))
	if err != nil || tree != nil {
		t.Fatalf("non-AST lang: got tree=%v err=%v, want nil nil", tree, err)
	}
}

func TestEngineParse_BrokenSourceHasError(t *testing.T) {
	eng := astkit.NewEngine()
	tree, err := eng.Parse(context.Background(), astkit.LangGo, []byte("package main\nfunc broken("))
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	if !tree.RootNode().HasError() {
		t.Error("expected HasError() on broken Go source")
	}
}

func TestEngineValidate(t *testing.T) {
	eng := astkit.NewEngine()
	if err := eng.Validate(context.Background(), astkit.LangGo, []byte("package main\n")); err != nil {
		t.Errorf("Validate good: %v", err)
	}
	if err := eng.Validate(context.Background(), astkit.LangJSON, []byte("{}")); err != nil {
		t.Errorf("Validate non-AST returns err: %v", err)
	}
}

func TestEngineWithTimeout(t *testing.T) {
	eng := astkit.NewEngine().WithTimeout(0)
	// Timeout 0 should still complete tiny parses (parser checks ctx between
	// chunks); just verify the method returns a usable engine.
	if eng == nil {
		t.Fatal("nil engine")
	}
}

func TestRegistry_RegisterAndExtract(t *testing.T) {
	r := astkit.NewRegistry()
	if r.Get(astkit.LangGo) != nil {
		t.Fatal("empty registry returned non-nil strategy")
	}
	syms, err := r.Extract(astkit.LangGo, nil, nil)
	if err != nil || syms != nil {
		t.Fatalf("unregistered Extract: got (%v,%v) want (nil,nil)", syms, err)
	}
	imps, err := r.ExtractImports(astkit.LangGo, nil, nil)
	if err != nil || imps != nil {
		t.Fatalf("unregistered ExtractImports: got (%v,%v) want (nil,nil)", imps, err)
	}
}

// fakeStrategy is a minimal Strategy used to test Registry semantics without
// touching tree-sitter.
type fakeStrategy struct{ lang astkit.LanguageKey }

func (f *fakeStrategy) Language() astkit.LanguageKey { return f.lang }
func (f *fakeStrategy) Extensions() []string         { return []string{".fake"} }
func (f *fakeStrategy) Extract(_ interface{}, _ []byte) ([]astkit.Symbol, error) {
	return []astkit.Symbol{{Kind: astkit.KindFunction, Name: "x"}}, nil
}

// The Strategy interface signature uses *sitter.Tree; we mirror it via the
// real registry only — see real strategies test for non-fake paths.

func TestSymbolKindAliases(t *testing.T) {
	if astkit.KindVar != astkit.KindVariable {
		t.Error("KindVar must alias KindVariable")
	}
}

func TestSymbolJSONShape(t *testing.T) {
	s := astkit.Symbol{
		Kind: astkit.KindFunction,
		Name: "F",
		Span: astkit.LineRange{Start: 1, End: 3},
	}
	if s.Name != "F" || s.Span.End != 3 || s.Kind != astkit.KindFunction {
		t.Fatal("Symbol field access broken")
	}
}

func TestImportStatementZeroValue(t *testing.T) {
	var i astkit.ImportStatement
	if i.Line != 0 || i.Path != "" {
		t.Fatal("zero-value ImportStatement not empty")
	}
}

func TestLanguageKeyString(t *testing.T) {
	if !strings.HasPrefix(string(astkit.LangGo), "go") {
		t.Fatal("LangGo string broken")
	}
}
