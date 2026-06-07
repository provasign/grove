package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestAnalyzeReportsSkippedPriorityAnalyzers(t *testing.T) {
	root := t.TempDir()
	symbols := []core.SymbolRecord{{
		ID: "main.py::main@1", FilePath: "main.py",
		Language: "python", Kind: core.KindFunction, Name: "main",
	}}

	result := Analyze(context.Background(), root, symbols)
	if len(result.Diagnostics) == 0 {
		t.Fatal("expected skipped analyzer diagnostic")
	}
}

func TestGoAnalyzerAvailabilityRequiresGoProject(t *testing.T) {
	root := t.TempDir()
	avail := goAnalyzer{}.Available(context.Background(), root)
	if avail.Available {
		t.Fatal("go analyzer should be unavailable without go.mod/go.work")
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	avail = goAnalyzer{}.Available(context.Background(), root)
	if commandExists("go") && !avail.Available {
		t.Fatalf("go analyzer should be available when go exists: %#v", avail)
	}
}

func TestGoSemanticEdgesResolveCallsAndTypeUses(t *testing.T) {
	root := t.TempDir()
	src := `package main

type User struct{}

func Helper(u User) {}

func Caller() {
	var u User
	Helper(u)
}
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	symbols := []core.SymbolRecord{
		{ID: "main.go::User@1", FilePath: "main.go", Language: "go", Kind: core.KindStruct, Name: "User", QualifiedName: "User"},
		{ID: "main.go::Helper@1", FilePath: "main.go", Language: "go", Kind: core.KindFunction, Name: "Helper", QualifiedName: "Helper"},
		{ID: "main.go::Caller@1", FilePath: "main.go", Language: "go", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller"},
	}
	edges, diagnostics := goSemanticEdges(root, []string{"main.go"}, symbols, nil)
	if len(diagnostics) == 0 {
		t.Fatal("expected diagnostics")
	}
	var foundCall, foundType bool
	for _, edge := range edges {
		if edge.From == "main.go::Caller@1" && edge.To == "main.go::Helper@1" && edge.Type == core.EdgeCalls && edge.Source == core.EvidenceSourceNative {
			foundCall = true
		}
		if edge.From == "main.go::Caller@1" && edge.To == "main.go::User@1" && edge.Type == core.EdgeUsesType && edge.Source == core.EvidenceSourceNative {
			foundType = true
		}
	}
	if !foundCall {
		t.Fatalf("expected native call edge, got %#v", edges)
	}
	if !foundType {
		t.Fatalf("expected native type-use edge, got %#v", edges)
	}
}

func TestRustModuleNames(t *testing.T) {
	mods := rustModuleNames(`mod private;
pub mod public;
mod inline {}
mod nested::bad;
`)
	if len(mods) != 2 || mods[0] != "private" || mods[1] != "public" {
		t.Fatalf("unexpected module names: %#v", mods)
	}
}

func TestRustModuleCandidates(t *testing.T) {
	got := rustModuleCandidates("src/lib.rs", "auth")
	want := []string{"src/auth.rs", "src/auth/mod.rs"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestCIncludes(t *testing.T) {
	got := cIncludes(`#include "local.h"
# include <lib/system.hpp>
// #include "ignored.h"
`)
	want := []string{"local.h", "lib/system.hpp"}
	if len(got) != len(want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestResolveCIncludeUsesFileDirAndIncludeDirs(t *testing.T) {
	scope := map[string]bool{
		"src/local.h":      true,
		"include/shared.h": true,
	}
	if got, ok := resolveCInclude("/repo", "src/main.c", "local.h", nil, scope); !ok || got != "src/local.h" {
		t.Fatalf("got %q/%v, want src/local.h/true", got, ok)
	}
	if got, ok := resolveCInclude("/repo", "src/main.c", "shared.h", []string{"include"}, scope); !ok || got != "include/shared.h" {
		t.Fatalf("got %q/%v, want include/shared.h/true", got, ok)
	}
}

func TestExplicitCSharpFiles(t *testing.T) {
	project := csProject{Items: []struct {
		Compile []struct {
			Include string `xml:"Include,attr"`
			Remove  string `xml:"Remove,attr"`
		} `xml:"Compile"`
		ProjectReference []struct {
			Include string `xml:"Include,attr"`
		} `xml:"ProjectReference"`
	}{{
		Compile: []struct {
			Include string `xml:"Include,attr"`
			Remove  string `xml:"Remove,attr"`
		}{
			{Include: "Program.cs"},
			{Include: "**/*.cs"},
		},
	}}}
	got := explicitCSharpFiles("src/App", project, map[string]bool{"src/App/Program.cs": true})
	if len(got) != 1 || got[0] != "src/App/Program.cs" {
		t.Fatalf("unexpected C# files: %#v", got)
	}
}

func TestPHPReferencedClassesAndPSR4Resolution(t *testing.T) {
	classes := phpReferencedClasses(`<?php
use App\Service\UserService;
$x = new \App\Model\User();
`)
	if len(classes) != 2 {
		t.Fatalf("unexpected classes: %#v", classes)
	}
	psr4 := map[string][]string{"App\\": {"src"}}
	scope := map[string]bool{
		"src/Service/UserService.php": true,
		"src/Model/User.php":          true,
	}
	if got, ok := resolvePHPClass("App\\Service\\UserService", psr4, scope); !ok || got != "src/Service/UserService.php" {
		t.Fatalf("got %q/%v, want src/Service/UserService.php/true", got, ok)
	}
}

func TestLexicalSemanticEdgesResolveSameFileCallsAndTypes(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/App.java::Controller@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindClass, Name: "Controller",
		},
		{
			ID: "src/App.java::save@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindMethod, Name: "save",
		},
		{
			ID: "src/App.java::handle@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindMethod, Name: "handle",
			RawText: `void handle(Controller c) { save(); String ignored = "save("; }`,
		},
	}
	edges := lexicalSemanticEdges(symbols, map[string]bool{"java": true}, 0.93, 0.9)
	var foundCall, foundType bool
	for _, edge := range edges {
		if edge.From == "src/App.java::handle@1" && edge.To == "src/App.java::save@1" && edge.Type == core.EdgeCalls {
			foundCall = true
		}
		if edge.From == "src/App.java::handle@1" && edge.To == "src/App.java::Controller@1" && edge.Type == core.EdgeUsesType {
			foundType = true
		}
	}
	if !foundCall || !foundType {
		t.Fatalf("expected call and type edges, got %#v", edges)
	}
}
