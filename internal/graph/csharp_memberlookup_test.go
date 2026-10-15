package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// C# member lookup over the receiver's type hierarchy, extension-method
// scope and argument conversions (newtonsoft oracle review 2026-10-16:
// P 0.941 → 0.951, R 0.956 → 0.976).

func csCaller(id, file, parent, raw string, start int, imports []string, sites ...core.CallSite) core.SymbolRecord {
	s := csSym(id, file, core.KindMethod, "Run", parent+".Run", parent, "public void Run()", imports, sites...)
	s.RawText = raw
	s.Span = core.LineRange{Start: start, End: start + 20}
	return s
}

func jvalueCtors() []core.SymbolRecord {
	sigs := []string{
		"public JValue(JValue other)", "public JValue(long value)", "public JValue(decimal value)",
		"public JValue(char value)", "public JValue(ulong value)", "public JValue(double value)",
		"public JValue(float value)", "public JValue(DateTime value)", "public JValue(bool value)",
		"public JValue(string? value)", "public JValue(Guid value)", "public JValue(object? value)",
	}
	var out []core.SymbolRecord
	for i, sig := range sigs {
		out = append(out, csSym("JValue.cs::JValue.JValue@sha#"+string(rune('a'+i)), "JValue.cs", core.KindConstructor,
			"JValue", "JValue.JValue", "JValue", sig, nil))
	}
	return out
}

func TestCSharpOverload_ImplicitNumericAndNullableConversions(t *testing.T) {
	var cands []*core.SymbolRecord
	for _, s := range jvalueCtors() {
		s := s
		cands = append(cands, &s)
	}
	for arg, want := range map[string]string{
		"#int": "public JValue(long value)", "#uint": "public JValue(long value)",
		"#byte": "public JValue(long value)", "#short": "public JValue(long value)",
		"#decimal": "public JValue(decimal value)", "#boolean": "public JValue(bool value)",
		"#int?": "public JValue(object? value)", "#char": "public JValue(char value)",
	} {
		got := csNarrowOverloads(nil, cands, []string{arg}, map[string]string{})
		if len(got) != 1 || got[0].Signature != want {
			var sigs []string
			for _, c := range got {
				sigs = append(sigs, c.Signature)
			}
			t.Errorf("%s: got %v, want %s", arg, sigs, want)
		}
	}
}

func TestCSharpMemberLookup_ExtensionOnBaseTypeInScope(t *testing.T) {
	// person is a JObject; IsValid is declared on JToken (JObject's base)
	// by a static class in an imported namespace.
	g := New()
	syms := []core.SymbolRecord{
		csNamespace("Linq/JToken.cs", "Json.Linq", 1, 100),
		csSym("Linq/JToken.cs::JToken@sha", "Linq/JToken.cs", core.KindClass, "JToken", "JToken", "", "public abstract class JToken", nil),
		csSym("Linq/JObject.cs::JObject@sha", "Linq/JObject.cs", core.KindClass, "JObject", "JObject", "", "public class JObject : JToken", nil),
		csNamespace("Schema/Extensions.cs", "Json.Schema", 1, 100),
		csSym("Schema/Extensions.cs::Extensions@sha", "Schema/Extensions.cs", core.KindClass, "Extensions", "Extensions", "", "public static class Extensions", nil),
		csSym("Schema/Extensions.cs::Extensions.IsValid@sha", "Schema/Extensions.cs", core.KindMethod, "IsValid", "Extensions.IsValid", "Extensions",
			"public static bool IsValid(this JToken source, JsonSchema schema)", nil),
		csNamespace("Tests/T.cs", "Json.Tests", 1, 100),
		csCaller("Tests/T.cs::T.Run@sha", "Tests/T.cs", "T", "public void Run()\n{\n    JObject person = JObject.Parse(json);\n    bool valid = person.IsValid(schema);\n}", 10,
			[]string{"Json.Linq", "Json.Schema"},
			core.CallSite{Callee: "person.IsValid", Line: 13, Col: 25, Argc: 1, Args: []string{"schema"}}),
		csNamespace("Tests/U.cs", "Json.Tests", 1, 100),
		csCaller("Tests/U.cs::U.Run@sha", "Tests/U.cs", "U", "public void Run()\n{\n    JObject person = JObject.Parse(json);\n    bool valid = person.IsValid(schema);\n}", 10,
			[]string{"Json.Linq"},
			core.CallSite{Callee: "person.IsValid", Line: 13, Col: 25, Argc: 1, Args: []string{"schema"}}),
	}
	g.Replace(syms, 4)
	if !hasEdge(g, core.EdgeCalls, "Tests/T.cs::T.Run@sha", "Schema/Extensions.cs::Extensions.IsValid@sha") {
		t.Fatalf("person.IsValid(schema) on a JObject must bind the JToken extension method")
	}
	if hasEdge(g, core.EdgeCalls, "Tests/U.cs::U.Run@sha", "Schema/Extensions.cs::Extensions.IsValid@sha") {
		t.Fatalf("an extension method whose namespace the file does not import is not in scope")
	}
}

func TestCSharpMemberLookup_OverloadChosenAcrossHierarchy(t *testing.T) {
	// JsonTextWriter overrides WriteValue(string) only; WriteValue(char?)
	// lives on the base JsonWriter and is the best match for (char?)null.
	g := New()
	syms := []core.SymbolRecord{
		csSym("W.cs::JsonWriter@sha", "W.cs", core.KindClass, "JsonWriter", "JsonWriter", "", "public abstract class JsonWriter", nil),
		csSym("W.cs::JsonWriter.WriteValue@sha#1", "W.cs", core.KindMethod, "WriteValue", "JsonWriter.WriteValue", "JsonWriter", "public virtual void WriteValue(string? value)", nil),
		csSym("W.cs::JsonWriter.WriteValue@sha#2", "W.cs", core.KindMethod, "WriteValue", "JsonWriter.WriteValue", "JsonWriter", "public virtual void WriteValue(char? value)", nil),
		csSym("TW.cs::JsonTextWriter@sha", "TW.cs", core.KindClass, "JsonTextWriter", "JsonTextWriter", "", "public class JsonTextWriter : JsonWriter", nil),
		csSym("TW.cs::JsonTextWriter.WriteValue@sha", "TW.cs", core.KindMethod, "WriteValue", "JsonTextWriter.WriteValue", "JsonTextWriter", "public override void WriteValue(string? value)", nil),
		csCaller("T.cs::T.Run@sha", "T.cs", "T", "public void Run()\n{\n    JsonTextWriter w = new JsonTextWriter(sw);\n    w.WriteValue((char?)null);\n    w.WriteValue(\"x\");\n}", 10, nil,
			core.CallSite{Callee: "w.WriteValue", Line: 13, Col: 7, Argc: 1, Args: []string{"#char?"}},
			core.CallSite{Callee: "w.WriteValue", Line: 14, Col: 7, Argc: 1, Args: []string{"#String"}}),
	}
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeCalls, "T.cs::T.Run@sha", "W.cs::JsonWriter.WriteValue@sha#2") {
		t.Fatalf("WriteValue((char?)null) on a JsonTextWriter must bind the base JsonWriter.WriteValue(char?)")
	}
	if !hasEdge(g, core.EdgeCalls, "T.cs::T.Run@sha", "TW.cs::JsonTextWriter.WriteValue@sha") {
		t.Fatalf("WriteValue(\"x\") must bind the receiver type's override")
	}
	if hasEdge(g, core.EdgeCalls, "T.cs::T.Run@sha", "W.cs::JsonWriter.WriteValue@sha#1") {
		t.Fatalf("an overridden base declaration is the same member as its override")
	}
}

func TestCSharpMemberLookup_StaticPropertyChain(t *testing.T) {
	// Factory.Instance.Create(): astkit reports "Instance.Create"; the chain
	// is recovered from the source line and typed through the static
	// property's declared type (the base factory).
	g := New()
	factory := csSym("F.cs::ExprFactory@sha", "F.cs", core.KindClass, "ExprFactory", "ExprFactory", "", "internal class ExprFactory : BaseFactory", nil)
	factory.RawText = "internal class ExprFactory : BaseFactory\n{\n    internal static BaseFactory Instance => _instance;\n}"
	syms := []core.SymbolRecord{
		factory,
		csSym("B.cs::BaseFactory@sha", "B.cs", core.KindClass, "BaseFactory", "BaseFactory", "", "internal abstract class BaseFactory", nil),
		csSym("B.cs::BaseFactory.Create@sha", "B.cs", core.KindMethod, "Create", "BaseFactory.Create", "BaseFactory", "public object Create(Type t)", nil),
		csSym("O.cs::Other@sha", "O.cs", core.KindClass, "Other", "Other", "", "class Other", nil),
		csSym("O.cs::Other.Create@sha", "O.cs", core.KindMethod, "Create", "Other.Create", "Other", "public object Create(Type t)", nil),
		csCaller("T.cs::T.Run@sha", "T.cs", "T", "public void Run()\n{\n    var o = ExprFactory.Instance.Create(typeof(int));\n}", 10, nil,
			core.CallSite{Callee: "Instance.Create", Line: 12, Col: 34, Argc: 1, Args: []string{"#Type"}}),
	}
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeCalls, "T.cs::T.Run@sha", "B.cs::BaseFactory.Create@sha") {
		t.Fatalf("ExprFactory.Instance.Create must bind BaseFactory.Create through the property's type")
	}
	if hasEdge(g, core.EdgeCalls, "T.cs::T.Run@sha", "O.cs::Other.Create@sha") {
		t.Fatalf("an unrelated Create must not bind")
	}
}
