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

func TestJavaOverrideEdgesFollowResolvedInheritance(t *testing.T) {
	iface := core.SymbolRecord{ID: "I.java::I", FilePath: "I.java", Language: "java", Kind: core.KindInterface, Name: "I", QualifiedName: "I", Signature: "interface I"}
	ifaceRun := core.SymbolRecord{ID: "I.java::I.run", FilePath: "I.java", Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "I.run", ParentSymbol: "I", Signature: "void run(int value)"}
	base := core.SymbolRecord{ID: "Base.java::Base", FilePath: "Base.java", Language: "java", Kind: core.KindClass, Name: "Base", QualifiedName: "Base", Signature: "class Base implements I"}
	baseRun := core.SymbolRecord{ID: "Base.java::Base.run", FilePath: "Base.java", Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Base.run", ParentSymbol: "Base", Signature: "public void run(int value)"}
	child := core.SymbolRecord{ID: "Child.java::Child", FilePath: "Child.java", Language: "java", Kind: core.KindClass, Name: "Child", QualifiedName: "Child", Signature: "class Child extends Base"}
	childRun := core.SymbolRecord{ID: "Child.java::Child.run", FilePath: "Child.java", Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Child.run", ParentSymbol: "Child", Signature: "public void run(int value)"}

	edges := BuildEdges([]core.SymbolRecord{iface, ifaceRun, base, baseRun, child, childRun})
	var baseToIface, childToBase, childToIface bool
	for _, edge := range edges {
		if edge.Type != core.EdgeOverrides {
			continue
		}
		baseToIface = baseToIface || edge.From == baseRun.ID && edge.To == iface.ID
		childToBase = childToBase || edge.From == childRun.ID && edge.To == base.ID
		childToIface = childToIface || edge.From == childRun.ID && edge.To == iface.ID
	}
	if !baseToIface || !childToBase || !childToIface {
		t.Fatalf("missing Java override closure: base→iface=%v child→base=%v child→iface=%v edges=%+v", baseToIface, childToBase, childToIface, edges)
	}
}

func TestInterfaceSatisfaction_DoesNotRequireDirectImport(t *testing.T) {
	iface := core.SymbolRecord{
		ID: "contracts/runner.go::Runner@1", FilePath: "contracts/runner.go", BlobSHA: "1",
		Language: "go", Kind: core.KindInterface, Name: "Runner", QualifiedName: "Runner",
		RawText: "type Runner interface { Run() }",
	}
	typ := core.SymbolRecord{
		ID: "workers/task.go::Task@1", FilePath: "workers/task.go", BlobSHA: "1",
		Language: "go", Kind: core.KindStruct, Name: "Task", QualifiedName: "Task",
	}
	method := core.SymbolRecord{
		ID: "workers/task.go::Task.Run@5", FilePath: "workers/task.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod, Name: "Run", QualifiedName: "Task.Run", ParentSymbol: "Task",
	}
	edges := BuildEdges([]core.SymbolRecord{iface, typ, method})
	found := false
	for _, edge := range edges {
		found = found || edge.From == typ.ID && edge.To == iface.ID && edge.Type == core.EdgeImplements
	}
	if !found {
		t.Fatal("structural Go satisfaction incorrectly required a direct import")
	}
}

func TestCppInterfaceSatisfactionIsNominal(t *testing.T) {
	iface := core.SymbolRecord{ID: "Closer", FilePath: "close.hpp", Language: "cpp", Kind: core.KindInterface, Name: "Closer"}
	typ := core.SymbolRecord{ID: "Accidental", FilePath: "other.hpp", Language: "cpp", Kind: core.KindClass, Name: "Accidental"}
	method := core.SymbolRecord{ID: "Accidental.close", FilePath: "other.hpp", Language: "cpp", Kind: core.KindMethod, Name: "close", ParentSymbol: "Accidental"}
	ifaceMethod := core.SymbolRecord{ID: "Closer.close", FilePath: "close.hpp", Language: "cpp", Kind: core.KindMethod, Name: "close", ParentSymbol: "Closer"}
	for _, edge := range BuildEdges([]core.SymbolRecord{iface, typ, method, ifaceMethod}) {
		if edge.Type == core.EdgeImplements || edge.Type == core.EdgeOverrides {
			t.Fatalf("unrelated C++ method synthesized structural interface edge: %+v", edge)
		}
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

func TestPythonProtocolUsesStructuralSatisfaction(t *testing.T) {
	protocol := core.SymbolRecord{ID: "proto.py::Reader", FilePath: "proto.py", Language: "python", Kind: core.KindInterface, Name: "Reader", QualifiedName: "Reader", Signature: "class Reader(Protocol):"}
	contract := core.SymbolRecord{ID: "proto.py::Reader.read", FilePath: "proto.py", Language: "python", Kind: core.KindMethod, Name: "read", QualifiedName: "Reader.read", ParentSymbol: "Reader", Signature: "def read(self) -> str:"}
	impl := core.SymbolRecord{ID: "file.py::FileReader", FilePath: "file.py", Language: "python", Kind: core.KindClass, Name: "FileReader", QualifiedName: "FileReader"}
	method := core.SymbolRecord{ID: "file.py::FileReader.read", FilePath: "file.py", Language: "python", Kind: core.KindMethod, Name: "read", QualifiedName: "FileReader.read", ParentSymbol: "FileReader", Signature: "def read(self) -> str:"}

	var implements, overrides bool
	for _, edge := range BuildEdges([]core.SymbolRecord{protocol, contract, impl, method}) {
		implements = implements || edge.Type == core.EdgeImplements && edge.From == impl.ID && edge.To == protocol.ID
		overrides = overrides || edge.Type == core.EdgeOverrides && edge.From == method.ID && edge.To == protocol.ID
	}
	if !implements || !overrides {
		t.Fatalf("Protocol structural edges missing: implements=%v overrides=%v", implements, overrides)
	}
}
