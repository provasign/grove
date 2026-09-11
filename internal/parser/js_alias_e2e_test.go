package parser

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestJSImportAliasesResolveCalls(t *testing.T) {
	files := map[string]string{
		"target.ts":   "export function actual(value: number): number { return value }\nexport class Service { run(): number { return 1 } }\n",
		"default.ts":  "export default\nfunction actualDefault(value: number): number { return value }\n",
		"consumer.ts": "import { actual as alias, type Service as S } from './target';\nimport * as api from './target';\nimport localDefault from './default';\nexport function direct(): number { return alias(1) }\nexport function namespaced(): number { return api.actual(2) }\nexport function typed(value: S): number { return value.run() }\nexport function defaults(): number { return localDefault(3) }\n",
	}
	var symbols []core.SymbolRecord
	for file, src := range files {
		syms, ok, _ := extractSymbolsFromAST("typescript", file, "sha", []byte(src), extractImports("typescript", src))
		if !ok {
			t.Fatalf("parse %s", file)
		}
		symbols = append(symbols, syms...)
	}
	byID := map[string]string{}
	for _, s := range symbols {
		byID[s.ID] = s.FilePath + ":" + s.QualifiedName
	}
	got := map[string]bool{}
	for _, edge := range graph.BuildEdges(symbols) {
		if edge.Type == core.EdgeCalls {
			got[byID[edge.From]+" -> "+byID[edge.To]] = true
		}
	}
	for _, want := range []string{
		"consumer.ts:direct -> target.ts:actual",
		"consumer.ts:namespaced -> target.ts:actual",
		"consumer.ts:typed -> target.ts:Service.run",
		"consumer.ts:defaults -> default.ts:actualDefault",
	} {
		if !got[want] {
			t.Errorf("missing alias edge %s; calls=%v", want, got)
		}
	}
}

func TestJSBarrelAndJSXEdgesEndToEnd(t *testing.T) {
	files := map[string]string{
		"src/button.tsx":   "export function Button() { return <button /> }\n",
		"src/util.ts":      "export function helper() { return 1 }\n",
		"src/index.ts":     "export * from './button';\nexport * from './util';\n",
		"src/named.ts":     "export { helper } from './util';\n",
		"src/app.tsx":      "import { Button } from './index';\nexport function App() { return <Button /> }\n",
		"src/star.ts":      "import { helper } from './index';\nexport function viaStar() { return helper() }\n",
		"src/named_use.ts": "import { helper } from './named';\nexport function viaNamed() { return helper() }\n",
		"src/private.ts":   "export function hidden() { return 1 }\n",
		"src/wrapper.ts":   "import { hidden } from './private';\nexport function wrapper() { return hidden() }\n",
		"src/no_leak.ts":   "import { wrapper } from './wrapper';\nexport function noLeak() { return hidden() }\n",
	}
	var symbols []core.SymbolRecord
	for file, src := range files {
		syms, ok, _ := extractSymbolsFromAST(languageForPath(file), file, "sha", []byte(src), extractImports(languageForPath(file), src))
		if !ok {
			t.Fatalf("parse %s", file)
		}
		symbols = append(symbols, syms...)
	}
	byID := map[string]string{}
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol.FilePath + ":" + symbol.QualifiedName
	}
	wantImport := false
	wantJSXCall := false
	wantStarCall := false
	wantNamedCall := false
	leakedPrivateImport := false
	for _, edge := range graph.BuildEdges(symbols) {
		if edge.Type == core.EdgeImports && edge.From == "file:src/index.ts" && edge.To == "import:./button" {
			wantImport = true
		}
		if edge.Type == core.EdgeCalls && byID[edge.From] == "src/app.tsx:App" && byID[edge.To] == "src/button.tsx:Button" {
			wantJSXCall = true
		}
		if edge.Type == core.EdgeCalls && byID[edge.From] == "src/star.ts:viaStar" && byID[edge.To] == "src/util.ts:helper" {
			wantStarCall = true
		}
		if edge.Type == core.EdgeCalls && byID[edge.From] == "src/named_use.ts:viaNamed" && byID[edge.To] == "src/util.ts:helper" {
			wantNamedCall = true
		}
		if edge.Type == core.EdgeCalls && byID[edge.From] == "src/no_leak.ts:noLeak" && byID[edge.To] == "src/private.ts:hidden" {
			leakedPrivateImport = true
		}
	}
	if !wantImport {
		t.Fatal("pure JS/TS barrel did not retain its re-export import edge")
	}
	if !wantJSXCall {
		t.Fatal("JSX component usage did not resolve through an export-star barrel")
	}
	if !wantStarCall || !wantNamedCall {
		t.Fatalf("plain calls through barrels missing: star=%v named=%v", wantStarCall, wantNamedCall)
	}
	if leakedPrivateImport {
		t.Fatal("ordinary implementation import was followed as though it were a re-export")
	}
}

func languageForPath(path string) string {
	if strings.HasSuffix(path, ".tsx") {
		return "tsx"
	}
	return "typescript"
}
