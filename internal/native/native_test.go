package native

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/provasign/grove/internal/core"
)

// TestGoAnalyzerEnvDoesNotRedirectIntoRepo guards against the regression
// where HOME/GOCACHE were pointed at <repo>/.grove, causing a full per-repo
// module-cache download and breaking GOPRIVATE auth.
func TestGoAnalyzerEnvDoesNotRedirectIntoRepo(t *testing.T) {
	root := t.TempDir()
	env := goAnalyzerEnv(root)
	for _, e := range env {
		if strings.HasPrefix(e, "HOME="+root) || strings.HasPrefix(e, "GOCACHE="+root) {
			t.Fatalf("goAnalyzerEnv redirects into the repo: %v", e)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".grove", "go-build")); !os.IsNotExist(err) {
		t.Fatalf("goAnalyzerEnv must not create .grove/go-build (err=%v)", err)
	}
}

func TestCleanupLegacyGoCaches(t *testing.T) {
	root := t.TempDir()
	modDir := filepath.Join(root, ".grove", "home", "go", "pkg", "mod", "example.com", "m@v1")
	if err := os.MkdirAll(modDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "f.go"), []byte("package m"), 0o444); err != nil {
		t.Fatal(err)
	}
	// Module caches are written with read-only directories.
	if err := os.Chmod(modDir, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".grove", "go-build"), 0o755); err != nil {
		t.Fatal(err)
	}

	CleanupLegacyCaches(root)

	for _, dir := range []string{
		filepath.Join(root, ".grove", "home"),
		filepath.Join(root, ".grove", "go-build"),
	} {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after cleanup (err=%v)", dir, err)
		}
	}
}

func TestPackageFilesRelativesGoFiles(t *testing.T) {
	root := t.TempDir()
	pkg := goListPackage{
		Dir:     filepath.Join(root, "auth"),
		GoFiles: []string{"auth.go", "login.go"},
	}
	got := packageFiles(root, pkg)
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(got), got)
	}
	for _, f := range got {
		if filepath.IsAbs(f) {
			t.Fatalf("expected relative path, got %q", f)
		}
	}
}

func TestIsFalseValues(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off", "disabled", "FALSE", "OFF"} {
		if !isFalse(v) {
			t.Errorf("isFalse(%q) = false, want true", v)
		}
	}
	for _, v := range []string{"1", "true", "yes", "on", "enabled", ""} {
		if isFalse(v) {
			t.Errorf("isFalse(%q) = true, want false", v)
		}
	}
}

func TestLanguageSetParsesCSV(t *testing.T) {
	got := languageSet("go, rust, python")
	if !got["go"] || !got["rust"] || !got["python"] || got["java"] {
		t.Fatalf("unexpected language set: %v", got)
	}
}

func TestFileSetBuildsMap(t *testing.T) {
	got := fileSet([]string{"src/main.go", "auth/login.go"})
	if !got["src/main.go"] || !got["auth/login.go"] || got["missing.go"] {
		t.Fatalf("unexpected file set: %v", got)
	}
}

func TestNativeImportEdge(t *testing.T) {
	edge := nativeImportEdge("src/main.go", "include/auth.h", 0.95)
	if edge.Type != core.EdgeImports || edge.Confidence != 0.95 || edge.Source != core.EvidenceSourceNative {
		t.Fatalf("unexpected edge: %#v", edge)
	}
}

func TestCountNativeEdges(t *testing.T) {
	edges := []core.Edge{
		{Type: core.EdgeCalls, Source: core.EvidenceSourceNative},
		{Type: core.EdgeCalls, Source: core.EvidenceSourceNative},
		{Type: core.EdgeUsesType, Source: core.EvidenceSourceNative},
	}
	if countNativeEdges(edges, core.EdgeCalls) != 2 {
		t.Fatal("expected 2 call edges")
	}
	if countNativeEdges(edges, core.EdgeUsesType) != 1 {
		t.Fatal("expected 1 type-use edge")
	}
}

func TestFirstExistingExecutable(t *testing.T) {
	got := firstExistingExecutable("no-such-tool-xyz", "go")
	if got != "go" {
		t.Fatalf("expected go, got %q", got)
	}
	if firstExistingExecutable("no-such-tool-xyz") != "" {
		t.Fatal("expected empty when nothing found")
	}
}

func TestBytesReader(t *testing.T) {
	r := bytesReader([]byte("hello"))
	if r.Len() != 5 {
		t.Fatal("unexpected reader length")
	}
}

func TestStringTrim(t *testing.T) {
	if got := stringTrim([]byte("  hi  ")); got != "hi" {
		t.Fatalf("got %q", got)
	}
}

func TestJsonMarshalAndUnmarshal(t *testing.T) {
	v := map[string]int{"a": 1}
	data, err := jsonMarshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]int
	if err := unmarshalJSON(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["a"] != 1 {
		t.Fatalf("unexpected %v", got)
	}
}

func TestOsReadFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := osReadFile(f)
	if err != nil || string(got) != "data" {
		t.Fatalf("got %q, err %v", got, err)
	}
}

func TestDecodeJSON(t *testing.T) {
	data, _ := json.Marshal(map[string]string{"k": "v"})
	got, err := decodeJSON[map[string]string](data)
	if err != nil || got["k"] != "v" {
		t.Fatalf("got %v, err %v", got, err)
	}
}

func TestSymbolByFileAndName(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "1", FilePath: "auth/login.go", Name: "Login", Language: "go"},
		{ID: "2", FilePath: "auth/login.go", Name: "Check", Language: "go", ParentSymbol: "Auth"},
	}
	m := symbolByFileAndName(syms, map[string]bool{"go": true})
	if _, ok := m["auth/login.go\x00Login"]; !ok {
		t.Fatal("missing Login key")
	}
	if _, ok := m["auth/login.go\x00Auth.Check"]; !ok {
		t.Fatal("missing qualified Auth.Check key")
	}
	// non-matching language filtered out
	m2 := symbolByFileAndName(syms, map[string]bool{"rust": true})
	if len(m2) != 0 {
		t.Fatalf("expected empty for rust: %v", m2)
	}
}

func TestGlobAndFilesWithExt(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "auth.go"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "notes.txt"), []byte(""), 0o644)

	matches := glob(root, "*.go")
	if len(matches) != 2 {
		t.Fatalf("glob: expected 2 .go files, got %v", matches)
	}

	goFiles := filesWithExt(root, ".go")
	if len(goFiles) != 2 {
		t.Fatalf("filesWithExt: expected 2 .go files, got %v", goFiles)
	}
	txtFiles := filesWithExt(root, ".txt")
	if len(txtFiles) != 1 {
		t.Fatalf("filesWithExt: expected 1 .txt file, got %v", txtFiles)
	}
}

func TestFilesUnderDir(t *testing.T) {
	files := []string{"src/auth.cs", "src/login.cs", "test/unit.cs", "README.md"}
	got := filesUnderDir("src", files, ".cs")
	if len(got) != 2 {
		t.Fatalf("expected 2 cs files under src, got %v", got)
	}
	// dot dir means no prefix filter
	all := filesUnderDir(".", files, ".cs")
	if len(all) != 3 {
		t.Fatalf("expected 3 cs files total, got %v", all)
	}
}

func TestResolveAgainst(t *testing.T) {
	if got := resolveAgainst("/base", "include/foo.h"); got != filepath.Join("/base", "include/foo.h") {
		t.Fatalf("got %q", got)
	}
	// absolute path passes through
	abs := "/abs/foo.h"
	if runtime.GOOS == "windows" {
		abs = `C:\abs\foo.h`
	}
	if got := resolveAgainst("/base", abs); got != abs {
		t.Fatalf("got %q", got)
	}
}

func TestAddPSR4(t *testing.T) {
	out := map[string][]string{}
	addPSR4(out, map[string]any{
		"App\\":  "src/",
		"Test\\": []any{"tests/", "extra/"},
	})
	if len(out["App\\"]) != 1 || out["App\\"][0] != "src/" {
		t.Fatalf("unexpected App key: %v", out["App\\"])
	}
	if len(out["Test\\"]) != 2 {
		t.Fatalf("unexpected Test key: %v", out["Test\\"])
	}
}

func TestRustModuleEdgesResolvesModDecl(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "src/main.rs"), []byte("mod auth;\nmod utils;\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "src/auth.rs"), []byte(""), 0o644)
	// utils.rs intentionally absent — edge should not be emitted

	edges := rustModuleEdges(root, []string{"src/main.rs", "src/auth.rs"})
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge for resolved auth, got %d: %v", len(edges), edges)
	}
	if edges[0].Type != core.EdgeImports {
		t.Fatalf("expected EdgeImports, got %v", edges[0].Type)
	}
}

func TestGoExprTypeName(t *testing.T) {
	cases := []struct {
		expr ast.Expr
		want string
	}{
		{&ast.Ident{Name: "User"}, "User"},
		{&ast.StarExpr{X: &ast.Ident{Name: "Repo"}}, "Repo"},
		{&ast.SelectorExpr{Sel: &ast.Ident{Name: "Client"}}, "Client"},
		{&ast.IndexExpr{X: &ast.Ident{Name: "Set"}}, "Set"},
		{&ast.BasicLit{}, ""},
	}
	for _, tc := range cases {
		if got := goExprTypeName(tc.expr); got != tc.want {
			t.Errorf("goExprTypeName(%T) = %q, want %q", tc.expr, got, tc.want)
		}
	}
}

func TestConfigFromEnvAppliesOverrides(t *testing.T) {
	t.Setenv("GROVE_NATIVE", "false")
	t.Setenv("GROVE_NATIVE_LANGUAGES", "go,rust")
	t.Setenv("GROVE_NATIVE_DISABLED_LANGUAGES", "php")
	t.Setenv("GROVE_NATIVE_TIMEOUT", "5s")
	cfg := ConfigFromEnv()
	if cfg.Enabled {
		t.Fatal("expected Enabled=false")
	}
	if !cfg.Languages["go"] || !cfg.Languages["rust"] {
		t.Fatalf("unexpected languages: %v", cfg.Languages)
	}
	if !cfg.DisabledLanguages["php"] {
		t.Fatalf("expected php disabled: %v", cfg.DisabledLanguages)
	}
	if cfg.Timeout != 5*time.Second {
		t.Fatalf("unexpected timeout: %v", cfg.Timeout)
	}

	// test TIMEOUT_MS branch
	t.Setenv("GROVE_NATIVE_TIMEOUT", "")
	t.Setenv("GROVE_NATIVE_TIMEOUT_MS", "2000")
	cfg2 := ConfigFromEnv()
	if cfg2.Timeout != 2000*time.Millisecond {
		t.Fatalf("unexpected timeout from ms: %v", cfg2.Timeout)
	}
}

func TestGoTypeBaseNameCoversTypes(t *testing.T) {
	pkg := types.NewPackage("example", "example")
	named := types.NewNamed(types.NewTypeName(0, pkg, "User", nil), nil, nil)
	if got := goTypeBaseName(named); got != "User" {
		t.Fatalf("Named: got %q", got)
	}
	if got := goTypeBaseName(types.NewPointer(named)); got != "User" {
		t.Fatalf("Pointer: got %q", got)
	}
	if got := goTypeBaseName(types.Typ[types.Int]); got != "" {
		t.Fatalf("basic type: got %q, want empty", got)
	}
}

func TestGoObjectDirsAllBranches(t *testing.T) {
	pkg := types.NewPackage("github.com/example/auth", "auth")
	m := map[string][]string{"github.com/example/auth": {"src/auth"}}

	// pkg == nil
	if got := goObjectDirs("src", nil, m); len(got) != 1 || got[0] != "src" {
		t.Fatalf("nil pkg: got %v", got)
	}
	// pkg found in map
	if got := goObjectDirs("src", pkg, m); len(got) != 1 || got[0] != "src/auth" {
		t.Fatalf("found in map: got %v", got)
	}
	// pkg not found — returns currentDir
	if got := goObjectDirs("src", pkg, map[string][]string{}); len(got) != 1 || got[0] != "src" {
		t.Fatalf("not found: got %v", got)
	}
}

func TestGoContainsTypeQualifiedName(t *testing.T) {
	// The regex branch fires when the type appears only as pkg.TypeName.
	if !goContainsType("func f() { var x auth.User }", "User") {
		t.Fatal("expected qualified auth.User to match")
	}
	if goContainsType("func f() { var x int }", "User") {
		t.Fatal("expected no match for absent type")
	}
}

// ---- Available/Analyze methods that require no external tools ----

func TestCFamilyAvailableChecksCompileCommands(t *testing.T) {
	root := t.TempDir()
	a := cFamilyAnalyzer{}
	if a.Available(context.Background(), root).Available {
		t.Fatal("expected not available without compile_commands.json")
	}
	_ = os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte("[]"), 0o644)
	if !a.Available(context.Background(), root).Available {
		t.Fatal("expected available with compile_commands.json")
	}
}

func TestCFamilyAnalyzeWithLocalFiles(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "include"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "include/auth.h"), []byte("struct Auth {};\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "src/main.c"), []byte(`#include "auth.h"
void login() {}
`), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src/main.c"), []byte(`#include "auth.h"
void login() {}
`), 0o644)
	cmds := `[{"directory":"` + root + `","file":"` + root + `/src/main.c","arguments":["cc","-I","` + root + `/include","` + root + `/src/main.c"]}]`
	_ = os.WriteFile(filepath.Join(root, "compile_commands.json"), []byte(cmds), 0o644)

	a := cFamilyAnalyzer{}
	result := a.Analyze(context.Background(), Request{
		Root:  root,
		Files: []string{"src/main.c", "include/auth.h"},
	})
	if len(result.Diagnostics) == 0 {
		t.Fatal("expected diagnostics from cfamily analyze")
	}
}

func TestCIncludeDirsParsesMixedFormats(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "include"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "extra"), 0o755)
	commands := []compileCommand{
		{Directory: root, Arguments: []string{"cc", "-I", "include", "-Iextra", "-isystem", "include"}},
		{Directory: root, Command: "cc -I include src/main.c"},
	}
	dirs := cIncludeDirs(root, commands)
	if len(dirs) == 0 {
		t.Fatal("expected some include dirs")
	}
}

func TestCSharpAvailableChecksCsproj(t *testing.T) {
	root := t.TempDir()
	a := csharpAnalyzer{}
	if a.Available(context.Background(), root).Available {
		t.Fatal("expected not available without .csproj")
	}
	_ = os.WriteFile(filepath.Join(root, "App.csproj"), []byte("<Project/>"), 0o644)
	if !a.Available(context.Background(), root).Available {
		t.Fatal("expected available with .csproj")
	}
}

func TestCSharpAnalyzeWithCsproj(t *testing.T) {
	root := t.TempDir()
	csproj := `<Project Sdk="Microsoft.NET.Sdk">
  <ItemGroup>
    <Compile Include="Auth.cs" />
  </ItemGroup>
</Project>`
	_ = os.WriteFile(filepath.Join(root, "App.csproj"), []byte(csproj), 0o644)
	_ = os.WriteFile(filepath.Join(root, "Auth.cs"), []byte("class Auth {}"), 0o644)

	a := csharpAnalyzer{}
	result := a.Analyze(context.Background(), Request{
		Root:  root,
		Files: []string{"Auth.cs"},
	})
	if len(result.Diagnostics) == 0 {
		t.Fatal("expected diagnostics from csharp analyze")
	}
}

func TestJavaAvailableNeedsProjectFiles(t *testing.T) {
	root := t.TempDir()
	a := javaAnalyzer{}
	av := a.Available(context.Background(), root)
	if av.Available {
		t.Fatalf("expected not available without Maven/Gradle files, got: %q", av.Reason)
	}
}

func TestJavaAnalyzeNoExternalTool(t *testing.T) {
	a := javaAnalyzer{}
	result := a.Analyze(context.Background(), Request{
		Root:    t.TempDir(),
		Symbols: []core.SymbolRecord{{ID: "1", Name: "Login", Language: "java", FilePath: "Auth.java", Kind: core.KindMethod, ParentSymbol: "Auth"}},
		Files:   []string{"Auth.java"},
	})
	if len(result.Diagnostics) == 0 {
		t.Fatal("expected diagnostics from java analyze")
	}
}

func TestRustAvailableNeedsCargoToml(t *testing.T) {
	root := t.TempDir()
	a := rustAnalyzer{}
	av := a.Available(context.Background(), root)
	if av.Available {
		t.Fatalf("expected not available without Cargo.toml, got: %q", av.Reason)
	}
}

func TestJsTSAvailableNeedsProjectConfig(t *testing.T) {
	root := t.TempDir()
	a := jsTSAnalyzer{}
	av := a.Available(context.Background(), root)
	if av.Available {
		t.Fatalf("expected not available without package.json/tsconfig.json, got: %q", av.Reason)
	}
}

func TestPHPAvailableChecksComposerJSON(t *testing.T) {
	root := t.TempDir()
	a := phpAnalyzer{}
	if a.Available(context.Background(), root).Available {
		t.Fatal("expected not available without composer.json")
	}
	_ = os.WriteFile(filepath.Join(root, "composer.json"), []byte("{}"), 0o644)
	if !a.Available(context.Background(), root).Available {
		t.Fatal("expected available with composer.json")
	}
}

func TestPHPAnalyzeReadsComposerJSON(t *testing.T) {
	root := t.TempDir()
	composer := `{"autoload":{"psr-4":{"App\\":"src/"}}}`
	_ = os.WriteFile(filepath.Join(root, "composer.json"), []byte(composer), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "src"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src/Auth.php"), []byte("<?php\nuse App\\User;\nclass Auth {}"), 0o644)

	a := phpAnalyzer{}
	result := a.Analyze(context.Background(), Request{
		Root:  root,
		Files: []string{"src/Auth.php"},
	})
	if len(result.Diagnostics) == 0 {
		t.Fatal("expected diagnostics from php analyze")
	}
}

func TestPythonAvailableWithProjectFile(t *testing.T) {
	root := t.TempDir()
	a := pythonAnalyzer{}
	_ = os.WriteFile(filepath.Join(root, "requirements.txt"), []byte(""), 0o644)
	// Covers anyFile() passing + firstExistingExecutable check
	a.Available(context.Background(), root) // result depends on environment
}

func TestPythonAnalyzeWithRealInterpreter(t *testing.T) {
	if firstExistingExecutable("python3", "python") == "" {
		t.Skip("python3/python not available")
	}
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "requirements.txt"), []byte(""), 0o644)
	_ = os.WriteFile(filepath.Join(root, "auth.py"), []byte(`
class User:
    pass

class Auth:
    def login(self, user: User) -> bool:
        return True
`), 0o644)

	a := pythonAnalyzer{}
	result := a.Analyze(context.Background(), Request{
		Root:  root,
		Files: []string{"auth.py"},
	})
	if len(result.Diagnostics) != 3 {
		t.Fatalf("expected 3 diagnostics, got %d: %v", len(result.Diagnostics), result.Diagnostics)
	}
}

func TestRustAvailableWithCargoToml(t *testing.T) {
	root := t.TempDir()
	a := rustAnalyzer{}
	_ = os.WriteFile(filepath.Join(root, "Cargo.toml"), []byte("[package]\nname = \"test\"\n"), 0o644)
	// Covers anyFile() passing + commandExists("cargo") check
	a.Available(context.Background(), root) // result depends on environment
}

func TestJavaAvailableWithProjectFiles(t *testing.T) {
	root := t.TempDir()
	a := javaAnalyzer{}
	_ = os.WriteFile(filepath.Join(root, "pom.xml"), []byte("<project/>"), 0o644)
	// Covers anyFile() passing + firstExistingExecutable check
	a.Available(context.Background(), root) // result depends on environment
}

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

func TestGoCallSiteEdgesResolveImportedCalls(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "main.go::main@1", FilePath: "main.go",
		Language: "go", Kind: core.KindFunction, Name: "main",
		Imports:   []string{"example.com/app/auth"},
		CallSites: []core.CallSite{{Callee: "auth.Login", Line: 4}},
		RawText:   "func main() { var u auth.User; auth.Login(u) }",
	}
	callee := core.SymbolRecord{
		ID: "auth/auth.go::Login@1", FilePath: "auth/auth.go",
		Language: "go", Kind: core.KindFunction, Name: "Login",
	}
	user := core.SymbolRecord{
		ID: "auth/auth.go::User@1", FilePath: "auth/auth.go",
		Language: "go", Kind: core.KindStruct, Name: "User",
	}
	edges := goCallSiteEdges([]core.SymbolRecord{caller, callee, user})
	assertNativeEdge(t, edges, caller.ID, callee.ID, core.EdgeCalls)
	typeEdges := goTypeUseEdges(context.Background(), []core.SymbolRecord{caller, callee, user})
	assertNativeEdge(t, typeEdges, caller.ID, user.ID, core.EdgeUsesType)
}

func TestGoCallSiteEdgesPreferExactImportedPackage(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "main.go::main@1", FilePath: "main.go",
		Language: "go", Kind: core.KindFunction, Name: "main",
		Imports:   []string{"example.com/app/auth", "example.com/app/other"},
		CallSites: []core.CallSite{{Callee: "auth.Login", Line: 4}},
	}
	authLogin := core.SymbolRecord{
		ID: "auth/auth.go::Login@1", FilePath: "auth/auth.go",
		Language: "go", Kind: core.KindFunction, Name: "Login",
	}
	otherLogin := core.SymbolRecord{
		ID: "other/auth.go::Login@1", FilePath: "other/auth.go",
		Language: "go", Kind: core.KindFunction, Name: "Login",
	}
	edges := goCallSiteEdges([]core.SymbolRecord{caller, authLogin, otherLogin})
	if len(edges) != 1 {
		t.Fatalf("expected one edge, got %#v", edges)
	}
	assertNativeEdge(t, edges, caller.ID, authLogin.ID, core.EdgeCalls)
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
		if edge.Type == core.EdgeCalls {
			foundCall = true
		}
		if edge.From == "src/lib.rs::run@1" && edge.To == "src/auth.rs::User@1" && edge.Type == core.EdgeUsesType {
			foundType = true
		}
	}
	// Native rust no longer emits call edges (the graph layer's narrowed
	// call-site resolution owns calls); text matching only contributes
	// type-usage and implements evidence.
	if foundCall {
		t.Fatalf("native rust must not emit call edges, got %#v", edges)
	}
	if !foundType {
		t.Fatalf("expected module type edge, got %#v", edges)
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
	assertNativeEdge(t, edges, "src/lib.rs::run@1", "src/lib.rs::Service@1", core.EdgeUsesType)
	assertNativeEdge(t, edges, "src/lib.rs::run@1", "src/lib.rs::User@1", core.EdgeUsesType)
	// Call edges are owned by the graph layer; the native pass must not
	// emit them (text matching exploded on same-named trait methods).
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls {
			t.Fatalf("native rust must not emit call edges, got %#v", edge)
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
	assertNativeEdge(t, edges, "src/Service/AuthService.php::login@1", "src/Repo/UserRepo.php::UserRepo@1", core.EdgeUsesType)
	// Call edges are owned by the graph layer; the native pass must not emit
	// them (text matching exploded on same-named methods).
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls {
			t.Fatalf("native php must not emit call edges, got %#v", edge)
		}
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
	assertNativeEdge(t, edges, "src/App.java::handle@1", "src/App.java::Repo@1", core.EdgeUsesType)
	// Calls edges are deliberately NOT native anymore: text matching edged
	// every overload (6x explosion on commons-lang). The graph layer's
	// arity- and type-narrowed resolution of astkit call sites owns calls.
	for _, e := range edges {
		if e.Type == core.EdgeCalls {
			t.Fatalf("native java pass must not emit calls edges, got %+v", e)
		}
	}
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
	assertNativeEdge(t, edges, "src/App.cs::Handle@1", "src/App.cs::Repo@1", core.EdgeUsesType)
	// Call edges are owned by the graph layer; the native pass must not
	// emit them (text matching exploded on overloads — Newtonsoft P 0.20).
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls {
			t.Fatalf("native csharp must not emit call edges, got %#v", edge)
		}
	}
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
