// A generic class whose type parameter carries an `extends` constraint
// (`class TreeRepo<Entity extends ObjectLiteral> extends Repo<Entity>`) broke
// base-class parsing: strings.Index found the CONSTRAINT's "extends" first,
// so the real base class was never parsed, the subclass inherited no field
// types, and every receiver chain starting from an inherited field died at
// hop 0. This is exactly typeorm's TreeRepository.findRoots miss
// (`this.manager.connection.driver.escape(...)`) in the ceiling benchmark.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func tsChainFixture() []core.SymbolRecord {
	return []core.SymbolRecord{
		{ID: "repo.ts::TreeRepo@sha", FilePath: "repo.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "TreeRepo", QualifiedName: "TreeRepo",
			Signature: "class TreeRepo<\n    Entity extends ObjectLiteral,\n> extends Repo<Entity>",
			RawText:   "class TreeRepo<\n    Entity extends ObjectLiteral,\n> extends Repo<Entity> {\n}"},
		{ID: "repo.ts::TreeRepo.findRoots@sha", FilePath: "repo.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "findRoots", QualifiedName: "TreeRepo.findRoots", ParentSymbol: "TreeRepo",
			Span:      core.LineRange{Start: 1, End: 1},
			RawText:   "findRoots() { const escapeAlias = (alias: string) => this.manager.connection.driver.escape(alias) }",
			CallSites: []core.CallSite{{Callee: "driver.escape", Line: 1}}},
		{ID: "base.ts::Repo@sha", FilePath: "base.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Repo", QualifiedName: "Repo",
			Signature: "class Repo<Entity extends ObjectLiteral>",
			RawText:   "class Repo<Entity extends ObjectLiteral> {\n    readonly manager: Manager\n}"},
		{ID: "manager.ts::Manager@sha", FilePath: "manager.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Manager", QualifiedName: "Manager",
			Signature: "class Manager",
			RawText:   "class Manager {\n    readonly connection: Source\n}"},
		{ID: "source.ts::Source@sha", FilePath: "source.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Source", QualifiedName: "Source",
			Signature: "class Source",
			RawText:   "class Source {\n    driver: Driver\n}"},
		{ID: "driver.ts::Driver@sha", FilePath: "driver.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindInterface, Name: "Driver", QualifiedName: "Driver",
			Signature: "interface Driver",
			RawText:   "interface Driver {\n    escape(name: string): string\n}"},
		{ID: "driver.ts::Driver.escape@sha", FilePath: "driver.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "escape", QualifiedName: "Driver.escape", ParentSymbol: "Driver",
			Signature: "escape(name: string): string"},
		// Decoy: a same-named method on an unrelated class. The chain resolves
		// to Driver, so the edge must not fan out here.
		{ID: "other.ts::Formatter.escape@sha", FilePath: "other.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindMethod, Name: "escape", QualifiedName: "Formatter.escape", ParentSymbol: "Formatter"},
		{ID: "other.ts::Formatter@sha", FilePath: "other.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "Formatter", QualifiedName: "Formatter",
			Signature: "class Formatter", RawText: "class Formatter {\n}"},
	}
}

func TestCalls_TSGenericConstraintExtendsChain(t *testing.T) {
	g := New()
	g.Replace(tsChainFixture(), 1)

	if !hasEdge(g, core.EdgeCalls, "repo.ts::TreeRepo.findRoots@sha", "driver.ts::Driver.escape@sha") {
		t.Fatalf("missing edge findRoots→Driver.escape: generic-constraint extends must not hide the real base class")
	}
	if hasEdge(g, core.EdgeCalls, "repo.ts::TreeRepo.findRoots@sha", "other.ts::Formatter.escape@sha") {
		t.Fatalf("chain resolved to Driver — edge must not fan out to Formatter.escape")
	}
}

func TestCalls_JavaFieldOfParameterChain(t *testing.T) {
	// jackson's WritableObjectId.writeAsId shape: `w.serializer.serialize(...)`
	// where w is a parameter (ObjectIdWriter) and serializer its field typed
	// JsonSerializer — a 2-hop chain through a field of a parameter, ending on
	// a type the caller's file never imports.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "WritableObjectId.java::WritableObjectId@sha", FilePath: "WritableObjectId.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindClass, Name: "WritableObjectId", QualifiedName: "WritableObjectId",
			Signature: "public class WritableObjectId", RawText: "public class WritableObjectId {\n}"},
		{ID: "WritableObjectId.java::WritableObjectId.writeAsId@sha", FilePath: "WritableObjectId.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "writeAsId", QualifiedName: "WritableObjectId.writeAsId", ParentSymbol: "WritableObjectId",
			Span:      core.LineRange{Start: 1, End: 3},
			RawText:   "public boolean writeAsId(JsonGenerator gen, SerializerProvider provider, ObjectIdWriter w) {\n    w.serializer.serialize(id, gen, provider);\n}",
			CallSites: []core.CallSite{{Callee: "serializer.serialize", Line: 2, Argc: 3}}},
		{ID: "ObjectIdWriter.java::ObjectIdWriter@sha", FilePath: "ObjectIdWriter.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindClass, Name: "ObjectIdWriter", QualifiedName: "ObjectIdWriter",
			Signature: "public class ObjectIdWriter",
			RawText:   "public class ObjectIdWriter {\n    public final JsonSerializer<Object> serializer;\n}"},
		{ID: "JsonSerializer.java::JsonSerializer@sha", FilePath: "JsonSerializer.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindClass, Name: "JsonSerializer", QualifiedName: "JsonSerializer",
			Signature: "public abstract class JsonSerializer<T>",
			RawText:   "public abstract class JsonSerializer<T> {\n}"},
		{ID: "JsonSerializer.java::JsonSerializer.serialize@sha", FilePath: "JsonSerializer.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "serialize", QualifiedName: "JsonSerializer.serialize", ParentSymbol: "JsonSerializer",
			Signature: "public abstract void serialize(T value, JsonGenerator gen, SerializerProvider serializers)"},
		// Decoy: same-named method on an unrelated class.
		{ID: "Other.java::Codec.serialize@sha", FilePath: "Other.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "serialize", QualifiedName: "Codec.serialize", ParentSymbol: "Codec"},
		{ID: "Other.java::Codec@sha", FilePath: "Other.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindClass, Name: "Codec", QualifiedName: "Codec",
			Signature: "public class Codec", RawText: "public class Codec {\n}"},
	}, 1)

	if !hasEdge(g, core.EdgeCalls, "WritableObjectId.java::WritableObjectId.writeAsId@sha", "JsonSerializer.java::JsonSerializer.serialize@sha") {
		t.Fatalf("missing edge writeAsId→JsonSerializer.serialize: Java field-of-parameter chain must resolve")
	}
	if hasEdge(g, core.EdgeCalls, "WritableObjectId.java::WritableObjectId.writeAsId@sha", "Other.java::Codec.serialize@sha") {
		t.Fatalf("chain resolved to JsonSerializer — edge must not fan out to Codec.serialize")
	}
}

func TestTSBaseClasses_GenericConstraintForms(t *testing.T) {
	cases := []struct {
		sig  string
		want string
	}{
		{"class TreeRepo<\n    Entity extends ObjectLiteral,\n> extends Repo<Entity>", "Repo"},
		{"class A<T extends B> extends C implements D", "C"},
		{"class A extends B<T>", "B"},
		{"class A<T = () => void> extends B", "B"},
		{"class A<T extends B>", ""}, // constraint only, no real base
	}
	for _, tc := range cases {
		idx := newEdgeIndex([]core.SymbolRecord{{
			ID: "x.ts::A@sha", FilePath: "x.ts", BlobSHA: "sha",
			Language: "typescript", Kind: core.KindClass, Name: "A",
			QualifiedName: "A", Signature: tc.sig, RawText: tc.sig + " {\n}"}})
		got := tsBaseClasses(idx, "A", "")
		if tc.want == "" {
			if len(got) != 0 {
				t.Errorf("sig %q: want no base, got %v", tc.sig, got)
			}
		} else if len(got) != 1 || got[0] != tc.want {
			t.Errorf("sig %q: want [%s], got %v", tc.sig, tc.want, got)
		}
	}
}
