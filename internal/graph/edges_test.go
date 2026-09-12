package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// ─── extends / implements / uses-type ────────────────────────────────────────

func TestExtendsEdgeTypeScript(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/auth.ts::Base@sha", FilePath: "src/auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "src/auth.ts::Child@sha", FilePath: "src/auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Child", QualifiedName: "Child", Signature: "class Child extends Base"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "src/auth.ts::Child@sha", "src/auth.ts::Base@sha") {
		t.Fatalf("missing extends edge Child→Base")
	}
}

func TestImplementsEdgeJava(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Service.java::Runnable@sha", FilePath: "Service.java", Language: "java", Kind: core.KindInterface, Name: "Runnable", QualifiedName: "Runnable"},
		{ID: "Service.java::MyService@sha", FilePath: "Service.java", Language: "java", Kind: core.KindClass, Name: "MyService", QualifiedName: "MyService", Signature: "public class MyService implements Runnable"},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "Service.java::MyService@sha", "Service.java::Runnable@sha") {
		t.Fatalf("missing implements edge MyService→Runnable")
	}
}

func TestGoBareCallNeverBindsMethods(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "x.go::Alpha.Close@sha", FilePath: "x.go", Language: "go", Kind: core.KindMethod, Name: "Close", ParentSymbol: "Alpha", Signature: "func (a Alpha) Close() error"},
		{ID: "x.go::Beta.Close@sha", FilePath: "x.go", Language: "go", Kind: core.KindMethod, Name: "Close", ParentSymbol: "Beta", Signature: "func (b Beta) Close() error"},
		{ID: "x.go::Close@sha", FilePath: "x.go", Language: "go", Kind: core.KindFunction, Name: "Close", Signature: "func Close(n int) error"},
		{ID: "x.go::useFree@sha", FilePath: "x.go", Language: "go", Kind: core.KindFunction, Name: "useFree",
			RawText: "func useFree() error { return Close(3) }", Span: core.LineRange{Start: 4, End: 4},
			CallSites: []core.CallSite{{Callee: "Close", Line: 4, Argc: 1}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "x.go::useFree@sha", "x.go::Close@sha") {
		t.Fatal("bare Go call did not bind package function")
	}
	for _, method := range []string{"x.go::Alpha.Close@sha", "x.go::Beta.Close@sha"} {
		if hasEdge(g, core.EdgeCalls, "x.go::useFree@sha", method) {
			t.Fatalf("bare Go call bound method %s", method)
		}
	}
}

func TestFanoutCapRunsAfterTypedReceiverNarrowing(t *testing.T) {
	var symbols []core.SymbolRecord
	for i := 0; i < 20; i++ {
		name := "R" + itoa(i)
		symbols = append(symbols,
			core.SymbolRecord{ID: name, FilePath: "all.cs", Language: "csharp", Kind: core.KindClass, Name: name},
			core.SymbolRecord{ID: name + ".Save", FilePath: "all.cs", Language: "csharp", Kind: core.KindMethod, Name: "Save", ParentSymbol: name, Signature: "void Save(int value)"})
	}
	symbols = append(symbols, core.SymbolRecord{ID: "Use", FilePath: "all.cs", Language: "csharp", Kind: core.KindMethod,
		Name: "Use", ParentSymbol: "Program", RawText: "void Use() { R1 r = new R1(); r.Save(1); }", Span: core.LineRange{Start: 1, End: 1},
		CallSites: []core.CallSite{{Callee: "r.Save", Line: 1, Argc: 1}}})
	g := New()
	g.Replace(symbols, 1)
	if !hasEdge(g, core.EdgeCalls, "Use", "R1.Save") {
		t.Fatal("typed receiver lost its target above the global fan-out cap")
	}
	for i := 0; i < 20; i++ {
		if i != 1 && hasEdge(g, core.EdgeCalls, "Use", "R"+itoa(i)+".Save") {
			t.Fatalf("typed receiver retained R%d.Save", i)
		}
	}
}

func TestStripCommentsAndStringsPreservesExecutableSyntax(t *testing.T) {
	for name, tc := range map[string]struct {
		input string
		want  string
	}{
		"template interpolation": {"return `value=${g.label()}`;", "g.label()"},
		"python f-string":        {"return f'value={g.label()}'", "g.label()"},
		"csharp interpolation":   {"return $\"value={g.Label()}\";", "g.Label()"},
		"rust lifetime":          {"fn f<'a>(x: &'a str) { target(); }", "target()"},
		"csharp verbatim":        {"var p = @\"C:\\dir\\\"; target();", "target()"},
		"javascript regex":       {"s.replace(/[\"']/g, ''); target();", "target()"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stripCommentsAndStrings(tc.input); !strings.Contains(got, tc.want) {
				t.Fatalf("stripped = %q, want it to retain %q", got, tc.want)
			}
		})
	}
}

func TestCSharpSemicolonRecordImplementsEdge(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "I.cs::IPerson", FilePath: "I.cs", Language: "csharp", Kind: core.KindInterface, Name: "IPerson", QualifiedName: "IPerson"},
		{ID: "P.cs::Person", FilePath: "P.cs", Language: "csharp", Kind: core.KindClass, Name: "Person", QualifiedName: "Person", Signature: "public record Person(string Name) : IPerson;"},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "P.cs::Person", "I.cs::IPerson") {
		t.Fatal("semicolon-terminated positional record lost its base list")
	}
}

func TestPHPInheritanceEdgesWithoutNativeAnalyzer(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "contracts.php::Runnable", FilePath: "contracts.php", Language: "php", Kind: core.KindInterface, Name: "Runnable", QualifiedName: "Runnable", Signature: "interface Runnable"},
		{ID: "base.php::Base", FilePath: "base.php", Language: "php", Kind: core.KindClass, Name: "Base", QualifiedName: "Base", Signature: "class Base"},
		{ID: "worker.php::Worker", FilePath: "worker.php", Language: "php", Kind: core.KindClass, Name: "Worker", QualifiedName: "Worker", Signature: "class Worker extends Base implements Runnable"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "worker.php::Worker", "base.php::Base") {
		t.Fatal("missing PHP extends edge without native analysis")
	}
	if !hasEdge(g, core.EdgeImplements, "worker.php::Worker", "contracts.php::Runnable") {
		t.Fatal("missing PHP implements edge without native analysis")
	}
}

// Generic bounds must not emit bogus extends edges, and generic arguments in
// the extends clause must still resolve to the base class (jackson style:
// `class ValueSer<T extends Number> extends JsonSerializer<T>`).
func TestExtendsEdgeJavaGenerics(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.java::JsonSerializer@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "JsonSerializer", QualifiedName: "JsonSerializer"},
		{ID: "a.java::Number@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "Number", QualifiedName: "Number"},
		{ID: "a.java::ValueSer@sha", FilePath: "a.java", Language: "java", Kind: core.KindClass, Name: "ValueSer", QualifiedName: "ValueSer",
			Signature: "public class ValueSer<T extends Number> extends JsonSerializer<T>"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "a.java::ValueSer@sha", "a.java::JsonSerializer@sha") {
		t.Fatalf("missing extends edge ValueSer→JsonSerializer through generics")
	}
	if hasEdge(g, core.EdgeExtends, "a.java::ValueSer@sha", "a.java::Number@sha") {
		t.Fatalf("generic bound `T extends Number` must not emit an extends edge")
	}
}

func TestPythonClassBaseExtends(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "models.py::Base@sha", FilePath: "models.py", Language: "python", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "models.py::User@sha", FilePath: "models.py", Language: "python", Kind: core.KindClass, Name: "User", QualifiedName: "User",
			RawText: "class User(Base):\n    pass\n"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "models.py::User@sha", "models.py::Base@sha") {
		t.Fatalf("missing python extends edge")
	}
}

func TestRustImplForTrait(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "lib.rs::Display@sha", FilePath: "lib.rs", Language: "rust", Kind: core.KindTrait, Name: "Display", QualifiedName: "Display"},
		{ID: "lib.rs::Point@sha", FilePath: "lib.rs", Language: "rust", Kind: core.KindStruct, Name: "Point", QualifiedName: "Point",
			RawText: "struct Point { x: i32 }\nimpl Display for Point { fn fmt() {} }"},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "lib.rs::Point@sha", "lib.rs::Display@sha") {
		t.Fatalf("missing rust implements edge")
	}
}

func TestRustImplAnnotationFromAstkitBuildsEdge(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "lib.rs::Greet", FilePath: "lib.rs", Language: "rust", Kind: core.KindTrait, Name: "Greet", QualifiedName: "Greet"},
		{ID: "lib.rs::Fish", FilePath: "lib.rs", Language: "rust", Kind: core.KindStruct, Name: "Fish", QualifiedName: "Fish", RawText: "struct Fish;", Annotations: []string{"implements:Greet"}},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "lib.rs::Fish", "lib.rs::Greet") {
		t.Fatal("missing Rust implements edge from Astkit type annotation")
	}
}

func TestRustPubRestrictedUseDoesNotLookExternal(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "src/lib.rs::crate", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindModule, Name: "crate"},
		{ID: "src/a.rs::helper", FilePath: "src/a.rs", Language: "rust", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper"},
		{ID: "src/b.rs::run", FilePath: "src/b.rs", Language: "rust", Kind: core.KindFunction, Name: "run", QualifiedName: "run", Imports: []string{"pub(crate) use crate::a::helper"}, RawText: "fn run() { helper(); }", CallSites: []core.CallSite{{Callee: "helper", Line: 1}}},
	}
	g := New()
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeCalls, "src/b.rs::run", "src/a.rs::helper") {
		t.Fatal("pub(crate) use was treated as an external import")
	}
}

func TestRustFacadeReExportReachesTargetCrate(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "crates/core/src/lib.rs::crate", FilePath: "crates/core/src/lib.rs", Language: "rust", Kind: core.KindModule, Name: "crate"},
		{ID: "crates/core/src/hiargs.rs::via_use", FilePath: "crates/core/src/hiargs.rs", Language: "rust", Kind: core.KindFunction, Name: "via_use", QualifiedName: "via_use", Imports: []string{"grep::printer::StandardBuilder"}, RawText: "fn via_use() { StandardBuilder::new().build(); }", Span: core.LineRange{Start: 1, End: 1}, CallSites: []core.CallSite{{Callee: "StandardBuilder.new", Line: 1}, {Callee: "build", Line: 1}}},
		{ID: "crates/grep/src/lib.rs::crate", FilePath: "crates/grep/src/lib.rs", Language: "rust", Kind: core.KindModule, Name: "crate", Imports: []string{"pub use grep_printer as printer"}},
		{ID: "crates/printer/src/lib.rs::StandardBuilder", FilePath: "crates/printer/src/lib.rs", Language: "rust", Kind: core.KindStruct, Name: "StandardBuilder", QualifiedName: "StandardBuilder"},
		{ID: "crates/printer/src/lib.rs::StandardBuilder.new", FilePath: "crates/printer/src/lib.rs", Language: "rust", Kind: core.KindConstructor, Name: "new", QualifiedName: "StandardBuilder.new", ParentSymbol: "StandardBuilder"},
		{ID: "crates/printer/src/lib.rs::StandardBuilder.build", FilePath: "crates/printer/src/lib.rs", Language: "rust", Kind: core.KindMethod, Name: "build", QualifiedName: "StandardBuilder.build", ParentSymbol: "StandardBuilder"},
	}
	g := New()
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeCalls, "crates/core/src/hiargs.rs::via_use", "crates/printer/src/lib.rs::StandardBuilder.new") {
		t.Fatal("call through Rust facade re-export did not reach target crate")
	}
}

func TestRustContainsCrossFileImplInSameCrate(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "src/lib.rs::crate", FilePath: "src/lib.rs", Language: "rust", Kind: core.KindModule, Name: "crate"},
		{ID: "src/a.rs::Thing", FilePath: "src/a.rs", Language: "rust", Kind: core.KindStruct, Name: "Thing", QualifiedName: "Thing"},
		{ID: "src/b.rs::Thing.bump", FilePath: "src/b.rs", Language: "rust", Kind: core.KindMethod, Name: "bump", QualifiedName: "Thing.bump", ParentSymbol: "Thing"},
	}
	g := New()
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeContains, "src/a.rs::Thing", "src/b.rs::Thing.bump") {
		t.Fatal("cross-file Rust impl method was not attached to its type")
	}
}

func TestPythonNestedFunctionContainsAndImpactReachesParent(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "app.py::helper", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "helper", QualifiedName: "helper", Span: core.LineRange{Start: 1, End: 1}},
		{ID: "app.py::outer", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "outer", QualifiedName: "outer", Span: core.LineRange{Start: 3, End: 8}},
		{ID: "app.py::outer.inner", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "inner", QualifiedName: "outer.inner", ParentSymbol: "outer", Span: core.LineRange{Start: 4, End: 6}, CallSites: []core.CallSite{{Callee: "helper", Line: 5}}},
		// Same leaf name, same file, but not the lexical parent.
		{ID: "app.py::other.outer", FilePath: "app.py", Language: "python", Kind: core.KindFunction, Name: "outer", QualifiedName: "other.outer", Span: core.LineRange{Start: 10, End: 12}},
	}
	g := New()
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeContains, "app.py::outer", "app.py::outer.inner") {
		t.Fatal("nested Python function was not attached to its lexical parent")
	}
	if hasEdge(g, core.EdgeContains, "app.py::other.outer", "app.py::outer.inner") {
		t.Fatal("same-named non-parent function claimed nested child")
	}
	got := g.Impact("helper", 3)
	foundOuter := false
	for _, symbol := range got {
		foundOuter = foundOuter || symbol.ID == "app.py::outer"
	}
	if !foundOuter {
		t.Fatalf("impact did not traverse nested child to lexical parent: %+v", got)
	}
}

func TestCSharpExtensionMethodAndBaseConstructorArity(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "Base.cs::Base", FilePath: "Base.cs", Language: "csharp", Kind: core.KindClass, Name: "Base", Signature: "class Base"},
		{ID: "Base.cs::Base.ctor0", FilePath: "Base.cs", Language: "csharp", Kind: core.KindConstructor, Name: "Base", ParentSymbol: "Base", Signature: "Base()"},
		{ID: "Base.cs::Base.ctor1", FilePath: "Base.cs", Language: "csharp", Kind: core.KindConstructor, Name: "Base", ParentSymbol: "Base", Signature: "Base(int value)"},
		{ID: "Child.cs::Child", FilePath: "Child.cs", Language: "csharp", Kind: core.KindClass, Name: "Child", Signature: "class Child : Base"},
		{ID: "Child.cs::Child.ctor", FilePath: "Child.cs", Language: "csharp", Kind: core.KindConstructor, Name: "Child", ParentSymbol: "Child", Signature: "Child() : base(1)", RawText: "Child() : base(1) {}", CallSites: []core.CallSite{{Callee: "super()", Line: 1, Argc: 1}}},
		{ID: "User.cs::User", FilePath: "User.cs", Language: "csharp", Kind: core.KindClass, Name: "User"},
		{ID: "Ext.cs::Ext.Shout", FilePath: "Ext.cs", Language: "csharp", Kind: core.KindMethod, Name: "Shout", ParentSymbol: "Ext", Signature: "public static string Shout(this User user)"},
		{ID: "Use.cs::Use.Run", FilePath: "Use.cs", Language: "csharp", Kind: core.KindMethod, Name: "Run", ParentSymbol: "Use", Signature: "void Run(User user)", RawText: "void Run(User user) { user.Shout(); }", CallSites: []core.CallSite{{Callee: "user.Shout", Line: 1}}},
	}
	if got := csharpLocalTypes(newEdgeIndex(syms), &syms[len(syms)-1])["user"]; got != "User" {
		t.Fatalf("extension receiver type = %q, want User", got)
	}
	g := New()
	g.Replace(syms, 4)
	if !hasEdge(g, core.EdgeCalls, "Use.cs::Use.Run", "Ext.cs::Ext.Shout") {
		t.Fatal("C# extension method receiver did not resolve")
	}
	if !hasEdge(g, core.EdgeCalls, "Child.cs::Child.ctor", "Base.cs::Base.ctor1") {
		t.Fatal("base(1) did not resolve to the one-argument base constructor")
	}
	if hasEdge(g, core.EdgeCalls, "Child.cs::Child.ctor", "Base.cs::Base.ctor0") {
		t.Fatal("base(1) resolved to the zero-argument base constructor")
	}
}

func TestCSharpConstructedReceiverExcludesUnrelatedMethod(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "Dog.cs::Dog", FilePath: "Dog.cs", Language: "csharp", Kind: core.KindClass, Name: "Dog", QualifiedName: "Dog"},
		{ID: "Dog.cs::Dog.Move", FilePath: "Dog.cs", Language: "csharp", Kind: core.KindMethod, Name: "Move", QualifiedName: "Dog.Move", ParentSymbol: "Dog"},
		{ID: "Other.cs::Other", FilePath: "Other.cs", Language: "csharp", Kind: core.KindClass, Name: "Other", QualifiedName: "Other"},
		{ID: "Other.cs::Other.Move", FilePath: "Other.cs", Language: "csharp", Kind: core.KindMethod, Name: "Move", QualifiedName: "Other.Move", ParentSymbol: "Other"},
		{ID: "Svc.cs::Svc.Chain", FilePath: "Svc.cs", Language: "csharp", Kind: core.KindMethod, Name: "Chain", QualifiedName: "Svc.Chain", ParentSymbol: "Svc", RawText: "void Chain() { new Dog().Move(); }", Span: core.LineRange{Start: 1, End: 1}, CallSites: []core.CallSite{{Callee: "Dog().Move", Line: 1}}},
	}
	g := New()
	g.Replace(syms, 1)
	if !hasEdge(g, core.EdgeCalls, "Svc.cs::Svc.Chain", "Dog.cs::Dog.Move") {
		t.Fatal("constructed C# receiver did not resolve to its method")
	}
	if hasEdge(g, core.EdgeCalls, "Svc.cs::Svc.Chain", "Other.cs::Other.Move") {
		t.Fatal("constructed C# receiver retained an unrelated same-named method")
	}
}

func TestCSharpPropertyBodyEmitsCalls(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "Svc.cs::Svc.Compute", FilePath: "Svc.cs", Language: "csharp", Kind: core.KindMethod, Name: "Compute", QualifiedName: "Svc.Compute", ParentSymbol: "Svc"},
		{ID: "Svc.cs::Svc.Age", FilePath: "Svc.cs", Language: "csharp", Kind: core.KindField, Name: "Age", QualifiedName: "Svc.Age", ParentSymbol: "Svc", RawText: "public int Age => Compute();", CallSites: []core.CallSite{{Callee: "Compute", Line: 1}}},
	}
	g := New()
	g.Replace(syms, 1)
	if !hasEdge(g, core.EdgeCalls, "Svc.cs::Svc.Age", "Svc.cs::Svc.Compute") {
		t.Fatal("C# property body call was not added to the graph")
	}
}

func TestPythonRelativeImportScopeDoesNotCrossPackages(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "alpha/models.py::Record", FilePath: "alpha/models.py", Language: "python", Kind: core.KindClass, Name: "Record", QualifiedName: "Record"},
		{ID: "alpha/models.py::Record.persist", FilePath: "alpha/models.py", Language: "python", Kind: core.KindMethod, Name: "persist", QualifiedName: "Record.persist", ParentSymbol: "Record"},
		{ID: "beta/models.py::Record", FilePath: "beta/models.py", Language: "python", Kind: core.KindClass, Name: "Record", QualifiedName: "Record"},
		{ID: "beta/models.py::Record.persist", FilePath: "beta/models.py", Language: "python", Kind: core.KindMethod, Name: "persist", QualifiedName: "Record.persist", ParentSymbol: "Record"},
		{ID: "alpha/svc.py::save", FilePath: "alpha/svc.py", Language: "python", Kind: core.KindFunction, Name: "save", QualifiedName: "save", Imports: []string{".models"}, RawText: "def save():\n    r = Record()\n    r.persist()", Span: core.LineRange{Start: 1, End: 3}, CallSites: []core.CallSite{{Callee: "Record", Line: 2}, {Callee: "r.persist", Line: 3}}},
	}
	g := New()
	g.Replace(syms, 1)
	if !hasEdge(g, core.EdgeCalls, "alpha/svc.py::save", "alpha/models.py::Record.persist") {
		t.Fatal("missing call to the relatively imported Record.persist")
	}
	if hasEdge(g, core.EdgeCalls, "alpha/svc.py::save", "beta/models.py::Record.persist") {
		t.Fatal("relative import leaked to beta/models.py")
	}
}

func TestTypeScriptTypedReceiverKeepsBaseBesideSameFileOverride(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "base.ts::A", FilePath: "base.ts", Language: "typescript", Kind: core.KindClass, Name: "A", QualifiedName: "A", RawText: "export class A { m() {} }"},
		{ID: "base.ts::A.m", FilePath: "base.ts", Language: "typescript", Kind: core.KindMethod, Name: "m", QualifiedName: "A.m", ParentSymbol: "A"},
		{ID: "consumer.ts::B", FilePath: "consumer.ts", Language: "typescript", Kind: core.KindClass, Name: "B", QualifiedName: "B", Signature: "class B extends A", RawText: "class B extends A { m() {} }", Imports: []string{"./base"}},
		{ID: "consumer.ts::B.m", FilePath: "consumer.ts", Language: "typescript", Kind: core.KindMethod, Name: "m", QualifiedName: "B.m", ParentSymbol: "B"},
		{ID: "consumer.ts::User", FilePath: "consumer.ts", Language: "typescript", Kind: core.KindClass, Name: "User", QualifiedName: "User", RawText: "class User { a: A; run() { this.a.m(); } }", Imports: []string{"./base"}},
		{ID: "consumer.ts::User.run", FilePath: "consumer.ts", Language: "typescript", Kind: core.KindMethod, Name: "run", QualifiedName: "User.run", ParentSymbol: "User", RawText: "run() { this.a.m(); }", Span: core.LineRange{Start: 1, End: 1}, Imports: []string{"./base"}, CallSites: []core.CallSite{{Callee: "a.m", Line: 1}}},
	}
	g := New()
	g.Replace(syms, 1)
	if !hasEdge(g, core.EdgeCalls, "consumer.ts::User.run", "base.ts::A.m") {
		t.Fatal("typed A receiver lost the base declaration to same-file B.m")
	}
}

func TestRustGenericTraitImplDispatchAndSupertraitDefault(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "lib.rs::Sink@1", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindTrait, Name: "Sink", QualifiedName: "Sink", Signature: "trait Sink"},
		{ID: "lib.rs::Sink.flush@2", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindMethod, Name: "flush", QualifiedName: "Sink.flush", ParentSymbol: "Sink"},
		{ID: "lib.rs::Matcher@3", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindTrait, Name: "Matcher", QualifiedName: "Matcher", Signature: "trait Matcher: Sink"},
		{ID: "lib.rs::Matcher.matches@4", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindMethod, Name: "matches", QualifiedName: "Matcher.matches", ParentSymbol: "Matcher"},
		{ID: "lib.rs::Concrete@5", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindStruct, Name: "Concrete", QualifiedName: "Concrete", RawText: "struct Concrete;\nimpl Matcher<Result<u8>> for Concrete {}"},
		{ID: "lib.rs::Concrete.matches@6", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindMethod, Name: "matches", QualifiedName: "Concrete.matches", ParentSymbol: "Concrete"},
		{ID: "lib.rs::Concrete.run@7", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindMethod, Name: "run", QualifiedName: "Concrete.run", ParentSymbol: "Concrete", Annotations: []string{"impl_trait:Matcher"}, RawText: "fn run(&self) { self.flush(); }", CallSites: []core.CallSite{{Callee: "self.flush", Line: 1}}},
		{ID: "lib.rs::drive@8", FilePath: "lib.rs", BlobSHA: "1", Language: "rust", Kind: core.KindFunction, Name: "drive", QualifiedName: "drive", Signature: "fn drive(m: &dyn Matcher)", RawText: "fn drive(m: &dyn Matcher) { m.matches(); }", CallSites: []core.CallSite{{Callee: "m.matches", Line: 1}}},
	}, 1)
	if !hasEdge(g, core.EdgeImplements, "lib.rs::Concrete@5", "lib.rs::Matcher@3") {
		t.Fatal("missing generic trait implementation edge")
	}
	if !hasEdge(g, core.EdgeExtends, "lib.rs::Matcher@3", "lib.rs::Sink@1") {
		t.Fatal("missing supertrait inheritance edge")
	}
	if !hasEdge(g, core.EdgeCalls, "lib.rs::drive@8", "lib.rs::Concrete.matches@6") {
		t.Fatal("missing trait-object dispatch to concrete override")
	}
	if !hasEdge(g, core.EdgeCalls, "lib.rs::Concrete.run@7", "lib.rs::Sink.flush@2") {
		t.Fatal("missing default-method resolution through supertrait")
	}
}

func TestPHPParentAndStaticCallsResolveNominally(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "base.php::Base@1", FilePath: "base.php", BlobSHA: "1", Language: "php", Kind: core.KindClass, Name: "Base", QualifiedName: "Base", Signature: "class Base"},
		{ID: "base.php::Base.work@2", FilePath: "base.php", BlobSHA: "1", Language: "php", Kind: core.KindMethod, Name: "work", QualifiedName: "Base.work", ParentSymbol: "Base"},
		{ID: "child.php::Child@1", FilePath: "child.php", BlobSHA: "1", Language: "php", Kind: core.KindClass, Name: "Child", QualifiedName: "Child", Signature: "class Child extends Base"},
		{ID: "child.php::Child.work@2", FilePath: "child.php", BlobSHA: "1", Language: "php", Kind: core.KindMethod, Name: "work", QualifiedName: "Child.work", ParentSymbol: "Child"},
		{ID: "child.php::Child.run@3", FilePath: "child.php", BlobSHA: "1", Language: "php", Kind: core.KindMethod, Name: "run", QualifiedName: "Child.run", ParentSymbol: "Child", RawText: "function run() { parent::work(); static::work(); }", CallSites: []core.CallSite{{Callee: "parent.work", Line: 1}, {Callee: "static.work", Line: 1}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "child.php::Child.run@3", "base.php::Base.work@2") {
		t.Fatal("parent:: call did not resolve to base method")
	}
	if !hasEdge(g, core.EdgeCalls, "child.php::Child.run@3", "child.php::Child.work@2") {
		t.Fatal("static:: call did not resolve to current class")
	}
}

func TestTypeScriptArrowFieldIsCallable(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "widget.ts::Widget@1", FilePath: "widget.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindClass, Name: "Widget", QualifiedName: "Widget", Signature: "class Widget"},
		{ID: "widget.ts::Widget.handle@2", FilePath: "widget.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindField, Name: "handle", QualifiedName: "Widget.handle", ParentSymbol: "Widget", Signature: "handle = () => 1", RawText: "handle = () => 1"},
		{ID: "widget.ts::Widget.run@3", FilePath: "widget.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindMethod, Name: "run", QualifiedName: "Widget.run", ParentSymbol: "Widget", RawText: "run() { return this.handle() }", CallSites: []core.CallSite{{Callee: "this.handle", Line: 1}}},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "widget.ts::Widget.run@3", "widget.ts::Widget.handle@2") {
		t.Fatal("call to function-valued class field was dropped")
	}
}

func TestGoStructEmbedding(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Reader@sha", FilePath: "a.go", Language: "go", Kind: core.KindStruct, Name: "Reader", QualifiedName: "Reader"},
		{ID: "a.go::Wrapper@sha", FilePath: "a.go", Language: "go", Kind: core.KindStruct, Name: "Wrapper", QualifiedName: "Wrapper",
			RawText: "type Wrapper struct {\n\tReader\n\tname string\n}"},
	}, 1)
	if !hasEdge(g, core.EdgeExtends, "a.go::Wrapper@sha", "a.go::Reader@sha") {
		t.Fatalf("missing go embedding extends edge")
	}
}

func TestUsesTypeScopedToImports(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		// Importer file imports "./auth" — so symbols in auth.ts are in scope.
		{ID: "main.ts::handle@sha", FilePath: "main.ts", Language: "typescript",
			Kind: core.KindFunction, Name: "handle", QualifiedName: "handle",
			Signature: "function handle(u: User): Session", Imports: []string{"./auth"}},
		{ID: "auth.ts::User@sha", FilePath: "auth.ts", Language: "typescript", Kind: core.KindClass, Name: "User", QualifiedName: "User"},
		{ID: "auth.ts::Session@sha", FilePath: "auth.ts", Language: "typescript", Kind: core.KindClass, Name: "Session", QualifiedName: "Session"},
		// Out-of-scope type with the same simple name: must NOT produce an edge.
		{ID: "billing.ts::Session@sha", FilePath: "billing.ts", Language: "typescript", Kind: core.KindClass, Name: "Session", QualifiedName: "Session"},
	}, 3)

	if !hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "auth.ts::User@sha") {
		t.Fatalf("missing uses-type edge handle→User (imported file)")
	}
	if !hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "auth.ts::Session@sha") {
		t.Fatalf("missing uses-type edge handle→Session (imported file)")
	}
	if hasEdge(g, core.EdgeUsesType, "main.ts::handle@sha", "billing.ts::Session@sha") {
		t.Fatalf("uses-type edge MUST NOT cross to non-imported file")
	}
}

// ─── calls: scoping + comment/string stripping ───────────────────────────────

func TestCallsRespectsCommentAndStringStripping(t *testing.T) {
	// The regex fallback only serves languages without AST call-site
	// extraction; its comment/string stripping is exercised with one of
	// those. For AST languages an empty CallSites list is authoritative.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "a.rb::Caller@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "void Caller() {\n\t// Real() should be ignored in comments\n\t/* Real(1,2) */\n\tchar *s = \"Real(literal)\";\n}"},
		{ID: "a.rb::Real@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "void Real() {}"},
	}, 1)
	if hasEdge(g, core.EdgeCalls, "a.rb::Caller@sha", "a.rb::Real@sha") {
		t.Fatalf("calls edge should not be emitted from comments or strings")
	}

	// Sanity: a real call must still produce the edge (fallback language).
	g.Replace([]core.SymbolRecord{
		{ID: "a.rb::Caller@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "void Caller() { Real(); }"},
		{ID: "a.rb::Real@sha", FilePath: "a.rb", Language: "ruby", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "void Real() {}"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.rb::Caller@sha", "a.rb::Real@sha") {
		t.Fatalf("expected calls edge for genuine call")
	}

	// AST language with an extracted call site still edges normally.
	g.Replace([]core.SymbolRecord{
		{ID: "a.go::Caller@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Caller", QualifiedName: "Caller",
			RawText: "func Caller() { Real() }", CallSites: []core.CallSite{{Callee: "Real", Line: 1}}},
		{ID: "a.go::Real@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction, Name: "Real", QualifiedName: "Real",
			RawText: "func Real() {}"},
	}, 1)
	if !hasEdge(g, core.EdgeCalls, "a.go::Caller@sha", "a.go::Real@sha") {
		t.Fatalf("expected calls edge from AST call site")
	}
}

func TestCallsAcrossImportedFiles(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "cmd/main.go::Run@sha", FilePath: "cmd/main.go", Language: "go", Kind: core.KindFunction, Name: "Run", QualifiedName: "Run",
			RawText: "func Run() { Login() }", Imports: []string{"github.com/provasign/grove/internal/auth"},
			CallSites: []core.CallSite{{Callee: "Login", Line: 1}}},
		{ID: "internal/auth/auth.go::Login@sha", FilePath: "internal/auth/auth.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			RawText: "func Login() {}"},
		// Same name in a non-imported file: must NOT be linked.
		{ID: "internal/billing/billing.go::Login@sha", FilePath: "internal/billing/billing.go", Language: "go", Kind: core.KindFunction, Name: "Login", QualifiedName: "Login",
			RawText: "func Login() {}"},
	}, 3)

	if !hasEdge(g, core.EdgeCalls, "cmd/main.go::Run@sha", "internal/auth/auth.go::Login@sha") {
		t.Fatalf("missing calls edge to imported package")
	}
	if hasEdge(g, core.EdgeCalls, "cmd/main.go::Run@sha", "internal/billing/billing.go::Login@sha") {
		t.Fatalf("calls edge MUST NOT cross to non-imported file")
	}
}

// TestPythonBareCallThroughUnresolvedParamSuppressed guards against the
// flask@36e4a824 false-edge bug: a bare call through a Callable parameter
// (`loads: t.Callable = json.loads`, then `loads(value)`) must not resolve
// by matching the parameter's NAME against an unrelated same-named function
// elsewhere in scope — the caller's own from_prefixed_env never calls
// TaggedJSONSerializer.loads, it calls whatever was passed as `loads`.
func TestPythonBareCallThroughUnresolvedParamSuppressed(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "config.py::from_prefixed_env@sha", FilePath: "config.py", Language: "python",
			Kind: core.KindMethod, Name: "from_prefixed_env", QualifiedName: "Config.from_prefixed_env",
			ParentSymbol: "Config",
			RawText:      "def from_prefixed_env(self, prefix=\"FLASK\", *, loads=json.loads):\n    value = loads(raw)\n",
			CallSites:    []core.CallSite{{Callee: "loads", Line: 2}}},
		{ID: "tag.py::loads@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindMethod, Name: "loads", QualifiedName: "TaggedJSONSerializer.loads",
			ParentSymbol: "TaggedJSONSerializer", RawText: "def loads(self, value):\n    pass\n"},
	}, 2)
	if hasEdge(g, core.EdgeCalls, "config.py::from_prefixed_env@sha", "tag.py::loads@sha") {
		t.Fatalf("bare call through a shadowing parameter name must not resolve to an unrelated same-named function")
	}
}

// TestPythonTypedFactoryParamStillResolves guards the companion case: a
// parameter annotated as a class reference (`tag_class: type[JSONTag]`) used
// as a factory (`tag_class(self)`) is a genuine, statically-inferable
// constructor call and must keep resolving — the fix above only suppresses
// calls through parameters localTypes could NOT positively type.
func TestPythonTypedFactoryParamStillResolves(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "tag.py::register@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindMethod, Name: "register", QualifiedName: "TaggedJSONSerializer.register",
			ParentSymbol: "TaggedJSONSerializer",
			RawText:      "def register(self, tag_class: type[JSONTag], force=False):\n    tag_class(self)\n",
			CallSites:    []core.CallSite{{Callee: "tag_class", Line: 2}}},
		{ID: "tag.py::JSONTag@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindClass, Name: "JSONTag", QualifiedName: "JSONTag"},
		{ID: "tag.py::JSONTag.__init__@sha", FilePath: "tag.py", Language: "python",
			Kind: core.KindConstructor, Name: "__init__", QualifiedName: "JSONTag.__init__",
			ParentSymbol: "JSONTag", RawText: "def __init__(self):\n    pass\n"},
	}, 3)
	if !hasEdge(g, core.EdgeCalls, "tag.py::register@sha", "tag.py::JSONTag.__init__@sha") {
		t.Fatalf("expected constructor edge through a type[X]-typed factory parameter to still resolve")
	}
}

// TestPythonModuleGlobalProxySetattr: a test assigns `g.foo = value` where
// `g` is a module-level global typed as a proxy stub (`_AppCtxGlobalsProxy`)
// that inherits the class carrying the real __setattr__ (`_AppCtxGlobals`).
// Attribute assignment has no call site and the declared type is a
// type-checking stub, so resolving it exercises module-global type inference
// plus a base-class walk. Asserts the calls edge test → __setattr__.
func TestPythonModuleGlobalProxySetattr(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		// Module-level global: `g: _AppCtxGlobalsProxy = LocalProxy(...)`.
		{ID: "globals.py::g@sha", FilePath: "src/flask/globals.py", Language: "python",
			Kind: core.KindVariable, Name: "g", QualifiedName: "g",
			Signature: "g: _AppCtxGlobalsProxy"},
		// Type-checking proxy stub, inherits the real class.
		{ID: "globals.py::_AppCtxGlobalsProxy@sha", FilePath: "src/flask/globals.py", Language: "python",
			Kind: core.KindClass, Name: "_AppCtxGlobalsProxy", QualifiedName: "_AppCtxGlobalsProxy",
			Signature: "class _AppCtxGlobalsProxy(ProxyMixin[_AppCtxGlobals], _AppCtxGlobals)"},
		// The class carrying the custom __setattr__.
		{ID: "ctx.py::_AppCtxGlobals@sha", FilePath: "src/flask/ctx.py", Language: "python",
			Kind: core.KindClass, Name: "_AppCtxGlobals", QualifiedName: "_AppCtxGlobals"},
		{ID: "ctx.py::_AppCtxGlobals.__setattr__@sha", FilePath: "src/flask/ctx.py", Language: "python",
			Kind: core.KindMethod, Name: "__setattr__", QualifiedName: "_AppCtxGlobals.__setattr__",
			ParentSymbol: "_AppCtxGlobals", RawText: "def __setattr__(self, name, value):\n    self.__dict__[name] = value\n"},
		// A test that assigns through the global. Imports flask, NOT flask.ctx.
		{ID: "test_basic.py::test_g@sha", FilePath: "tests/test_basic.py", Language: "python",
			Kind: core.KindFunction, Name: "test_g", QualifiedName: "test_g",
			Imports: []string{"flask"},
			RawText: "def test_g():\n    flask.g.foo = 42\n"},
	}, 5)
	if !hasEdge(g, core.EdgeCalls, "test_basic.py::test_g@sha", "ctx.py::_AppCtxGlobals.__setattr__@sha") {
		t.Fatalf("expected calls edge from test through module-global proxy to _AppCtxGlobals.__setattr__")
	}
}

// TestPythonSetattrGuards covers the review-found precision holes in the
// module-global __setattr__ resolution: (a) a parameter or local rebinding
// that SHADOWS a module global must not resolve through the global's type;
// (b) a chained attribute assignment through a VARIABLE head must resolve
// nothing, even when the middle segment collides with a module global's
// name; (c) a module-qualified chain (mod.global.attr=) resolves only when
// the head is actually imported.
func TestPythonSetattrGuards(t *testing.T) {
	base := []core.SymbolRecord{
		{ID: "globals.py::g@sha", FilePath: "globals.py", Language: "python",
			Kind: core.KindVariable, Name: "g", QualifiedName: "g", Signature: "g: Holder"},
		{ID: "globals.py::headers@sha", FilePath: "globals.py", Language: "python",
			Kind: core.KindVariable, Name: "headers", QualifiedName: "headers", Signature: "headers: Holder"},
		{ID: "holder.py::Holder@sha", FilePath: "holder.py", Language: "python",
			Kind: core.KindClass, Name: "Holder", QualifiedName: "Holder"},
		{ID: "holder.py::Holder.__setattr__@sha", FilePath: "holder.py", Language: "python",
			Kind: core.KindMethod, Name: "__setattr__", QualifiedName: "Holder.__setattr__",
			ParentSymbol: "Holder", RawText: "def __setattr__(self, n, v):\n    pass\n"},
	}
	target := "holder.py::Holder.__setattr__@sha"

	// (a) parameter shadowing: def f(g): g.attr = 1 — param g is NOT the global.
	g1 := New()
	g1.Replace(append(append([]core.SymbolRecord{}, base...), core.SymbolRecord{
		ID: "app.py::f@sha", FilePath: "app.py", Language: "python",
		Kind: core.KindFunction, Name: "f", QualifiedName: "f",
		RawText: "def f(g):\n    g.attr = 1\n"}), 3)
	if hasEdge(g1, core.EdgeCalls, "app.py::f@sha", target) {
		t.Fatalf("param-shadowed global must not resolve to __setattr__")
	}

	// (a2) local rebinding: g = make(); g.attr = 1 — local g is NOT the global.
	g2 := New()
	g2.Replace(append(append([]core.SymbolRecord{}, base...), core.SymbolRecord{
		ID: "app.py::h@sha", FilePath: "app.py", Language: "python",
		Kind: core.KindFunction, Name: "h", QualifiedName: "h",
		RawText: "def h():\n    g = make()\n    g.attr = 1\n"}), 3)
	if hasEdge(g2, core.EdgeCalls, "app.py::h@sha", target) {
		t.Fatalf("locally-rebound global must not resolve to __setattr__")
	}

	// (b) chained through a variable head: resp.headers.foo = x — `headers`
	// here is an attribute of resp, not the module global named headers.
	g3 := New()
	g3.Replace(append(append([]core.SymbolRecord{}, base...), core.SymbolRecord{
		ID: "app.py::k@sha", FilePath: "app.py", Language: "python",
		Kind: core.KindFunction, Name: "k", QualifiedName: "k",
		RawText: "def k(resp):\n    resp.headers.foo = 1\n"}), 3)
	if hasEdge(g3, core.EdgeCalls, "app.py::k@sha", target) {
		t.Fatalf("variable-head chain must not resolve middle segment as a module global")
	}

	// (c) genuine unqualified global access still resolves.
	g4 := New()
	g4.Replace(append(append([]core.SymbolRecord{}, base...), core.SymbolRecord{
		ID: "app.py::ok@sha", FilePath: "app.py", Language: "python",
		Kind: core.KindFunction, Name: "ok", QualifiedName: "ok",
		RawText: "def ok():\n    g.attr = 1\n"}), 3)
	if !hasEdge(g4, core.EdgeCalls, "app.py::ok@sha", target) {
		t.Fatalf("unshadowed global assignment must still resolve to __setattr__")
	}
}

// TestPyParamNamesRobustness covers the review-found parsing holes: lambda
// defaults and comma/paren-bearing string defaults must neither mint phantom
// params (suppressing real call edges) nor abort parsing (disabling the
// suppression guard entirely).
func TestPyParamNamesRobustness(t *testing.T) {
	// Lambda default: the lambda's own params are not params of f.
	p := pyParamNames("def f(items, key=lambda a, b: cmp(a, b)):\n    pass")
	if !p["items"] || !p["key"] {
		t.Fatalf("real params missing: %v", p)
	}
	if p["a"] || p["b"] {
		t.Fatalf("lambda params leaked as phantom params: %v", p)
	}
	// Comma inside a string default must not split.
	p = pyParamNames(`def f(names="a, b, c", flag=True):` + "\n    pass")
	if !p["names"] || !p["flag"] || p["b"] || p["c"] {
		t.Fatalf("string-default comma mishandled: %v", p)
	}
	// Unbalanced paren inside a string default must not abort the scan —
	// the suppression guard must still see `loads`.
	p = pyParamNames(`def f(loads=json.loads, prompt="(y/n"):` + "\n    pass")
	if !p["loads"] || !p["prompt"] {
		t.Fatalf("quote-blind paren scan disabled param extraction: %v", p)
	}
}

// TestPyDunderTargetsCrossFileCollision: an unrelated same-named class in
// another package defining __setattr__ must not hijack resolution away from
// the real hierarchy.
func TestPyDunderTargetsCrossFileCollision(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "globals.py::cfg@sha", FilePath: "app/globals.py", Language: "python",
			Kind: core.KindVariable, Name: "cfg", QualifiedName: "cfg", Signature: "cfg: Config"},
		// The REAL Config (no __setattr__ of its own) inherits Base.
		{ID: "app/config.py::Config@sha", FilePath: "app/config.py", Language: "python",
			Kind: core.KindClass, Name: "Config", QualifiedName: "Config",
			Signature: "class Config(Base)"},
		{ID: "app/base.py::Base@sha", FilePath: "app/base.py", Language: "python",
			Kind: core.KindClass, Name: "Base", QualifiedName: "Base"},
		{ID: "app/base.py::Base.__setattr__@sha", FilePath: "app/base.py", Language: "python",
			Kind: core.KindMethod, Name: "__setattr__", QualifiedName: "Base.__setattr__",
			ParentSymbol: "Base", RawText: "def __setattr__(self, n, v):\n    pass\n"},
		// An UNRELATED Config in another package that happens to define one.
		{ID: "vendor/other.py::Config@sha", FilePath: "vendor/other.py", Language: "python",
			Kind: core.KindClass, Name: "Config", QualifiedName: "Config"},
		{ID: "vendor/other.py::Config.__setattr__@sha", FilePath: "vendor/other.py", Language: "python",
			Kind: core.KindMethod, Name: "__setattr__", QualifiedName: "Config.__setattr__",
			ParentSymbol: "Config", RawText: "def __setattr__(self, n, v):\n    pass\n"},
		// Caller in app/: its Config is app/config.py's (preferDir match).
		{ID: "app/use.py::use@sha", FilePath: "app/use.py", Language: "python",
			Kind: core.KindFunction, Name: "use", QualifiedName: "use",
			RawText: "def use():\n    cfg.attr = 1\n"},
	}, 5)
	if hasEdge(g, core.EdgeCalls, "app/use.py::use@sha", "vendor/other.py::Config.__setattr__@sha") {
		t.Fatalf("unrelated same-named class hijacked __setattr__ resolution")
	}
	if !hasEdge(g, core.EdgeCalls, "app/use.py::use@sha", "app/base.py::Base.__setattr__@sha") {
		t.Fatalf("real hierarchy's inherited __setattr__ not reached")
	}
}

func TestComputeICRNoSeedsHasZeroConfidence(t *testing.T) {
	g := New()
	icr := g.ComputeICR("nonexistent-feature")
	if icr.Confidence > 0.5 {
		t.Fatalf("expected low confidence for empty ICR, got %v", icr.Confidence)
	}
}

func TestDetectConflictsFileOverlap(t *testing.T) {
	a := core.IsolatedChangeRegion{ExclusiveFiles: []string{"a.go", "shared.go"}}
	b := core.IsolatedChangeRegion{ExclusiveFiles: []string{"b.go", "shared.go"}}
	result := DetectConflicts(a, b)
	if !result.Conflicts || len(result.OverlapFiles) != 1 || result.OverlapFiles[0] != "shared.go" {
		t.Fatalf("expected file overlap conflict on shared.go, got %+v", result)
	}
}

// ─── helpers ────────────────────────────────────────────────────────────────

func hasEdge(g *CodeGraph, t core.EdgeType, from, to string) bool {
	_, edges := g.Snapshot()
	for _, e := range edges {
		if e.Type == t && e.From == from && e.To == to {
			return true
		}
	}
	return false
}

// ─── benchmarks ──────────────────────────────────────────────────────────────

func BenchmarkBuildEdges10K(b *testing.B) {
	symbols := make([]core.SymbolRecord, 0, 10_000)
	for i := 0; i < 1000; i++ {
		file := "pkg/file" + itoa(i) + ".go"
		for j := 0; j < 10; j++ {
			symbols = append(symbols, core.SymbolRecord{
				ID:            file + "::Fn" + itoa(j) + "@sha",
				FilePath:      file,
				Language:      "go",
				Kind:          core.KindFunction,
				Name:          "Fn" + itoa(j),
				QualifiedName: "Fn" + itoa(j),
				RawText:       "func Fn" + itoa(j) + "() { Fn" + itoa((j+1)%10) + "() }",
			})
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildEdges(symbols)
	}
}

func BenchmarkSearch10K(b *testing.B) {
	symbols := make([]core.SymbolRecord, 0, 10_000)
	for i := 0; i < 10_000; i++ {
		symbols = append(symbols, core.SymbolRecord{
			ID:            "f.go::Sym" + itoa(i) + "@s",
			FilePath:      "f.go",
			Kind:          core.KindFunction,
			Name:          "Sym" + itoa(i),
			QualifiedName: "Sym" + itoa(i),
		})
	}
	g := New()
	g.Replace(symbols, 1)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.Search("sym42", 20)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b strings.Builder
	for i > 0 {
		b.WriteByte(byte('0' + i%10))
		i /= 10
	}
	// reverse
	out := []byte(b.String())
	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return string(out)
}

func TestResolveCalleesMatchesLanguageAndCSharpPrivateScope(t *testing.T) {
	caller := core.SymbolRecord{ID: "use.cs::Other.Run", FilePath: "use.cs", Language: "csharp", Kind: core.KindMethod, Name: "Run", ParentSymbol: "Other"}
	public := core.SymbolRecord{ID: "lib.cs::Helpers.Format", FilePath: "lib.cs", Language: "csharp", Kind: core.KindMethod, Name: "Format", ParentSymbol: "Helpers", Modifiers: []string{"public"}}
	private := public
	private.ID = "lib.cs::Helpers.Format#2"
	private.Modifiers = []string{"private"}
	wrongLanguage := public
	wrongLanguage.ID = "lib.go::Helpers.Format"
	wrongLanguage.FilePath = "lib.go"
	wrongLanguage.Language = "go"
	syms := []core.SymbolRecord{caller, public, private, wrongLanguage}
	idx := newEdgeIndex(syms)
	scope := map[string]struct{}{"use.cs": {}, "lib.cs": {}, "lib.go": {}}
	got, _ := resolveCallees(idx, &syms[0], "Format", scope, true, false)
	if len(got) != 1 || got[0].ID != public.ID {
		t.Fatalf("cross-type candidates = %#v, want only public same-language method", got)
	}
	cHeader := core.SymbolRecord{ID: "api.h::Format", FilePath: "api.h", Language: "cpp", Kind: core.KindFunction, Name: "Format"}
	cCaller := core.SymbolRecord{ID: "use.c::run", FilePath: "use.c", Language: "c", Kind: core.KindFunction, Name: "run"}
	cIdx := newEdgeIndex([]core.SymbolRecord{cCaller, cHeader})
	cGot, _ := resolveCallees(cIdx, &cCaller, "Format", map[string]struct{}{"use.c": {}, "api.h": {}}, true, false)
	if len(cGot) != 1 || cGot[0].ID != cHeader.ID {
		t.Fatalf("C caller did not accept C++-classified header: %#v", cGot)
	}
	syms[0].ParentSymbol = "Helpers"
	got, _ = resolveCallees(idx, &syms[0], "Format", scope, true, false)
	if len(got) != 2 {
		t.Fatalf("same-type candidates = %#v, want public and private methods", got)
	}
}

func TestResolveCalleesKeepsSelfRecursionWithoutCrossLanguagePromotion(t *testing.T) {
	for _, language := range []string{"go", "java", "typescript", "python", "php", "rust"} {
		t.Run(language, func(t *testing.T) {
			self := core.SymbolRecord{
				ID: language + "::walk", FilePath: "walk." + language, Language: language,
				Kind: core.KindFunction, Name: "walk", QualifiedName: "walk",
				CallSites: []core.CallSite{{Callee: "walk", Line: 1}},
			}
			foreign := core.SymbolRecord{ID: "foreign::walk", FilePath: "walk.foreign", Language: "csharp", Kind: core.KindFunction, Name: "walk", QualifiedName: "walk"}
			g := New()
			g.Replace([]core.SymbolRecord{self, foreign}, 2)
			if !hasEdge(g, core.EdgeCalls, self.ID, self.ID) {
				_, edges := g.Snapshot()
				t.Fatalf("%s recursive call did not produce a self edge: %+v", language, edges)
			}
			if hasEdge(g, core.EdgeCalls, self.ID, foreign.ID) {
				t.Fatalf("%s recursive call promoted to another language", language)
			}
		})
	}
}

func TestSelfNamedWrapperDoesNotShadowQualifiedTarget(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "wrapper.go::Use", FilePath: "wrapper.go", Language: "go", Kind: core.KindFunction,
		Name: "Use", QualifiedName: "Use", Imports: []string{"example/engine"},
		CallSites: []core.CallSite{{Callee: "engine().Use", Line: 1}},
	}
	target := core.SymbolRecord{ID: "engine/use.go::Engine.Use", FilePath: "engine/use.go", Language: "go", Kind: core.KindMethod, Name: "Use", QualifiedName: "Engine.Use", ParentSymbol: "Engine"}
	g := New()
	g.Replace([]core.SymbolRecord{caller, target}, 2)
	if !hasEdge(g, core.EdgeCalls, caller.ID, target.ID) {
		_, edges := g.Snapshot()
		t.Fatalf("qualified wrapper target missing: %+v", edges)
	}
	if hasEdge(g, core.EdgeCalls, caller.ID, caller.ID) {
		t.Fatal("qualified non-self receiver was misclassified as recursion")
	}
}

// @property method blueprints, and never to a plain (non-property) method.
func TestBuildCalls_PropertyReadEdges(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "ctx.py::AppContext.f@1", FilePath: "ctx.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "f", QualifiedName: "AppContext.f", ParentSymbol: "AppContext",
		Imports:   []string{"wrappers"},
		AttrSites: []core.CallSite{{Callee: "request.blueprints", Line: 3}, {Callee: "request.environ", Line: 4}},
	}
	prop := core.SymbolRecord{
		ID: "wrappers.py::Request.blueprints@10", FilePath: "wrappers.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "blueprints", QualifiedName: "Request.blueprints", ParentSymbol: "Request",
		Annotations: []string{"property"},
	}
	plain := core.SymbolRecord{
		ID: "wrappers.py::Request.environ@20", FilePath: "wrappers.py", BlobSHA: "1",
		Language: "python", Kind: core.KindMethod,
		Name: "environ", QualifiedName: "Request.environ", ParentSymbol: "Request",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, prop, plain})
	var gotProp, gotPlain bool
	for _, e := range edges {
		if e.Type != core.EdgeCalls || e.From != caller.ID {
			continue
		}
		switch e.To {
		case prop.ID:
			gotProp = true
			if e.Confidence > 0.75 {
				t.Errorf("property edge confidence = %v, want reduced", e.Confidence)
			}
		case plain.ID:
			gotPlain = true
		}
	}
	if !gotProp {
		t.Error("expected property-read edge to Request.blueprints")
	}
	if gotPlain {
		t.Error("attribute access must not edge to non-property methods")
	}
}
