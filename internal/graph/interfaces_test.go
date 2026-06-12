// Tests for Go interface satisfaction (implicit, by method-set inclusion)
// and the dynamic-dispatch rescue of fan-out-capped call sites.
package graph

import (
	"fmt"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func ifaceFixture() (iface, typ, render, write, partial core.SymbolRecord) {
	iface = core.SymbolRecord{
		ID: "render/render.go::Render@1", FilePath: "render/render.go", BlobSHA: "1",
		Language: "go", Kind: core.KindInterface,
		Name: "Render", QualifiedName: "Render",
		RawText: "type Render interface {\n\t// Render writes data.\n\tRender(http.ResponseWriter) error\n\tWriteContentType(w http.ResponseWriter)\n}",
	}
	typ = core.SymbolRecord{
		ID: "render/json.go::JSON@1", FilePath: "render/json.go", BlobSHA: "1",
		Language: "go", Kind: core.KindStruct,
		Name: "JSON", QualifiedName: "JSON",
	}
	render = core.SymbolRecord{
		ID: "render/json.go::JSON.Render@10", FilePath: "render/json.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "JSON.Render", ParentSymbol: "JSON",
	}
	write = core.SymbolRecord{
		ID: "render/json.go::JSON.WriteContentType@20", FilePath: "render/json.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "WriteContentType", QualifiedName: "JSON.WriteContentType", ParentSymbol: "JSON",
	}
	partial = core.SymbolRecord{
		ID: "render/half.go::Half.Render@10", FilePath: "render/half.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "Half.Render", ParentSymbol: "Half",
	}
	return
}

func TestInterfaceSatisfaction_MethodSetInclusion(t *testing.T) {
	iface, typ, render, write, partial := ifaceFixture()
	edges := BuildEdges([]core.SymbolRecord{iface, typ, render, write, partial})

	var implements, overrides, partialEdges int
	for _, e := range edges {
		switch {
		case e.Type == core.EdgeImplements && e.From == typ.ID && e.To == iface.ID:
			implements++
		case e.Type == core.EdgeOverrides && e.To == iface.ID && (e.From == render.ID || e.From == write.ID):
			overrides++
		case e.From == partial.ID && (e.Type == core.EdgeOverrides || e.Type == core.EdgeImplements):
			partialEdges++
		}
	}
	if implements != 1 {
		t.Errorf("implements JSON→Render = %d, want 1", implements)
	}
	if overrides != 2 {
		t.Errorf("overrides edges to Render iface = %d, want 2 (Render + WriteContentType)", overrides)
	}
	if partialEdges != 0 {
		t.Errorf("Half (missing WriteContentType) must not satisfy Render; got %d edges", partialEdges)
	}
}

func TestBuildCalls_CappedFanoutRescuedAsDispatch(t *testing.T) {
	// More same-named cross-file methods than maxCalleeFanout: the plain
	// resolver drops them all. With an in-scope interface declaring the
	// method, dispatch edges must appear at reduced confidence.
	symbols := []core.SymbolRecord{{
		ID: "render/render.go::Render@1", FilePath: "render/render.go", BlobSHA: "1",
		Language: "go", Kind: core.KindInterface,
		Name: "Render", QualifiedName: "Render",
		RawText: "type Render interface {\n\tRender(http.ResponseWriter) error\n}",
	}, {
		ID: "context.go::Context.Render@1", FilePath: "context.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "Context.Render", ParentSymbol: "Context",
		Signature: "func (c *Context) Render(code int, r render.Render)",
		Imports:   []string{"render"},
		CallSites: []core.CallSite{{Callee: "r.Render", Line: 5}},
	}}
	for i := 0; i <= maxCalleeFanout+1; i++ {
		typeName := fmt.Sprintf("R%d", i)
		file := fmt.Sprintf("render/r%d.go", i)
		symbols = append(symbols, core.SymbolRecord{
			ID: file + "::" + typeName + ".Render@1", FilePath: file, BlobSHA: "1",
			Language: "go", Kind: core.KindMethod,
			Name: "Render", QualifiedName: typeName + ".Render", ParentSymbol: typeName,
		})
	}

	edges := BuildEdges(symbols)
	caller := "context.go::Context.Render@1"
	var dispatch int
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller {
			if e.Confidence > 0.75 {
				t.Errorf("dispatch edge confidence = %v, want reduced (≤0.75): %+v", e.Confidence, e)
			}
			dispatch++
		}
	}
	if dispatch != maxCalleeFanout+2 {
		t.Errorf("dispatch edges = %d, want %d (one per implementation)", dispatch, maxCalleeFanout+2)
	}
}

func TestBuildCalls_CappedFanoutWithoutInterfaceStaysDropped(t *testing.T) {
	// Same fan-out but no interface declares the method: stays dropped, no
	// noise edges.
	symbols := []core.SymbolRecord{{
		ID: "context.go::Context.Render@1", FilePath: "context.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "Context.Render", ParentSymbol: "Context",
		Signature: "func (c *Context) Render(code int, r render.Render)",
		Imports:   []string{"render"},
		CallSites: []core.CallSite{{Callee: "r.Render", Line: 5}},
	}}
	for i := 0; i <= maxCalleeFanout+1; i++ {
		typeName := fmt.Sprintf("R%d", i)
		file := fmt.Sprintf("render/r%d.go", i)
		symbols = append(symbols, core.SymbolRecord{
			ID: file + "::" + typeName + ".Render@1", FilePath: file, BlobSHA: "1",
			Language: "go", Kind: core.KindMethod,
			Name: "Render", QualifiedName: typeName + ".Render", ParentSymbol: typeName,
		})
	}
	for _, e := range BuildEdges(symbols) {
		if e.Type == core.EdgeCalls && e.From == "context.go::Context.Render@1" {
			t.Fatalf("capped fan-out without a declaring interface must stay dropped, got %+v", e)
		}
	}
}
