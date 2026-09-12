package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestJavaCallEdgesDoNotBindDifferentArityOverload(t *testing.T) {
	baseFile := "src/main/java/com/fasterxml/jackson/databind/JsonDeserializer.java"
	callerFile := "src/main/java/com/fasterxml/jackson/databind/deser/BeanDeserializerBase.java"
	base := core.SymbolRecord{ID: "base", FilePath: baseFile, Language: "java", Kind: core.KindClass, Name: "JsonDeserializer", QualifiedName: "JsonDeserializer"}
	two := core.SymbolRecord{ID: "two", FilePath: baseFile, Language: "java", Kind: core.KindMethod, Name: "deserialize", QualifiedName: "JsonDeserializer.deserialize", ParentSymbol: "JsonDeserializer", Signature: "T deserialize(JsonParser p, DeserializationContext ctxt)"}
	three := core.SymbolRecord{ID: "three", FilePath: baseFile, Language: "java", Kind: core.KindMethod, Name: "deserialize", QualifiedName: "JsonDeserializer.deserialize", ParentSymbol: "JsonDeserializer", Signature: "T deserialize(JsonParser p, DeserializationContext ctxt, T intoValue)"}
	beanType := core.SymbolRecord{ID: "bean-type", FilePath: callerFile, Language: "java", Kind: core.KindClass, Name: "BeanDeserializerBase", QualifiedName: "BeanDeserializerBase", Signature: "class BeanDeserializerBase extends JsonDeserializer<Object>"}
	caller := core.SymbolRecord{
		ID: "caller", FilePath: callerFile, Language: "java", Kind: core.KindMethod,
		Name: "handlePolymorphic", QualifiedName: "BeanDeserializerBase.handlePolymorphic", ParentSymbol: "BeanDeserializerBase",
		Imports:   []string{"com.fasterxml.jackson.databind.JsonDeserializer"},
		Signature: "Object handlePolymorphic(JsonParser p, DeserializationContext ctxt, Object bean)",
		RawText:   "Object handlePolymorphic(JsonParser p, DeserializationContext ctxt, Object bean) { JsonDeserializer<Object> subDeser = find(); subDeser.deserialize(p, ctxt, bean); return deserialize(p, ctxt, bean); }",
		CallSites: []core.CallSite{{Callee: "subDeser.deserialize", Argc: 3, Args: []string{"p", "ctxt", "bean"}}, {Callee: "deserialize", Argc: 3, Args: []string{"p", "ctxt", "bean"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{base, two, three, beanType, caller})
	if !javaHasCall(edges, caller.ID, three.ID) {
		t.Fatal("three-argument call lost its matching overload")
	}
	if javaHasCall(edges, caller.ID, two.ID) {
		t.Fatal("three-argument call incorrectly bound the two-argument overload")
	}
}

func TestJavaNestedBareCallBindsEnclosingMethod(t *testing.T) {
	contractFile := "src/main/java/org/apache/commons/collections4/Transformer.java"
	helperFile := "src/main/java/org/apache/commons/collections4/collection/TransformedCollection.java"
	callerFile := "src/main/java/org/apache/commons/collections4/list/TransformedList.java"
	contract := core.SymbolRecord{ID: "contract", FilePath: contractFile, Language: "java", Kind: core.KindInterface, Name: "Transformer", QualifiedName: "Transformer"}
	contractTransform := core.SymbolRecord{ID: "contract-transform", FilePath: contractFile, Language: "java", Kind: core.KindMethod, Name: "transform", QualifiedName: "Transformer.transform", ParentSymbol: "Transformer", Signature: "O transform(I input)"}
	helperType := core.SymbolRecord{ID: "helper-type", FilePath: helperFile, Language: "java", Kind: core.KindClass, Name: "TransformedCollection", QualifiedName: "TransformedCollection"}
	helper := core.SymbolRecord{ID: "helper", FilePath: helperFile, Language: "java", Kind: core.KindMethod, Name: "transform", QualifiedName: "TransformedCollection.transform", ParentSymbol: "TransformedCollection", Signature: "E transform(E object)"}
	outer := core.SymbolRecord{ID: "outer", FilePath: callerFile, Language: "java", Kind: core.KindClass, Name: "TransformedList", QualifiedName: "TransformedList", Signature: "class TransformedList extends TransformedCollection"}
	inner := core.SymbolRecord{ID: "inner", FilePath: callerFile, Language: "java", Kind: core.KindClass, Name: "TransformedListIterator", QualifiedName: "TransformedList.TransformedListIterator", ParentSymbol: "TransformedList", Signature: "class TransformedListIterator"}
	caller := core.SymbolRecord{
		ID: "caller", FilePath: callerFile, Language: "java", Kind: core.KindMethod,
		Name: "add", QualifiedName: "TransformedList.TransformedListIterator.add", ParentSymbol: "TransformedListIterator",
		Imports:   []string{"org.apache.commons.collections4.Transformer", "org.apache.commons.collections4.collection.TransformedCollection"},
		Signature: "void add(E object)", RawText: "void add(E object) { object = transform(object); }",
		CallSites: []core.CallSite{{Callee: "transform", Argc: 1, Args: []string{"object"}}},
	}
	setter := core.SymbolRecord{
		ID: "setter", FilePath: callerFile, Language: "java", Kind: core.KindMethod,
		Name: "set", QualifiedName: "TransformedList.TransformedListIterator.set", ParentSymbol: "TransformedListIterator",
		Signature: "void set(E object)", RawText: "void set(E object) { getListIterator().set(transform(object)); }",
		CallSites: []core.CallSite{{Callee: "transform", Argc: 1, Args: []string{"object"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{contract, contractTransform, helperType, helper, outer, inner, caller, setter})
	for _, site := range []core.SymbolRecord{caller, setter} {
		if !javaHasCall(edges, site.ID, helper.ID) {
			t.Errorf("%s: nested class must call the inherited method of its enclosing class", site.Name)
		}
		if javaHasCall(edges, site.ID, contractTransform.ID) {
			t.Errorf("%s: nested class bare call incorrectly bound an unrelated interface method", site.Name)
		}
	}
}

func TestJavaArityCountsGenericParameters(t *testing.T) {
	for _, tc := range []struct {
		signature string
		raw       string
		want      int
	}{
		{"void serialize(Map<?,?> value, JsonGenerator gen, SerializerProvider provider)", "@Override\npublic void serialize(Map<?,?> value, JsonGenerator gen, SerializerProvider provider) {}", 3},
		{"void serializeWithType(Map<?,?> value, JsonGenerator gen, SerializerProvider provider, TypeSerializer typeSer)", "@Override\npublic void serializeWithType(Map<?,?> value, JsonGenerator gen, SerializerProvider provider, TypeSerializer typeSer) {}", 4},
	} {
		symbol := core.SymbolRecord{Language: "java", Signature: tc.signature, RawText: tc.raw}
		n, _, ok := declParamCount(&symbol)
		if !ok || n != tc.want {
			t.Errorf("%s: count = %d, parsed = %v; want %d", tc.signature, n, ok, tc.want)
		}
	}
}
