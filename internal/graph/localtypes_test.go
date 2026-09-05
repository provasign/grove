// Tests for shallow Go local type inference and the typed-receiver
// narrowing of call edges.
package graph

import (
	"strings"
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

func TestJavaOverloadNarrowing(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "A.java::Utils.addFirst@1", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "addFirst", QualifiedName: "Utils.addFirst", ParentSymbol: "Utils",
		RawText:   "public static boolean[] addFirst(final boolean[] array, final boolean element) {\n    return add(array, element);\n}",
		CallSites: []core.CallSite{{Callee: "add", Line: 2, Argc: 2, Args: []string{"array", "element"}}},
	}
	boolAdd := core.SymbolRecord{
		ID: "A.java::Utils.add.bool@10", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "add", QualifiedName: "Utils.add", ParentSymbol: "Utils",
		Signature: "public static boolean[] add(final boolean[] array, final boolean element)",
	}
	longAdd := core.SymbolRecord{
		ID: "A.java::Utils.add.long@20", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "add", QualifiedName: "Utils.add", ParentSymbol: "Utils",
		Signature: "public static long[] add(final long[] array, final long element)",
	}
	threeArg := core.SymbolRecord{
		ID: "A.java::Utils.add.three@30", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "add", QualifiedName: "Utils.add", ParentSymbol: "Utils",
		Signature: "public static boolean[] add(final boolean[] array, final int index, final boolean element)",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, boolAdd, longAdd, threeArg})
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got[boolAdd.ID] {
		t.Error("expected edge to the boolean[] overload")
	}
	if got[longAdd.ID] {
		t.Error("long[] overload conflicts with boolean[] argument types")
	}
	if got[threeArg.ID] {
		t.Error("three-arg overload conflicts with argc 2")
	}
}

// A primitive-array argument never binds a type-variable or Object array
// parameter, and an overload matching every known argument type exactly
// beats the wildcard-compatible siblings (javac's no-boxing phase).
func TestJavaOverloadNarrowing_ExactBeatsWildcard(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "A.java::Utils.addAll@1", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "addAll", QualifiedName: "Utils.addAll", ParentSymbol: "Utils",
		RawText:   "public static float[] addAll(final float[] array1, final float... array2) {\n    return clone(array1);\n}",
		CallSites: []core.CallSite{{Callee: "clone", Line: 2, Argc: 1, Args: []string{"array1"}}},
	}
	floatClone := core.SymbolRecord{
		ID: "A.java::Utils.clone.float@10", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "clone", QualifiedName: "Utils.clone", ParentSymbol: "Utils",
		Signature: "public static float[] clone(final float[] array)",
	}
	genericClone := core.SymbolRecord{
		ID: "A.java::Utils.clone.T@20", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "clone", QualifiedName: "Utils.clone", ParentSymbol: "Utils",
		Signature: "public static <T> T[] clone(final T[] array)", TypeParameters: []string{"T"},
	}
	objectIndex := core.SymbolRecord{
		ID: "A.java::Utils.clone.obj@30", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "clone", QualifiedName: "Utils.clone", ParentSymbol: "Utils",
		Signature: "public static Object[] clone(final Object[] array)",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, floatClone, genericClone, objectIndex})
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got[floatClone.ID] {
		t.Error("expected edge to the float[] overload")
	}
	if got[genericClone.ID] {
		t.Error("float[] cannot instantiate T[]")
	}
	if got[objectIndex.ID] {
		t.Error("float[] is not an Object[]")
	}

	// Scalar primitive with an exact overload: boxing-compatible Object
	// sibling loses; without the exact overload it stays.
	caller.RawText = "public void put(final String name, final boolean value) {\n    append(name, value);\n}"
	caller.CallSites = []core.CallSite{{Callee: "append", Line: 2, Argc: 2, Args: []string{"name", "value"}}}
	boolAppend := core.SymbolRecord{
		ID: "A.java::Utils.append.bool@40", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "append", QualifiedName: "Utils.append", ParentSymbol: "Utils",
		Signature: "public Utils append(final String fieldName, final boolean value)",
	}
	objAppend := core.SymbolRecord{
		ID: "A.java::Utils.append.obj@50", FilePath: "A.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "append", QualifiedName: "Utils.append", ParentSymbol: "Utils",
		Signature: "public Utils append(final String fieldName, final Object value)",
	}
	has := func(syms []core.SymbolRecord, id string) bool {
		for _, e := range BuildEdges(syms) {
			if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == id {
				return true
			}
		}
		return false
	}
	if has([]core.SymbolRecord{caller, boolAppend, objAppend}, objAppend.ID) {
		t.Error("append(String, Object) loses to the exact append(String, boolean)")
	}
	if !has([]core.SymbolRecord{caller, objAppend}, objAppend.ID) {
		t.Error("with no exact overload, boxing keeps append(String, Object)")
	}
}

// `with` items run __enter__/__exit__ with no call syntax; the item's class
// comes from a constructor call, a typed local, or a call whose return
// annotation names it. The same annotation types `ctx = self.app_context()`
// and the `self.app_context().push()` receiver.
func TestPyWithProtocolAndReturnAnnotation(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "ctx.py::AppContext@1", FilePath: "ctx.py", BlobSHA: "1", Language: "python",
			Kind: core.KindClass, Name: "AppContext", QualifiedName: "AppContext",
			RawText: "class AppContext:\n    pass\n"},
		{ID: "ctx.py::AppContext.__enter__@3", FilePath: "ctx.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "__enter__", QualifiedName: "AppContext.__enter__", ParentSymbol: "AppContext",
			RawText: "def __enter__(self):\n    return self\n"},
		{ID: "ctx.py::AppContext.__exit__@5", FilePath: "ctx.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "__exit__", QualifiedName: "AppContext.__exit__", ParentSymbol: "AppContext",
			RawText: "def __exit__(self, *a):\n    pass\n"},
		{ID: "ctx.py::AppContext.push@7", FilePath: "ctx.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "push", QualifiedName: "AppContext.push", ParentSymbol: "AppContext",
			RawText: "def push(self):\n    pass\n"},
		{ID: "other.py::Other.push@1", FilePath: "other.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "push", QualifiedName: "Other.push", ParentSymbol: "Other",
			RawText: "def push(self):\n    pass\n"},
		{ID: "other.py::Other.__enter__@3", FilePath: "other.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "__enter__", QualifiedName: "Other.__enter__", ParentSymbol: "Other",
			RawText: "def __enter__(self):\n    return self\n"},
		{ID: "app.py::App.app_context@1", FilePath: "app.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "app_context", QualifiedName: "App.app_context", ParentSymbol: "App",
			RawText: "def app_context(\n    self,\n) -> AppContext:\n    return AppContext()\n",
			Imports: []string{"ctx"}},
		{ID: "app.py::App.wsgi@5", FilePath: "app.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "wsgi", QualifiedName: "App.wsgi", ParentSymbol: "App",
			RawText: "def wsgi(self):\n    ctx = self.app_context()\n    ctx.push()\n    with self.app_context() as c:\n        pass\n    self.app_context().push()\n",
			Imports: []string{"ctx", "other"},
			CallSites: []core.CallSite{
				{Callee: "app_context", Line: 2},
				{Callee: "ctx.push", Line: 3},
				{Callee: "app_context", Line: 4},
				{Callee: "app_context().push", Line: 6},
			}},
	}
	caller := "app.py::App.wsgi@5"
	got := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.From == caller {
			got[e.To] = true
		}
	}
	for _, want := range []string{"ctx.py::AppContext.__enter__@3", "ctx.py::AppContext.__exit__@5", "ctx.py::AppContext.push@7"} {
		if !got[want] {
			t.Errorf("missing edge to %s", want)
		}
	}
	for _, bad := range []string{"other.py::Other.push@1", "other.py::Other.__enter__@3"} {
		if got[bad] {
			t.Errorf("unexpected edge to %s (return annotation says AppContext)", bad)
		}
	}
	if rt := pyReturnType(&syms[6]); rt != "AppContext" {
		t.Errorf("pyReturnType = %q, want AppContext", rt)
	}
}

// self.m() inside a base class dispatches to subclass overrides of m.
func TestPyTemplateMethodDispatch(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "tag.py::JSONTag@1", FilePath: "tag.py", BlobSHA: "1", Language: "python",
			Kind: core.KindClass, Name: "JSONTag", QualifiedName: "JSONTag", Signature: "class JSONTag:",
			RawText: "class JSONTag:\n    pass\n"},
		{ID: "tag.py::JSONTag.to_json@2", FilePath: "tag.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "to_json", QualifiedName: "JSONTag.to_json", ParentSymbol: "JSONTag",
			RawText: "def to_json(self, value):\n    raise NotImplementedError\n"},
		{ID: "tag.py::JSONTag.tag@4", FilePath: "tag.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "tag", QualifiedName: "JSONTag.tag", ParentSymbol: "JSONTag",
			RawText:   "def tag(self, value):\n    return {self.key: self.to_json(value)}\n",
			CallSites: []core.CallSite{{Callee: "self.to_json", Line: 2, Argc: 1}}},
		{ID: "tag.py::TagDict@6", FilePath: "tag.py", BlobSHA: "1", Language: "python",
			Kind: core.KindClass, Name: "TagDict", QualifiedName: "TagDict", Signature: "class TagDict(JSONTag):",
			RawText: "class TagDict(JSONTag):\n    pass\n"},
		{ID: "tag.py::TagDict.to_json@7", FilePath: "tag.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "to_json", QualifiedName: "TagDict.to_json", ParentSymbol: "TagDict",
			RawText: "def to_json(self, value):\n    return value\n"},
		{ID: "other.py::Unrelated.to_json@1", FilePath: "other.py", BlobSHA: "1", Language: "python",
			Kind: core.KindMethod, Name: "to_json", QualifiedName: "Unrelated.to_json", ParentSymbol: "Unrelated",
			RawText: "def to_json(self, value):\n    return value\n"},
	}
	got := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.From == "tag.py::JSONTag.tag@4" {
			got[e.To] = true
		}
	}
	if !got["tag.py::JSONTag.to_json@2"] {
		t.Error("own-class to_json edge missing")
	}
	if !got["tag.py::TagDict.to_json@7"] {
		t.Error("subclass override TagDict.to_json not reached by dispatch")
	}
	if got["other.py::Unrelated.to_json@1"] {
		t.Error("unrelated to_json must not be a dispatch target")
	}
}

// A bare call to a function declared inside the caller's own body binds
// that closure; it must not resolve to a same-named method elsewhere.
func TestLocalFunctionShadowsBareCall(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "socket.ts::Socket.run@1", FilePath: "socket.ts", BlobSHA: "1", Language: "typescript",
			Kind: core.KindMethod, Name: "run", QualifiedName: "Socket.run", ParentSymbol: "Socket",
			RawText:   "run(event, fn) {\n    function run(i) {\n      run(i + 1);\n    }\n    run(0);\n}",
			CallSites: []core.CallSite{{Callee: "run", Line: 3}, {Callee: "run", Line: 5}},
			Imports:   []string{"./namespace"}},
		{ID: "namespace.ts::Namespace.run@1", FilePath: "namespace.ts", BlobSHA: "1", Language: "typescript",
			Kind: core.KindMethod, Name: "run", QualifiedName: "Namespace.run", ParentSymbol: "Namespace",
			RawText: "run(socket, fn) {\n    fn();\n}"},
		{ID: "mod.py::outer@1", FilePath: "mod.py", BlobSHA: "1", Language: "python",
			Kind: core.KindFunction, Name: "outer", QualifiedName: "outer",
			RawText:   "def outer():\n    def helper():\n        pass\n    helper()\n",
			CallSites: []core.CallSite{{Callee: "helper", Line: 4}}},
		{ID: "util.py::helper@1", FilePath: "util.py", BlobSHA: "1", Language: "python",
			Kind: core.KindFunction, Name: "helper", QualifiedName: "helper",
			RawText: "def helper():\n    pass\n"},
	}
	for _, e := range BuildEdges(syms) {
		if e.Type != core.EdgeCalls {
			continue
		}
		if e.From == "socket.ts::Socket.run@1" && e.To == "namespace.ts::Namespace.run@1" {
			t.Error("nested function run(i) shadowed: must not resolve to Namespace.run")
		}
		if e.From == "mod.py::outer@1" && e.To == "util.py::helper@1" {
			t.Error("nested def helper shadowed: must not resolve to util.helper")
		}
	}
}

// A leading annotation's parens are not the parameter list.
func TestJavaDeclSource_SkipsAnnotations(t *testing.T) {
	s := &core.SymbolRecord{Language: "java",
		Signature: `@SuppressWarnings("unchecked")`,
		RawText:   "/** Removes (shifts) the element.\n * @param array may be {@code null} (any type) */\n@SuppressWarnings(\"unchecked\") // trailing note (ignored)\n@Deprecated public static <T> T[] remove(final T[] array, final int index) {\n    return null;\n}"}
	if got := javaParamTypes(s); len(got) != 2 || got[0] != "T[]" || got[1] != "int" {
		t.Fatalf("javaParamTypes = %v, want [T[] int]", got)
	}
	if got := javaReturnType(s); got != "T[]" {
		t.Fatalf("javaReturnType = %q, want T[]", got)
	}
	if n, variadic, ok := declParamCount(s); !ok || n != 2 || variadic {
		t.Fatalf("declParamCount = %d %v %v", n, variadic, ok)
	}
}

// Shape rules: String[] cannot bind Collection; (Object) x cannot bind T[];
// a lambda cannot bind an int slot.
func TestJavaOverloadNarrowing_ShapeMismatch(t *testing.T) {
	mk := func(id, sig string, tps ...string) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: "A.java", BlobSHA: "1", Language: "java", Kind: core.KindMethod,
			Name:          strings.TrimSpace(sig[strings.LastIndex(sig[:strings.Index(sig, "(")], " ")+1 : strings.Index(sig, "(")]),
			QualifiedName: "Utils.x", ParentSymbol: "Utils", Signature: sig, TypeParameters: tps}
	}
	caller := core.SymbolRecord{
		ID: "A.java::Utils.caller@1", FilePath: "A.java", BlobSHA: "1", Language: "java", Kind: core.KindMethod,
		Name: "caller", QualifiedName: "Utils.caller", ParentSymbol: "Utils",
		RawText: "void caller(final String... keys, final Object array, final int[] src) {\n    of(keys);\n    removeAll(array, 1);\n    copy(src, 0, () -> 1);\n}",
		CallSites: []core.CallSite{
			{Callee: "of", Line: 2, Argc: 1, Args: []string{"keys"}},
			{Callee: "removeAll", Line: 3, Argc: 2, Args: []string{"array", "#int"}},
			{Callee: "copy", Line: 4, Argc: 3, Args: []string{"src", "#int", "#lambda"}},
		},
	}
	ofColl := mk("A.java::Utils.of.coll@10", "public static <E> Stream<E> of(final Collection<E> collection)", "E")
	ofArr := mk("A.java::Utils.of.arr@11", "public static <T> Stream<T> of(final T... values)", "T")
	rmT := mk("A.java::Utils.removeAll.T@20", "public static <T> T[] removeAll(final T[] array, final int index)", "T")
	rmObj := mk("A.java::Utils.removeAll.obj@21", "static Object removeAll(final Object array, final int index)")
	copyInt := mk("A.java::Utils.copy.int@30", "public static int[] copy(final int[] src, final int pos, final int len)")
	copySup := mk("A.java::Utils.copy.sup@31", "public static <T> T copy(final T src, final int pos, final Supplier<T> s)", "T")
	got := map[string]bool{}
	for _, e := range BuildEdges([]core.SymbolRecord{caller, ofColl, ofArr, rmT, rmObj, copyInt, copySup}) {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	for _, bad := range []string{ofColl.ID, rmT.ID, copyInt.ID} {
		if got[bad] {
			t.Errorf("shape-incompatible overload kept: %s", bad)
		}
	}
	for _, want := range []string{ofArr.ID, rmObj.ID, copySup.ID} {
		if !got[want] {
			t.Errorf("compatible overload lost: %s", want)
		}
	}
}

// C# `class X : Base` inheritance: base.M(v) binds the base overload that
// matches v's type; a JValue argument binds a JToken parameter.
func TestCSharpBaseAndAssignability(t *testing.T) {
	mk := func(id, name, parent, sig string) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: parent + ".cs", BlobSHA: "1", Language: "csharp", Kind: core.KindMethod,
			Name: name, QualifiedName: parent + "." + name, ParentSymbol: parent, Signature: sig, RawText: sig + "\n{\n}"}
	}
	cls := func(name, sig string) core.SymbolRecord {
		return core.SymbolRecord{ID: name + ".cs::" + name + "@1", FilePath: name + ".cs", BlobSHA: "1", Language: "csharp", Kind: core.KindClass,
			Name: name, QualifiedName: name, Signature: sig, RawText: sig + "\n{\n}"}
	}
	syms := []core.SymbolRecord{
		cls("JsonWriter", "public abstract class JsonWriter : IDisposable"),
		cls("BsonWriter", "public class BsonWriter : JsonWriter"),
		cls("JToken", "public abstract class JToken"),
		cls("JValue", "public class JValue : JToken"),
		cls("JArray", "public class JArray : JContainer"),
		mk("JsonWriter.cs::JsonWriter.WriteValue.int@30", "WriteValue", "JsonWriter", "public virtual void WriteValue(int value)"),
		mk("JsonWriter.cs::JsonWriter.WriteValue.obj@40", "WriteValue", "JsonWriter", "public virtual void WriteValue(object value)"),
		mk("BsonWriter.cs::BsonWriter.WriteValue.obj@20", "WriteValue", "BsonWriter", "public override void WriteValue(object value)"),
		mk("JArray.cs::JArray.IndexOf.tok@5", "IndexOf", "JArray", "public int IndexOf(JToken item)"),
		mk("JArray.cs::JArray.IndexOf.str@6", "IndexOf", "JArray", "public int IndexOf(string item)"),
	}
	caller := mk("BsonWriter.cs::BsonWriter.WriteValue.int@10", "WriteValue", "BsonWriter", "public override void WriteValue(int value)")
	caller.RawText = "public override void WriteValue(int value)\n{\n    base.WriteValue(value);\n    JValue v1 = new JValue(1);\n    JArray j = new JArray();\n    j.IndexOf(v1);\n}"
	caller.CallSites = []core.CallSite{{Callee: "base.WriteValue", Line: 3, Argc: 1, Args: []string{"value"}}, {Callee: "j.IndexOf", Line: 6, Argc: 1, Args: []string{"v1"}}}
	caller.Imports = []string{"JsonWriter", "JArray", "JValue"}
	got := map[string]bool{}
	for _, e := range BuildEdges(append(syms, caller)) {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got["JsonWriter.cs::JsonWriter.WriteValue.int@30"] {
		t.Errorf("base.WriteValue(int) missing: %v", got)
	}
	for _, bad := range []string{"JsonWriter.cs::JsonWriter.WriteValue.obj@40", "BsonWriter.cs::BsonWriter.WriteValue.obj@20", "JArray.cs::JArray.IndexOf.str@6"} {
		if got[bad] {
			t.Errorf("unexpected edge %s", bad)
		}
	}
	if !got["JArray.cs::JArray.IndexOf.tok@5"] {
		t.Errorf("JValue argument must bind IndexOf(JToken): %v", got)
	}
}

// PHP `new Stmt\ClassConst(` names a namespace astkit drops; the
// constructor must come from the file that namespace maps to, not from
// every class called ClassConst.
func TestPhpNewNarrowsByNamespace(t *testing.T) {
	ctor := func(path string) core.SymbolRecord {
		return core.SymbolRecord{ID: path + "::ClassConst.__construct@1", FilePath: path, BlobSHA: "1", Language: "php",
			Kind: core.KindConstructor, Name: "__construct", QualifiedName: "ClassConst.__construct", ParentSymbol: "ClassConst",
			RawText: "public function __construct($name) {\n}"}
	}
	cls := func(path string) core.SymbolRecord {
		return core.SymbolRecord{ID: path + "::ClassConst@1", FilePath: path, BlobSHA: "1", Language: "php",
			Kind: core.KindClass, Name: "ClassConst", QualifiedName: "ClassConst", Signature: "class ClassConst", RawText: "class ClassConst {\n}"}
	}
	caller := core.SymbolRecord{ID: "lib/PhpParser/Builder/ClassConst.php::ClassConst.getNode@10", FilePath: "lib/PhpParser/Builder/ClassConst.php", BlobSHA: "1", Language: "php",
		Kind: core.KindMethod, Name: "getNode", QualifiedName: "ClassConst.getNode", ParentSymbol: "ClassConst", Span: core.LineRange{Start: 10, End: 14},
		RawText:   "public function getNode(): Node {\n    return new Stmt\\ClassConst(\n        $this->name\n    );\n}",
		Imports:   []string{"PhpParser\\Node\\Stmt"},
		CallSites: []core.CallSite{{Callee: "ClassConst", Line: 11, Argc: 1}}}
	syms := []core.SymbolRecord{cls("lib/PhpParser/Builder/ClassConst.php"), ctor("lib/PhpParser/Builder/ClassConst.php"),
		cls("lib/PhpParser/Node/Stmt/ClassConst.php"), ctor("lib/PhpParser/Node/Stmt/ClassConst.php"), caller}
	got := map[string]bool{}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got["lib/PhpParser/Node/Stmt/ClassConst.php::ClassConst.__construct@1"] {
		t.Errorf("Stmt\\ClassConst constructor missing: %v", got)
	}
	if got["lib/PhpParser/Builder/ClassConst.php::ClassConst.__construct@1"] {
		t.Errorf("Builder\\ClassConst constructor must not match Stmt\\ClassConst")
	}
}

// tsResolveClassFile must pick the class the referencing file imports, not
// whichever same-named class sorts first — else a receiver chain
// (this.connection.driver.escape) misattributes to an unrelated class.
func TestTsResolveClassFile_ImportScope(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "a/conn.ts::Connection@1", FilePath: "a/conn.ts", Language: "typescript",
			Kind: core.KindClass, Name: "Connection", QualifiedName: "Connection",
			RawText: "class Connection { driver: FakeDriver; }"},
		{ID: "z/conn.ts::Connection@1", FilePath: "z/conn.ts", Language: "typescript",
			Kind: core.KindClass, Name: "Connection", QualifiedName: "Connection",
			RawText: "class Connection { driver: Driver; }"},
		{ID: "wrapper.ts::Wrapper@1", FilePath: "wrapper.ts", Language: "typescript",
			Kind: core.KindClass, Name: "Wrapper", QualifiedName: "Wrapper",
			RawText: "class Wrapper { connection: Connection; }",
			Imports: []string{"./z/conn"}},
	}
	idx := newEdgeIndex(syms)
	// Referenced from wrapper.ts, which imports z/conn: must resolve there,
	// not to a/conn.ts (which sorts first).
	got := tsResolveClassFile(idx, "Connection", "wrapper.ts")
	if got != "z/conn.ts" {
		t.Fatalf("tsResolveClassFile = %q, want z/conn.ts (the imported one)", got)
	}
	// No referencing scope: falls back to first-match (historical behavior).
	if got := tsResolveClassFile(idx, "Connection", ""); got == "" {
		t.Fatal("empty preferFile must still resolve (first-match fallback)")
	}
}

// A Python module-aliased base (`import pkg as Mod; class C(Mod.Base)`) must
// resolve to the imported module's class, not a same-named decoy elsewhere.
// Exercises both the `as`-alias import stripping and the import-scope tier.
func TestResolveTypeEdges_PythonModuleAliasBase(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "driver/dbapi.py::Cursor@1", FilePath: "driver/dbapi.py", Language: "python",
			Kind: core.KindClass, Name: "Cursor", QualifiedName: "Cursor",
			Signature: "class Cursor"},
		{ID: "decoy/cursor.py::Cursor@1", FilePath: "decoy/cursor.py", Language: "python",
			Kind: core.KindClass, Name: "Cursor", QualifiedName: "Cursor",
			Signature: "class Cursor"},
		{ID: "backend/base.py::Wrapper@1", FilePath: "backend/base.py", Language: "python",
			Kind: core.KindClass, Name: "Wrapper", QualifiedName: "Wrapper",
			Signature: "class Wrapper(Database.Cursor)",
			RawText:   "class Wrapper(Database.Cursor):\n    pass",
			Imports:   []string{"driver.dbapi as Database"}},
	}
	var targets []string
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeExtends && e.From == "backend/base.py::Wrapper@1" {
			targets = append(targets, e.To)
		}
	}
	if len(targets) != 1 || targets[0] != "driver/dbapi.py::Cursor@1" {
		t.Fatalf("Wrapper(Database.Cursor) must resolve to the imported Cursor only, got %v", targets)
	}
}
