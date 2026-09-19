package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Kotlin overload and receiver rules measured on lordcodes/turtle (P 1.00 /
// R 0.99 against the kotlinc-javap oracle). Each fixture is the minimal
// shape of a turtle site that was a false positive or miss before the rule.

func kotlinSym(id, file string, kind core.SymbolKind, name, parent, sig, raw string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "kotlin", Kind: kind,
		Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig,
		RawText: raw, CallSites: sites,
	}
}

func TestKotlinOverloads_ArgTypeNarrowing(t *testing.T) {
	// `Arguments(url)` with url: String binds the vararg-Any factory (the
	// constructor takes List<String>); `Arguments(listOf(x))` the reverse.
	g := New()
	syms := []core.SymbolRecord{
		kotlinSym("A.kt::Arguments@sha", "A.kt", core.KindClass, "Arguments", "", "data class Arguments(val arguments: List<String>,)", ""),
		kotlinSym("A.kt::Arguments.Arguments@sha", "A.kt", core.KindConstructor, "Arguments", "Arguments", "Arguments(val arguments: List<String>,)", ""),
		kotlinSym("A.kt::ArgumentsFactory@sha", "A.kt", core.KindFunction, "Arguments", "", "fun Arguments(vararg arguments: Any?): Arguments", ""),
		kotlinSym("F.kt::F.open@sha", "F.kt", core.KindMethod, "open", "F",
			`@Suppress("unused") fun open(url: String): String`, "",
			core.CallSite{Callee: "Arguments", Line: 1, Argc: 1, Args: []string{"url"}}),
		kotlinSym("F.kt::F.list@sha", "F.kt", core.KindMethod, "list", "F",
			"fun list(x: String): String", "",
			core.CallSite{Callee: "Arguments", Line: 1, Argc: 1, Args: []string{"call:listOf"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "F.kt::F.open@sha", "A.kt::ArgumentsFactory@sha") {
		t.Fatalf("Arguments(url: String) must bind the vararg Any? factory")
	}
	if hasEdge(g, core.EdgeCalls, "F.kt::F.open@sha", "A.kt::Arguments.Arguments@sha") {
		t.Fatalf("Arguments(url: String) must not bind the List<String> constructor")
	}
	if !hasEdge(g, core.EdgeCalls, "F.kt::F.list@sha", "A.kt::Arguments.Arguments@sha") {
		t.Fatalf("Arguments(listOf(x)) must bind the List<String> constructor")
	}
	if hasEdge(g, core.EdgeCalls, "F.kt::F.list@sha", "A.kt::ArgumentsFactory@sha") {
		t.Fatalf("Arguments(listOf(x)): exact List match beats the Any? catch-all")
	}
}

func TestKotlinReceivers_UnknownExternalAndExtension(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		kotlinSym("C.kt::Command@sha", "C.kt", core.KindClass, "Command", "", "data class Command(val executable: String)", ""),
		kotlinSym("C.kt::Command.contains@sha", "C.kt", core.KindMethod, "contains", "Command", "operator fun contains(argument: String)", ""),
		kotlinSym("S.kt::ShellScript@sha", "S.kt", core.KindClass, "ShellScript", "", "class ShellScript", ""),
		kotlinSym("S.kt::ShellScript.retrieveOutput@sha", "S.kt", core.KindMethod, "retrieveOutput", "ShellScript", "private fun Process.retrieveOutput(): String", ""),
		// A String parameter is an external receiver: its `contains` is
		// the stdlib's, never Command's.
		kotlinSym("S.kt::ShellScript.check@sha", "S.kt", core.KindMethod, "check", "ShellScript",
			"fun check(command: String): Boolean", "",
			core.CallSite{Callee: "command.contains", Line: 1, Argc: 1, Args: []string{"#String"}}),
		// An untyped local reaches an in-repo extension function.
		kotlinSym("S.kt::ShellScript.command@sha", "S.kt", core.KindMethod, "command", "ShellScript",
			"fun command(cmd: String): String", "fun command(cmd: String): String { val process = builder.start(); return process.retrieveOutput() }",
			core.CallSite{Callee: "process.retrieveOutput", Line: 1, Argc: 0}),
		// A call-result receiver nobody in the repo produces is external.
		kotlinSym("S.kt::ShellScript.seq@sha", "S.kt", core.KindMethod, "seq", "ShellScript",
			"fun seq(cmd: String): String", "",
			core.CallSite{Callee: "redirectErrorStream().command", Line: 1, Argc: 1, Args: []string{"cmd"}}),
	}
	g.Replace(syms, 2)

	if hasEdge(g, core.EdgeCalls, "S.kt::ShellScript.check@sha", "C.kt::Command.contains@sha") {
		t.Fatalf("String.contains must not resolve to Command.contains")
	}
	if !hasEdge(g, core.EdgeCalls, "S.kt::ShellScript.command@sha", "S.kt::ShellScript.retrieveOutput@sha") {
		t.Fatalf("process.retrieveOutput() must reach the Process extension function")
	}
	if hasEdge(g, core.EdgeCalls, "S.kt::ShellScript.seq@sha", "S.kt::ShellScript.command@sha") {
		t.Fatalf("ProcessBuilder chain .command() must not resolve to ShellScript.command")
	}
}

func TestKotlinReceivers_PropertyTypeAndCallResult(t *testing.T) {
	// `arguments + other` inside Command: the property's declared type
	// picks Arguments.plus over Command's and Executable's; `Executable("ls")
	// + args` picks Executable.plus by the constructor call's type.
	g := New()
	syms := []core.SymbolRecord{
		kotlinSym("A.kt::Arguments@sha", "A.kt", core.KindClass, "Arguments", "", "data class Arguments(val arguments: List<String>,)", ""),
		kotlinSym("A.kt::Arguments.plus@sha", "A.kt", core.KindMethod, "plus", "Arguments", "operator fun plus(arguments: Arguments): Arguments", ""),
		kotlinSym("E.kt::Executable@sha", "E.kt", core.KindClass, "Executable", "", "data class Executable(val name: String)", ""),
		kotlinSym("E.kt::Executable.Executable@sha", "E.kt", core.KindConstructor, "Executable", "Executable", "Executable(val name: String)", ""),
		kotlinSym("E.kt::Executable.plus@sha", "E.kt", core.KindMethod, "plus", "Executable", "operator fun plus(arguments: Arguments): Command", ""),
		kotlinSym("C.kt::Command@sha", "C.kt", core.KindClass, "Command", "", "data class Command(val executable: Executable, val arguments: Arguments)", ""),
		kotlinSym("C.kt::Command.arguments@sha", "C.kt", core.KindField, "arguments", "Command", "val arguments: Arguments", ""),
		kotlinSym("C.kt::Command.plus@sha", "C.kt", core.KindMethod, "plus", "Command", "operator fun plus(arguments: Arguments): Command", "",
			core.CallSite{Callee: "arguments.plus", Line: 1, Argc: 1, Args: []string{"arguments"}}),
		kotlinSym("C.kt::Command.minus@sha", "C.kt", core.KindMethod, "minus", "Command", "operator fun minus(arguments: Arguments): Command", ""),
		kotlinSym("F.kt::F.open@sha", "F.kt", core.KindMethod, "open", "F", "fun open(args: Arguments): Command", "",
			core.CallSite{Callee: "Executable().plus", Line: 1, Argc: 1, Args: []string{"args"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "C.kt::Command.plus@sha", "A.kt::Arguments.plus@sha") {
		t.Fatalf("arguments + x must resolve through the property's type to Arguments.plus")
	}
	for _, wrong := range []string{"E.kt::Executable.plus@sha"} {
		if hasEdge(g, core.EdgeCalls, "C.kt::Command.plus@sha", wrong) {
			t.Fatalf("arguments + x must not resolve to %s", wrong)
		}
	}
	if !hasEdge(g, core.EdgeCalls, "F.kt::F.open@sha", "E.kt::Executable.plus@sha") {
		t.Fatalf("Executable(..) + args must resolve to Executable.plus")
	}
	if hasEdge(g, core.EdgeCalls, "F.kt::F.open@sha", "A.kt::Arguments.plus@sha") {
		t.Fatalf("Executable(..) + args must not resolve to Arguments.plus")
	}
}

func TestKotlinBareCalls_TopLevelAndExtensionScope(t *testing.T) {
	// toString() inside `Any.asArgumentOrNull()` is Any's, not Command's;
	// a top-level function's bare call still reaches top-level functions.
	g := New()
	syms := []core.SymbolRecord{
		kotlinSym("C.kt::Command@sha", "C.kt", core.KindClass, "Command", "", "data class Command(val executable: String)", ""),
		kotlinSym("C.kt::Command.toString@sha", "C.kt", core.KindMethod, "toString", "Command", "override fun toString(): String", ""),
		kotlinSym("A.kt::flatten@sha", "A.kt", core.KindFunction, "recursivelyFlatten", "", "private fun List<Any?>.recursivelyFlatten(): List<Any?>", ""),
		kotlinSym("A.kt::asArgumentOrNull@sha", "A.kt", core.KindFunction, "asArgumentOrNull", "", "private fun Any.asArgumentOrNull(): String?", "",
			core.CallSite{Callee: "toString", Line: 1, Argc: 0}),
		kotlinSym("A.kt::Arguments@sha", "A.kt", core.KindFunction, "Arguments", "", "fun Arguments(vararg arguments: Any?): Arguments", "",
			core.CallSite{Callee: "recursivelyFlatten", Line: 1, Argc: 0}),
	}
	g.Replace(syms, 2)

	if hasEdge(g, core.EdgeCalls, "A.kt::asArgumentOrNull@sha", "C.kt::Command.toString@sha") {
		t.Fatalf("bare toString() in an Any extension must not resolve to Command.toString")
	}
	if !hasEdge(g, core.EdgeCalls, "A.kt::Arguments@sha", "A.kt::flatten@sha") {
		t.Fatalf("a top-level function's bare call must still reach a top-level function")
	}
}

func TestKotlinConstructors_DelegationAndSuper(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		kotlinSym("B.kt::Base@sha", "B.kt", core.KindClass, "Base", "", "open class Base(val n: Int)", ""),
		kotlinSym("B.kt::Base.Base@sha", "B.kt", core.KindConstructor, "Base", "Base", "Base(val n: Int)", ""),
		kotlinSym("P.kt::Plain@sha", "P.kt", core.KindClass, "Plain", "", "class Plain : Base(1)", ""),
		kotlinSym("P.kt::Plain.ctor1@sha", "P.kt", core.KindConstructor, "Plain", "Plain", "Plain(x: Int)", "",
			core.CallSite{Callee: "this()", Line: 2, Argc: 0}),
		kotlinSym("P.kt::Plain.ctor0@sha", "P.kt", core.KindConstructor, "Plain", "Plain", "Plain()", "",
			core.CallSite{Callee: "super()", Line: 3, Argc: 1, Args: []string{"#int"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "P.kt::Plain.ctor1@sha", "P.kt::Plain.ctor0@sha") {
		t.Fatalf("constructor(x) : this() must resolve to the zero-arg sibling")
	}
	if !hasEdge(g, core.EdgeCalls, "P.kt::Plain.ctor0@sha", "B.kt::Base.Base@sha") {
		t.Fatalf("super(1) must resolve to Base's constructor")
	}
}
