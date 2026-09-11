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

func TestBuildCalls_JavaSameLineAnnotatedFieldReceiver(t *testing.T) {
	target := core.SymbolRecord{ID: "Repo.java::Repo.find", FilePath: "Repo.java", Language: "java", Kind: core.KindMethod, Name: "find", QualifiedName: "Repo.find", ParentSymbol: "Repo"}
	owner := core.SymbolRecord{ID: "Svc.java::Svc", FilePath: "Svc.java", Language: "java", Kind: core.KindClass, Name: "Svc", QualifiedName: "Svc", RawText: "class Svc {\n    @Autowired private Repo repo;\n}"}
	caller := core.SymbolRecord{ID: "Svc.java::Svc.load", FilePath: "Svc.java", Language: "java", Kind: core.KindMethod, Name: "load", QualifiedName: "Svc.load", ParentSymbol: "Svc", RawText: "void load() { repo.find(); }", Span: core.LineRange{Start: 3, End: 3}, CallSites: []core.CallSite{{Callee: "repo.find", Line: 3}}}
	edges := BuildEdges([]core.SymbolRecord{target, owner, caller})
	for _, e := range edges {
		if e.Type == core.EdgeCalls && e.From == caller.ID && e.To == target.ID {
			return
		}
	}
	t.Fatal("same-line annotated Java field did not retain its receiver type")
}

func TestBuildCalls_CPPFieldReceiverType(t *testing.T) {
	widget := core.SymbolRecord{ID: "Widget.h::Widget", FilePath: "Widget.h", Language: "cpp", Kind: core.KindClass, Name: "Widget", QualifiedName: "Widget", RawText: "class Widget { public: void render(); };"}
	render := core.SymbolRecord{ID: "Widget.h::Widget.render", FilePath: "Widget.h", Language: "cpp", Kind: core.KindMethod, Name: "render", QualifiedName: "Widget::render", ParentSymbol: "Widget"}
	otherRender := core.SymbolRecord{ID: "Other.h::Other.render", FilePath: "Other.h", Language: "cpp", Kind: core.KindMethod, Name: "render", QualifiedName: "Other::render", ParentSymbol: "Other"}
	owner := core.SymbolRecord{ID: "Owner.cpp::Owner", FilePath: "Owner.cpp", Language: "cpp", Kind: core.KindClass, Name: "Owner", QualifiedName: "Owner", RawText: "class Owner { Widget field; void go() { field.render(); } };"}
	caller := core.SymbolRecord{ID: "Owner.cpp::Owner.go", FilePath: "Owner.cpp", Language: "cpp", Kind: core.KindMethod, Name: "go", QualifiedName: "Owner::go", ParentSymbol: "Owner", RawText: "void go() { field.render(); }", Span: core.LineRange{Start: 1, End: 1}, CallSites: []core.CallSite{{Callee: "field.render", Line: 1}}}
	edges := BuildEdges([]core.SymbolRecord{widget, render, otherRender, owner, caller})
	found := false
	for _, edge := range edges {
		if edge.Type != core.EdgeCalls || edge.From != caller.ID {
			continue
		}
		if edge.To == render.ID {
			found = true
		}
		if edge.To == otherRender.ID {
			t.Fatal("C++ field receiver resolved to an unrelated same-named method")
		}
	}
	if !found {
		t.Fatal("C++ field receiver did not resolve to its declared type")
	}
}
