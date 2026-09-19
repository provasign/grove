// Hand-built table tests for Objective-C edge resolution, mirroring
// swift_kotlin_callresult_test.go: construct SymbolRecord/CallSite literals
// directly and assert against the edges BuildEdges (via CodeGraph.Replace)
// produces. astkit/tree-sitter extraction itself is covered by astkit's own
// strategies_test.go / objc_round2_test.go.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestObjCCalls_CrossFileWholeRepoScope(t *testing.T) {
	// A class's @interface (Person.h) and @implementation (Person.m) live
	// in different files with no import between them and no shared
	// directory convention — this asserts the "objc" branch in
	// importedFiles gives every .m/.h file whole-repo visibility.
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.h::Person@sha", FilePath: "Person.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "@interface Person : NSObject"},
		{ID: "Person.m::initWithName:@sha", FilePath: "Person.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "initWithName:", QualifiedName: "initWithName:",
			ParentSymbol: "Person", Signature: "- (instancetype)initWithName:(NSString *)name"},
		{ID: "App.m::personWithName:@sha", FilePath: "App.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "personWithName:", QualifiedName: "personWithName:",
			ParentSymbol: "App", Signature: "+ (instancetype)personWithName:(NSString *)name",
			RawText:   "+ (instancetype)personWithName:(NSString *)name { return [[Person alloc] initWithName:name]; }",
			CallSites: []core.CallSite{{Callee: "Person.alloc", Line: 1}, {Callee: "alloc().initWithName:", Line: 1}},
		},
	}
	g.Replace(syms, 3)

	if !hasEdge(g, core.EdgeCalls, "App.m::personWithName:@sha", "Person.m::initWithName:@sha") {
		t.Fatalf("[[Person alloc] initWithName:] must resolve to Person's initWithName: via whole-repo scope")
	}
}

func TestObjCExtendsImplements_SuperclassAndProtocols(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "NSObject.h::NSObject@sha", FilePath: "NSObject.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindClass, Name: "NSObject", QualifiedName: "NSObject",
			Signature: "@interface NSObject"},
		{ID: "Greeter.h::Greeter@sha", FilePath: "Greeter.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindInterface, Name: "Greeter", QualifiedName: "Greeter",
			Signature: "@protocol Greeter"},
		{ID: "NSCopying.h::NSCopying@sha", FilePath: "NSCopying.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindInterface, Name: "NSCopying", QualifiedName: "NSCopying",
			Signature: "@protocol NSCopying"},
		{ID: "Person.h::Person@sha", FilePath: "Person.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "@interface Person : NSObject <Greeter, NSCopying>"},
	}
	g.Replace(syms, 4)

	if !hasEdge(g, core.EdgeExtends, "Person.h::Person@sha", "NSObject.h::NSObject@sha") {
		t.Fatalf("Person must extend NSObject")
	}
	if !hasEdge(g, core.EdgeImplements, "Person.h::Person@sha", "Greeter.h::Greeter@sha") {
		t.Fatalf("Person must implement Greeter")
	}
	if !hasEdge(g, core.EdgeImplements, "Person.h::Person@sha", "NSCopying.h::NSCopying@sha") {
		t.Fatalf("Person must implement NSCopying")
	}
	if hasEdge(g, core.EdgeImplements, "Person.h::Person@sha", "NSObject.h::NSObject@sha") {
		t.Fatalf("NSObject must not ALSO be recorded as an implemented protocol")
	}
}

func TestObjCExtendsImplements_ProtocolExtendsProtocol(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "NSObject.h::NSObjectProtocol@sha", FilePath: "NSObject.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindInterface, Name: "NSObject", QualifiedName: "NSObject",
			Signature: "@protocol NSObject"},
		{ID: "Greeter.h::Greeter@sha", FilePath: "Greeter.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindInterface, Name: "Greeter", QualifiedName: "Greeter",
			Signature: "@protocol Greeter <NSObject>"},
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeExtends, "Greeter.h::Greeter@sha", "NSObject.h::NSObjectProtocol@sha") {
		t.Fatalf("Greeter protocol must extend NSObject protocol")
	}
}

func TestObjCCalls_SelfMessageSendResolvesWithinClass(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Person.h::Person@sha", FilePath: "Person.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindClass, Name: "Person", QualifiedName: "Person",
			Signature: "@interface Person : NSObject"},
		{ID: "Person.m::helper@sha", FilePath: "Person.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "helper", QualifiedName: "helper",
			ParentSymbol: "Person", Signature: "- (NSString *)helper"},
		{ID: "Person.m::greet@sha", FilePath: "Person.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "greet", QualifiedName: "greet",
			ParentSymbol: "Person", Signature: "- (NSString *)greet",
			CallSites: []core.CallSite{{Callee: "self.helper", Line: 1}},
		},
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "Person.m::greet@sha", "Person.m::helper@sha") {
		t.Fatalf("[self helper] must resolve to Person.helper")
	}
}

func TestObjCLocalTypes_PointerLocalNarrowsCall(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "Formatter.h::Formatter@sha", FilePath: "Formatter.h", BlobSHA: "sha",
			Language: "objc", Kind: core.KindClass, Name: "Formatter", QualifiedName: "Formatter",
			Signature: "@interface Formatter : NSObject"},
		{ID: "Formatter.m::format:@sha", FilePath: "Formatter.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "format:", QualifiedName: "format:",
			ParentSymbol: "Formatter", Signature: "- (NSString *)format:(id)x"},
		{ID: "App.m::run@sha", FilePath: "App.m", BlobSHA: "sha",
			Language: "objc", Kind: core.KindMethod, Name: "run", QualifiedName: "run",
			ParentSymbol: "App", Signature: "- (void)run",
			RawText:   "- (void)run { Formatter *f = [[Formatter alloc] init]; [f format:x]; }",
			CallSites: []core.CallSite{{Callee: "f.format:", Line: 1}},
		},
	}
	g.Replace(syms, 3)

	if !hasEdge(g, core.EdgeCalls, "App.m::run@sha", "Formatter.m::format:@sha") {
		t.Fatalf("[f format:] must resolve to Formatter.format: via the `Formatter *f = ...` local type")
	}
}
