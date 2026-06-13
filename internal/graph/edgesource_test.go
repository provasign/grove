// Every graph-built edge must carry a meaningful evidence Source. An empty
// Source is normalized to "unknown" by mergeEdges, which leaves consumers
// (notably the shale semantic-review signals in RFC #5) unable to explain why
// an edge exists. This guard fails if any BuildEdges output is untagged.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestBuildEdges_EverySourceTagged(t *testing.T) {
	symbols := []core.SymbolRecord{
		// calls (AST), defines, contains
		{ID: "svc.go::Service.Run@sha", FilePath: "svc.go", BlobSHA: "sha",
			Language: "go", Kind: core.KindMethod, Name: "Run", QualifiedName: "Service.Run", ParentSymbol: "Service",
			RawText:   "func (s *Service) Run() { s.Helper() }",
			CallSites: []core.CallSite{{Callee: "s.Helper", Line: 1}}},
		{ID: "svc.go::Service.Helper@sha", FilePath: "svc.go", BlobSHA: "sha",
			Language: "go", Kind: core.KindMethod, Name: "Helper", QualifiedName: "Service.Helper", ParentSymbol: "Service"},
		{ID: "svc.go::Service@sha", FilePath: "svc.go", BlobSHA: "sha",
			Language: "go", Kind: core.KindStruct, Name: "Service", QualifiedName: "Service"},
		// tests edge
		{ID: "svc_test.go::TestRun@sha", FilePath: "svc_test.go", BlobSHA: "sha",
			Language: "go", Kind: core.KindFunction, Name: "TestRun", QualifiedName: "TestRun",
			RawText:   "func TestRun(t *testing.T) { (&Service{}).Run() }",
			CallSites: []core.CallSite{{Callee: "Run", Line: 1}}},
		// extends / uses-type (TS), imports
		{ID: "a.ts::Child@sha", FilePath: "a.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Child", QualifiedName: "Child",
			Signature: "class Child extends Base", Imports: []string{"./base"}},
		{ID: "base.ts::Base@sha", FilePath: "base.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "a.ts::use@sha", FilePath: "a.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindFunction, Name: "use", QualifiedName: "use",
			Signature: "function use(b: Base): void", Imports: []string{"./base"}},
	}

	edges := BuildEdges(symbols)
	if len(edges) == 0 {
		t.Fatal("fixture produced no edges")
	}
	seenTypes := map[core.EdgeType]bool{}
	for _, e := range edges {
		seenTypes[e.Type] = true
		if e.Source == "" || e.Source == core.EvidenceSourceUnknown {
			t.Errorf("edge %s %s→%s has unusable source %q", e.Type, e.From, e.To, e.Source)
		}
		if e.Reason == "" {
			t.Errorf("edge %s %s→%s has no resolver reason", e.Type, e.From, e.To)
		}
	}
	// Sanity: the fixture must actually exercise the main builders, otherwise
	// the guard could pass vacuously.
	for _, want := range []core.EdgeType{core.EdgeCalls, core.EdgeDefines, core.EdgeContains, core.EdgeTests} {
		if !seenTypes[want] {
			t.Errorf("fixture did not produce any %q edges — guard is not covering it", want)
		}
	}
}
