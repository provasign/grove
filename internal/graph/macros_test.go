package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// C preprocessor call-through, measured on cJSON (R 0.646 → 0.999) and
// jansson (R 0.925 → 0.984): a function-like macro's body calls execute in
// every function that invokes it.

func cSym(id, file string, kind core.SymbolKind, name, sig string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "c", Kind: kind,
		Name: name, QualifiedName: name, Signature: sig, CallSites: sites,
	}
}

func TestCMacros_CallThroughSubstitutesArguments(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		// #define RUN_TEST(func) UnityDefaultTestRun(func, #func, __LINE__)
		cSym("u.h::RUN_TEST@sha", "u.h", core.KindMacro, "RUN_TEST", "#define RUN_TEST(func)",
			core.CallSite{Callee: "UnityDefaultTestRun", Line: 1, Argc: 3, Args: []string{"func", "#String", "__LINE__"}}),
		// #define UNITY_BEGIN() UnityBegin(__FILE__)
		cSym("u.h::UNITY_BEGIN@sha", "u.h", core.KindMacro, "UNITY_BEGIN", "#define UNITY_BEGIN()",
			core.CallSite{Callee: "UnityBegin", Line: 2, Argc: 1, Args: []string{"__FILE__"}}),
		cSym("u.c::UnityDefaultTestRun@sha", "u.c", core.KindFunction, "UnityDefaultTestRun", "void UnityDefaultTestRun(UnityTestFunction Func, const char* FuncName, const int FuncLineNum)"),
		cSym("u.c::UnityBegin@sha", "u.c", core.KindFunction, "UnityBegin", "void UnityBegin(const char* filename)"),
		cSym("t.c::should_add_null@sha", "t.c", core.KindFunction, "should_add_null", "static void should_add_null(void)"),
		cSym("t.c::main@sha", "t.c", core.KindFunction, "main", "int main(void)",
			core.CallSite{Callee: "UNITY_BEGIN", Line: 10, Argc: 0},
			core.CallSite{Callee: "RUN_TEST", Line: 11, Argc: 1, Args: []string{"should_add_null"}}),
	}
	g.Replace(syms, 2)

	for _, want := range []string{"u.c::UnityBegin@sha", "u.c::UnityDefaultTestRun@sha", "t.c::should_add_null@sha"} {
		if !hasEdge(g, core.EdgeCalls, "t.c::main@sha", want) {
			t.Fatalf("main must reach %s through the macro expansion", want)
		}
	}
}

func TestCMacros_NestedExpansionParameterCalleeAndCycle(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		// #define CALL(f, x) f(x)          — the callee IS a parameter
		cSym("m.h::CALL@sha", "m.h", core.KindMacro, "CALL", "#define CALL(f,x)",
			core.CallSite{Callee: "f", Line: 1, Argc: 1, Args: []string{"x"}}),
		// #define APPLY(x) CALL(handle, x) — nested macro invocation
		cSym("m.h::APPLY@sha", "m.h", core.KindMacro, "APPLY", "#define APPLY(x)",
			core.CallSite{Callee: "CALL", Line: 2, Argc: 2, Args: []string{"handle", "x"}}),
		// #define LOOP(n) LOOP(n)          — self-referential, must not recurse
		cSym("m.h::LOOP@sha", "m.h", core.KindMacro, "LOOP", "#define LOOP(n)",
			core.CallSite{Callee: "LOOP", Line: 3, Argc: 1, Args: []string{"n"}}),
		cSym("h.c::handle@sha", "h.c", core.KindFunction, "handle", "void handle(int x)"),
		cSym("h.c::other@sha", "h.c", core.KindFunction, "other", "void other(int x)"),
		cSym("h.c::run@sha", "h.c", core.KindFunction, "run", "void run(void)",
			core.CallSite{Callee: "APPLY", Line: 10, Argc: 1, Args: []string{"1"}},
			core.CallSite{Callee: "LOOP", Line: 11, Argc: 1, Args: []string{"3"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "h.c::run@sha", "h.c::handle@sha") {
		t.Fatalf("APPLY(1) → CALL(handle, 1) → handle(1) must reach handle")
	}
	if hasEdge(g, core.EdgeCalls, "h.c::run@sha", "h.c::other@sha") {
		t.Fatalf("expansion must not reach a function the macro never names")
	}
}

func TestCMacros_MacroParamsAndNonCLanguagesUntouched(t *testing.T) {
	if got := macroParams("#define RUN_TEST(func, num)"); len(got) != 2 || got[0] != "func" || got[1] != "num" {
		t.Fatalf("macroParams = %v", got)
	}
	if got := macroParams("#define UNITY_BEGIN()"); len(got) != 0 {
		t.Fatalf("empty parameter list = %v", got)
	}
	if got := macroParams("#define MAX_DEPTH"); got != nil {
		t.Fatalf("object-like macro has no params, got %v", got)
	}
	idx := &edgeIndex{byName: map[string][]*core.SymbolRecord{}}
	goSym := core.SymbolRecord{Language: "go", CallSites: []core.CallSite{{Callee: "RUN_TEST"}}}
	if got := expandMacroCallSites(idx, &goSym); len(got) != 1 {
		t.Fatalf("non-C caller must be returned as is, got %v", got)
	}
}
