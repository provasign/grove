package parser

import (
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
