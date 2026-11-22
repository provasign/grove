package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func goType(file, name string) core.SymbolRecord {
	return core.SymbolRecord{ID: file + "::" + name + "@1", FilePath: file, Language: "go", Kind: core.KindStruct,
		Name: name, QualifiedName: name, Signature: "type " + name + " struct", Span: core.LineRange{Start: 1, End: 3}}
}

func goMethod(file, typ, name, sig string, line int) core.SymbolRecord {
	return core.SymbolRecord{ID: file + "::" + typ + "." + name + "@1", FilePath: file, Language: "go", Kind: core.KindMethod,
		Name: name, QualifiedName: typ + "." + name, ParentSymbol: typ, Signature: sig, Span: core.LineRange{Start: line, End: line + 2}}
}

// BUG-01 (prism repo, 2026-11-22): Server.Handler merged graphui.Server and
// httpapi.Server into one closed family; graphui.Server.Handler ignored its
// qualifier and returned the same merge.
func TestSeedScope_SameNamedGoTypesInTwoPackagesAreAmbiguous(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		goType("internal/graphui/server.go", "Server"),
		goMethod("internal/graphui/server.go", "Server", "Handler", "func (s *Server) Handler() http.Handler", 73),
		goType("internal/httpapi/http.go", "Server"),
		goMethod("internal/httpapi/http.go", "Server", "Handler", "func (s *Server) Handler() http.Handler", 22),
		// A third Server without Handler must not appear among candidates.
		goType("internal/mcp/server.go", "Server"),
	}, 3)

	_, err := g.ChangeImpact("Server.Handler")
	if err == nil {
		t.Fatal("bare Server.Handler must be ambiguous, got a merged answer")
	}
	msg := err.Error()
	for _, want := range []string{
		"is ambiguous — 2 candidates",
		"\n  graphui.Server.Handler  (internal/graphui/server.go:73)",
		"\n  httpapi.Server.Handler  (internal/httpapi/http.go:22)",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "internal/mcp/server.go") {
		t.Fatalf("a Server without Handler became a candidate:\n%s", msg)
	}

	for query, file := range map[string]string{
		"graphui.Server.Handler":          "internal/graphui/server.go",
		"internal.httpapi.Server.Handler": "internal/httpapi/http.go",
	} {
		r, err := g.ChangeImpact(query)
		if err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		if len(r.Declarations) != 1 || r.Declarations[0].FilePath != file {
			t.Fatalf("%s declarations = %+v, want only %s", query, r.Declarations, file)
		}
	}

	if _, err := g.MissingImplementations("Server.Handler"); err == nil || !strings.Contains(err.Error(), "is ambiguous — 2 candidates") {
		t.Fatalf("missing-implementations must refuse the merge too, got %v", err)
	}
}

// BUG-02 (prism repo, 2026-11-22): a Rust struct Client with no Status method
// made every Go Client.Status query a cross-language ambiguity.
func TestSeedScope_OtherLanguageTypeWithoutTheMemberDoesNotBlock(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		goType("internal/grove/client.go", "Client"),
		goMethod("internal/grove/client.go", "Client", "Status", "func (c *Client) Status(ctx context.Context) (StatusResult, error)", 306),
		{ID: "fx/src/net/client.rs::Client@1", FilePath: "fx/src/net/client.rs", Language: "rust", Kind: core.KindStruct,
			Name: "Client", QualifiedName: "Client", Signature: "pub struct Client { addr: String }", Span: core.LineRange{Start: 1, End: 1}},
	}, 2)

	r, err := g.ChangeImpact("Client.Status")
	if err != nil {
		t.Fatalf("Client.Status: %v", err)
	}
	if len(r.Declarations) != 1 || r.Declarations[0].FilePath != "internal/grove/client.go" {
		t.Fatalf("declarations = %+v, want the Go method only", r.Declarations)
	}

	// When the other language does declare the member, it stays a
	// cross-language choice.
	g2 := New()
	g2.Replace([]core.SymbolRecord{
		goType("internal/grove/client.go", "Client"),
		goMethod("internal/grove/client.go", "Client", "Status", "func (c *Client) Status() error", 306),
		{ID: "fx/src/net/client.rs::Client@1", FilePath: "fx/src/net/client.rs", Language: "rust", Kind: core.KindStruct,
			Name: "Client", QualifiedName: "Client", Span: core.LineRange{Start: 1, End: 1}},
		{ID: "fx/src/net/client.rs::Client.Status@1", FilePath: "fx/src/net/client.rs", Language: "rust", Kind: core.KindMethod,
			Name: "Status", QualifiedName: "Client.Status", ParentSymbol: "Client", Signature: "fn status(&self)", Span: core.LineRange{Start: 3, End: 3}},
	}, 4)
	if _, err := g2.ChangeImpact("Client.Status"); err == nil || !strings.Contains(err.Error(), crossLanguageAmbiguityMarkerForTest) {
		t.Fatalf("want the cross-language ambiguity, got %v", err)
	}
}

const crossLanguageAmbiguityMarkerForTest = "ambiguous across language families"

// One declaration unit split across files stays one seed group: Go build-tag
// twins, a Python stub beside its module, a TS declaration file beside its
// source, a C++ class in one namespace across header and source.
func TestSeedScope_SplitDeclarationUnitsStayMerged(t *testing.T) {
	cases := []struct {
		name string
		syms []core.SymbolRecord
	}{
		{"go build tags", []core.SymbolRecord{
			goType("pkg/srv/srv_linux.go", "Srv"),
			goMethod("pkg/srv/srv_linux.go", "Srv", "Run", "func (s *Srv) Run() error", 5),
			goType("pkg/srv/srv_windows.go", "Srv"),
			goMethod("pkg/srv/srv_windows.go", "Srv", "Run", "func (s *Srv) Run() error", 5),
		}},
		{"python stub", []core.SymbolRecord{
			{ID: "pkg/m.py::Repo@1", FilePath: "pkg/m.py", Language: "python", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
			{ID: "pkg/m.py::Repo.get@1", FilePath: "pkg/m.py", Language: "python", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "def get(self, k)"},
			{ID: "pkg/m.pyi::Repo@1", FilePath: "pkg/m.pyi", Language: "python", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
			{ID: "pkg/m.pyi::Repo.get@1", FilePath: "pkg/m.pyi", Language: "python", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "def get(self, k: str) -> int: ..."},
		}},
		{"ts declaration file", []core.SymbolRecord{
			{ID: "src/a.ts::Repo@1", FilePath: "src/a.ts", Language: "typescript", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
			{ID: "src/a.ts::Repo.get@1", FilePath: "src/a.ts", Language: "typescript", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "get(k: string): number"},
			{ID: "src/a.d.ts::Repo@1", FilePath: "src/a.d.ts", Language: "typescript", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
			{ID: "src/a.d.ts::Repo.get@1", FilePath: "src/a.d.ts", Language: "typescript", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "get(k: string): number"},
		}},
		{"cpp namespace", []core.SymbolRecord{
			{ID: "include/geo/shape.h::Shape@1", FilePath: "include/geo/shape.h", Language: "cpp", Kind: core.KindClass, Name: "Shape", QualifiedName: "geo::Shape"},
			{ID: "include/geo/shape.h::Shape.area@1", FilePath: "include/geo/shape.h", Language: "cpp", Kind: core.KindMethod, Name: "area", QualifiedName: "geo::Shape.area", ParentSymbol: "Shape", Signature: "double area() const"},
			{ID: "src/shape.cpp::Shape@1", FilePath: "src/shape.cpp", Language: "cpp", Kind: core.KindClass, Name: "Shape", QualifiedName: "geo::Shape"},
			{ID: "src/shape.cpp::Shape.area@1", FilePath: "src/shape.cpp", Language: "cpp", Kind: core.KindMethod, Name: "area", QualifiedName: "geo::Shape.area", ParentSymbol: "Shape", Signature: "double area() const"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := New()
			g.Replace(c.syms, len(c.syms))
			typ := c.syms[0].Name
			method := c.syms[1].Name
			r, err := g.ChangeImpact(typ + "." + method)
			if err != nil {
				t.Fatalf("ChangeImpact: %v", err)
			}
			if len(r.Declarations) != 2 {
				t.Fatalf("declarations = %d, want both halves of one unit", len(r.Declarations))
			}
		})
	}
}

// Same-named types joined by inheritance are one family, not an ambiguity:
// a subclass that shares its base's simple name in another package.
func TestSeedScope_InheritanceJoinsSameNamedTypes(t *testing.T) {
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{
		{ID: "src/main/java/a/Repo.java::Repo@1", FilePath: "src/main/java/a/Repo.java", Language: "java", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
		{ID: "src/main/java/a/Repo.java::Repo.get@1", FilePath: "src/main/java/a/Repo.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "public int get(String k)"},
		{ID: "src/main/java/b/Repo.java::Repo@1", FilePath: "src/main/java/b/Repo.java", Language: "java", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo"},
		{ID: "src/main/java/b/Repo.java::Repo.get@1", FilePath: "src/main/java/b/Repo.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Repo.get", ParentSymbol: "Repo", Signature: "public int get(String k)"},
	}, []core.Edge{{From: "src/main/java/b/Repo.java::Repo@1", To: "src/main/java/a/Repo.java::Repo@1", Type: core.EdgeExtends}}, 4)
	r, err := g.ChangeImpact("Repo.get")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.Declarations) != 2 {
		t.Fatalf("declarations = %d, want base and subclass", len(r.Declarations))
	}
}

func TestImpactQualifier(t *testing.T) {
	for q, want := range map[string]string{
		"Server.Handler":             "",
		"graphui.Server.Handler":     "graphui",
		"com.x.Foo.bar(int, String)": "com.x",
		"Cart.Line.total":            "Cart",
		"geo::Shape.area":            "",
		"handler":                    "",
	} {
		if got := impactQualifier(q); got != want {
			t.Errorf("impactQualifier(%q) = %q, want %q", q, got, want)
		}
	}
}
