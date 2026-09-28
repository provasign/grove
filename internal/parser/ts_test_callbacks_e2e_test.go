package parser_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

// hono wide bed (2026-09-27): tests call app.basePath(...) inside
// describe/it callbacks. Those calls were dropped (the top-level walk
// stopped at every function-like node), and a typed receiver could not
// reach the inherited method: the public Hono (hono.ts) extends HonoBase,
// which hono-base.ts exports as an alias of its own class Hono.
func TestTSCallsInTestCallbacksReachInheritedAliasedMethod(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"src/hono-base.ts": "class Hono {\n  basePath(path: string): Hono {\n    return this\n  }\n}\n\nexport { Hono as HonoBase }\n",
		"src/hono.ts":      "import { HonoBase } from './hono-base'\n\nexport class Hono<E = {}> extends HonoBase {\n  constructor() {\n    super()\n  }\n}\n",
		"src/hono.test.ts": "import { Hono } from './hono'\n\ndescribe('basePath', () => {\n  it('works', () => {\n    const app = new Hono()\n    app.basePath('/api')\n  })\n})\n",
		// the constructed-receiver form, with type arguments
		"src/client.test.ts": "import { Hono } from './hono'\n\ndescribe('client', () => {\n  const base = new Hono<{}>().basePath('/v1')\n})\n",
	}
	var symbols []core.SymbolRecord
	for rel, src := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel := range files {
		syms, err := parser.NewEngine().ExtractFile(filepath.Join(root, filepath.FromSlash(rel)), root)
		if err != nil {
			t.Fatal(err)
		}
		symbols = append(symbols, syms...)
	}
	g := graph.New()
	g.Replace(symbols, len(files))
	result, err := g.ChangeImpactScoped("Hono.basePath", "src/hono-base.ts")
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, c := range result.Callers {
		found[c.FilePath] = true
	}
	if !found["src/hono.test.ts"] {
		t.Errorf("app.basePath inside a describe/it callback is not a caller: %+v", result.Callers)
	}
	if !found["src/client.test.ts"] {
		t.Errorf("new Hono<{}>().basePath inside a callback is not a caller: %+v", result.Callers)
	}
}

// h3 EventStream.push: four bodiless overload signatures sit above the
// implementation. They belong to the declaration (a rename touches them).
func TestTSOverloadSignaturesJoinTheDeclaration(t *testing.T) {
	root := t.TempDir()
	src := "export class EventStream {\n  /** doc */\n  async push(m: string): Promise<void>;\n  async push(m: string[]): Promise<void>;\n  async push(m: string | string[]) {\n    return\n  }\n}\n"
	path := filepath.Join(root, "event-stream.ts")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	syms, err := parser.NewEngine().ExtractFile(path, root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range syms {
		if s.QualifiedName == "EventStream.push" {
			if s.Span.Start != 3 || s.Span.End != 7 {
				t.Fatalf("push spans %d-%d, want 3-7 (overloads included)", s.Span.Start, s.Span.End)
			}
			if first := strings.SplitN(s.RawText, "\n", 2)[0]; !strings.Contains(first, "push(m: string)") {
				t.Fatalf("RawText not aligned with the widened span: %q", first)
			}
			return
		}
	}
	t.Fatal("EventStream.push not extracted")
}
