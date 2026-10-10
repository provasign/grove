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
