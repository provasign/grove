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

func javaHasCall(edges []core.Edge, from, to string) bool {
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls && edge.From == from && edge.To == to {
			return true
		}
	}
	return false
}
