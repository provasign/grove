package native

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// grafana (2026-10-12): 499 types declared inside function bodies, with 1642
// fields, were in no index; go/types had attributed every one. They become
// compiler-declared symbols named after their function, uses inside the
// function land on them, and a package-level type of the same name is no
// longer credited with the local one's uses.
func TestGoLocalTypesBecomeCompilerDeclaredSymbols(t *testing.T) {
	root := t.TempDir()
	src := `package main

type opts struct{ verbose bool }

type Repo struct{}

func (r *Repo) Clone() int {
	type opts struct {
		depth int ` + "`json:\"depth\"`" + `
		Base
	}
	type unwrapper interface {
		Unwrap() error
	}
	var o opts
	var _ unwrapper
	return o.depth
}

type Base struct{}

func Plain() {
	var o opts
	_ = o
}
`
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	parsed := []core.SymbolRecord{
		{ID: "main.go::opts@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindStruct, Name: "opts", QualifiedName: "opts", Span: core.LineRange{Start: 3, End: 3}},
		{ID: "main.go::Repo@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindStruct, Name: "Repo", QualifiedName: "Repo", Span: core.LineRange{Start: 5, End: 5}},
		{ID: "main.go::Repo.Clone@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindMethod, Name: "Clone", QualifiedName: "Repo.Clone", ParentSymbol: "Repo", Span: core.LineRange{Start: 7, End: 18}},
		{ID: "main.go::Base@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindStruct, Name: "Base", QualifiedName: "Base", Span: core.LineRange{Start: 20, End: 20}},
		{ID: "main.go::Plain@b", FilePath: "main.go", BlobSHA: "b", Language: "go", Kind: core.KindFunction, Name: "Plain", QualifiedName: "Plain", Span: core.LineRange{Start: 22, End: 25}},
	}
	edges, decls, _ := goSemanticEdges(context.Background(), root, []string{"main.go"}, parsed, nil, nil)
	declared := compilerDeclaredSymbolsFor(Request{Root: root, Symbols: parsed}, "go", decls)

	byQualified := map[string]core.SymbolRecord{}
	for _, s := range declared {
		byQualified[s.QualifiedName] = s
		if !goLocalSymbol(s) {
			t.Errorf("%s: want modifiers compiler-declared+local, got %v", s.QualifiedName, s.Modifiers)
		}
	}
	want := map[string]core.SymbolKind{
		"Repo.Clone.opts":             core.KindStruct,
		"Repo.Clone.opts.depth":       core.KindField,
		"Repo.Clone.unwrapper":        core.KindInterface,
		"Repo.Clone.unwrapper.Unwrap": core.KindMethod,
	}
	if len(declared) != len(want) {
		t.Fatalf("want %d declarations (no embedded Base field), got %+v", len(want), declared)
	}
	for q, kind := range want {
		s, ok := byQualified[q]
		if !ok || s.Kind != kind {
			t.Fatalf("want %s as %s, got %+v", q, kind, s)
		}
	}
	if s := byQualified["Repo.Clone.opts"]; s.ParentSymbol != "Clone" || s.ID != "main.go::Repo.Clone.opts@b" || s.Span != (core.LineRange{Start: 8, End: 11}) {
		t.Errorf("local struct record: %+v", s)
	}
	if s := byQualified["Repo.Clone.opts.depth"]; s.ParentSymbol != "opts" || s.Signature != "depth int" {
		t.Errorf("local field record: %+v", s)
	}
	if s := byQualified["Repo.Clone.unwrapper.Unwrap"]; s.Signature != "Unwrap() error" || !slices.Contains(s.Annotations, "declaration") {
		t.Errorf("local interface method record: %+v", s)
	}

	has := func(from, to string) bool {
		for _, e := range edges {
			if e.From == from && e.To == to && e.Type == core.EdgeUsesType {
				return true
			}
		}
		return false
	}
	if !has("main.go::Repo.Clone@b", "main.go::Repo.Clone.opts@b") {
		t.Error("Clone's use of its local opts should land on the local declaration")
	}
	if has("main.go::Repo.Clone@b", "main.go::opts@b") {
		t.Error("Clone's local opts was credited to the package-level opts")
	}
	if !has("main.go::Plain@b", "main.go::opts@b") {
		t.Error("Plain's use of the package-level opts was lost")
	}

	// Stored local symbols come back in req.Symbols on an incremental run; the
	// name-keyed index must not let the local opts shadow the package's.
	idx := newGoSymbolIndex(append(append([]core.SymbolRecord{}, parsed...), declared...))
	if got := idx.byType[packageDir("main.go")+"\x00opts"].ID; got != "main.go::opts@b" {
		t.Errorf("byType[opts] = %q, want the package-level opts", got)
	}
}
