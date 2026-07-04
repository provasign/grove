package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Jackson pattern: a method calls an abstract method through a *field* whose
// declared type is the abstract base (`_nullSerializer.serialize(null, gen,
// prov)` where `protected JsonSerializer<Object> _nullSerializer;`). The
// field's declared type must narrow the callee to the base declaration (plus
// dispatch), not drop the call site.
func TestBuildCalls_JavaFieldReceiverType(t *testing.T) {
	base := core.SymbolRecord{
		ID: "JsonSerializer.java::JsonSerializer@1", FilePath: "JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "JsonSerializer", QualifiedName: "JsonSerializer",
		Signature: "public abstract class JsonSerializer<T>",
		RawText:   "public abstract class JsonSerializer<T> {\n    public abstract void serialize(T value, JsonGenerator gen, SerializerProvider serializers);\n}",
	}
	baseSerialize := core.SymbolRecord{
		ID: "JsonSerializer.java::JsonSerializer.serialize@1", FilePath: "JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serialize", QualifiedName: "JsonSerializer.serialize", ParentSymbol: "JsonSerializer",
		Signature: "public abstract void serialize(T value, JsonGenerator gen, SerializerProvider serializers)",
	}
	// Unrelated same-named method that must NOT be matched.
	otherSerialize := core.SymbolRecord{
		ID: "Prefetch.java::Prefetch.serialize@1", FilePath: "Prefetch.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serialize", QualifiedName: "Prefetch.serialize", ParentSymbol: "Prefetch",
		Signature: "public void serialize(JsonGenerator gen, Object value, DefaultSerializerProvider prov)",
	}
	writer := core.SymbolRecord{
		ID: "BeanPropertyWriter.java::BeanPropertyWriter@1", FilePath: "BeanPropertyWriter.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "BeanPropertyWriter", QualifiedName: "BeanPropertyWriter",
		Signature: "public class BeanPropertyWriter",
		RawText:   "public class BeanPropertyWriter {\n    protected JsonSerializer<Object> _nullSerializer;\n}",
	}
	caller := core.SymbolRecord{
		ID: "BeanPropertyWriter.java::BeanPropertyWriter.serializeAsField@1", FilePath: "BeanPropertyWriter.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serializeAsField", QualifiedName: "BeanPropertyWriter.serializeAsField", ParentSymbol: "BeanPropertyWriter",
		RawText:   "public void serializeAsField(Object bean, JsonGenerator gen, SerializerProvider prov) {\n    _nullSerializer.serialize(null, gen, prov);\n}",
		Imports:   []string{"JsonSerializer"},
		CallSites: []core.CallSite{{Callee: "_nullSerializer.serialize", Line: 2, Argc: 3, Args: []string{"", "gen", "prov"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{base, baseSerialize, otherSerialize, writer, caller})
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got[baseSerialize.ID] {
		t.Errorf("expected call edge to JsonSerializer.serialize through field-typed receiver; got %v", got)
	}
	if got[otherSerialize.ID] {
		t.Error("unrelated Prefetch.serialize must not be matched")
	}
}

// A file that OVERRIDES a method still calls other types' same-named method
// through typed receivers: same-file shadowing must not hide the cross-file
// declaration from `serializer.serialize(...)` when the local variable's
// declared type is the base class.
func TestBuildCalls_JavaTypedReceiverBeatsSameFileShadow(t *testing.T) {
	base := core.SymbolRecord{
		ID: "JsonSerializer.java::JsonSerializer@1", FilePath: "JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "JsonSerializer", QualifiedName: "JsonSerializer",
		Signature: "public abstract class JsonSerializer<T>",
	}
	baseSerialize := core.SymbolRecord{
		ID: "JsonSerializer.java::JsonSerializer.serialize@1", FilePath: "JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serialize", QualifiedName: "JsonSerializer.serialize", ParentSymbol: "JsonSerializer",
		Signature: "public abstract void serialize(T value, JsonGenerator gen, SerializerProvider serializers)",
	}
	listSer := core.SymbolRecord{
		ID: "IndexedListSerializer.java::IndexedListSerializer@1", FilePath: "IndexedListSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "IndexedListSerializer", QualifiedName: "IndexedListSerializer",
		Signature: "public final class IndexedListSerializer extends JsonSerializer<Object>",
	}
	// The same-file override that used to shadow the real target.
	ownSerialize := core.SymbolRecord{
		ID: "IndexedListSerializer.java::IndexedListSerializer.serialize@1", FilePath: "IndexedListSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serialize", QualifiedName: "IndexedListSerializer.serialize", ParentSymbol: "IndexedListSerializer",
		Signature: "public void serialize(Object value, JsonGenerator gen, SerializerProvider provider)",
	}
	caller := core.SymbolRecord{
		ID: "IndexedListSerializer.java::IndexedListSerializer.serializeContents@1", FilePath: "IndexedListSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serializeContents", QualifiedName: "IndexedListSerializer.serializeContents", ParentSymbol: "IndexedListSerializer",
		RawText: "public void serializeContents(List<?> value, JsonGenerator g, SerializerProvider provider) {\n" +
			"    JsonSerializer<Object> serializer = serializers.serializerFor(cc);\n" +
			"    serializer.serialize(elem, g, provider);\n}",
		Imports:   []string{"JsonSerializer"},
		CallSites: []core.CallSite{{Callee: "serializer.serialize", Line: 3, Argc: 3, Args: []string{"elem", "g", "provider"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{base, baseSerialize, listSer, ownSerialize, caller})
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID {
			got[e.To] = true
		}
	}
	if !got[baseSerialize.ID] {
		t.Errorf("typed receiver must reach JsonSerializer.serialize past the same-file override; got %v", got)
	}
}

// A Java wildcard import (`import com.example.databind.*;`) must bring the
// package's files into scope: the callee's class is named by no explicit
// import, and caller and callee live in different packages.
func TestBuildCalls_JavaWildcardImportScope(t *testing.T) {
	base := core.SymbolRecord{
		ID: "src/main/java/com/example/databind/JsonSerializer.java::JsonSerializer@1", FilePath: "src/main/java/com/example/databind/JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "JsonSerializer", QualifiedName: "JsonSerializer",
		Signature: "public abstract class JsonSerializer<T>",
	}
	baseSerialize := core.SymbolRecord{
		ID: "src/main/java/com/example/databind/JsonSerializer.java::JsonSerializer.serialize@1", FilePath: "src/main/java/com/example/databind/JsonSerializer.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serialize", QualifiedName: "JsonSerializer.serialize", ParentSymbol: "JsonSerializer",
		Signature: "public abstract void serialize(T value, JsonGenerator gen, SerializerProvider serializers)",
	}
	writer := core.SymbolRecord{
		ID: "src/main/java/com/example/databind/ser/BeanPropertyWriter.java::BeanPropertyWriter@1", FilePath: "src/main/java/com/example/databind/ser/BeanPropertyWriter.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass,
		Name: "BeanPropertyWriter", QualifiedName: "BeanPropertyWriter",
		Signature: "public class BeanPropertyWriter",
		RawText:   "public class BeanPropertyWriter {\n    protected JsonSerializer<Object> _nullSerializer;\n}",
	}
	caller := core.SymbolRecord{
		ID: "src/main/java/com/example/databind/ser/BeanPropertyWriter.java::BeanPropertyWriter.serializeAsField@1", FilePath: "src/main/java/com/example/databind/ser/BeanPropertyWriter.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod,
		Name: "serializeAsField", QualifiedName: "BeanPropertyWriter.serializeAsField", ParentSymbol: "BeanPropertyWriter",
		RawText:   "public void serializeAsField(Object bean, JsonGenerator gen, SerializerProvider prov) {\n    _nullSerializer.serialize(null, gen, prov);\n}",
		Imports:   []string{"com.example.databind.*"},
		CallSites: []core.CallSite{{Callee: "_nullSerializer.serialize", Line: 2, Argc: 3, Args: []string{"", "gen", "prov"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{base, baseSerialize, writer, caller})
	found := false
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == baseSerialize.ID {
			found = true
		}
	}
	if !found {
		t.Error("wildcard import must bring the package into scope: missing call edge to JsonSerializer.serialize")
	}
}
