package graph

import (
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestCSharpAttributeTypeNames(t *testing.T) {
	idx := newEdgeIndex([]core.SymbolRecord{
		{ID: "a.cs::JsonPropertyAttribute", FilePath: "a.cs", Language: "csharp", Kind: core.KindClass,
			Name: "JsonPropertyAttribute", QualifiedName: "JsonPropertyAttribute"},
	})
	for _, tc := range []struct {
		ann  string
		want []string
	}{
		{`[JsonConverter(typeof(StringEnumConverter), typeof(CamelCaseNamingStrategy))]`,
			[]string{"JsonConverter", "StringEnumConverter", "CamelCaseNamingStrategy"}},
		{`[JsonProperty(NullValueHandling = NullValueHandling.Ignore, Order = 2)]`,
			[]string{"JsonPropertyAttribute", "NullValueHandling"}},
		{`[DataMember(Name = "first_name")]`, []string{"DataMember"}},
		{`[Obsolete("Use TypeNameAssemblyFormatHandling instead.")]`, []string{"Obsolete"}},
		{`[global::System.Data.Linq.Mapping.ColumnAttribute(Storage="_Name", CanBeNull=false)]`, []string{"ColumnAttribute"}},
		{`[CompilationMapping(SourceConstructFlags.UnionCase, 1)]`, []string{"CompilationMapping", "SourceConstructFlags"}},
		{`[return: NotNull, Flags]`, []string{"NotNull", "Flags"}},
		{`[JsonConverter(typeof(Outer.Converter), new object[] { Mode.Fast /* Fake.Thing */ })]`,
			[]string{"JsonConverter", "Converter", "Mode"}},
	} {
		s := core.SymbolRecord{Language: "csharp", Annotations: []string{tc.ann}}
		if got := csharpAttributeTypeNames(idx, &s); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.ann, got, tc.want)
		}
	}
}
