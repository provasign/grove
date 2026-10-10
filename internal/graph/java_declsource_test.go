package graph

import (
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestJavaParamTypes_CommentInsideLeadingAnnotation(t *testing.T) {
	// jackson-databind BuilderCreatorSubtype4742Test, with a decoy comment
	// in the annotation's argument list.
	m := core.SymbolRecord{
		Language: "java", Kind: core.KindMethod, Name: "properties",
		RawText: "@JsonSubTypes({ // don't 'quote \"unterminated (\n" +
			"        @JsonSubTypes.Type(name = \"bird\", value = Bird.class) /* ) */\n})\n" +
			"public abstract Builder properties(AnimalProperties properties);",
	}
	if got := javaParamTypes(&m); !reflect.DeepEqual(got, []string{"AnimalProperties"}) {
		t.Fatalf("param types = %v, want [AnimalProperties]", got)
	}
}

func TestJavaLocalTypes_LocalDeclaredWithoutInitializer(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "T.java::T.test", FilePath: "T.java", Language: "java", Kind: core.KindMethod,
		Name: "test", QualifiedName: "T.test", ParentSymbol: "T",
		RawText: "public void test() {\n  Map<K, V> map;\n  try {\n    map = make();\n  } catch (RuntimeException e) {\n    return;\n  }\n  // Wrong other;\n  map.size();\n}",
	}
	types := javaLocalTypes(newEdgeIndex([]core.SymbolRecord{caller}), &caller)
	if types["map"] != "Map" {
		t.Fatalf("map = %q, want Map", types["map"])
	}
	if _, ok := types["other"]; ok {
		t.Fatalf("a commented-out declaration typed other = %q", types["other"])
	}
}
