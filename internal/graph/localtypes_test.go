// Tests for shallow Go local type inference and the typed-receiver
// narrowing of call edges.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestGoParamTypes(t *testing.T) {
	got := goParamTypes("func (c *Context) Render(code int, a, b string, r render.Render, f func(x int) error, rest ...*Opt)")
	want := map[string]string{"code": "int", "a": "string", "b": "string", "r": "Render", "rest": "Opt"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("param %q = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if _, ok := got["f"]; ok {
		t.Errorf("func-typed param must be skipped, got %v", got["f"])
	}
}

func TestGoLocalTypes_BodyAndFields(t *testing.T) {
	typ := core.SymbolRecord{
		ID: "a.go::Server@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindStruct,
		Name: "Server", QualifiedName: "Server",
		RawText: "type Server struct {\n\tengine *Engine\n\tcount int\n}",
	}
	client := core.SymbolRecord{
		ID: "a.go::Client@3", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindStruct,
		Name: "Client", QualifiedName: "Client",
	}
	method := core.SymbolRecord{
		ID: "a.go::Server.Run@5", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Run", QualifiedName: "Server.Run", ParentSymbol: "Server",
		Signature: "func (s *Server) Run(w http.ResponseWriter)",
		RawText:   "func (s *Server) Run(w http.ResponseWriter) {\n\tvar buf strings.Builder\n\tu := User{}\n\tcl := NewClient(w)\n\t_ = cl\n}",
	}
	idx := newEdgeIndex([]core.SymbolRecord{typ, client, method})
	got := goLocalTypes(idx, &method)
	want := map[string]string{"engine": "Engine", "w": "ResponseWriter", "buf": "Builder", "u": "User", "cl": "Client"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("local %q = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

// var buf strings.Builder; buf.String() — Builder isn't indexed, so the
// same-file Context.String candidate must be dropped, not matched.
func TestBuildCalls_KnownTypeNoMatchDrops(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "errors.go::errorMsgs.String@1", FilePath: "errors.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "String", QualifiedName: "errorMsgs.String", ParentSymbol: "errorMsgs",
		Signature: "func (a errorMsgs) String() string",
		RawText:   "func (a errorMsgs) String() string {\n\tvar buffer strings.Builder\n\treturn buffer.String()\n}",
		CallSites: []core.CallSite{{Callee: "buffer.String", Line: 3}},
	}
	wrong := core.SymbolRecord{
		ID: "errors.go::Context.String@10", FilePath: "errors.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "String", QualifiedName: "Context.String", ParentSymbol: "Context",
	}
	for _, e := range BuildEdges([]core.SymbolRecord{caller, wrong}) {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			t.Fatalf("known-typed receiver with no matching candidate must drop, got %+v", e)
		}
	}
}

// Param of an indexed concrete type: u.save() resolves only to User.save.
func TestBuildCalls_ParamTypeNarrows(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "a.go::Process@1", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindFunction,
		Name: "Process", QualifiedName: "Process",
		Signature: "func Process(u User)",
		CallSites: []core.CallSite{{Callee: "u.save", Line: 2}},
	}
	right := core.SymbolRecord{
		ID: "a.go::User.save@10", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "save", QualifiedName: "User.save", ParentSymbol: "User",
	}
	wrong := core.SymbolRecord{
		ID: "a.go::Account.save@20", FilePath: "a.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "save", QualifiedName: "Account.save", ParentSymbol: "Account",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, right, wrong})
	var gotRight, gotWrong bool
	for _, e := range edges {
		if e.Type != core.EdgeCalls || e.From != caller.ID {
			continue
		}
		switch e.To {
		case right.ID:
			gotRight = true
		case wrong.ID:
			gotWrong = true
		}
	}
	if !gotRight || gotWrong {
		t.Fatalf("param-typed narrowing: right=%v wrong=%v, want true/false", gotRight, gotWrong)
	}
}

// Param of an interface type: r.Render() dispatches to implementations at
// reduced confidence, even when the plain candidate set was small.
func TestBuildCalls_InterfaceParamDispatches(t *testing.T) {
	iface := core.SymbolRecord{
		ID: "render/render.go::Render@1", FilePath: "render/render.go", BlobSHA: "1",
		Language: "go", Kind: core.KindInterface,
		Name: "Render", QualifiedName: "Render",
		RawText: "type Render interface {\n\tRender(w W) error\n}",
	}
	caller := core.SymbolRecord{
		ID: "context.go::Context.Render@1", FilePath: "context.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "Context.Render", ParentSymbol: "Context",
		Signature: "func (c *Context) Render(code int, r render.Render)",
		Imports:   []string{"render"},
		CallSites: []core.CallSite{{Callee: "r.Render", Line: 3}},
	}
	implA := core.SymbolRecord{
		ID: "render/json.go::JSON.Render@10", FilePath: "render/json.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "JSON.Render", ParentSymbol: "JSON",
	}
	implB := core.SymbolRecord{
		ID: "render/yaml.go::YAML.Render@10", FilePath: "render/yaml.go", BlobSHA: "1",
		Language: "go", Kind: core.KindMethod,
		Name: "Render", QualifiedName: "YAML.Render", ParentSymbol: "YAML",
	}
	edges := BuildEdges([]core.SymbolRecord{iface, caller, implA, implB})
	got := map[string]float64{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = e.Confidence
		}
	}
	if got[implA.ID] == 0 || got[implB.ID] == 0 {
		t.Fatalf("interface-typed param must dispatch to implementations, got %v", got)
	}
	if got[implA.ID] > 0.75 || got[implB.ID] > 0.75 {
		t.Fatalf("dispatch edges must carry reduced confidence, got %v", got)
	}
}

func TestPyLocalTypes_AnnotationsAndSuper(t *testing.T) {
	base := core.SymbolRecord{
		ID: "scaffold.py::Scaffold@1", FilePath: "scaffold.py", BlobSHA: "1",
		Language: "python", Kind: core.KindClass,
		Name: "Scaffold", QualifiedName: "Scaffold",
		Signature: "class Scaffold:",
	}
	baseInit := core.SymbolRecord{
		ID: "scaffold.py::Scaffold.__init__@5", FilePath: "scaffold.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "__init__", QualifiedName: "Scaffold.__init__", ParentSymbol: "Scaffold",
	}
	cls := core.SymbolRecord{
		ID: "blueprints.py::Blueprint@1", FilePath: "blueprints.py", BlobSHA: "1",
		Language: "python", Kind: core.KindClass,
		Name: "Blueprint", QualifiedName: "Blueprint",
		Signature: "class Blueprint(Scaffold):",
		RawText:   "class Blueprint(Scaffold):\n    session_class = SessionA\n",
	}
	sessionA := core.SymbolRecord{
		ID: "sess.py::SessionA@1", FilePath: "sess.py", BlobSHA: "1",
		Language: "python", Kind: core.KindClass,
		Name: "SessionA", QualifiedName: "SessionA",
	}
	sessionInit := core.SymbolRecord{
		ID: "sess.py::SessionA.__init__@3", FilePath: "sess.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "__init__", QualifiedName: "SessionA.__init__", ParentSymbol: "SessionA",
	}
	saveA := core.SymbolRecord{
		ID: "sess.py::SessionA.save@10", FilePath: "sess.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "save", QualifiedName: "SessionA.save", ParentSymbol: "SessionA",
	}
	saveB := core.SymbolRecord{
		ID: "other.py::Other.save@10", FilePath: "other.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "save", QualifiedName: "Other.save", ParentSymbol: "Other",
	}
	method := core.SymbolRecord{
		ID: "blueprints.py::Blueprint.run@20", FilePath: "blueprints.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "run", QualifiedName: "Blueprint.run", ParentSymbol: "Blueprint",
		Imports: []string{"sess", "other", "scaffold"},
		RawText: "def run(self, s: SessionA):\n    super().__init__()\n    s.save()\n    obj = self.session_class()\n",
		CallSites: []core.CallSite{
			{Callee: "super().__init__", Line: 2},
			{Callee: "s.save", Line: 3},
			{Callee: "session_class", Line: 4},
		},
	}
	symbols := []core.SymbolRecord{base, baseInit, cls, sessionA, sessionInit, saveA, saveB, method}
	edges := BuildEdges(symbols)
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == method.ID {
			got[e.To] = true
		}
	}
	if !got[baseInit.ID] {
		t.Error("super().__init__() must resolve to the base class __init__")
	}
	if !got[saveA.ID] {
		t.Error("annotated param s: SessionA must narrow s.save() to SessionA.save")
	}
	if got[saveB.ID] {
		t.Error("annotated param must exclude Other.save")
	}
	if !got[sessionInit.ID] {
		t.Error("class-attribute call self.session_class() must construct SessionA")
	}
}
