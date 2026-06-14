package graph

import (
	"fmt"
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
