package native

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// typeorm src/container.ts (2026-10-12): the one implementation of
// ContainerInterface.get lives in a class expression, `new (class implements
// ContainerInterface { get() {} })()`, which no index had; neither had
// functions nested in a function body or the bodyless members of an ambient
// `declare class`. The compiler parsed all of them: they become
// compiler-declared symbols and the checker's calls land on them. Overload
// signatures stay out, as the parser leaves them out.
func TestJsTSCompilerDeclaredSymbols(t *testing.T) {
	root := t.TempDir()
	linkTypeScriptForTest(t, root)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tsconfig.json", `{"compilerOptions":{"strict":true},"include":["**/*.ts"]}`)
	write("main.ts", `export interface Container {
  get(name: string): number
}
const defaultContainer: Container = new (class implements Container {
  get(name: string): number { return name.length }
})()
export function sort(xs: number[]): number[] {
  function less(a: number, b: number) { return a - b }
  return xs.sort((a, b) => less(a, b))
}
export declare class Client {
  connect(url: string): void
}
export function pick(x: string): string
export function pick(x: number): number
export function pick(x: any): any { return x }
export function use(c: Client) {
  c.connect("u")
  return defaultContainer.get("k")
}
`)
	parsed := []core.SymbolRecord{
		{ID: "main.ts::Container@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindInterface, Name: "Container", QualifiedName: "Container", Span: core.LineRange{Start: 1, End: 3}},
		{ID: "main.ts::Container.get@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindMethod, Name: "get", QualifiedName: "Container.get", ParentSymbol: "Container", Span: core.LineRange{Start: 2, End: 2}},
		{ID: "main.ts::defaultContainer@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindVariable, Name: "defaultContainer", QualifiedName: "defaultContainer", Span: core.LineRange{Start: 4, End: 6}},
		{ID: "main.ts::sort@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindFunction, Name: "sort", QualifiedName: "sort", Span: core.LineRange{Start: 7, End: 10}},
		{ID: "main.ts::Client@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindClass, Name: "Client", QualifiedName: "Client", Span: core.LineRange{Start: 11, End: 13}},
		{ID: "main.ts::pick@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindFunction, Name: "pick", QualifiedName: "pick", Span: core.LineRange{Start: 16, End: 16}},
		{ID: "main.ts::use@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript", Kind: core.KindFunction, Name: "use", QualifiedName: "use", Span: core.LineRange{Start: 17, End: 20}},
	}
	res := (jsTSAnalyzer{}).Analyze(context.Background(), Request{Root: root, Files: []string{"main.ts"}, Symbols: parsed})

	byQualified := map[string]core.SymbolRecord{}
	for _, s := range res.Symbols {
		byQualified[s.QualifiedName] = s
		if !slices.Contains(s.Modifiers, "compiler-declared") {
			t.Errorf("%s: missing compiler-declared modifier: %v", s.QualifiedName, s.Modifiers)
		}
	}
	anon := "<anonymous@4:42>"
	want := map[string]core.SymbolKind{
		anon:             core.KindClass,
		anon + ".get":    core.KindMethod,
		"sort.less":      core.KindFunction,
		"Client.connect": core.KindMethod,
	}
	if len(res.Symbols) != len(want) {
		t.Fatalf("want %d compiler-declared symbols (no overload signatures), got %+v (diagnostics %v)", len(want), res.Symbols, res.Diagnostics)
	}
	for q, kind := range want {
		if s, ok := byQualified[q]; !ok || s.Kind != kind {
			t.Fatalf("want %s as %s, got %+v", q, kind, byQualified)
		}
	}
	if s := byQualified[anon]; s.Signature != "class "+anon+" implements Container" || !slices.Contains(s.Modifiers, "anonymous") {
		t.Errorf("anonymous class record: %+v", s)
	}
	if s := byQualified[anon+".get"]; s.ParentSymbol != anon {
		t.Errorf("anonymous member parent: %+v", s)
	}
	if s := byQualified["sort.less"]; !slices.Contains(s.Modifiers, "local") || s.ParentSymbol != "sort" {
		t.Errorf("nested function record: %+v", s)
	}

	has := func(from, to string) bool {
		for _, e := range res.Edges {
			if e.From == from && e.To == to && e.Type == core.EdgeCalls {
				return true
			}
		}
		return false
	}
	for _, e := range res.Edges {
		if e.From == "main.ts::use@b" && e.To == byQualified["Client.connect"].ID && e.Lines != "18:5" {
			t.Errorf("use -> Client.connect positions %q, want 18:5", e.Lines)
		}
	}
	if !has("main.ts::use@b", byQualified["Client.connect"].ID) {
		t.Errorf("use -> Client.connect (ambient member) did not land: %v", res.Edges)
	}
	if !has("main.ts::sort@b", byQualified["sort.less"].ID) {
		t.Errorf("sort -> sort.less (nested function) did not land: %v", res.Edges)
	}
}
