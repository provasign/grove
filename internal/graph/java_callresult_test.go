package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestJavaCallResult_OwnMethodReachesOutOfScopeReturnType(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "Caller.java::Caller.run", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Caller.run", ParentSymbol: "Caller",
		Span: core.LineRange{Start: 1}, RawText: "String run() {\n    return secure().finish();\n}",
		CallSites: []core.CallSite{{Callee: "secure().finish", Line: 2}},
	}
	factory := core.SymbolRecord{
		ID: "Caller.java::Caller.secure", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "secure", QualifiedName: "Caller.secure", ParentSymbol: "Caller",
		Signature: "static Product secure()",
	}
	finish := core.SymbolRecord{
		ID: "Product.java::Product.finish", FilePath: "Product.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "finish", QualifiedName: "Product.finish", ParentSymbol: "Product",
		Signature: "String finish()",
	}
	if !javaHasCall(BuildEdges([]core.SymbolRecord{caller, factory, finish}), caller.ID, finish.ID) {
		t.Fatal("own-class call result must reach its out-of-scope return type")
	}
}

func TestJavaCallResult_ExplicitOwnerReachesOutOfScopeReturnType(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "Caller.java::Caller.run", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Caller.run", ParentSymbol: "Caller",
		Imports: []string{"Factory"}, Span: core.LineRange{Start: 1},
		RawText:   "String run() {\n    return Factory.make().finish();\n}",
		CallSites: []core.CallSite{{Callee: "make().finish", Line: 2}},
	}
	factory := core.SymbolRecord{
		ID: "Factory.java::Factory.make", FilePath: "Factory.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "make", QualifiedName: "Factory.make", ParentSymbol: "Factory",
		Signature: "static Product make()",
	}
	finish := core.SymbolRecord{
		ID: "Product.java::Product.finish", FilePath: "Product.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "finish", QualifiedName: "Product.finish", ParentSymbol: "Product",
		Signature: "String finish()",
	}
	if !javaHasCall(BuildEdges([]core.SymbolRecord{caller, factory, finish}), caller.ID, finish.ID) {
		t.Fatal("explicitly qualified call result must reach its out-of-scope return type")
	}
}

func TestJavaCallResult_LowercaseReceiverBeatsCallerMethod(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "Caller.java::Caller.run", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Caller.run", ParentSymbol: "Caller",
		Imports: []string{"Chain", "Product"}, Span: core.LineRange{Start: 1},
		RawText:   "String run(Chain chain) {\n    return chain.leaf().work();\n}",
		CallSites: []core.CallSite{{Callee: "chain.leaf", Line: 2}, {Callee: "leaf().work", Line: 2}},
	}
	chainLeaf := core.SymbolRecord{
		ID: "Chain.java::Chain.leaf", FilePath: "Chain.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "leaf", QualifiedName: "Chain.leaf", ParentSymbol: "Chain",
		Signature: "Product leaf()",
	}
	callerLeaf := core.SymbolRecord{
		ID: "Caller.java::Caller.leaf", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "leaf", QualifiedName: "Caller.leaf", ParentSymbol: "Caller",
		Signature: "Wrong leaf()",
	}
	productWork := core.SymbolRecord{
		ID: "Product.java::Product.work", FilePath: "Product.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "work", QualifiedName: "Product.work", ParentSymbol: "Product",
		Signature: "String work()",
	}
	wrongWork := core.SymbolRecord{
		ID: "Wrong.java::Wrong.work", FilePath: "Wrong.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "work", QualifiedName: "Wrong.work", ParentSymbol: "Wrong",
		Signature: "String work()",
	}
	edges := BuildEdges([]core.SymbolRecord{caller, chainLeaf, callerLeaf, productWork, wrongWork})
	if !javaHasCall(edges, caller.ID, productWork.ID) {
		t.Fatal("lowercase receiver's call result must resolve the downstream method")
	}
	if javaHasCall(edges, caller.ID, wrongWork.ID) {
		t.Fatal("caller's same-named method must not shadow an explicit lowercase receiver")
	}
}

func TestJavaCallResult_DoesNotGuessFromSameNamedMethod(t *testing.T) {
	caller := core.SymbolRecord{
		ID: "Caller.java::Caller.run", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Caller.run", ParentSymbol: "Caller",
		Imports: []string{"Streams"}, Span: core.LineRange{Start: 1},
		RawText:   "Object run(List values) {\n    return values.stream().filter();\n}",
		CallSites: []core.CallSite{{Callee: "stream().filter", Line: 2}},
	}
	unrelatedFactory := core.SymbolRecord{
		ID: "Streams.java::Streams.stream", FilePath: "Streams.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "stream", QualifiedName: "Streams.stream", ParentSymbol: "Streams",
		Signature: "static FailableStream stream()",
	}
	wrong := core.SymbolRecord{
		ID: "FailableStream.java::FailableStream.filter", FilePath: "FailableStream.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "filter", QualifiedName: "FailableStream.filter", ParentSymbol: "FailableStream",
		Signature: "FailableStream filter()",
	}
	if javaHasCall(BuildEdges([]core.SymbolRecord{caller, unrelatedFactory, wrong}), caller.ID, wrong.ID) {
		t.Fatal("external receiver chain must not bind to an unrelated same-named in-repo factory")
	}
}

func TestJavaCallResult_IndexedReceiverReachesReturnType(t *testing.T) {
	class := core.SymbolRecord{
		ID: "Caller.java::Caller", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindClass, Name: "Caller", QualifiedName: "Outer.Caller",
		RawText: "class Caller {\n    private ExtTypedProperty[] properties;\n}",
	}
	field := core.SymbolRecord{
		ID: "Caller.java::Caller.properties", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindField, Name: "properties", QualifiedName: "Outer.Caller.properties", ParentSymbol: "Outer.Caller",
		RawText: "private ExtTypedProperty[] properties;",
	}
	caller := core.SymbolRecord{
		ID: "Caller.java::Caller.run", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Outer.Caller.run", ParentSymbol: "Outer.Caller",
		Span:    core.LineRange{Start: 1},
		RawText: "void run(int index, Object bean) {\n    properties[index].getProperty().set(bean, null);\n}",
		CallSites: []core.CallSite{
			{Callee: "properties.getProperty", Line: 2},
			{Callee: "getProperty().set", Line: 2, Argc: 2, Args: []string{"bean", ""}},
		},
	}
	getter := core.SymbolRecord{
		ID: "Caller.java::ExtTypedProperty.getProperty", FilePath: "Caller.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "getProperty", QualifiedName: "ExtTypedProperty.getProperty", ParentSymbol: "ExtTypedProperty",
		Signature: "SettableBeanProperty getProperty()",
	}
	set := core.SymbolRecord{
		ID: "SettableBeanProperty.java::SettableBeanProperty.set", FilePath: "SettableBeanProperty.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "set", QualifiedName: "SettableBeanProperty.set", ParentSymbol: "SettableBeanProperty",
		Signature: "void set(Object bean, Object value)",
	}
	wrong := core.SymbolRecord{
		ID: "Other.java::Other.set", FilePath: "Other.java", BlobSHA: "1",
		Language: "java", Kind: core.KindMethod, Name: "set", QualifiedName: "Other.set", ParentSymbol: "Other",
		Signature: "void set(Object bean, Object value)",
	}
	edges := BuildEdges([]core.SymbolRecord{class, field, caller, getter, set, wrong})
	if !javaHasCall(edges, caller.ID, set.ID) {
		t.Fatal("indexed receiver's call result must resolve the downstream method")
	}
	if javaHasCall(edges, caller.ID, wrong.ID) {
		t.Fatal("indexed receiver's call result must not fan out to an unrelated method")
	}
}

func TestJavaDispatchNarrowsOverloadsByArgumentType(t *testing.T) {
	base := core.SymbolRecord{ID: "Base.java::Base", FilePath: "Base.java", Language: "java", Kind: core.KindClass, Name: "Base", QualifiedName: "Base"}
	baseInt := core.SymbolRecord{ID: "Base.java::Base.get-int", FilePath: "Base.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Base.get", ParentSymbol: "Base", Signature: "Object get(int index)"}
	baseString := core.SymbolRecord{ID: "Base.java::Base.get-string", FilePath: "Base.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Base.get", ParentSymbol: "Base", Signature: "Object get(String name)"}
	sub := core.SymbolRecord{ID: "Sub.java::Sub", FilePath: "Sub.java", Language: "java", Kind: core.KindClass, Name: "Sub", QualifiedName: "Sub", Signature: "class Sub extends Base"}
	subInt := core.SymbolRecord{ID: "Sub.java::Sub.get-int", FilePath: "Sub.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Sub.get", ParentSymbol: "Sub", Signature: "Object get(int index)"}
	subString := core.SymbolRecord{ID: "Sub.java::Sub.get-string", FilePath: "Sub.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "Sub.get", ParentSymbol: "Sub", Signature: "Object get(String name)"}
	caller := core.SymbolRecord{
		ID: "Use.java::Use.run", FilePath: "Use.java", Language: "java", Kind: core.KindMethod,
		Name: "run", QualifiedName: "Use.run", ParentSymbol: "Use", Signature: "void run(Base node)",
		RawText:   "void run(Base node) { node.get(\"name\"); }",
		CallSites: []core.CallSite{{Callee: "node.get", Line: 1, Argc: 1, Args: []string{"#String"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{base, baseInt, baseString, sub, subInt, subString, caller})
	if !javaHasCall(edges, caller.ID, subString.ID) {
		t.Fatal("dispatch must retain the override matching the argument type")
	}
	if javaHasCall(edges, caller.ID, subInt.ID) {
		t.Fatal("dispatch must exclude an override of a different overload")
	}
}

func TestJavaDispatchNarrowsCallResultArgumentFromKnownExternalContract(t *testing.T) {
	objectInt := core.SymbolRecord{ID: "ObjectNode.java::ObjectNode.get-int", FilePath: "ObjectNode.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "ObjectNode.get", ParentSymbol: "ObjectNode", Signature: "Object get(int index)"}
	objectString := core.SymbolRecord{ID: "ObjectNode.java::ObjectNode.get-string", FilePath: "ObjectNode.java", Language: "java", Kind: core.KindMethod, Name: "get", QualifiedName: "ObjectNode.get", ParentSymbol: "ObjectNode", Signature: "Object get(String name)"}
	caller := core.SymbolRecord{
		ID: "ObjectNode.java::ObjectNode.at", FilePath: "ObjectNode.java", Language: "java", Kind: core.KindMethod,
		Name: "at", QualifiedName: "ObjectNode.at", ParentSymbol: "ObjectNode", Signature: "Object at(JsonPointer ptr)",
		RawText:   "Object at(JsonPointer ptr) { return get(ptr.getMatchingProperty()); }",
		CallSites: []core.CallSite{{Callee: "get", Line: 1, Argc: 1, Args: []string{"call:getMatchingProperty"}}},
	}
	edges := BuildEdges([]core.SymbolRecord{objectInt, objectString, caller})
	if !javaHasCall(edges, caller.ID, objectString.ID) {
		t.Fatal("known external return type must retain the matching overload")
	}
	if javaHasCall(edges, caller.ID, objectInt.ID) {
		t.Fatal("known external return type must exclude a conflicting overload")
	}
}

func TestJavaConstructorDelegationUsesArity(t *testing.T) {
	baseType := core.SymbolRecord{ID: "Base.java::type", FilePath: "Base.java", Language: "java", Kind: core.KindClass, Name: "Base", QualifiedName: "Base", Signature: "class Base"}
	baseZero := core.SymbolRecord{ID: "Base.java::Base@1", FilePath: "Base.java", Language: "java", Kind: core.KindConstructor, Name: "Base", ParentSymbol: "Base", Signature: "Base()"}
	baseOne := core.SymbolRecord{ID: "Base.java::Base@2", FilePath: "Base.java", Language: "java", Kind: core.KindConstructor, Name: "Base", ParentSymbol: "Base", Signature: "Base(String value)"}
	childType := core.SymbolRecord{ID: "Child.java::type", FilePath: "Child.java", Language: "java", Kind: core.KindClass, Name: "Child", QualifiedName: "Child", Signature: "class Child extends Base"}
	childZero := core.SymbolRecord{ID: "Child.java::Child@1", FilePath: "Child.java", Language: "java", Kind: core.KindConstructor, Name: "Child", ParentSymbol: "Child", Signature: "Child()", CallSites: []core.CallSite{{Callee: "this()", Argc: 1}}}
	childOne := core.SymbolRecord{ID: "Child.java::Child@2", FilePath: "Child.java", Language: "java", Kind: core.KindConstructor, Name: "Child", ParentSymbol: "Child", Signature: "Child(int value)", CallSites: []core.CallSite{{Callee: "super()", Argc: 1}}}

	edges := BuildEdges([]core.SymbolRecord{baseType, baseZero, baseOne, childType, childZero, childOne})
	if !javaHasCall(edges, childZero.ID, childOne.ID) {
		t.Fatal("this(1) must resolve to the one-argument sibling constructor")
	}
	if !javaHasCall(edges, childOne.ID, baseOne.ID) {
		t.Fatal("super(1) must resolve to the one-argument base constructor")
	}
	if javaHasCall(edges, childOne.ID, baseZero.ID) {
		t.Fatal("super(1) must not resolve to the zero-argument base constructor")
	}
}

func TestJavaExplicitImportShadowsSamePackageType(t *testing.T) {
	aPath := "src/main/java/com/ex/a/Leaf.java"
	bPath := "src/main/java/com/ex/b/Leaf.java"
	callerPath := "src/main/java/com/ex/b/UseB.java"
	aType := core.SymbolRecord{ID: aPath + "::type", FilePath: aPath, Language: "java", Kind: core.KindClass, Name: "Leaf", QualifiedName: "Leaf"}
	aWork := core.SymbolRecord{ID: aPath + "::work", FilePath: aPath, Language: "java", Kind: core.KindMethod, Name: "work", QualifiedName: "Leaf.work", ParentSymbol: "Leaf"}
	bType := core.SymbolRecord{ID: bPath + "::type", FilePath: bPath, Language: "java", Kind: core.KindClass, Name: "Leaf", QualifiedName: "Leaf"}
	bWork := core.SymbolRecord{ID: bPath + "::work", FilePath: bPath, Language: "java", Kind: core.KindMethod, Name: "work", QualifiedName: "Leaf.work", ParentSymbol: "Leaf"}
	caller := core.SymbolRecord{
		ID: callerPath + "::go", FilePath: callerPath, Language: "java", Kind: core.KindMethod,
		Name: "go", QualifiedName: "UseB.go", ParentSymbol: "UseB",
		Imports: []string{"com.ex.a.Leaf"}, Signature: "void go(Leaf leaf)",
		CallSites: []core.CallSite{{Callee: "leaf.work"}},
	}
	edges := BuildEdges([]core.SymbolRecord{aType, aWork, bType, bWork, caller})
	if !javaHasCall(edges, caller.ID, aWork.ID) {
		t.Fatal("explicitly imported Leaf must receive leaf.work()")
	}
	if javaHasCall(edges, caller.ID, bWork.ID) {
		t.Fatal("same-package Leaf is shadowed by the explicit import")
	}
}

func javaHasCall(edges []core.Edge, from, to string) bool {
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls && edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}
