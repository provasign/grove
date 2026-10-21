package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// Stack audit 2026-10-20 #2: editing only tsconfig.json after a completed
// TypeScript pass skipped the analyzer ("no changed files in its
// languages"): config files are plaintext and native scope used only the
// changed source languages.
func TestConfigOnlyChangeInvalidatesNative(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	writeFiles(t, root, map[string]string{
		"a.ts":          "export function target() {}\nexport function caller() { target(); }\n",
		"tsconfig.json": `{"compilerOptions":{"strict":false},"include":["*.ts"]}`,
	})
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := NewWithNativeConfig(parser.NewEngine(), st, native.Config{Enabled: false})
	if _, initial, err := idx.Index(ctx, root); err != nil || initial.EdgeCount == 0 {
		t.Fatalf("seed: %v %#v", err, initial)
	}
	if err := st.SetMeta(ctx, core.MetaNativeComplete, `["go","js-ts"]`); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{"tsconfig.json": `{"compilerOptions":{"strict":true},"include":["*.ts"]}`})
	idx.SetNativeConfig(native.Config{Enabled: true})
	_, r, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Native, "\n")
	if strings.Contains(joined, "js-ts: skipped: no changed files") {
		t.Fatalf("tsconfig changed but TypeScript analysis was skipped: %v", r.Native)
	}
	if !strings.Contains(joined, "js-ts: compiler inputs changed (tsconfig.json)") {
		t.Fatalf("invalidation not reported: %v", r.Native)
	}
	raw, _, _ := st.GetMeta(ctx, core.MetaNativeComplete)
	if strings.Contains(raw, "js-ts") || !strings.Contains(raw, "go") {
		t.Fatalf("native-complete = %s, want js-ts dropped and go kept", raw)
	}
}

// go.mod is not even an indexed file: a module-only edit was a no-op index
// that kept the old go list/type-check results.
func TestGoModOnlyChangeRerunsGoAnalyzer(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	ctx := context.Background()
	writeFiles(t, root, map[string]string{
		"go.mod":  "module inputs\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() { run() }\n\nfunc run() {}\n",
	})
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	_, first, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	skipUnless(t, first.Native, "go: resolved ")

	// Touched, identical content: still a no-op.
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(filepath.Join(root, "go.mod"), later, later); err != nil {
		t.Fatal(err)
	}
	_, touched, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(touched.Native, "\n"), "no file changes") {
		t.Fatalf("touch-only go.mod should stay a no-op: %v", touched.Native)
	}

	writeFiles(t, root, map[string]string{"go.mod": "module inputs\n\ngo 1.23\n"})
	_, r, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(r.Native, "\n")
	if strings.Contains(joined, "no file changes") || !strings.Contains(joined, "go: compiler inputs changed (go.mod)") ||
		!strings.Contains(joined, "go: full pass") {
		t.Fatalf("go.mod change did not re-run the Go analyzer: %v", r.Native)
	}
	if r.EdgeCount == 0 {
		t.Fatalf("graph lost its edges: %#v", r)
	}
	// Settled: the next plain index is a no-op again.
	_, again, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(again.Native, "\n"), "no file changes") {
		t.Fatalf("after the re-run the index should settle: %v", again.Native)
	}
}

func TestCompilerInputAnalyzers(t *testing.T) {
	cases := map[string]string{
		"go.mod": "go", "sub/go.sum": "go", "go.work": "go",
		"tsconfig.json": "js-ts", "packages/x/tsconfig.build.json": "js-ts", "jsconfig.json": "js-ts", "web/package.json": "js-ts",
		"pom.xml": "java", "app/build.gradle.kts": "java", "Cargo.toml": "rust", "compile_commands.json": "cfamily",
		"src/App.csproj": "csharp", "composer.json": "php", "pyproject.toml": "python",
		"README.md": "", "config.json": "", "main.go": "",
	}
	for rel, want := range cases {
		got := strings.Join(native.CompilerInputAnalyzers(rel), ",")
		if got != want {
			t.Errorf("%s: got %q want %q", rel, got, want)
		}
	}
}
