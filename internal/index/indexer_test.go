package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

func TestIndexerPersistsAndSkipsUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

type AuthService struct{}

func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	idx := New(parser.NewEngine(), st)
	_, first, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.FilesUpdated != 1 || first.FilesSkipped != 0 || first.SymbolCount != 2 {
		t.Fatalf("unexpected first index result: %#v", first)
	}

	_, second, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if second.FilesUpdated != 0 || second.FilesSkipped != 1 || second.SymbolCount != 2 {
		t.Fatalf("unexpected second index result: %#v", second)
	}
}

func TestIndexerPrunesDeletedFiles(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "main.go")
	if err := os.WriteFile(filePath, []byte(`package main

func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	idx := New(parser.NewEngine(), st)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	_, result, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesPruned != 1 || result.SymbolCount != 0 {
		t.Fatalf("unexpected prune result: %#v", result)
	}
}

func TestNewWithNativeConfigAndSetNativeConfig(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	cfg := native.Config{Enabled: false}
	idx := NewWithNativeConfig(parser.NewEngine(), st, cfg)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	cfg2 := native.Config{Enabled: false, Timeout: 1}
	idx.SetNativeConfig(cfg2)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}

// TestScopedNativeAnalyzersCarryForwardEdges: editing only a Go file must
// not re-run (or drop the edges of) analyzers for untouched languages.
func TestScopedNativeAnalyzersCarryForwardEdges(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module scoped\n\ngo 1.22\n")
	write("main.go", "package main\n\nfunc Run() { helper() }\n\nfunc helper() {}\n")
	write("app.py", "def serve():\n    handle()\n\ndef handle():\n    pass\n")
	write("pyproject.toml", "[project]\nname = \"scoped\"\n")

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)

	g1, r1, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	nativeEdges := func(g *graph.CodeGraph, lang string) int {
		syms, edges := g.Snapshot()
		langByID := map[string]string{}
		for _, s := range syms {
			langByID[s.ID] = s.Language
		}
		n := 0
		for _, e := range edges {
			if e.Source == core.EvidenceSourceNative && langByID[e.From] == lang {
				n++
			}
		}
		return n
	}
	pyBefore := nativeEdges(g1, "python")
	if pyBefore == 0 {
		t.Skipf("python native analyzer unavailable in this environment (diagnostics: %v)", r1.Native)
	}

	// Edit only the Go file.
	write("main.go", "package main\n\nfunc Run() { helper(); helper() }\n\nfunc helper() {}\n")
	g2, r2, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	skippedPython := false
	for _, d := range r2.Native {
		if strings.Contains(d, "python") && strings.Contains(d, "no changed files") {
			skippedPython = true
		}
	}
	if !skippedPython {
		t.Fatalf("python analyzer should be skipped on a Go-only change; diagnostics: %v", r2.Native)
	}
	if got := nativeEdges(g2, "python"); got != pyBefore {
		t.Fatalf("python native edges = %d after Go-only change, want %d (carried forward)", got, pyBefore)
	}
}
