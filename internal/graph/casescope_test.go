package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// --- Fix A: case-sensitive contains attachment -----------------------------
//
// gin's shape: interface ResponseWriter + struct responseWriter (Go's
// idiomatic exported/unexported pair) in one file. The lowercase byName key
// conflated them, so the interface got contains edges to the struct's
// methods — change-impact then found a "real" declaration through the bogus
// edge, skipped synthesis, and reported an empty family.
func TestBuildContains_CaseSensitiveParents(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "rw.go::ResponseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "ResponseWriter", QualifiedName: "ResponseWriter",
			RawText: "type ResponseWriter interface {\n\tStatus() int\n\tSize() int\n}"},
		{ID: "rw.go::responseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "responseWriter", QualifiedName: "responseWriter",
			RawText: "type responseWriter struct{}"},
		{ID: "rw.go::responseWriter.Status@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Status", QualifiedName: "responseWriter.Status",
			ParentSymbol: "responseWriter", Signature: "func (w *responseWriter) Status() int"},
		{ID: "rw.go::responseWriter.Size@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Size", QualifiedName: "responseWriter.Size",
			ParentSymbol: "responseWriter", Signature: "func (w *responseWriter) Size() int"},
	}
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeContains && e.From == "rw.go::ResponseWriter@1" {
			t.Fatalf("interface must not contain the case-distinct struct's method: %+v", e)
		}
	}
}

func TestChangeImpact_CasePairInterfaceResolves(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "rw.go::ResponseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "ResponseWriter", QualifiedName: "ResponseWriter",
			RawText: "type ResponseWriter interface {\n\tStatus() int\n\tSize() int\n}"},
		{ID: "rw.go::responseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "responseWriter", QualifiedName: "responseWriter",
			RawText: "type responseWriter struct{}"},
		{ID: "rw.go::responseWriter.Status@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Status", QualifiedName: "responseWriter.Status",
			ParentSymbol: "responseWriter", Signature: "func (w *responseWriter) Status() int"},
		{ID: "rw.go::responseWriter.Size@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Size", QualifiedName: "responseWriter.Size",
			ParentSymbol: "responseWriter", Signature: "func (w *responseWriter) Size() int"},
	}, 3)
	r, err := g.ChangeImpact("ResponseWriter.Status")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	// The declaration is the interface's own spec (synthesized), never the
	// concrete method reached through a case-conflated contains edge.
	for _, d := range r.Declarations {
		if d.ParentSymbol == "responseWriter" {
			t.Fatalf("declaration poisoned by case-conflated contains edge: %+v", d)
		}
	}
	famNames := map[string]bool{}
	for _, f := range r.Family {
		famNames[f.QualifiedName] = true
	}
	if !famNames["responseWriter.Status"] {
		t.Fatalf("family must include the concrete implementation, got %v", famNames)
	}
	if len(r.DeclaringTypes) != 1 || r.DeclaringTypes[0].Name != "ResponseWriter" {
		t.Fatalf("DeclaringTypes = %v, want the interface", r.DeclaringTypes)
	}
}

// --- Fix B: nested-qualified supertype resolution ---------------------------
//
// jackson's shape: `extends ValueInstantiator.Base` resolved by bare "Base"
// to every type named Base in the repo (30+), including NumberSerializers.Base
// which extends into the JsonSerializer hierarchy — polluting every closure.
func TestResolveTypeEdges_NestedQualifierScopes(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "VI.java::ValueInstantiator@1", FilePath: "VI.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "ValueInstantiator", QualifiedName: "ValueInstantiator",
			Signature: "public abstract class ValueInstantiator"},
		{ID: "VI.java::ValueInstantiator.Base@1", FilePath: "VI.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "Base", QualifiedName: "ValueInstantiator.Base",
			ParentSymbol: "ValueInstantiator", Signature: "public static class Base extends ValueInstantiator"},
		// The constructor shares the class name — must never be a supertype target.
		{ID: "VI.java::Base.Base@1", FilePath: "VI.java", BlobSHA: "1", Language: "java",
			Kind: core.KindMethod, Name: "Base", QualifiedName: "Base.Base",
			ParentSymbol: "Base", Signature: "protected Base(Class<?> type)"},
		// Unrelated same-named nested type in another hierarchy.
		{ID: "NS.java::NumberSerializers@1", FilePath: "NS.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "NumberSerializers", QualifiedName: "NumberSerializers",
			Signature: "public class NumberSerializers"},
		{ID: "NS.java::NumberSerializers.Base@1", FilePath: "NS.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "Base", QualifiedName: "NumberSerializers.Base",
			ParentSymbol: "NumberSerializers", Signature: "public abstract static class Base extends StdSerializer"},
		{ID: "JVI.java::JDKValueInstantiator@1", FilePath: "JVI.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "JDKValueInstantiator", QualifiedName: "JDKValueInstantiator",
			Signature: "abstract static class JDKValueInstantiator extends ValueInstantiator.Base"},
	}
	var targets []string
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeExtends && e.From == "JVI.java::JDKValueInstantiator@1" {
			targets = append(targets, e.To)
		}
	}
	if len(targets) != 1 || targets[0] != "VI.java::ValueInstantiator.Base@1" {
		t.Fatalf("extends ValueInstantiator.Base must resolve to exactly the nested type, got %v", targets)
	}
}

// Suffix boundary: a qualifier of "Serializers" must not accept
// NumberSerializers.Base (the string suffix matches without a dot boundary —
// the exact leak observed on jackson after the first round of this fix).
func TestResolveTypeEdges_QualifierSuffixBoundary(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "S.java::Serializers@1", FilePath: "S.java", BlobSHA: "1", Language: "java",
			Kind: core.KindInterface, Name: "Serializers", QualifiedName: "Serializers",
			Signature: "public interface Serializers"},
		{ID: "S.java::Serializers.Base@1", FilePath: "S.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "Base", QualifiedName: "Serializers.Base",
			ParentSymbol: "Serializers", Signature: "public static class Base implements Serializers"},
		{ID: "NS.java::NumberSerializers.Base@1", FilePath: "NS.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "Base", QualifiedName: "NumberSerializers.Base",
			ParentSymbol: "NumberSerializers", Signature: "public abstract static class Base"},
		{ID: "C.java::CoreXMLSerializers@1", FilePath: "C.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "CoreXMLSerializers", QualifiedName: "CoreXMLSerializers",
			Signature: "public class CoreXMLSerializers extends Serializers.Base"},
	}
	var targets []string
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeExtends && e.From == "C.java::CoreXMLSerializers@1" {
			targets = append(targets, e.To)
		}
	}
	if len(targets) != 1 || targets[0] != "S.java::Serializers.Base@1" {
		t.Fatalf("qualifier Serializers must not match NumberSerializers.Base, got %v", targets)
	}
}

// Package-qualified externals ("java.util.List") keep bare-name fallback:
// the lowercase qualifier is a package path, not a nested-type scope.
func TestResolveTypeEdges_PackageQualifierFallsBack(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "L.java::List@1", FilePath: "L.java", BlobSHA: "1", Language: "java",
			Kind: core.KindInterface, Name: "List", QualifiedName: "List",
			Signature: "public interface List"},
		{ID: "M.java::MyList@1", FilePath: "M.java", BlobSHA: "1", Language: "java",
			Kind: core.KindClass, Name: "MyList", QualifiedName: "MyList",
			Signature: "public class MyList implements custom.pkg.List"},
	}
	found := false
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeImplements && e.From == "M.java::MyList@1" && e.To == "L.java::List@1" {
			found = true
		}
	}
	if !found {
		t.Fatal("package-qualified supertype must still resolve by simple name")
	}
}

// Go struct-embeds-interface: embedding promotes the interface's method set
// onto the struct (delegation), so the embedding type is COVERED for
// missing-implementations purposes — gin's interceptedWriter embeds
// ResponseWriter, declares no Status() of its own, and compiles.
func TestMissingImplementations_GoEmbeddedInterfaceIsCoverage(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "rw.go::ResponseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "ResponseWriter", QualifiedName: "ResponseWriter",
			RawText: "type ResponseWriter interface {\n\tStatus() int\n}"},
		{ID: "rw.go::responseWriter@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "responseWriter", QualifiedName: "responseWriter",
			RawText: "type responseWriter struct{}"},
		{ID: "rw.go::responseWriter.Status@1", FilePath: "rw.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Status", QualifiedName: "responseWriter.Status",
			ParentSymbol: "responseWriter", Signature: "func (w *responseWriter) Status() int",
			RawText: "func (w *responseWriter) Status() int { return 0 }"},
		// Embeds the interface; declares no Status of its own.
		{ID: "t.go::interceptedWriter@1", FilePath: "t.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "interceptedWriter", QualifiedName: "interceptedWriter",
			RawText: "type interceptedWriter struct {\n\tResponseWriter\n\tbody *bytes.Buffer\n}"},
	}, 3)
	r, err := g.MissingImplementations("ResponseWriter.Status")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	for _, m := range r.Missing {
		if m.Name == "interceptedWriter" {
			t.Fatalf("embedding type wrongly flagged missing: %v", r.Missing)
		}
	}
}

// --- Fix C: rename-plan edits the interface member spec line ---------------
//
// The synthesized declaration has no RawText, so the plan previously dropped
// the contract's own spec line into Unresolved: an applied plan renamed every
// impl and caller but left the interface declaring the old name.
func TestRenamePlan_GoInterfaceSpecLineEdited(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "cache.go::DataKeyCache@1", FilePath: "cache.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "DataKeyCache", QualifiedName: "DataKeyCache",
			Span:    core.LineRange{Start: 10, End: 14},
			RawText: "type DataKeyCache interface {\n\t// GetById looks a key up.\n\tGetById(id string) (string, bool)\n\tFlush()\n}"},
		{ID: "impl.go::ossCache@1", FilePath: "impl.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "ossCache", QualifiedName: "ossCache",
			RawText: "type ossCache struct{}"},
		{ID: "impl.go::ossCache.GetById@1", FilePath: "impl.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "GetById", QualifiedName: "ossCache.GetById",
			ParentSymbol: "ossCache", Span: core.LineRange{Start: 3, End: 5},
			Signature: "func (c *ossCache) GetById(id string) (string, bool)",
			RawText:   "func (c *ossCache) GetById(id string) (string, bool) {\n\treturn \"\", false\n}"},
		{ID: "impl.go::ossCache.Flush@1", FilePath: "impl.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Flush", QualifiedName: "ossCache.Flush",
			ParentSymbol: "ossCache", Span: core.LineRange{Start: 7, End: 8},
			Signature: "func (c *ossCache) Flush()", RawText: "func (c *ossCache) Flush() {\n}"},
	}, 3)
	r, err := g.RenamePlan("DataKeyCache.GetById", "GetDataKeyById")
	if err != nil {
		t.Fatalf("RenamePlan: %v", err)
	}
	// Spec line: type span starts at 10; GetById sits on the third line (offset 2).
	specEdited := false
	for _, e := range r.Edits {
		if e.FilePath == "cache.go" && e.Line == 12 &&
			strings.Contains(e.After, "GetDataKeyById(id string)") {
			specEdited = true
		}
	}
	if !specEdited {
		t.Fatalf("interface spec line not edited; edits=%v", r.Edits)
	}
	for _, u := range r.Unresolved {
		if strings.HasPrefix(u, "cache.go:") {
			t.Fatalf("contract spec must not be Unresolved anymore: %v", r.Unresolved)
		}
	}
}

// --- Review round 2: Go named non-struct types, zero-implementor interfaces,
//     rename-from-implementation, interface-embeds-interface ---

// A method on a named non-struct type (`type Status int`) is Go's idiomatic
// non-struct receiver; excluding KindType from buildContains made it and its
// whole interface-satisfaction invisible.
func TestChangeImpact_GoNamedNonStructType(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "x.go::Stringer@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "Stringer", QualifiedName: "Stringer",
			RawText: "type Stringer interface {\n\tString() string\n}"},
		{ID: "x.go::Status@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindType, Name: "Status", QualifiedName: "Status",
			RawText: "type Status int"},
		{ID: "x.go::Status.String@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "String", QualifiedName: "Status.String",
			ParentSymbol: "Status", Signature: "func (s Status) String() string",
			RawText: "func (s Status) String() string { return \"\" }"},
	}, 3)
	r, err := g.ChangeImpact("Stringer.String")
	if err != nil {
		t.Fatalf("ChangeImpact errored on named non-struct type: %v", err)
	}
	found := false
	for _, f := range r.Family {
		if f.QualifiedName == "Status.String" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Status.String (method on a named type) missing from family: %+v", r.Family)
	}
	mi, err := g.MissingImplementations("Stringer.String")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	if mi.ImplementedCount != 1 {
		t.Fatalf("ImplementedCount = %d, want 1 (Status implements it)", mi.ImplementedCount)
	}
}

// change-impact on an interface method with zero current implementors is an
// ordinary "what breaks if I change this" query; the empty-set gate ran
// before the contract-synthesis step and threw a false "declares no method".
func TestChangeImpact_InterfaceWithNoImplementors(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "x.go::Greeter@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "Greeter", QualifiedName: "Greeter",
			RawText: "type Greeter interface {\n\tGreet(name string) string\n}"},
	}, 1)
	r, err := g.ChangeImpact("Greeter.Greet")
	if err != nil {
		t.Fatalf("ChangeImpact errored on zero-implementor interface: %v", err)
	}
	if len(r.Declarations) != 1 || len(r.DeclaringTypes) != 1 {
		t.Fatalf("want synthesized decl + declaringType, got decls=%v declaringTypes=%v",
			r.Declarations, r.DeclaringTypes)
	}
}

// A rename seeded on an IMPLEMENTATION must still edit the interface's own
// member spec — it arrives via Supers, which Sites() excluded, so rename-plan
// dropped the contract declaration and produced a build-breaking plan.
func TestRenamePlan_FromImplementationEditsInterfaceSpec(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "x.go::Reader@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "Reader", QualifiedName: "Reader",
			Span:    core.LineRange{Start: 3, End: 5},
			RawText: "type Reader interface {\n\tRead(name string) string\n}"},
		{ID: "x.go::FileImpl@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindStruct, Name: "FileImpl", QualifiedName: "FileImpl",
			Span: core.LineRange{Start: 6, End: 6}, RawText: "type FileImpl struct{}"},
		{ID: "x.go::FileImpl.Read@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindMethod, Name: "Read", QualifiedName: "FileImpl.Read",
			ParentSymbol: "FileImpl", Span: core.LineRange{Start: 7, End: 7},
			Signature: "func (f FileImpl) Read(name string) string",
			RawText:   "func (f FileImpl) Read(name string) string { return name }"},
	}, 3)
	r, err := g.RenamePlan("FileImpl.Read", "ReadFile")
	if err != nil {
		t.Fatalf("RenamePlan: %v", err)
	}
	specEdited := false
	for _, e := range r.Edits {
		if e.Line == 4 && strings.Contains(e.After, "ReadFile(name string) string") &&
			!strings.Contains(e.After, "func") {
			specEdited = true
		}
	}
	if !specEdited {
		t.Fatalf("interface spec line (Reader.Read) not edited; edits=%+v", r.Edits)
	}
	for _, u := range r.Unresolved {
		t.Fatalf("nothing should be Unresolved, got %v", u)
	}
}

// An interface embedding another interface must produce an extends edge (the
// embedded method set is promoted); the Go branch of buildExtendsImplements
// only processed structs.
func TestBuildEdges_GoInterfaceEmbedsInterface(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "x.go::Reader@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "Reader", QualifiedName: "Reader",
			RawText: "type Reader interface {\n\tRead() string\n}"},
		{ID: "x.go::ReadWriter@1", FilePath: "x.go", BlobSHA: "1", Language: "go",
			Kind: core.KindInterface, Name: "ReadWriter", QualifiedName: "ReadWriter",
			RawText: "type ReadWriter interface {\n\tReader\n\tWrite(s string)\n}"},
	}
	found := false
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeExtends && e.From == "x.go::ReadWriter@1" && e.To == "x.go::Reader@1" {
			found = true
		}
	}
	if !found {
		t.Fatal("ReadWriter embedding Reader must produce an extends edge")
	}
}

// C# uses `class X : Base, IFoo` (colon syntax). Without a graph-layer parse,
// C# inheritance edges existed only when the native roslyn analyzer ran; a
// bare source tree got zero edges and an empty change-impact closure.
func TestBuildEdges_CSharpColonInheritance(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "s.cs::IShape@1", FilePath: "s.cs", BlobSHA: "1", Language: "csharp",
			Kind: core.KindInterface, Name: "IShape", QualifiedName: "IShape",
			Signature: "public interface IShape"},
		{ID: "s.cs::Circle@1", FilePath: "s.cs", BlobSHA: "1", Language: "csharp",
			Kind: core.KindClass, Name: "Circle", QualifiedName: "Circle",
			Signature: "public class Circle : IShape"},
		{ID: "s.cs::Base@1", FilePath: "s.cs", BlobSHA: "1", Language: "csharp",
			Kind: core.KindClass, Name: "Base", QualifiedName: "Base",
			Signature: "public class Base"},
		{ID: "s.cs::Derived@1", FilePath: "s.cs", BlobSHA: "1", Language: "csharp",
			Kind: core.KindClass, Name: "Derived", QualifiedName: "Derived",
			Signature: "public class Derived : Base, IShape where T : struct"},
	}
	var impl, ext bool
	for _, e := range BuildEdges(syms) {
		if e.Type == core.EdgeImplements && e.From == "s.cs::Circle@1" && e.To == "s.cs::IShape@1" {
			impl = true
		}
		if e.Type == core.EdgeExtends && e.From == "s.cs::Derived@1" && e.To == "s.cs::Base@1" {
			ext = true
		}
	}
	if !impl {
		t.Error("Circle : IShape must produce an implements edge")
	}
	if !ext {
		t.Error("Derived : Base (base class first) must produce an extends edge, and the where-clause must not corrupt parsing")
	}
}
