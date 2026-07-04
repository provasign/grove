package strategies_test

import (
	"context"
	"strings"
	"testing"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/strategies"
)

// extract is a small helper that parses src under lang and runs the
// registered strategy.
func extract(t *testing.T, lang astkit.LanguageKey, src string) ([]astkit.Symbol, []astkit.ImportStatement) {
	t.Helper()
	eng := astkit.NewEngine()
	reg := strategies.Default()
	tree, err := eng.Parse(context.Background(), lang, []byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tree == nil {
		t.Fatalf("nil tree for %s", lang)
	}
	defer tree.Close()
	syms, err := reg.Extract(lang, tree, []byte(src))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	imps, err := reg.ExtractImports(lang, tree, []byte(src))
	if err != nil {
		t.Fatalf("imports: %v", err)
	}
	return syms, imps
}

func names(syms []astkit.Symbol) []string {
	out := make([]string, len(syms))
	for i, s := range syms {
		out[i] = s.QualifiedName
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestDefault_RegistersAllLanguages(t *testing.T) {
	reg := strategies.Default()
	for _, l := range []astkit.LanguageKey{
		astkit.LangGo, astkit.LangPython, astkit.LangJava, astkit.LangRust,
		astkit.LangJavaScript, astkit.LangTypeScript, astkit.LangTSX,
		astkit.LangC, astkit.LangCPP, astkit.LangCSharp, astkit.LangPHP,
	} {
		if reg.Get(l) == nil {
			t.Errorf("strategy missing for %s", l)
		}
	}
}

func TestStrategy_Extensions(t *testing.T) {
	reg := strategies.Default()
	cases := map[astkit.LanguageKey]string{
		astkit.LangGo:         ".go",
		astkit.LangPython:     ".py",
		astkit.LangJava:       ".java",
		astkit.LangRust:       ".rs",
		astkit.LangJavaScript: ".js",
		astkit.LangTypeScript: ".ts",
		astkit.LangTSX:        ".tsx",
		astkit.LangC:          ".c",
		astkit.LangCPP:        ".cpp",
		astkit.LangCSharp:     ".cs",
		astkit.LangPHP:        ".php",
	}
	for lang, want := range cases {
		exts := reg.Get(lang).Extensions()
		found := false
		for _, e := range exts {
			if e == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s extensions %v missing %s", lang, exts, want)
		}
	}
}

func TestExtract_Go(t *testing.T) {
	src := `package main

import (
	"fmt"
	"strings"
)

// Hello says hi.
func Hello(name string) string {
	return fmt.Sprintf("hi %s", strings.ToUpper(name))
}

type Greeter struct {
	Prefix string
}

func (g *Greeter) Greet(n string) string {
	return g.Prefix + Hello(n)
}

const Version = "1"
var pkg = "x"
`
	syms, imps := extract(t, astkit.LangGo, src)
	got := names(syms)
	for _, want := range []string{"Hello", "Greeter", "Greet", "Version", "pkg"} {
		if !contains(got, want) {
			t.Errorf("missing symbol %q in %v", want, got)
		}
	}
	if len(imps) != 2 {
		t.Errorf("imports=%d want 2", len(imps))
	}
	for _, i := range imps {
		if i.Group != "stdlib" {
			t.Errorf("expected stdlib group, got %q for %s", i.Group, i.Path)
		}
	}
	// Find Hello and check call-sites + Exported.
	for _, s := range syms {
		if s.QualifiedName == "Hello" {
			if !s.Exported {
				t.Error("Hello must be Exported")
			}
			calls := make(map[string]bool)
			for _, c := range s.CallSites {
				calls[c.Callee] = true
			}
			if !calls["fmt.Sprintf"] && !calls["Sprintf"] {
				t.Errorf("Hello call-sites missing fmt.Sprintf: %v", s.CallSites)
			}
		}
	}
	for _, s := range syms {
		switch s.QualifiedName {
		case "Greeter":
			if !strings.HasPrefix(s.Body, "type Greeter struct") {
				t.Fatalf("Greeter body = %q", s.Body)
			}
		case "Version":
			if !strings.HasPrefix(s.Body, "const Version") {
				t.Fatalf("Version body = %q", s.Body)
			}
		case "pkg":
			if !strings.HasPrefix(s.Body, "var pkg") {
				t.Fatalf("pkg body = %q", s.Body)
			}
		}
	}
}

func TestExtract_GoImportGroups(t *testing.T) {
	src := `package main

import (
	"fmt"
	"github.com/x/y"
	"./local"
)
`
	_, imps := extract(t, astkit.LangGo, src)
	g := map[string]string{}
	for _, i := range imps {
		g[i.Path] = i.Group
	}
	if g["fmt"] != "stdlib" {
		t.Errorf("fmt group=%q", g["fmt"])
	}
	if g["github.com/x/y"] != "external" {
		t.Errorf("external group=%q", g["github.com/x/y"])
	}
	if g["./local"] != "relative" {
		t.Errorf("relative group=%q", g["./local"])
	}
}

func TestExtract_Python(t *testing.T) {
	src := `import os
from pathlib import Path

class Greeter:
    def __init__(self, prefix):
        self.prefix = prefix

    @staticmethod
    def say(n):
        return n

def hello(name):
    return name
`
	syms, imps := extract(t, astkit.LangPython, src)
	got := names(syms)
	for _, w := range []string{"Greeter", "hello"} {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if len(imps) < 2 {
		t.Errorf("imports=%d want >=2", len(imps))
	}
}

func TestExtract_JavaScript(t *testing.T) {
	src := `import {x} from "./mod";

export function hello(name) {
  return x(name);
}

export class A {
  greet(n) { return n; }
}

const Z = () => 1;
`
	syms, imps := extract(t, astkit.LangJavaScript, src)
	got := names(syms)
	for _, w := range []string{"hello", "A"} {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
	if len(imps) != 1 || imps[0].Path != "./mod" {
		t.Errorf("imports=%+v", imps)
	}
}

func TestExtract_TypeScript(t *testing.T) {
	src := `import { x } from "./m";

export function add<T extends number>(a: T, b: T): T {
  return (a + b) as T;
}

export interface I { go(): void }
export class C implements I { go() {} }
`
	syms, _ := extract(t, astkit.LangTypeScript, src)
	got := names(syms)
	for _, w := range []string{"add", "C", "I"} {
		if !contains(got, w) {
			t.Errorf("missing %q in %v", w, got)
		}
	}
}

func TestExtract_TSX(t *testing.T) {
	src := `import React from "react";
export const C = (p: {n: string}) => <div>{p.n}</div>;
`
	syms, _ := extract(t, astkit.LangTSX, src)
	if len(syms) == 0 {
		t.Fatal("tsx produced 0 symbols")
	}
}

func TestExtract_Java(t *testing.T) {
	src := `package x;
import java.util.List;

public class Greeter {
  private String prefix;
  public Greeter(String p) { this.prefix = p; }
  @Override public String toString() { return prefix; }
}
`
	syms, imps := extract(t, astkit.LangJava, src)
	got := names(syms)
	if !contains(got, "Greeter") {
		t.Errorf("missing Greeter in %v", got)
	}
	if len(imps) != 1 {
		t.Errorf("imports=%v", imps)
	}
}

func TestExtract_Rust(t *testing.T) {
	src := `use std::fmt;

pub struct Greeter { pub prefix: String }

impl Greeter {
    pub fn new(p: String) -> Self { Self { prefix: p } }
}

pub fn hello(n: &str) -> String { n.to_string() }
`
	syms, imps := extract(t, astkit.LangRust, src)
	got := names(syms)
	if !contains(got, "Greeter") || !contains(got, "hello") {
		t.Errorf("missing in %v", got)
	}
	if len(imps) != 1 {
		t.Errorf("imports=%v", imps)
	}
}

func TestExtract_C(t *testing.T) {
	src := `#include <stdio.h>
#include "x.h"

int add(int a, int b) { return a + b; }
typedef struct Point { int x, y; } Point;
`
	syms, imps := extract(t, astkit.LangC, src)
	got := names(syms)
	if !contains(got, "add") {
		t.Errorf("missing add in %v", got)
	}
	if len(imps) != 2 {
		t.Errorf("imports=%v", imps)
	}
}

func TestExtract_CPP(t *testing.T) {
	src := `#include <vector>
namespace ns {
  class Greeter {
   public:
    Greeter(std::string p) : prefix(p) {}
    std::string greet();
   private:
    std::string prefix;
  };
}
`
	syms, _ := extract(t, astkit.LangCPP, src)
	if len(syms) == 0 {
		t.Fatal("cpp produced 0 symbols")
	}
}

func TestExtract_CSharp(t *testing.T) {
	src := `using System;

namespace App {
  public class Greeter {
    public string Prefix { get; set; }
    public Greeter(string p) { Prefix = p; }
    public string Say(string n) => Prefix + n;
  }
}
`
	syms, imps := extract(t, astkit.LangCSharp, src)
	if len(syms) == 0 {
		t.Fatal("csharp produced 0 symbols")
	}
	if len(imps) != 1 {
		t.Errorf("imports=%v", imps)
	}
}

func TestExtract_PHP(t *testing.T) {
	src := `<?php
namespace App;
use Foo\Bar;

function hello($n) { return $n; }

class Greeter {
  public function __construct(public string $prefix) {}
  public function say($n) { return $this->prefix . $n; }
}
`
	syms, imps := extract(t, astkit.LangPHP, src)
	got := names(syms)
	if !contains(got, "hello") {
		t.Errorf("missing hello in %v", got)
	}
	if len(imps) == 0 {
		t.Errorf("expected imports got %v", imps)
	}
}

func TestExtract_NilTreeReturnsNil(t *testing.T) {
	reg := strategies.Default()
	for _, l := range []astkit.LanguageKey{astkit.LangGo, astkit.LangPython, astkit.LangJava, astkit.LangRust, astkit.LangC, astkit.LangCPP, astkit.LangCSharp, astkit.LangPHP, astkit.LangJavaScript, astkit.LangTypeScript, astkit.LangTSX} {
		s, err := reg.Get(l).Extract(nil, nil)
		if err != nil || s != nil {
			t.Errorf("%s: Extract(nil) returned (%v, %v)", l, s, err)
		}
		i, err := reg.Get(l).ExtractImports(nil, nil)
		if err != nil || i != nil {
			t.Errorf("%s: ExtractImports(nil) returned (%v, %v)", l, i, err)
		}
	}
}

func TestExtract_Signature(t *testing.T) {
	syms, _ := extract(t, astkit.LangGo, "package x\nfunc Foo(a int, b string) (string, error) { return \"\", nil }\n")
	for _, s := range syms {
		if s.QualifiedName == "Foo" {
			if !strings.Contains(s.Signature, "Foo") || strings.Contains(s.Signature, "{") {
				t.Errorf("signature wrong: %q", s.Signature)
			}
			return
		}
	}
	t.Fatal("Foo not found")
}
