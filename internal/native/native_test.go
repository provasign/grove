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

func TestAnalyzeWithConfigDisabled(t *testing.T) {
	root := t.TempDir()
	result := AnalyzeWithConfig(context.Background(), root, []core.SymbolRecord{{
		ID: "main.go::main@1", FilePath: "main.go",
		Language: "go", Kind: core.KindFunction, Name: "main",
	}}, Config{Enabled: false})
	if len(result.Edges) != 0 {
		t.Fatalf("got %d edges, want 0", len(result.Edges))
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0] != "native analyzers disabled" {
		t.Fatalf("unexpected diagnostics: %#v", result.Diagnostics)
	}
}

func TestAnalyzerEnabledLanguageAllowDeny(t *testing.T) {
	if !analyzerEnabled(rustAnalyzer{}, Config{Enabled: true, Languages: map[string]bool{"rust": true}}) {
		t.Fatal("rust analyzer should be enabled by language allow-list")
	}
	if analyzerEnabled(rustAnalyzer{}, Config{Enabled: true, Languages: map[string]bool{"java": true}}) {
		t.Fatal("rust analyzer should be disabled by allow-list")
	}
	if analyzerEnabled(rustAnalyzer{}, Config{Enabled: true, DisabledLanguages: map[string]bool{"rust": true}}) {
		t.Fatal("rust analyzer should be disabled by deny-list")
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

func TestRustSemanticEdgesResolveModuleQualifiedSymbols(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/lib.rs::run@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindFunction, Name: "run",
			RawText: `pub fn run() { let u: auth::User = auth::save(); }`,
		},
		{
			ID: "src/auth.rs::save@1", FilePath: "src/auth.rs",
			Language: "rust", Kind: core.KindFunction, Name: "save",
		},
		{
			ID: "src/auth.rs::User@1", FilePath: "src/auth.rs",
			Language: "rust", Kind: core.KindStruct, Name: "User",
		},
	}
	edges := rustSemanticEdges(symbols, []string{"src/lib.rs", "src/auth.rs"})
	var foundCall, foundType bool
	for _, edge := range edges {
		if edge.From == "src/lib.rs::run@1" && edge.To == "src/auth.rs::save@1" && edge.Type == core.EdgeCalls {
			foundCall = true
		}
		if edge.From == "src/lib.rs::run@1" && edge.To == "src/auth.rs::User@1" && edge.Type == core.EdgeUsesType {
			foundType = true
		}
	}
	if !foundCall || !foundType {
		t.Fatalf("expected module call and type edges, got %#v", edges)
	}
}

func TestRustSemanticEdgesResolveImplReceiverAndSignatureTypes(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/lib.rs::AuthProvider@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindTrait, Name: "AuthProvider",
		},
		{
			ID: "src/lib.rs::User@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindStruct, Name: "User",
		},
		{
			ID: "src/lib.rs::Service@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindStruct, Name: "Service",
			RawText: `impl AuthProvider for Service { fn authenticate(&self) -> User { User {} } }`,
		},
		{
			ID: "src/lib.rs::authenticate@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindMethod, Name: "authenticate", ParentSymbol: "Service",
		},
		{
			ID: "src/lib.rs::run@1", FilePath: "src/lib.rs",
			Language: "rust", Kind: core.KindFunction, Name: "run",
			Signature: "pub fn run(svc: Service) -> User",
			RawText:   `pub fn run() -> User { let svc: Service = Service {}; svc.authenticate() }`,
		},
	}
	edges := rustSemanticEdges(symbols, []string{"src/lib.rs"})
	assertNativeEdge(t, edges, "src/lib.rs::Service@1", "src/lib.rs::AuthProvider@1", core.EdgeImplements)
	assertNativeEdge(t, edges, "src/lib.rs::run@1", "src/lib.rs::authenticate@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/lib.rs::run@1", "src/lib.rs::Service@1", core.EdgeUsesType)
	assertNativeEdge(t, edges, "src/lib.rs::run@1", "src/lib.rs::User@1", core.EdgeUsesType)
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

func TestCFamilySemanticEdgesUseIncludedHeaderScope(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/main.c::run@1", FilePath: "src/main.c",
			Language: "c", Kind: core.KindFunction, Name: "run",
			RawText: `void run() { User u; save(&u); }`,
		},
		{
			ID: "include/auth.h::User@1", FilePath: "include/auth.h",
			Language: "c", Kind: core.KindStruct, Name: "User",
		},
		{
			ID: "include/auth.h::save@1", FilePath: "include/auth.h",
			Language: "c", Kind: core.KindFunction, Name: "save",
		},
	}
	edges := cFamilySemanticEdges(symbols, map[string][]string{"src/main.c": {"include/auth.h"}})
	assertNativeEdge(t, edges, "src/main.c::run@1", "include/auth.h::save@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/main.c::run@1", "include/auth.h::User@1", core.EdgeUsesType)
}

func TestCFamilySemanticEdgesResolveCppQualifiedCallsAndConstructors(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/main.cpp::run@1", FilePath: "src/main.cpp",
			Language: "cpp", Kind: core.KindFunction, Name: "run",
			RawText: `void run() { Repo::Save(); Repo repo = Repo(); }`,
		},
		{
			ID: "include/repo.hpp::Repo@1", FilePath: "include/repo.hpp",
			Language: "cpp", Kind: core.KindClass, Name: "Repo",
		},
		{
			ID: "include/repo.hpp::Repo_ctor@1", FilePath: "include/repo.hpp",
			Language: "cpp", Kind: core.KindConstructor, Name: "Repo", ParentSymbol: "Repo",
		},
		{
			ID: "include/repo.hpp::Save@1", FilePath: "include/repo.hpp",
			Language: "cpp", Kind: core.KindMethod, Name: "Save", ParentSymbol: "Repo",
		},
	}
	edges := cFamilySemanticEdges(symbols, map[string][]string{"src/main.cpp": {"include/repo.hpp"}})
	assertNativeEdge(t, edges, "src/main.cpp::run@1", "include/repo.hpp::Save@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/main.cpp::run@1", "include/repo.hpp::Repo_ctor@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/main.cpp::run@1", "include/repo.hpp::Repo@1", core.EdgeUsesType)
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

func TestPHPSemanticEdgesResolveClassAwareBindings(t *testing.T) {
	psr4 := map[string][]string{"App\\": {"src"}}
	scope := map[string]bool{
		"src/Service/AuthService.php": true,
		"src/Service/BaseService.php": true,
		"src/Contract/Loginable.php":  true,
		"src/Support/Loggable.php":    true,
		"src/Repo/UserRepo.php":       true,
	}
	symbols := []core.SymbolRecord{
		{
			ID: "src/Service/BaseService.php::BaseService@1", FilePath: "src/Service/BaseService.php",
			Language: "php", Kind: core.KindClass, Name: "BaseService",
		},
		{
			ID: "src/Contract/Loginable.php::Loginable@1", FilePath: "src/Contract/Loginable.php",
			Language: "php", Kind: core.KindInterface, Name: "Loginable",
		},
		{
			ID: "src/Support/Loggable.php::Loggable@1", FilePath: "src/Support/Loggable.php",
			Language: "php", Kind: core.KindTrait, Name: "Loggable",
		},
		{
			ID: "src/Repo/UserRepo.php::UserRepo@1", FilePath: "src/Repo/UserRepo.php",
			Language: "php", Kind: core.KindClass, Name: "UserRepo",
		},
		{
			ID: "src/Repo/UserRepo.php::__construct@1", FilePath: "src/Repo/UserRepo.php",
			Language: "php", Kind: core.KindMethod, Name: "__construct", ParentSymbol: "UserRepo",
		},
		{
			ID: "src/Repo/UserRepo.php::create@1", FilePath: "src/Repo/UserRepo.php",
			Language: "php", Kind: core.KindMethod, Name: "create", ParentSymbol: "UserRepo",
		},
		{
			ID: "src/Service/AuthService.php::AuthService@1", FilePath: "src/Service/AuthService.php",
			Language: "php", Kind: core.KindClass, Name: "AuthService",
			RawText: `class AuthService extends BaseService implements Loginable { use Loggable; }`,
		},
		{
			ID: "src/Service/AuthService.php::login@1", FilePath: "src/Service/AuthService.php",
			Language: "php", Kind: core.KindMethod, Name: "login", ParentSymbol: "AuthService",
			RawText: `public function login(): UserRepo { $repo = new UserRepo(); UserRepo::create(); return $repo; }`,
		},
	}
	edges := phpSemanticEdges(symbols, psr4, scope)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::AuthService@1", "src/Service/BaseService.php::BaseService@1", core.EdgeExtends)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::AuthService@1", "src/Contract/Loginable.php::Loginable@1", core.EdgeImplements)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::AuthService@1", "src/Support/Loggable.php::Loggable@1", core.EdgeImplements)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::login@1", "src/Repo/UserRepo.php::create@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::login@1", "src/Repo/UserRepo.php::__construct@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/Service/AuthService.php::login@1", "src/Repo/UserRepo.php::UserRepo@1", core.EdgeUsesType)
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

func TestJavaSemanticEdgesResolveClassAwareBindings(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/App.java::Runnable@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindInterface, Name: "Runnable",
		},
		{
			ID: "src/App.java::BaseController@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindClass, Name: "BaseController",
		},
		{
			ID: "src/App.java::Controller@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindClass, Name: "Controller",
			Signature: "public class Controller extends BaseController implements Runnable",
		},
		{
			ID: "src/App.java::Repo@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindClass, Name: "Repo",
		},
		{
			ID: "src/App.java::Repo_ctor@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindConstructor, Name: "Repo", ParentSymbol: "Repo",
		},
		{
			ID: "src/App.java::save@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindMethod, Name: "save", ParentSymbol: "Repo",
		},
		{
			ID: "src/App.java::helper@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindMethod, Name: "helper", ParentSymbol: "Controller",
		},
		{
			ID: "src/App.java::handle@1", FilePath: "src/App.java",
			Language: "java", Kind: core.KindMethod, Name: "handle", ParentSymbol: "Controller",
			RawText: `void handle() { helper(); Repo.save(); Repo repo = new Repo(); }`,
		},
	}
	edges := javaSemanticEdges(symbols)
	assertNativeEdge(t, edges, "src/App.java::Controller@1", "src/App.java::BaseController@1", core.EdgeExtends)
	assertNativeEdge(t, edges, "src/App.java::Controller@1", "src/App.java::Runnable@1", core.EdgeImplements)
	assertNativeEdge(t, edges, "src/App.java::handle@1", "src/App.java::helper@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.java::handle@1", "src/App.java::save@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.java::handle@1", "src/App.java::Repo_ctor@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.java::handle@1", "src/App.java::Repo@1", core.EdgeUsesType)
}

func TestCSharpSemanticEdgesResolveClassAwareBindings(t *testing.T) {
	symbols := []core.SymbolRecord{
		{
			ID: "src/App.cs::IRunnable@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindInterface, Name: "IRunnable",
		},
		{
			ID: "src/App.cs::BaseController@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindClass, Name: "BaseController",
		},
		{
			ID: "src/App.cs::Controller@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindClass, Name: "Controller",
			Signature: "public class Controller : BaseController, IRunnable",
		},
		{
			ID: "src/App.cs::Repo@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindClass, Name: "Repo",
		},
		{
			ID: "src/App.cs::Repo_ctor@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindConstructor, Name: "Repo", ParentSymbol: "Repo",
		},
		{
			ID: "src/App.cs::Save@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindMethod, Name: "Save", ParentSymbol: "Repo",
		},
		{
			ID: "src/App.cs::Helper@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindMethod, Name: "Helper", ParentSymbol: "Controller",
		},
		{
			ID: "src/App.cs::Handle@1", FilePath: "src/App.cs",
			Language: "csharp", Kind: core.KindMethod, Name: "Handle", ParentSymbol: "Controller",
			RawText: `void Handle() { Helper(); Repo.Save(); Repo repo = new Repo(); }`,
		},
	}
	edges := csharpSemanticEdges(symbols)
	assertNativeEdge(t, edges, "src/App.cs::Controller@1", "src/App.cs::BaseController@1", core.EdgeExtends)
	assertNativeEdge(t, edges, "src/App.cs::Controller@1", "src/App.cs::IRunnable@1", core.EdgeImplements)
	assertNativeEdge(t, edges, "src/App.cs::Handle@1", "src/App.cs::Helper@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.cs::Handle@1", "src/App.cs::Save@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.cs::Handle@1", "src/App.cs::Repo_ctor@1", core.EdgeCalls)
	assertNativeEdge(t, edges, "src/App.cs::Handle@1", "src/App.cs::Repo@1", core.EdgeUsesType)
}

func assertNativeEdge(t *testing.T, edges []core.Edge, from, to string, edgeType core.EdgeType) {
	t.Helper()
	for _, edge := range edges {
		if edge.From == from && edge.To == to && edge.Type == edgeType && edge.Source == core.EvidenceSourceNative {
			return
		}
	}
	t.Fatalf("missing %s edge %s -> %s in %#v", edgeType, from, to, edges)
}
