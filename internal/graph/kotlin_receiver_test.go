package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A bare call inside a lambda with receiver binds the receiver's member:
// shellRun's last parameter is `script: ShellScript.() -> String`, and
// x.apply { } runs its block with x as this.
func TestKotlinLambdaWithReceiver(t *testing.T) {
	g := New()
	kt := func(kind core.SymbolKind, file, name, parent, sig, raw string, start, end int, sites ...core.CallSite) core.SymbolRecord {
		q := name
		if parent != "" {
			q = parent + "." + name
		}
		return core.SymbolRecord{ID: file + "::" + q + "@b", FilePath: file, BlobSHA: "b", Language: "kotlin", Kind: kind,
			Name: name, QualifiedName: q, ParentSymbol: parent, Signature: sig, RawText: raw,
			Span: core.LineRange{Start: start, End: end}, CallSites: sites, Imports: []string{"S.kt", "B.kt", "Shell.kt"}}
	}
	syms := []core.SymbolRecord{
		kt(core.KindClass, "S.kt", "ShellScript", "", "class ShellScript", "class ShellScript {}", 1, 9),
		kt(core.KindMethod, "S.kt", "command", "ShellScript", "fun command(command: String, arguments: List<String>): String", "fun command(command: String, arguments: List<String>): String = \"\"", 3, 3),
		kt(core.KindClass, "B.kt", "Builder", "", "class Builder", "class Builder {}", 1, 9),
		kt(core.KindMethod, "B.kt", "command", "Builder", "fun command(command: String, arguments: List<String>): Builder", "fun command(command: String, arguments: List<String>): Builder = this", 3, 3),
		kt(core.KindMethod, "B.kt", "add", "Builder", "fun add(x: String)", "fun add(x: String) {}", 4, 4),
		kt(core.KindFunction, "Shell.kt", "shellRun", "", "fun shellRun(dryRun: Boolean = false, script: ShellScript.() -> String): String",
			"fun shellRun(dryRun: Boolean = false, script: ShellScript.() -> String): String = \"\"", 1, 1),
		kt(core.KindFunction, "Shell.kt", "shellRunCommand", "", "fun shellRunCommand(command: String, arguments: List<String>): String",
			"fun shellRunCommand(command: String, arguments: List<String>): String = shellRun(dryRun = false) { command(command, arguments) }", 3, 3,
			core.CallSite{Callee: "shellRun", Line: 3, Col: 73, Argc: 1},
			core.CallSite{Callee: "command", Line: 3, Col: 100, Argc: 2, Args: []string{"command", "arguments"}}),
		kt(core.KindFunction, "Shell.kt", "build", "", "fun build(b: Builder)",
			"fun build(b: Builder) {\n    b.apply {\n        add(\"x\")\n    }\n}", 5, 9,
			core.CallSite{Callee: "b.apply", Line: 6, Col: 7},
			core.CallSite{Callee: "add", Line: 7, Col: 9, Argc: 1, Args: []string{"#String"}}),
	}
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeCalls, "Shell.kt::shellRunCommand@b", "S.kt::ShellScript.command@b") {
		t.Errorf("command(...) inside shellRun's ShellScript.() -> String lambda must bind ShellScript.command")
	}
	if hasEdge(g, core.EdgeCalls, "Shell.kt::shellRunCommand@b", "B.kt::Builder.command@b") {
		t.Errorf("the lambda's receiver is ShellScript, not Builder")
	}
	if !hasEdge(g, core.EdgeCalls, "Shell.kt::build@b", "B.kt::Builder.add@b") {
		t.Errorf("add(...) inside b.apply { } must bind Builder.add")
	}
}
