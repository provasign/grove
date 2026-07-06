package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// grafanaRouteFixture mirrors the wt-120119 shape that exposed two gaps:
// a Go interface whose member specs are not indexed symbols (RouteService),
// a package-private SIBLING interface declaring the same member and satisfied
// by the same implementation (routeService — reachable only by walking up
// from the closure, not from the seed), and a same-named decoy interface
// declaring different members (must be excluded).
func grafanaRouteFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "routingtree/legacy_storage.go::RouteService@1", FilePath: "routingtree/legacy_storage.go", BlobSHA: "1",
			Language: "go", Kind: core.KindInterface, Name: "RouteService", QualifiedName: "RouteService",
			RawText: "type RouteService interface {\n\tGetManagedRoute(ctx context.Context, orgID int64, name string) (ManagedRoute, error)\n\tDeleteManagedRoute(ctx context.Context, orgID int64, name string) error\n}"},
		{ID: "api/api_provisioning.go::routeService@1", FilePath: "api/api_provisioning.go", BlobSHA: "1",
			Language: "go", Kind: core.KindInterface, Name: "routeService", QualifiedName: "routeService",
			RawText: "type routeService interface {\n\tGetManagedRoute(ctx context.Context, orgID int64, name string) (ManagedRoute, error)\n}"},
		// Same-named decoy declaring DIFFERENT members (wt-120119's receiver_svc.go).
		{ID: "notifier/receiver_svc.go::routeService@1", FilePath: "notifier/receiver_svc.go", BlobSHA: "1",
			Language: "go", Kind: core.KindInterface, Name: "routeService", QualifiedName: "routeService",
			RawText: "type routeService interface {\n\tReceiverUseByName(ctx context.Context, name string) int\n}"},
		{ID: "routes/service.go::Service@1", FilePath: "routes/service.go", BlobSHA: "1",
			Language: "go", Kind: core.KindStruct, Name: "Service", QualifiedName: "Service",
			RawText: "type Service struct{}"},
		{ID: "routes/service.go::Service.GetManagedRoute@1", FilePath: "routes/service.go", BlobSHA: "1",
			Language: "go", Kind: core.KindMethod, Name: "GetManagedRoute", QualifiedName: "Service.GetManagedRoute",
			ParentSymbol: "Service", Signature: "func (s *Service) GetManagedRoute(ctx context.Context, orgID int64, name string) (ManagedRoute, error)"},
		{ID: "routes/service.go::Service.DeleteManagedRoute@1", FilePath: "routes/service.go", BlobSHA: "1",
			Language: "go", Kind: core.KindMethod, Name: "DeleteManagedRoute", QualifiedName: "Service.DeleteManagedRoute",
			ParentSymbol: "Service", Signature: "func (s *Service) DeleteManagedRoute(ctx context.Context, orgID int64, name string) error"},
		// Caller through the sibling contract.
		{ID: "api/api_provisioning.go::API.RouteGetPolicyTree@1", FilePath: "api/api_provisioning.go", BlobSHA: "1",
			Language: "go", Kind: core.KindMethod, Name: "RouteGetPolicyTree", QualifiedName: "API.RouteGetPolicyTree",
			ParentSymbol: "API", Signature: "func (a *API) RouteGetPolicyTree(ctx context.Context) error",
			CallSites: []core.CallSite{{Callee: "a.svc.GetManagedRoute", Line: 3, Argc: 3}}},
		{ID: "api/api_provisioning.go::API@1", FilePath: "api/api_provisioning.go", BlobSHA: "1",
			Language: "go", Kind: core.KindStruct, Name: "API", QualifiedName: "API",
			RawText: "type API struct {\n\tsvc routeService\n}"},
	}, 3)
	return g
}

func TestChangeImpact_DeclaringTypesForGoInterface(t *testing.T) {
	g := grafanaRouteFixture()
	r, err := g.ChangeImpact("RouteService.GetManagedRoute")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}

	types := map[string]string{} // name -> file
	for _, s := range r.DeclaringTypes {
		types[s.Name] = s.FilePath
	}
	// The seed interface: its member spec is not an indexed symbol, so the
	// TYPE is the change site a diff names.
	if types["RouteService"] != "routingtree/legacy_storage.go" {
		t.Errorf("DeclaringTypes missing seed interface RouteService: %v", types)
	}
	// The sibling contract: satisfied by the same impl, reachable only by
	// walking up from the closure. Both its declaring type and its member
	// declaration must be surfaced.
	if types["routeService"] != "api/api_provisioning.go" {
		t.Errorf("DeclaringTypes missing sibling contract routeService: %v", types)
	}
	// The same-named decoy declares different members — excluded.
	for _, s := range r.DeclaringTypes {
		if s.FilePath == "notifier/receiver_svc.go" {
			t.Errorf("decoy routeService (different members) wrongly included: %+v", s)
		}
	}

	superFiles := map[string]bool{}
	for _, s := range r.Supers {
		superFiles[s.FilePath+":"+s.Name] = true
	}
	if !superFiles["api/api_provisioning.go:GetManagedRoute"] {
		t.Errorf("Supers missing sibling contract member declaration: %v", superFiles)
	}
}

func TestChangeImpact_DeclaringTypesEmptyForJava(t *testing.T) {
	// Java member declarations are real symbols: the method site suffices and
	// DeclaringTypes must stay empty (jackson output unchanged).
	g := mapIteratorFixture()
	r, err := g.ChangeImpact("MapIterator.next")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.DeclaringTypes) != 0 {
		t.Fatalf("DeclaringTypes = %v, want empty for Java", r.DeclaringTypes)
	}
}
