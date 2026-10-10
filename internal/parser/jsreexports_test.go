package parser

import (
	"os"
	"path/filepath"
	"testing"
)

// zod pr6129: mini/checks.ts is nothing but a re-export block, so no symbol
// (and no call edge) ever points at `_gte as gte`.
func TestJSExportSpecifiersFindsAliasedReExports(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("core/api.ts", "export function _gte(v: number) { return v; }\n"+
		"export {\n  /** @deprecated Use `z.gte()` instead. */\n  _gte as _min,\n};\n")
	write("mini/checks.ts", "export {\n  _lt as lt,\n  _gte as gte,\n  _gte as minimum, // alias\n  type $RefinementCtx as RefinementCtx,\n} from \"../core/index.js\";\n")
	write("other.ts", "import { _gte as g } from './core/api';\nconst s = 'export { _gte as fake }';\n// export { _gte as commented }\n")
	write("node_modules/dep/index.ts", "export { _gte as gte } from './x';\n")

	specs, _ := JSExportSpecifiers(dir, "_gte")
	type key struct {
		file, exported, source string
		line                   int
	}
	got := map[key]bool{}
	for _, s := range specs {
		got[key{s.File, s.Exported, s.Source, s.Line}] = true
	}
	want := []key{
		{"core/api.ts", "_min", "", 4},
		{"mini/checks.ts", "gte", "../core/index.js", 3},
		{"mini/checks.ts", "minimum", "../core/index.js", 4},
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %+v in %+v", w, specs)
		}
	}
	if len(specs) != len(want) {
		t.Fatalf("specs = %+v, want exactly %d (imports, strings, comments and node_modules excluded)", specs, len(want))
	}
	if specs[1].Text != "_gte as gte," {
		t.Fatalf("text = %q", specs[1].Text)
	}
}

func TestJSExportIndexMatchesPerNameScan(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.ts"), []byte("export { f as g, h } from './b';\nexport { f };\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, _ := JSExportIndex(dir)
	one, _ := JSExportSpecifiers(dir, "f")
	if len(idx["f"]) != 2 || len(one) != 2 || idx["f"][0] != one[0] || idx["f"][1] != one[1] || len(idx["h"]) != 1 {
		t.Fatalf("index = %+v, scan = %+v", idx, one)
	}
}

// Comment/string-as-code audit (2026-10-10): a template literal holding an
// export clause is text, and a regex literal holding `/*` or a backtick
// does not hide a real clause after it.
func TestJSExportSpecifiersIgnoreLiterals(t *testing.T) {
	src := "const doc = `\nexport { a as fromTemplate }\n`;\n" +
		"const re = /[/*]/;\nconst tick = /`/;\n" +
		"export { a as real } from './m';\n"
	got := jsExportSpecifiersIn("x.ts", []byte(src), "a")
	if len(got) != 1 || got[0].Exported != "real" || got[0].Source != "./m" || got[0].Line != 6 {
		t.Fatalf("specifiers = %+v, want only a as real from ./m on line 6", got)
	}
}
