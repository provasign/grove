package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A class extending an aliased export gets its extends edge from the
// checker. hono's hono-base.ts ends with `export { Hono as HonoBase }` and
// hono.ts declares `class Hono extends HonoBase`: no symbol is named
// HonoBase, so the name-based heritage edge was never made, and every
// override below hono.ts's Hono (a test's `class ExtendedHono extends Hono {
// basePath() }`) was missing from HonoBase's basePath family (2026-11-28).
func TestJsTSHeritageThroughAliasedExport(t *testing.T) {
	root := t.TempDir()
	linkTypeScriptForTest(t, root)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tsconfig.json", `{"compilerOptions":{"strict":true},"include":["**/*.ts"]}`)
	write("base.ts", "class Hono {\n  basePath(p: string): Hono { return this }\n}\nexport interface Shape {}\nexport { Hono as HonoBase, Shape as BaseShape }\n")
	write("hono.ts", "import { HonoBase, BaseShape } from './base'\nexport class Hono extends HonoBase implements BaseShape {}\nexport interface Wide extends BaseShape {}\n")
	sym := func(file, name, qual, parent string, kind core.SymbolKind, start, end int) core.SymbolRecord {
		return core.SymbolRecord{ID: file + "::" + qual + "@b", FilePath: file, BlobSHA: "b", Language: "typescript",
			Kind: kind, Name: name, QualifiedName: qual, ParentSymbol: parent, Span: core.LineRange{Start: start, End: end}}
	}
	parsed := []core.SymbolRecord{
		sym("base.ts", "Hono", "Hono", "", core.KindClass, 1, 3),
		sym("base.ts", "basePath", "Hono.basePath", "Hono", core.KindMethod, 2, 2),
		sym("base.ts", "Shape", "Shape", "", core.KindInterface, 4, 4),
		sym("hono.ts", "Hono", "Hono", "", core.KindClass, 2, 2),
		sym("hono.ts", "Wide", "Wide", "", core.KindInterface, 3, 3),
	}
	res := (jsTSAnalyzer{}).Analyze(context.Background(), Request{Root: root, Files: []string{"base.ts", "hono.ts"}, Symbols: parsed})
	have := map[string]bool{}
	for _, e := range res.Edges {
		have[e.From+" "+string(e.Type)+" "+e.To] = true
	}
	for _, want := range []string{
		"hono.ts::Hono@b extends base.ts::Hono@b",
		"hono.ts::Hono@b implements base.ts::Shape@b",
		"hono.ts::Wide@b extends base.ts::Shape@b",
	} {
		if !have[want] {
			t.Errorf("missing edge %q; diagnostics %v", want, res.Diagnostics)
		}
	}
	if have["hono.ts::Hono@b extends base.ts::Shape@b"] || have["hono.ts::Hono@b implements base.ts::Hono@b"] {
		t.Errorf("heritage kind crossed over: %v", have)
	}
}
