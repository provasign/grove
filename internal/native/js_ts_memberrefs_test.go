package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// hono Router.match (bench29, 2026-10-16): change_impact said "closed" and
// missed Router<T>['match'], `this.match = match`, a member's own
// `this.match = ...` and an object literal's `match:`. The checker resolved
// every one, but its member edges carried no positions (so no edit site
// listed them) and a member's reference to itself was dropped. A direct
// recursive call is an edit site too (zod $ZodRegistry.get's this.get(p)).
func TestJsTSMemberRefsCarryPositions(t *testing.T) {
	root := t.TempDir()
	linkTypeScriptForTest(t, root)
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("tsconfig.json", `{"compilerOptions":{"strict":true},"include":["**/*.ts"]}`)
	write("main.ts", `export interface Router {
  match(p: string): number
}
export function match<R extends Router>(this: R, p: string): number {
  const m = ((q: string) => q.length) as Router['match']
  this.match = m
  return m(p)
}
export class Smart implements Router {
  match(p: string): number {
    this.match = (q: string) => q.length
    return p === "" ? 0 : this.match(p.slice(1))
  }
}
export const literal: Router = {
  match: (p: string) => p.length,
}
`)
	sym := func(name, qual, parent string, kind core.SymbolKind, start, end int) core.SymbolRecord {
		return core.SymbolRecord{ID: "main.ts::" + qual + "@b", FilePath: "main.ts", BlobSHA: "b", Language: "typescript",
			Kind: kind, Name: name, QualifiedName: qual, ParentSymbol: parent, Span: core.LineRange{Start: start, End: end}}
	}
	parsed := []core.SymbolRecord{
		sym("Router", "Router", "", core.KindInterface, 1, 3),
		sym("match", "Router.match", "Router", core.KindMethod, 2, 2),
		sym("match", "match", "", core.KindFunction, 4, 8),
		sym("Smart", "Smart", "", core.KindClass, 9, 14),
		sym("match", "Smart.match", "Smart", core.KindMethod, 10, 13),
		sym("<top-level>", "<top-level>", "", core.KindFunction, 1, 17),
	}
	res := (jsTSAnalyzer{}).Analyze(context.Background(), Request{Root: root, Files: []string{"main.ts"}, Symbols: parsed})
	got := map[string]string{}
	for _, e := range res.Edges {
		if e.Type == core.EdgeReads || e.Type == core.EdgeWrites || e.Type == core.EdgeCalls {
			got[string(e.Type)+" "+e.From+" -> "+e.To] = e.Lines
		}
	}
	want := map[string]string{
		"reads main.ts::match@b -> main.ts::Router.match@b":        "5:50",
		"writes main.ts::match@b -> main.ts::Router.match@b":       "6:8",
		"writes main.ts::Smart.match@b -> main.ts::Smart.match@b":  "11:10",
		"calls main.ts::Smart.match@b -> main.ts::Smart.match@b":   "12:32",
		"writes main.ts::<top-level>@b -> main.ts::Router.match@b": "16:3",
	}
	for k, lines := range want {
		if got[k] != lines {
			t.Errorf("%s: lines %q, want %q", k, got[k], lines)
		}
	}
	if t.Failed() {
		t.Logf("edges: %v\ndiagnostics: %v", got, res.Diagnostics)
	}
}
