package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// C# lookup-order rules measured on newtonsoft (P 0.901 → 0.935): each
// fixture is the minimal shape of a false edge before the rule.

func csSym(id, file string, kind core.SymbolKind, name, qualified, parent, sig string, imports []string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "csharp", Kind: kind,
		Name: name, QualifiedName: qualified, ParentSymbol: parent, Signature: sig,
		Imports: imports, CallSites: sites, Span: core.LineRange{Start: 10, End: 20},
	}
}

func csNamespace(file, name string, start, end int) core.SymbolRecord {
	return core.SymbolRecord{
		ID: file + "::" + name + "@sha", FilePath: file, BlobSHA: "sha", Language: "csharp",
		Kind: core.KindNamespace, Name: name, QualifiedName: name, Span: core.LineRange{Start: start, End: end},
	}
}

func TestCSharpConstructor_NamespaceLookupOrder(t *testing.T) {
	// Three `Person` classes. The caller imports TestObjects, whose Person
	// declares no constructor: `new Person()` is its implicit constructor
	// (no symbol), never the explicit constructors of the others.
	g := New()
	syms := []core.SymbolRecord{
		csNamespace("Tests/Converters/IsoTests.cs", "Newtonsoft.Json.Tests.Converters", 1, 100),
		csSym("Tests/Converters/IsoTests.cs::IsoTests.BlogCodeSample@sha", "Tests/Converters/IsoTests.cs", core.KindMethod, "BlogCodeSample", "IsoTests.BlogCodeSample", "IsoTests", "public void BlogCodeSample()",
			[]string{"Newtonsoft.Json.Tests.TestObjects"},
			core.CallSite{Callee: "Person", Line: 12, Argc: 0}),
		csNamespace("Tests/TestObjects/Person.cs", "Newtonsoft.Json.Tests.TestObjects", 1, 100),
		csSym("Tests/TestObjects/Person.cs::Person@sha", "Tests/TestObjects/Person.cs", core.KindClass, "Person", "Person", "", "public class Person", nil),
		csNamespace("Tests/Documentation/PerformanceTests.cs", "Newtonsoft.Json.Tests.Documentation", 1, 100),
		csSym("Tests/Documentation/PerformanceTests.cs::Person@sha", "Tests/Documentation/PerformanceTests.cs", core.KindClass, "Person", "Person", "", "public class Person", nil),
		csSym("Tests/Documentation/PerformanceTests.cs::Person.Person@sha", "Tests/Documentation/PerformanceTests.cs", core.KindConstructor, "Person", "Person.Person", "Person", "public Person()", nil),
		csNamespace("Tests/LinqToSql/Classes.cs", "Newtonsoft.Json.Tests.LinqToSql", 1, 100),
		csSym("Tests/LinqToSql/Classes.cs::Person@sha", "Tests/LinqToSql/Classes.cs", core.KindClass, "Person", "Person", "", "public partial class Person", nil),
		csSym("Tests/LinqToSql/Classes.cs::Person.Person@sha", "Tests/LinqToSql/Classes.cs", core.KindConstructor, "Person", "Person.Person", "Person", "public Person()", nil),
	}
	g.Replace(syms, 4)
	for _, wrong := range []string{"Tests/Documentation/PerformanceTests.cs::Person.Person@sha", "Tests/LinqToSql/Classes.cs::Person.Person@sha"} {
		if hasEdge(g, core.EdgeCalls, "Tests/Converters/IsoTests.cs::IsoTests.BlogCodeSample@sha", wrong) {
			t.Fatalf("new Person() must not bind a Person in a namespace the caller does not see: %s", wrong)
		}
	}
}

func TestCSharpConstructor_NestedTypeWinsAndInvisibleDrops(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		csNamespace("A.cs", "Samples.Serializer", 1, 100),
		csSym("A.cs::Sample@sha", "A.cs", core.KindClass, "Sample", "Sample", "", "public class Sample", nil),
		// Nested Sample.Person with no constructor: innermost scope wins.
		csSym("A.cs::Sample.Person@sha", "A.cs", core.KindClass, "Person", "Sample.Person", "Sample", "public class Person", nil),
		csSym("A.cs::Sample.Example@sha", "A.cs", core.KindMethod, "Example", "Sample.Example", "Sample", "public static void Example()", nil,
			core.CallSite{Callee: "Person", Line: 12, Argc: 0},
			core.CallSite{Callee: "StreamWriter", Line: 13, Argc: 1, Args: []string{"path"}}),
		// An enclosing-namespace Person WITH a constructor is further out.
		csNamespace("B.cs", "Samples", 1, 100),
		csSym("B.cs::Person@sha", "B.cs", core.KindClass, "Person", "Person", "", "public class Person", nil),
		csSym("B.cs::Person.Person@sha", "B.cs", core.KindConstructor, "Person", "Person.Person", "Person", "public Person()", nil),
		// A test-only StreamWriter in a namespace nobody imports: `new
		// StreamWriter(path)` is System.IO's.
		csNamespace("C.cs", "Other.Docs", 1, 100),
		csSym("C.cs::StreamWriter@sha", "C.cs", core.KindClass, "StreamWriter", "StreamWriter", "", "public class StreamWriter", nil),
		csSym("C.cs::StreamWriter.StreamWriter@sha", "C.cs", core.KindConstructor, "StreamWriter", "StreamWriter.StreamWriter", "StreamWriter", "public StreamWriter(string path)", nil),
	}
	g.Replace(syms, 3)
	if hasEdge(g, core.EdgeCalls, "A.cs::Sample.Example@sha", "B.cs::Person.Person@sha") {
		t.Fatalf("nested Sample.Person (implicit ctor) must shadow the enclosing namespace's Person")
	}
	if hasEdge(g, core.EdgeCalls, "A.cs::Sample.Example@sha", "C.cs::StreamWriter.StreamWriter@sha") {
		t.Fatalf("new StreamWriter(..) must not bind a StreamWriter in an unrelated namespace")
	}
}

func TestCSharpBareCall_OwnTypeChainOnly(t *testing.T) {
	// `Equals(a, b)` with no own-class Equals is object.Equals; a bare
	// call never reaches an unrelated class's method. Explicit interface
	// implementations are never direct targets.
	g := New()
	syms := []core.SymbolRecord{
		csNamespace("T.cs", "Tests", 1, 100),
		csSym("T.cs::Base@sha", "T.cs", core.KindClass, "Base", "Base", "", "public class Base", nil),
		csSym("T.cs::Base.Helper@sha", "T.cs", core.KindMethod, "Helper", "Base.Helper", "Base", "protected void Helper()", nil),
		csSym("T.cs::Derived@sha", "T.cs", core.KindClass, "Derived", "Derived", "", "public class Derived : Base", nil),
		csSym("T.cs::Derived.Run@sha", "T.cs", core.KindMethod, "Run", "Derived.Run", "Derived", "public void Run(object a, object b)", nil,
			core.CallSite{Callee: "Equals", Line: 12, Argc: 2, Args: []string{"a", "b"}},
			core.CallSite{Callee: "Helper", Line: 13, Argc: 0}),
		csNamespace("S.cs", "Tests", 1, 100),
		csSym("S.cs::StringAssert@sha", "S.cs", core.KindClass, "StringAssert", "StringAssert", "", "public static class StringAssert", nil),
		csSym("S.cs::StringAssert.Equals@sha", "S.cs", core.KindMethod, "Equals", "StringAssert.Equals", "StringAssert", "public static void Equals(string expected, string actual)", nil),
		csSym("J.cs::JContainer@sha", "J.cs", core.KindClass, "JContainer", "JContainer", "", "public abstract class JContainer", nil),
		csSym("J.cs::JContainer.IList.Add@sha", "J.cs", core.KindMethod, "Add", "JContainer.System.Collections.IList.Add", "JContainer", "int System.Collections.IList.Add(object value)", nil),
		csSym("J.cs::JContainer.Add@sha", "J.cs", core.KindMethod, "Add", "JContainer.Add", "JContainer", "public virtual void Add(object content)", nil),
		{ID: "U.cs::Use.M@sha", FilePath: "U.cs", BlobSHA: "sha", Language: "csharp", Kind: core.KindMethod,
			Name: "M", QualifiedName: "Use.M", ParentSymbol: "Use", Signature: "public void M(JContainer c)",
			RawText:   "public void M(JContainer c) { c.Add(1); }",
			CallSites: []core.CallSite{{Callee: "c.Add", Line: 12, Argc: 1, Args: []string{"#int"}}}},
	}
	g.Replace(syms, 4)
	if hasEdge(g, core.EdgeCalls, "T.cs::Derived.Run@sha", "S.cs::StringAssert.Equals@sha") {
		t.Fatalf("bare Equals(a, b) must not bind an unrelated class's Equals")
	}
	if !hasEdge(g, core.EdgeCalls, "T.cs::Derived.Run@sha", "T.cs::Base.Helper@sha") {
		t.Fatalf("bare Helper() must still reach an inherited member")
	}
	if hasEdge(g, core.EdgeCalls, "U.cs::Use.M@sha", "J.cs::JContainer.IList.Add@sha") {
		t.Fatalf("an explicit interface implementation is never a direct call target")
	}
	if !hasEdge(g, core.EdgeCalls, "U.cs::Use.M@sha", "J.cs::JContainer.Add@sha") {
		_, edges := g.Snapshot()
		var from []string
		for _, e := range edges {
			if e.From == "U.cs::Use.M@sha" {
				from = append(from, string(e.Type)+"→"+e.To+" "+string(e.Reason))
			}
		}
		t.Fatalf("c.Add(1) must bind the public Add; edges from Use.M: %v", from)
	}
}

func TestCSharpReceivers_IndexerElementAndKnownTypeWithoutMember(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		csSym("O.cs::JObject@sha", "O.cs", core.KindClass, "JObject", "JObject", "", "public class JObject : JContainer", nil),
		csSym("O.cs::JObject.this[]@sha", "O.cs", core.KindField, "this[]", "JObject.this[]", "JObject", "public JToken this[string propertyName]", nil),
		csSym("K.cs::JToken@sha", "K.cs", core.KindClass, "JToken", "JToken", "", "public abstract class JToken", nil),
		csSym("K.cs::JToken.Children@sha", "K.cs", core.KindMethod, "Children", "JToken.Children", "JToken", "public virtual JEnumerable<JToken> Children()", nil),
		csSym("X.cs::JTokenTests.Children@sha", "X.cs", core.KindMethod, "Children", "JTokenTests.Children", "JTokenTests", "public void Children()", nil),
		csSym("N.cs::NamingStrategy@sha", "N.cs", core.KindClass, "NamingStrategy", "NamingStrategy", "", "public abstract class NamingStrategy", nil),
		csSym("R.cs::ReflectionObject.GetType@sha", "R.cs", core.KindMethod, "GetType", "ReflectionObject.GetType", "ReflectionObject", "public Type GetType(string member)", nil),
		{ID: "U.cs::Use.M@sha", FilePath: "U.cs", BlobSHA: "sha", Language: "csharp", Kind: core.KindMethod,
			Name: "M", QualifiedName: "Use.M", ParentSymbol: "Use", Signature: "public void M(JObject o)",
			RawText: "public void M(JObject o) { o[\"x\"].Children(); NamingStrategy.GetType(); }",
			CallSites: []core.CallSite{
				{Callee: "o[].Children", Line: 1},
				{Callee: "NamingStrategy.GetType", Line: 1},
			}},
	}
	g.Replace(syms, 5)
	if !hasEdge(g, core.EdgeCalls, "U.cs::Use.M@sha", "K.cs::JToken.Children@sha") {
		t.Fatalf("o[\"x\"].Children() must type the element by JObject's indexer (JToken)")
	}
	if hasEdge(g, core.EdgeCalls, "U.cs::Use.M@sha", "X.cs::JTokenTests.Children@sha") {
		t.Fatalf("an indexer-typed receiver must not fan out to a same-named test method")
	}
	if hasEdge(g, core.EdgeCalls, "U.cs::Use.M@sha", "R.cs::ReflectionObject.GetType@sha") {
		t.Fatalf("Type.GetType() on an indexed type without that member is the runtime's, not ReflectionObject.GetType")
	}
}
