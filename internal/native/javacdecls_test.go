package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// gson TypeAdapters.java (2026-10-12): 25 TypeAdapter.write overrides live in
// anonymous classes assigned to static fields; the parser indexed none, javac
// attributed all of them, and change_impact reported the family closed at 6
// of 31. The compiler's declarations are the universe: whatever the parser
// lacks is added as a compiler-declared symbol, in the parser's naming, and
// takes part in the inheritance pass like any other symbol.
func TestJavacAddsDeclarationsTheParserMissed(t *testing.T) {
	if findJDK() == nil {
		t.Skip("no JDK 11+ available")
	}
	root := t.TempDir()
	src := `package p;

public class Adapters {
  public static final Adapter CLASS =
      new Adapter() {
        @Override
        public void write(Out out, Object value) {
          out.nullValue();
        }
      };

  public abstract static class Adapter {
    public abstract void write(Out out, Object value);
  }

  public static class Out {
    public void nullValue() {}
  }
}
`
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "p", "Adapters.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// The parser's view before the astkit fix: the field, but nothing
	// declared inside its initializer.
	file, blob := "p/Adapters.java", "blob1"
	sym := func(kind core.SymbolKind, name, qualified, parent string, start, end int, signature string) core.SymbolRecord {
		return core.SymbolRecord{ID: file + "::" + qualified + "@" + blob, FilePath: file, BlobSHA: blob, Language: "java",
			Kind: kind, Name: name, QualifiedName: qualified, ParentSymbol: parent,
			Span: core.LineRange{Start: start, End: end}, Signature: signature, RawText: signature}
	}
	parsed := []core.SymbolRecord{
		sym(core.KindClass, "Adapters", "Adapters", "", 3, 20, "public class Adapters {"),
		sym(core.KindField, "CLASS", "Adapters.CLASS", "Adapters", 4, 10, "public static final Adapter CLASS ="),
		sym(core.KindClass, "Adapter", "Adapters.Adapter", "Adapters", 12, 14, "public abstract static class Adapter {"),
		sym(core.KindMethod, "write", "Adapters.Adapter.write", "Adapter", 13, 13, "public abstract void write(Out out, Object value);"),
		sym(core.KindClass, "Out", "Adapters.Out", "Adapters", 16, 18, "public static class Out {"),
		sym(core.KindMethod, "nullValue", "Adapters.Out.nullValue", "Out", 17, 17, "public void nullValue() {}"),
	}
	result := javaAnalyzer{}.Analyze(context.Background(), Request{Root: root, Symbols: parsed, Files: []string{file}})
	byQN := map[string]core.SymbolRecord{}
	for _, s := range result.Symbols {
		byQN[s.QualifiedName] = s
	}
	anon, ok := byQN["Adapters.<anonymous@5:7>"]
	if !ok {
		t.Fatalf("anonymous class not declared by the compiler; got %v\n%s", keys(byQN), strings.Join(result.Diagnostics, "\n"))
	}
	if anon.Kind != core.KindClass || anon.Span.Start != 5 || anon.Span.End != 10 ||
		!strings.Contains(anon.Signature, "extends Adapter") || anon.ParentSymbol != "Adapters" {
		t.Fatalf("anonymous class record: %+v", anon)
	}
	write, ok := byQN["Adapters.<anonymous@5:7>.write"]
	if !ok || write.Kind != core.KindMethod || write.ParentSymbol != "<anonymous@5:7>" || write.Span.Start != 6 {
		t.Fatalf("override inside the anonymous class: %+v (found %v)", write, ok)
	}
	for _, s := range result.Symbols {
		if s.QualifiedName == "Adapters" || s.QualifiedName == "Adapters.Adapter.write" {
			t.Fatalf("a parsed declaration was declared again: %+v", s)
		}
	}
	// Parser-identical records: RawText is the `new Adapter() {...}` text
	// (the family pass matches method sets, not an extends header, for
	// anonymous classes), and the method body text is its own span.
	if !strings.HasPrefix(anon.RawText, "new Adapter() {") || !strings.Contains(write.RawText, "out.nullValue();") {
		t.Fatalf("raw text: class %q / method %q", anon.RawText, write.RawText)
	}
	joined := strings.Join(result.Diagnostics, "\n")
	if !strings.Contains(joined, "javac declared 2 symbol(s) in 1 file(s) the parser did not index") {
		t.Fatalf("diagnostic missing:\n%s", joined)
	}
}

func keys(m map[string]core.SymbolRecord) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A declaration the parser has under the same name, one line off (javac and
// tree-sitter disagree on line counting around control bytes), is covered,
// not declared twice.
func TestCompilerDeclaredSymbolsCoverOverlappingSpans(t *testing.T) {
	root := t.TempDir()
	body := "class A {\n  void m() {\n  }\n}\n"
	if err := os.WriteFile(filepath.Join(root, "A.java"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	req := Request{Root: root, Symbols: []core.SymbolRecord{
		{ID: "A.java::A@b", FilePath: "A.java", BlobSHA: "b", Language: "java", Kind: core.KindClass, Name: "A", Span: core.LineRange{Start: 1, End: 4}},
		{ID: "A.java::A.m@b", FilePath: "A.java", BlobSHA: "b", Language: "java", Kind: core.KindMethod, Name: "m", Span: core.LineRange{Start: 3, End: 3}},
	}}
	got := compilerDeclaredSymbols(req, []compilerDecl{
		{File: "A.java", Kind: "method", Name: "m", Qualified: "A.m", Parent: "A", Start: 2, End: 3},
		{File: "A.java", Kind: "method", Name: "n", Qualified: "A.n", Parent: "A", Start: 2, End: 3},
	})
	if len(got) != 1 || got[0].Name != "n" {
		t.Fatalf("want only the undeclared n, got %+v", got)
	}
}

// javac reports each call's line, and a call edge carries the lines javac
// resolved to its target: one caller calling two overloads gets each line
// under its own overload (gson JsonElementTypeAdapter.write: value(Number)
// at 145, value(boolean) at 147, value(String) at 149), and a call in a
// lambda lands on the lambda's edge too.
func TestJavacCallEdgesCarryTheirLines(t *testing.T) {
	if findJDK() == nil {
		t.Skip("no JDK 11+ available")
	}
	root := t.TempDir()
	src := `package p;

public class W {
  public W value(String s) { return this; }
  public W value(long n) { return this; }
  void both() {
    value("a");
    value(2L);
    Runnable r = () -> value("b");
    this
        .value(3L);
  }
}
`
	if err := os.MkdirAll(filepath.Join(root, "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "p", "W.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	file, blob := "p/W.java", "b1"
	sym := func(kind core.SymbolKind, name, qualified, id string, start, end int, sig string) core.SymbolRecord {
		return core.SymbolRecord{ID: file + "::" + id + "@" + blob, FilePath: file, BlobSHA: blob, Language: "java",
			Kind: kind, Name: name, QualifiedName: qualified, ParentSymbol: "W", Span: core.LineRange{Start: start, End: end}, Signature: sig, RawText: sig}
	}
	parsed := []core.SymbolRecord{
		{ID: file + "::W@" + blob, FilePath: file, BlobSHA: blob, Language: "java", Kind: core.KindClass, Name: "W", QualifiedName: "W", Span: core.LineRange{Start: 3, End: 13}},
		sym(core.KindMethod, "value", "W.value", "W.value", 4, 4, "public W value(String s)"),
		sym(core.KindMethod, "value", "W.value", "W.value#2", 5, 5, "public W value(long n)"),
		sym(core.KindMethod, "both", "W.both", "W.both", 6, 12, "void both()"),
		sym(core.KindFunction, "<lambda@9:18>", "W.<lambda@9:18>", "W.<lambda@9:18>", 9, 9, "() -> value(\"b\")"),
	}
	result := javaAnalyzer{}.Analyze(context.Background(), Request{Root: root, Symbols: parsed, Files: []string{file}})
	got := map[string]string{}
	for _, e := range result.Edges {
		if e.Type == core.EdgeCalls && e.Source == core.EvidenceSourceNative {
			got[strings.TrimPrefix(e.From, file+"::")+" -> "+strings.TrimPrefix(e.To, file+"::")] = e.Lines
		}
	}
	for edge, lines := range map[string]string{
		"W.both@b1 -> W.value@b1":          "7:5,9:24",
		"W.both@b1 -> W.value#2@b1":        "8:5,11:10",
		"W.<lambda@9:18>@b1 -> W.value@b1": "9:24",
	} {
		if got[edge] != lines {
			t.Errorf("%s: lines %q, want %q (all: %v)\n%s", edge, got[edge], lines, got, strings.Join(result.Diagnostics, "\n"))
		}
	}
}
