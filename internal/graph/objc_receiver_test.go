package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Objective-C receiver rules measured on SBJson (json-framework, P 1.00 /
// R 0.99 against the clang-AST oracle): each fixture is the minimal shape
// of a site that was a false positive before the rule.

func objcSym(id, file string, kind core.SymbolKind, name, parent, sig string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "objc", Kind: kind,
		Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig, CallSites: sites,
	}
}

func TestObjCReceivers_IvarTypeSuperAndDynamic(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		objcSym("S.h::State@sha", "S.h", core.KindClass, "State", "", "@interface State : NSObject"),
		objcSym("S.m::State.append:@sha", "S.m", core.KindMethod, "append:", "State", "- (void)append:(Writer *)w"),
		objcSym("S.h::KeyState@sha", "S.h", core.KindClass, "KeyState", "", "@interface KeyState : State"),
		objcSym("S.m::KeyState.append:@sha", "S.m", core.KindMethod, "append:", "KeyState", "- (void)append:(Writer *)w"),
		objcSym("W.h::Writer@sha", "W.h", core.KindClass, "Writer", "", "@interface Writer : NSObject"),
		// The synthesized ivar `_state` backs the property `state`.
		objcSym("W.h::Writer.state@sha", "W.h", core.KindField, "state", "Writer", "@property (nonatomic, strong) State *state;"),
		objcSym("W.h::Writer.delegate@sha", "W.h", core.KindField, "delegate", "Writer", "@property (weak) id<WriterDelegate> delegate;"),
		objcSym("W.m::Writer.init@sha", "W.m", core.KindMethod, "init", "Writer", "- (id)init"),
		objcSym("D.m::Delegate.didWrite@sha", "D.m", core.KindMethod, "didWrite", "Delegate", "- (void)didWrite"),
		objcSym("W.m::Writer.writeNull@sha", "W.m", core.KindMethod, "writeNull", "Writer", "- (BOOL)writeNull",
			core.CallSite{Callee: "_state.append:", Line: 1, Argc: 1, Args: []string{"self"}},
			core.CallSite{Callee: "delegate.didWrite", Line: 2},
			core.CallSite{Callee: "unknownThing.didWrite", Line: 3}),
		objcSym("W.m::Writer.initWithX:@sha", "W.m", core.KindMethod, "initWithX:", "Writer", "- (id)initWithX:(int)x",
			core.CallSite{Callee: "super.init", Line: 1},
			core.CallSite{Callee: "KeyState().append:", Line: 2, Argc: 1, Args: []string{"self"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "W.m::Writer.writeNull@sha", "S.m::State.append:@sha") {
		t.Fatalf("[_state append:] must bind the ivar's declared class State")
	}
	if hasEdge(g, core.EdgeCalls, "W.m::Writer.writeNull@sha", "D.m::Delegate.didWrite@sha") {
		t.Fatalf("an id<Protocol> property and an undeclared receiver are dynamic: no edge")
	}
	if hasEdge(g, core.EdgeCalls, "W.m::Writer.initWithX:@sha", "W.m::Writer.init@sha") {
		t.Fatalf("[super init] must not bind the class's own init (NSObject is not indexed)")
	}
	if !hasEdge(g, core.EdgeCalls, "W.m::Writer.initWithX:@sha", "S.m::KeyState.append:@sha") {
		t.Fatalf("[[KeyState alloc] init] result must bind KeyState's override")
	}
}
