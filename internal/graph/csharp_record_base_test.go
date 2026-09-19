package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A C# record's base list may pass primary-constructor arguments to the
// base (`record Student(string Name) : Person(Name)`); the parens must not
// keep the base type from being parsed.
func TestCSharpRecordBaseWithConstructorArgs(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "P.cs::Person@sha", FilePath: "P.cs", BlobSHA: "sha", Language: "csharp",
			Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "public record Person(string Name)"},
		{ID: "P.cs::IStudent@sha", FilePath: "P.cs", BlobSHA: "sha", Language: "csharp",
			Kind: core.KindInterface, Name: "IStudent", QualifiedName: "IStudent",
			Signature: "public interface IStudent"},
		{ID: "S.cs::Student@sha", FilePath: "S.cs", BlobSHA: "sha", Language: "csharp",
			Kind: core.KindClass, Name: "Student", QualifiedName: "Student",
			Signature: "public record Student(string Name, int Year) : Person(Name), IStudent"},
	}, 2)
	if !hasEdge(g, core.EdgeExtends, "S.cs::Student@sha", "P.cs::Person@sha") {
		t.Fatalf("Student must extend Person despite the base constructor arguments")
	}
	if !hasEdge(g, core.EdgeImplements, "S.cs::Student@sha", "P.cs::IStudent@sha") {
		t.Fatalf("Student must implement IStudent")
	}
	if got := declaredSuperNames(&core.SymbolRecord{Language: "csharp", Kind: core.KindClass,
		Signature: "public record Student(string Name, int Year) : Person(Name, Year), IStudent"}); len(got) != 2 || got[0] != "Person" || got[1] != "IStudent" {
		t.Fatalf("declaredSuperNames = %v, want [Person IStudent]", got)
	}
}
