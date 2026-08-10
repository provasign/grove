package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestChangeImpactGoInterfaceSignatureFiltersSameNamedDecoy(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "contract.go::Scheduler@1", FilePath: "contract.go", Language: "go", Kind: core.KindInterface,
			Name: "Scheduler", QualifiedName: "Scheduler", RawText: "type Scheduler interface {\n\tDispatch(Job) error\n}"},
		{ID: "fast.go::FastScheduler@1", FilePath: "fast.go", Language: "go", Kind: core.KindStruct,
			Name: "FastScheduler", QualifiedName: "FastScheduler", RawText: "type FastScheduler struct{}"},
		{ID: "fast.go::FastScheduler.Dispatch@1", FilePath: "fast.go", Language: "go", Kind: core.KindMethod,
			Name: "Dispatch", QualifiedName: "FastScheduler.Dispatch", ParentSymbol: "FastScheduler",
			Signature: "func (*FastScheduler) Dispatch(Job) error", RawText: "func (*FastScheduler) Dispatch(Job) error { return nil }"},
		{ID: "safe.go::SafeScheduler@1", FilePath: "safe.go", Language: "go", Kind: core.KindStruct,
			Name: "SafeScheduler", QualifiedName: "SafeScheduler", RawText: "type SafeScheduler struct{}"},
		{ID: "safe.go::SafeScheduler.Dispatch@1", FilePath: "safe.go", Language: "go", Kind: core.KindMethod,
			Name: "Dispatch", QualifiedName: "SafeScheduler.Dispatch", ParentSymbol: "SafeScheduler",
			Signature: "func (*SafeScheduler) Dispatch(Job) error", RawText: "func (*SafeScheduler) Dispatch(Job) error { return nil }"},
		{ID: "logger.go::Logger@1", FilePath: "logger.go", Language: "go", Kind: core.KindStruct,
			Name: "Logger", QualifiedName: "Logger", RawText: "type Logger struct{}"},
		{ID: "logger.go::Logger.Dispatch@1", FilePath: "logger.go", Language: "go", Kind: core.KindMethod,
			Name: "Dispatch", QualifiedName: "Logger.Dispatch", ParentSymbol: "Logger",
			Signature: "func (*Logger) Dispatch(message string) error", RawText: "func (*Logger) Dispatch(message string) error { return nil }"},
	}, 4)

	for _, query := range []string{"Scheduler.Dispatch", "Scheduler.Dispatch(Job)"} {
		t.Run(query, func(t *testing.T) {
			r, err := g.ChangeImpact(query)
			if err != nil {
				t.Fatalf("ChangeImpact: %v", err)
			}
			got := map[string]bool{}
			for _, member := range r.Family {
				got[member.QualifiedName] = true
			}
			if !got["FastScheduler.Dispatch"] || !got["SafeScheduler.Dispatch"] {
				t.Fatalf("family = %v, want both scheduler implementations", got)
			}
			if got["Logger.Dispatch"] {
				t.Fatalf("incompatible Logger.Dispatch joined family: %v", got)
			}
			if r.Completeness != "closed" {
				t.Fatalf("completeness = %q, want closed", r.Completeness)
			}
		})
	}
}

func TestChangeImpactTypeScriptPreciseInterfaceSignature(t *testing.T) {
	g := New()
	g.Replace(typeScriptEncoderSymbols(false), 5)
	r, err := g.ChangeImpact("Encoder.encode(Uint8Array)")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	got := map[string]bool{}
	for _, member := range r.Family {
		got[member.QualifiedName] = true
	}
	if !got["HexEncoder.encode"] || !got["Base64Encoder.encode"] || len(got) != 2 {
		t.Fatalf("family = %v, want only HexEncoder.encode and Base64Encoder.encode", got)
	}
}

func TestRenamePlanTypeScriptInterfaceCallerIsConfirmed(t *testing.T) {
	g := New()
	g.Replace(typeScriptEncoderSymbols(true), 6)
	r, err := g.RenamePlan("Encoder.encode", "encodeV2")
	if err != nil {
		t.Fatalf("RenamePlan: %v", err)
	}
	for _, ambiguous := range r.Ambiguous {
		if ambiguous.FilePath == "use-interface.ts" {
			t.Fatalf("type-resolved interface caller marked ambiguous: %+v", ambiguous)
		}
	}
	for _, edit := range r.Edits {
		if edit.FilePath == "use-interface.ts" && edit.Line == 2 {
			return
		}
	}
	t.Fatalf("confirmed caller edit missing: edits=%+v ambiguous=%+v", r.Edits, r.Ambiguous)
}

// A case-fold twin (exported WithDbSession + unexported withDbSession with a
// DIFFERENT arity — grafana's real shape) must not fail signature-aware
// satisfaction: the single-entry lowercase method map compared the interface's
// anchor against whichever twin landed last, failed the WHOLE interface, and
// severed every dispatch edge through it (grafana-bigblast recall 1.00→0.02).
func TestInterfaceSatisfactionSurvivesCaseFoldTwin(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "db.go::DB@1", FilePath: "db.go", Language: "go", Kind: core.KindInterface,
			Name: "DB", QualifiedName: "DB",
			RawText: "type DB interface {\n\tWithDbSession(ctx context.Context, callback DBTransactionFunc) error\n}"},
		{ID: "store.go::Store@1", FilePath: "store.go", Language: "go", Kind: core.KindStruct,
			Name: "Store", QualifiedName: "Store", RawText: "type Store struct{}"},
		{ID: "store.go::Store.WithDbSession@1", FilePath: "store.go", Language: "go", Kind: core.KindMethod,
			Name: "WithDbSession", QualifiedName: "Store.WithDbSession", ParentSymbol: "Store",
			Signature: "func (ss *Store) WithDbSession(ctx context.Context, callback DBTransactionFunc) error"},
		{ID: "store.go::Store.withDbSession@1", FilePath: "store.go", Language: "go", Kind: core.KindMethod,
			Name: "withDbSession", QualifiedName: "Store.withDbSession", ParentSymbol: "Store",
			Signature: "func (ss *Store) withDbSession(ctx context.Context, engine *Engine, callback DBTransactionFunc) error"},
	}, 2)

	if !hasEdge(g, core.EdgeImplements, "store.go::Store@1", "db.go::DB@1") {
		t.Fatalf("Store must satisfy DB: the exported twin matches the interface signature")
	}
	if !hasEdge(g, core.EdgeOverrides, "store.go::Store.WithDbSession@1", "db.go::DB@1") {
		t.Fatalf("overrides edge must come from the EXPORTED twin, not the case-fold collision")
	}
	if hasEdge(g, core.EdgeOverrides, "store.go::Store.withDbSession@1", "db.go::DB@1") {
		t.Fatalf("unexported 3-arg twin must not carry the overrides edge")
	}
}

func TestGoParamTokens(t *testing.T) {
	cases := []struct {
		inner string
		want  []string
	}{
		{"ctx context.Context, callback DBTransactionFunc", []string{"Context", "DBTransactionFunc"}},
		{"a, b string", []string{"string", "string"}},
		{"ctx context.Context, fn func(ctx context.Context) error", []string{"Context", "func"}},
		{"context.Context, DBTransactionFunc", []string{"Context", "DBTransactionFunc"}}, // unnamed interface spec
		{"args ...interface{}", []string{"interface{}"}}, // variadic strips; token is stable on both sides
	}
	for _, tc := range cases {
		got := goParamTokens(tc.inner)
		if len(got) != len(tc.want) {
			t.Errorf("goParamTokens(%q) = %v, want %v", tc.inner, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("goParamTokens(%q) = %v, want %v", tc.inner, got, tc.want)
				break
			}
		}
	}
}

func typeScriptEncoderSymbols(withCaller bool) []core.SymbolRecord {
	symbols := []core.SymbolRecord{
		{ID: "contract.ts::Encoder@1", FilePath: "contract.ts", Language: "typescript", Kind: core.KindInterface,
			Name: "Encoder", QualifiedName: "Encoder", Signature: "export interface Encoder",
			RawText: "export interface Encoder {\n  encode(value: Uint8Array): string;\n}", Span: core.LineRange{Start: 1, End: 3}},
		{ID: "hex.ts::HexEncoder@1", FilePath: "hex.ts", Language: "typescript", Kind: core.KindClass,
			Name: "HexEncoder", QualifiedName: "HexEncoder", Signature: "export class HexEncoder implements Encoder"},
		{ID: "hex.ts::HexEncoder.encode@1", FilePath: "hex.ts", Language: "typescript", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "HexEncoder.encode", ParentSymbol: "HexEncoder",
			Signature: "encode(value: Uint8Array): string", RawText: "encode(value: Uint8Array): string { return value[0].toString(16); }",
			Span: core.LineRange{Start: 3, End: 3}},
		{ID: "base64.ts::Base64Encoder@1", FilePath: "base64.ts", Language: "typescript", Kind: core.KindClass,
			Name: "Base64Encoder", QualifiedName: "Base64Encoder", Signature: "export class Base64Encoder implements Encoder"},
		{ID: "base64.ts::Base64Encoder.encode@1", FilePath: "base64.ts", Language: "typescript", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Base64Encoder.encode", ParentSymbol: "Base64Encoder",
			Signature: "encode(value: Uint8Array): string", RawText: "encode(value: Uint8Array): string { return value[0].toString(36); }",
			Span: core.LineRange{Start: 3, End: 3}},
		{ID: "logger.ts::Logger@1", FilePath: "logger.ts", Language: "typescript", Kind: core.KindClass,
			Name: "Logger", QualifiedName: "Logger", Signature: "export class Logger"},
		{ID: "logger.ts::Logger.encode@1", FilePath: "logger.ts", Language: "typescript", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Logger.encode", ParentSymbol: "Logger",
			Signature: "encode(message: string): string", RawText: "encode(message: string): string { return message.trim(); }",
			Span: core.LineRange{Start: 2, End: 2}},
	}
	if withCaller {
		symbols = append(symbols, core.SymbolRecord{
			ID: "use-interface.ts::useInterface@1", FilePath: "use-interface.ts", Language: "typescript", Kind: core.KindFunction,
			Name: "useInterface", QualifiedName: "useInterface", Signature: "function useInterface(encoder: Encoder, value: Uint8Array): string",
			RawText: "function useInterface(encoder: Encoder, value: Uint8Array): string {\n  return encoder.encode(value);\n}",
			Span:    core.LineRange{Start: 1, End: 3}, Imports: []string{"Encoder"},
			CallSites: []core.CallSite{{Callee: "encoder.encode", Line: 2, Argc: 1, Args: []string{"value"}}},
		})
	}
	return symbols
}
