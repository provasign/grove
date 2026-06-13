// Implicit-self narrowing (Wave 1 precision lever). In Java/C#/C++ a bare
// unqualified call inside a method is this.method() — member lookup binds it to
// the caller's own class first, so it must not fan out to a same-named method
// on an unrelated class. PHP/Python/JS are excluded (bare call = free function).
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestBuildCalls_JavaUnqualifiedCallBindsOwnClass(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "A.java::A.run@s", FilePath: "A.java", BlobSHA: "s", Language: "java",
			Kind: core.KindMethod, Name: "run", QualifiedName: "A.run", ParentSymbol: "A",
			RawText: "void run(){ helper(); }", CallSites: []core.CallSite{{Callee: "helper", Line: 1}}},
		{ID: "A.java::A.helper@s", FilePath: "A.java", BlobSHA: "s", Language: "java",
			Kind: core.KindMethod, Name: "helper", QualifiedName: "A.helper", ParentSymbol: "A"},
		// Same-named method on an unrelated class in scope — must NOT be linked.
		{ID: "B.java::B.helper@s", FilePath: "B.java", BlobSHA: "s", Language: "java",
			Kind: core.KindMethod, Name: "helper", QualifiedName: "B.helper", ParentSymbol: "B"},
	}, 2)

	if !hasEdge(g, core.EdgeCalls, "A.java::A.run@s", "A.java::A.helper@s") {
		t.Fatalf("unqualified helper() must bind to own class A.helper")
	}
	if hasEdge(g, core.EdgeCalls, "A.java::A.run@s", "B.java::B.helper@s") {
		t.Fatalf("unqualified helper() MUST NOT fan out to unrelated B.helper")
	}
}

func TestBuildCalls_PHPUnqualifiedCallNotImplicitSelf(t *testing.T) {
	// In PHP a bare foo() is a free function, not $this->foo(). The own-class
	// method must NOT capture it; the free function is the right target.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.php::A.run@s", FilePath: "a.php", BlobSHA: "s", Language: "php",
			Kind: core.KindMethod, Name: "run", QualifiedName: "A.run", ParentSymbol: "A",
			RawText: "function run(){ helper(); }", CallSites: []core.CallSite{{Callee: "helper", Line: 1}}},
		{ID: "a.php::helper@s", FilePath: "a.php", BlobSHA: "s", Language: "php",
			Kind: core.KindFunction, Name: "helper", QualifiedName: "helper"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.php::A.run@s", "a.php::helper@s") {
		t.Fatalf("PHP bare helper() should resolve to the free function")
	}
}
