package graph

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// TestBuildCalls_ResolvedInterfaceDoesNotFanOut locks the fix for the grafana
// 45-Get fanout: a call whose receiver resolves through a struct field to a
// multi-method interface must dispatch ONLY to that interface's implementors,
// not to every same-named method via the blanket dispatch rescue — even when the
// same-name set trips the fan-out cap. Satisfaction is name-set based, so the
// interface has several methods that the decoys (Get only) do not implement,
// mirroring SecretsKVStore (Get/Set/Del…) vs the repo's 42 client Gets.
func TestBuildCalls_ResolvedInterfaceDoesNotFanOut(t *testing.T) {
	method := func(id, file, parent, name string) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: name, QualifiedName: parent + "." + name, ParentSymbol: parent}
	}
	syms := []core.SymbolRecord{
		{ID: "store/store.go::Store@1", FilePath: "store/store.go", BlobSHA: "1",
			Language: "go", Kind: core.KindInterface, Name: "Store", QualifiedName: "Store",
			RawText: "type Store interface {\n\tGet(k string) string\n\tSet(k, v string)\n\tDel(k string)\n}"},
		// Two full implementors (Get+Set+Del) — the only correct Get targets.
		method("store/sql.go::SQLStore.Get@1", "store/sql.go", "SQLStore", "Get"),
		method("store/sql.go::SQLStore.Set@1", "store/sql.go", "SQLStore", "Set"),
		method("store/sql.go::SQLStore.Del@1", "store/sql.go", "SQLStore", "Del"),
		method("store/cache.go::CacheStore.Get@1", "store/cache.go", "CacheStore", "Get"),
		method("store/cache.go::CacheStore.Set@1", "store/cache.go", "CacheStore", "Set"),
		method("store/cache.go::CacheStore.Del@1", "store/cache.go", "CacheStore", "Del"),
		// Caller: receiver struct field `store Store`; calls s.store.Get.
		{ID: "svc.go::Svc@1", FilePath: "svc.go", BlobSHA: "1", Language: "go", Kind: core.KindStruct,
			Name: "Svc", QualifiedName: "Svc", RawText: "type Svc struct {\n\tstore Store\n}"},
		{ID: "svc.go::Svc.Use@1", FilePath: "svc.go", BlobSHA: "1", Language: "go", Kind: core.KindMethod,
			Name: "Use", QualifiedName: "Svc.Use", ParentSymbol: "Svc", Signature: "func (s *Svc) Use() string",
			Imports: []string{"store"}, CallSites: []core.CallSite{{Callee: "s.store.Get", Line: 2}}},
	}
	// 17 decoys that have only Get (satisfy their own one-method client interface,
	// so they enter dispatchTargets("Get"), but NOT the multi-method Store).
	for i := 0; i < 17; i++ {
		syms = append(syms,
			core.SymbolRecord{ID: fmt.Sprintf("d%d/i.go::Client%d@1", i, i), FilePath: fmt.Sprintf("d%d/i.go", i), BlobSHA: "1",
				Language: "go", Kind: core.KindInterface, Name: fmt.Sprintf("Client%d", i), QualifiedName: fmt.Sprintf("Client%d", i),
				RawText: fmt.Sprintf("type Client%d interface {\n\tGet(k string) string\n}", i)},
			method(fmt.Sprintf("d%d/impl.go::Decoy%d.Get@1", i, i), fmt.Sprintf("d%d/impl.go", i), fmt.Sprintf("Decoy%d", i), "Get"),
		)
	}

	caller := "svc.go::Svc.Use@1"
	callees := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.From == caller {
			callees[e.To] = true
		}
	}
	if !callees["store/sql.go::SQLStore.Get@1"] || !callees["store/cache.go::CacheStore.Get@1"] {
		t.Fatalf("must dispatch to Store's implementors, got %v", callees)
	}
	for to := range callees {
		if to != "store/sql.go::SQLStore.Get@1" && to != "store/cache.go::CacheStore.Get@1" {
			t.Errorf("unexpected fan-out edge to %s (should be suppressed)", to)
		}
	}
}

func TestCSharpDispatchExpansionFiltersEachImplementorByArity(t *testing.T) {
	method := func(id, parent, signature string) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: "all.cs", BlobSHA: "1", Language: "csharp",
			Kind: core.KindMethod, Name: "WriteValue", QualifiedName: parent + ".WriteValue",
			ParentSymbol: parent, Signature: signature}
	}
	syms := []core.SymbolRecord{
		{ID: "all.cs::Writer", FilePath: "all.cs", Language: "csharp", Kind: core.KindInterface, Name: "Writer", QualifiedName: "Writer", RawText: "interface Writer { void WriteValue(int value); }"},
		method("all.cs::Writer.WriteValue", "Writer", "void WriteValue(int value)"),
		{ID: "all.cs::Use", FilePath: "all.cs", Language: "csharp", Kind: core.KindClass, Name: "Use", QualifiedName: "Use"},
		{ID: "all.cs::Use.Run", FilePath: "all.cs", Language: "csharp", Kind: core.KindMethod, Name: "Run", QualifiedName: "Use.Run", ParentSymbol: "Use", Signature: "void Run(Writer w)", RawText: "void Run(Writer w) { w.WriteValue(1); }", CallSites: []core.CallSite{{Callee: "w.WriteValue", Line: 1, Argc: 1}}},
	}
	for i := 0; i < 5; i++ {
		parent := fmt.Sprintf("Writer%d", i)
		syms = append(syms, core.SymbolRecord{ID: "all.cs::" + parent, FilePath: "all.cs", Language: "csharp", Kind: core.KindClass, Name: parent, QualifiedName: parent, RawText: "class " + parent + " : Writer {}"})
		for argc := 0; argc < 4; argc++ {
			params := []string{"", "int a", "int a, int b", "int a, int b, int c"}[argc]
			syms = append(syms, method(fmt.Sprintf("all.cs::%s.WriteValue%d", parent, argc), parent, "void WriteValue("+params+")"))
		}
	}

	callees := map[string]bool{}
	for _, edge := range BuildEdges(syms) {
		if edge.Type == core.EdgeCalls && edge.From == "all.cs::Use.Run" {
			callees[edge.To] = true
		}
	}
	if len(callees) != 6 {
		t.Fatalf("one-argument dispatch targets = %d, want base + five implementors: %v", len(callees), callees)
	}
	for target := range callees {
		if strings.HasSuffix(target, "WriteValue0") || strings.HasSuffix(target, "WriteValue2") || strings.HasSuffix(target, "WriteValue3") {
			t.Errorf("one-argument call dispatched to wrong overload %s", target)
		}
	}
}

func TestCSharpBroadUnknownConstructorOverloadsStayCapped(t *testing.T) {
	syms := []core.SymbolRecord{{
		ID: "use.cs::Use.Make@1", FilePath: "use.cs", Language: "csharp", Kind: core.KindMethod,
		Name: "Make", QualifiedName: "Use.Make", ParentSymbol: "Use",
		RawText:   "void Make() { var x = new Value(factory.Create()); }",
		CallSites: []core.CallSite{{Callee: "Value", Line: 1, Argc: 1, Args: []string{""}}},
	}}
	for i := 0; i < 15; i++ {
		syms = append(syms, core.SymbolRecord{
			ID: fmt.Sprintf("value.cs::Value.Value@%d", i), FilePath: "value.cs", Language: "csharp",
			Kind: core.KindConstructor, Name: "Value", QualifiedName: "Value.Value", ParentSymbol: "Value",
			Signature: fmt.Sprintf("Value(Type%d value)", i),
		})
	}
	for i := 0; i < 2; i++ {
		syms = append(syms, core.SymbolRecord{
			ID: fmt.Sprintf("other.cs::Other.Value@%d", i), FilePath: "other.cs", Language: "csharp",
			Kind: core.KindMethod, Name: "Value", QualifiedName: "Other.Value", ParentSymbol: "Other",
			Signature: "void Value(int first, int second)",
		})
	}
	for _, edge := range BuildEdges(syms) {
		if edge.Type == core.EdgeCalls && edge.From == syms[0].ID {
			t.Fatalf("unresolved broad constructor overload set emitted %s", edge.To)
		}
	}
}
