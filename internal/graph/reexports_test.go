package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestChangeImpactTSFunctionReportsReExports(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "packages/zod/src/v4/core/api.ts::_gte@918", FilePath: "packages/zod/src/v4/core/api.ts", Language: "typescript",
			Kind: core.KindFunction, Name: "_gte", QualifiedName: "_gte", Span: core.LineRange{Start: 918, End: 925}},
		{ID: "packages/other/api.ts::_gte@1", FilePath: "packages/other/api.ts", Language: "typescript",
			Kind: core.KindFunction, Name: "_gte", QualifiedName: "_gte", Span: core.LineRange{Start: 1, End: 3}},
	}, 2)
	g.SetJSExportScanner(func(name string) ([]core.JSExportSpecifier, int) {
		return []core.JSExportSpecifier{
			{File: "packages/zod/src/v4/core/api.ts", Line: 929, Local: "_gte", Exported: "_min", Text: "_gte as _min,"},
			{File: "packages/zod/src/v4/mini/checks.ts", Line: 6, Local: "_gte", Exported: "gte", Source: "../core/index.js", Text: "_gte as gte,"},
			// resolves to neither declaration's module: dropped
			{File: "packages/zod/src/v4/mini/other.ts", Line: 2, Local: "_gte", Exported: "x", Source: "./elsewhere.js", Text: "_gte as x,"},
			// bare package specifier with two same-named declarations: dropped
			{File: "app/index.ts", Line: 1, Local: "_gte", Exported: "gte", Source: "zod", Text: "_gte as gte"},
		}, 0
	})
	r, err := g.ChangeImpactScoped("_gte", "zod/src/v4/core")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.ReExports) != 2 {
		t.Fatalf("reExports = %+v, want the local alias and the mini barrel", r.ReExports)
	}
	if r.ReExports[1].FilePath != "packages/zod/src/v4/mini/checks.ts" || r.ReExports[1].Access != "re-export" ||
		r.ReExports[1].Evidence != "re-exported as gte from ../core/index.js" {
		t.Fatalf("mini re-export = %+v", r.ReExports[1])
	}
	for _, s := range r.Sites() {
		if s.FilePath == "packages/zod/src/v4/mini/checks.ts" {
			t.Fatal("re-exports are not symbols and must not enter Sites()")
		}
	}
}

// zod pr6129: `$ZodCheckGreaterThan` is an interface AND a same-named const
// constructor; the behavior (and the fix) lives in the const.
func TestChangeImpactTSTypeIncludesMergedValueDeclaration(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "checks.ts::$ZodCheckGreaterThan@111", FilePath: "checks.ts", Language: "typescript", Kind: core.KindInterface,
			Name: "$ZodCheckGreaterThan", QualifiedName: "$ZodCheckGreaterThan", Span: core.LineRange{Start: 111, End: 113}},
		{ID: "checks.ts::$ZodCheckGreaterThan@115", FilePath: "checks.ts", Language: "typescript", Kind: core.KindVariable,
			Name: "$ZodCheckGreaterThan", QualifiedName: "$ZodCheckGreaterThan", Span: core.LineRange{Start: 115, End: 146}},
		{ID: "other.ts::$ZodCheckGreaterThan@1", FilePath: "other.ts", Language: "typescript", Kind: core.KindVariable,
			Name: "$ZodCheckGreaterThan", QualifiedName: "$ZodCheckGreaterThan", Span: core.LineRange{Start: 1, End: 2}},
	}, 2)
	r, err := g.ChangeImpact("$ZodCheckGreaterThan")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Declarations) != 2 || r.Declarations[0].Span.Start != 111 || r.Declarations[1].Span.Start != 115 {
		t.Fatalf("declarations = %+v, want the interface and the same-file const", r.Declarations)
	}
}

func TestJSModuleCovers(t *testing.T) {
	for _, c := range []struct {
		module, decl string
		want         bool
	}{
		{"packages/zod/src/v4/core/index.js", "packages/zod/src/v4/core/api.ts", true},
		{"packages/zod/src/v4/core", "packages/zod/src/v4/core/sub/api.ts", true},
		{"packages/zod/src/v4/core/api.js", "packages/zod/src/v4/core/api.ts", true},
		{"packages/zod/src/v4/core/util.js", "packages/zod/src/v4/core/api.ts", false},
		{"packages/zod/src/v4/mini", "packages/zod/src/v4/core/api.ts", false},
	} {
		if got := jsModuleCovers(c.module, c.decl); got != c.want {
			t.Errorf("jsModuleCovers(%q, %q) = %v, want %v", c.module, c.decl, got, c.want)
		}
	}
}
