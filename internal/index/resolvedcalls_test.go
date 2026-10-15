package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// callEdges indexes files and returns "caller→callee" for every call edge
// (any source), the diagnostics, and the stored edges.
func callEdges(t *testing.T, files map[string]string) (map[string]bool, []string, []core.Edge) {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	g, r, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	syms, edges := g.Snapshot()
	name := map[string]string{}
	for _, s := range syms {
		name[s.ID] = s.QualifiedName
		if s.Language == "java" && s.Kind == core.KindMethod {
			name[s.ID] += ":" + strconv.Itoa(s.Span.Start)
		}
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeResolvedCalls {
			t.Errorf("a resolved-calls record reached the graph: %+v", e)
		}
		if e.Type == core.EdgeCalls {
			got[name[e.From]+"→"+name[e.To]] = true
		}
	}
	stored, err := st.AllEdges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return got, r.Native, stored
}

// A call the compiler resolved to a library method, or to the caller
// itself, drops the name-based guess at the same position: gin's
// c.Request.Header.Get(key) is not Context.Get, and a method recursing is
// not calling its overloads (oracle review 2026-10-15).
func TestJavacLibraryAndRecursiveCallsDropNameGuesses(t *testing.T) {
	got, diags, stored := callEdges(t, map[string]string{
		"src/main/java/p/Text.java": `package p;
public class Text {
  public static String trim(String s) { return s == null ? null : s.strip(); }
  public static String norm(String s) { return s.trim(); }
  public static String own(String s) { return trim(s); }
  public int walk(int n) { return n <= 0 ? 0 : walk(n - 1); }
  public int walk(String s) { return 0; }
  public int walk(Object o) { return 1; }
}
`,
	})
	skipUnless(t, diags, "javac attributed")
	for _, bad := range []string{"Text.norm:4→Text.trim:3", "Text.walk:6→Text.walk:7", "Text.walk:6→Text.walk:8"} {
		if got[bad] {
			t.Errorf("name guess survived the compiler's resolution: %s", bad)
		}
	}
	if !got["Text.own:5→Text.trim:3"] {
		t.Errorf("the real call own→trim is missing: %v", got)
	}
	marked := false
	for _, e := range stored {
		marked = marked || e.Type == core.EdgeResolvedCalls
	}
	if !marked {
		t.Error("no resolved-calls record stored; an incremental build could not drop the guesses")
	}
}

func TestGoLibraryAndRecursiveCallsDropNameGuesses(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	got, diags, _ := callEdges(t, map[string]string{
		"go.mod": "module m\n\ngo 1.22\n",
		"ctx.go": `package m

import "net/http"

type Ctx struct{ r *http.Request }

func (c *Ctx) Get(key string) string { return c.r.Header.Get(key) }

func (c *Ctx) Own(key string) string { return c.Get(key) }

func Count(n int) int {
	if n <= 0 {
		return 0
	}
	return Count(n - 1)
}
`,
	})
	skipUnless(t, diags, "go")
	if got["Ctx.Get→Ctx.Get"] {
		t.Error("c.r.Header.Get guessed as Ctx.Get")
	}
	if !got["Ctx.Own→Ctx.Get"] {
		t.Errorf("the real call Own→Get is missing: %v", got)
	}
}

// Renaming an overload re-binds same-package callers under another source
// root (src/test/java next to src/main/java), which import nothing; the
// incremental index equals a full one (fuzz, 2026-10-15: StopWatch.split).
func TestJavacIncrementalRebindsSamePackageOtherRoot(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/main/java/t/W.java", "package t;\npublic class W {\n  public int check(long x) { return 1; }\n  public int check(int x) { return 2; }\n}\n")
	write("src/test/java/t/WTest.java", "package t;\npublic class WTest {\n  int run() { return new W().check(1); }\n}\n")
	for i := 0; i < 4; i++ { // unrelated packages keep the run scoped
		write("src/main/java/u"+strconv.Itoa(i)+"/X.java", "package u"+strconv.Itoa(i)+";\npublic class X { public int n() { return 3; } }\n")
	}
	_, diags, st := compilerIndex(t, root)
	skipUnless(t, diags, "javac attributed")
	st.Close()
	write("src/main/java/t/W.java", "package t;\npublic class W {\n  public int check(long x) { return 1; }\n  public int checkFz(int x) { return 2; }\n}\n")
	inc, incDiags, st2 := compilerIndex(t, root)
	st2.Close()
	if err := os.RemoveAll(filepath.Join(root, ".grove")); err != nil {
		t.Fatal(err)
	}
	full, _, st3 := compilerIndex(t, root)
	st3.Close()
	if !full["run→check calls"] {
		t.Fatalf("fixture: full index lacks run→check: %v", full)
	}
	for k := range full {
		if !inc[k] {
			t.Errorf("full index has %q, incremental does not\ninc diags: %v", k, incDiags)
		}
	}
}

// A half-typed line between a method's annotations garbles its parsed
// declaration; callers that reach it through a subclass import neither the
// class nor its package. Their stored edges must not be lost: the run
// re-attributes the files the graph knows call into the edited file
// (fuzz, 2026-10-15: commons-io AbstractByteArrayOutputStream.toString).
func TestJavacIncrementalKeepsCallersOfAGarbledDeclaration(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	abs := func(garble string) string {
		return "package o;\npublic abstract class Abs {\n  public void setFlag(boolean b) {}\n  @Override\n" + garble +
			"  @Deprecated\n  public String toString() { return \"\"; }\n  public String toString(String enc) { return enc; }\n}\n"
	}
	write("src/main/java/o/Abs.java", abs(""))
	write("src/main/java/o/Sub.java", "package o;\npublic class Sub extends Abs {}\n")
	write("src/test/java/q/T.java", "package q;\nimport o.Sub;\npublic class T {\n  String run() { return new Sub().toString(); }\n}\n")
	for i := 0; i < 4; i++ {
		write("src/main/java/u"+strconv.Itoa(i)+"/X.java", "package u"+strconv.Itoa(i)+";\npublic class X { public int n() { return 3; } }\n")
	}
	_, diags, st := compilerIndex(t, root)
	skipUnless(t, diags, "javac attributed")
	st.Close()
	write("src/main/java/o/Abs.java", abs("  setFlag((1,\n"))
	inc, incDiags, st2 := compilerIndex(t, root)
	st2.Close()
	if err := os.RemoveAll(filepath.Join(root, ".grove")); err != nil {
		t.Fatal(err)
	}
	full, _, st3 := compilerIndex(t, root)
	st3.Close()
	for k := range full {
		if !inc[k] {
			t.Errorf("full index has %q, incremental does not\ninc diags: %v", k, incDiags)
		}
	}
	for k := range inc {
		if !full[k] {
			t.Errorf("incremental index has %q, full does not", k)
		}
	}
}

// try-with-resources calls the resource's close() on exit (oracle review
// 2026-10-15: 16 such calls missing in commons-io).
func TestJavacTryWithResourcesCallsClose(t *testing.T) {
	got, diags, _ := callEdges(t, map[string]string{
		"src/main/java/p/Res.java": "package p;\npublic class Res implements AutoCloseable {\n  public static Res open() { return new Res(); }\n  @Override public void close() {}\n}\n",
		"src/main/java/p/Sub.java": "package p;\npublic class Sub extends Res {}\n",
		"src/main/java/p/Use.java": "package p;\npublic class Use {\n  void use() {\n    try (Res r = Res.open()) {\n    }\n  }\n  void inherited() {\n    try (Sub s = new Sub()) {\n    }\n  }\n}\n",
	})
	skipUnless(t, diags, "javac attributed")
	for _, want := range []string{"Use.use:3→Res.close:4", "Use.inherited:7→Res.close:4"} {
		if !got[want] {
			t.Errorf("missing %s: %v", want, got)
		}
	}
}

// incMatchesFull indexes files, applies edit, indexes again, and compares
// every edge (any source, with positions) with a fresh index of the result.
func incMatchesFull(t *testing.T, files map[string]string, edit map[string]string, ranMarker string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range files {
		write(rel, body)
	}
	all := func() (map[string]bool, []string) {
		st, err := store.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		_, r, err := New(parser.NewEngine(), st).Index(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		edges, err := st.AllEdges(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, e := range edges {
			out[stripSHA(e.From)+" "+string(e.Type)+" "+stripSHA(e.To)+" "+string(e.Source)+" "+e.Lines] = true
		}
		return out, r.Native
	}
	_, diags := all()
	skipUnless(t, diags, ranMarker)
	for rel, body := range edit {
		write(rel, body)
	}
	inc, _ := all()
	if err := os.RemoveAll(filepath.Join(root, ".grove")); err != nil {
		t.Fatal(err)
	}
	full, _ := all()
	for k := range full {
		if !inc[k] {
			t.Errorf("full has, incremental lacks: %s", k)
		}
	}
	for k := range inc {
		if !full[k] {
			t.Errorf("incremental has, full lacks: %s", k)
		}
	}
}

func stripSHA(id string) string {
	if i := strings.LastIndexByte(id, '@'); i >= 0 && len(id)-i > 40 {
		return id[:i] + id[i+41:]
	}
	return id
}

// A half-typed line stops a Go package from type-checking: its callers'
// compiler edges into the broken file go away and their name-based guesses
// must come back, as in a full index (fuzz, 2026-10-15: cobra).
func TestGoIncrementalBrokenFileMatchesFull(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	files := map[string]string{
		"go.mod": "module m\n\ngo 1.22\n",
		"a.go":   "package m\n\ntype C struct{}\n\nfunc (c *C) Mark(n string) {}\n\nfunc validate(n int) error {\n\treturn nil\n}\n",
		"b.go":   "package m\n\nfunc Use(c *C) error {\n\tc.Mark(\"x\")\n\treturn validate(1)\n}\n",
	}
	for i := 0; i < 40; i++ { // keep the edit a small share, so the run is incremental
		n := strconv.Itoa(i)
		files["p"+n+"/f.go"] = "package p" + n + "\n\nfunc F" + n + "() int { return G" + n + "() }\n\nfunc G" + n + "() int { return " + n + " }\n"
	}
	incMatchesFull(t, files, map[string]string{
		"a.go": "package m\n\ntype C struct{}\n\nfunc (c *C) Mark(n string) {}\n\nfunc validate(n int) error {\n\tUse((1,\n\treturn nil\n}\n",
	}, "go")
}

// A caller whose stored compiler call edges name a callee the fresh result
// no longer resolves is re-resolved; one whose edges only moved to new IDs
// (the edited file's symbols) is not.
func TestLostCompilerCoverage(t *testing.T) {
	native := func(from, to string) core.Edge {
		return core.Edge{From: from, To: to, Type: core.EdgeCalls, Source: core.EvidenceSourceNative}
	}
	prev := []core.Edge{
		native("b.go::Use@1", "a.go::C.Mark@old"),
		native("c.go::Keep@1", "a.go::C.Mark@old"),
		{From: "d.go::Rec@1", To: "d.go::Rec@1", Type: core.EdgeResolvedCalls, Source: core.EvidenceSourceNative, Lines: "Get@3:5"},
	}
	fresh := []core.Edge{native("c.go::Keep@1", "a.go::C.Mark@new")}
	lost := lostCompilerCoverage(prev, fresh)
	if !lost["b.go::Use@1"] || lost["c.go::Keep@1"] || !lost["d.go::Rec@1"] || len(lost) != 2 {
		t.Fatalf("lost = %v, want b.go::Use@1 and d.go::Rec@1", lost)
	}
}

// TypeScript: calls the checker resolved to its libs (JSON.parse,
// Function.bind) drop the name-based guesses onto same-named project
// members (oracle review 2026-10-15: socket.io path.join -> Socket.join).
func TestTSLibraryCallsDropNameGuesses(t *testing.T) {
	ts := os.Getenv("GROVE_TEST_TYPESCRIPT")
	if ts == "" {
		if os.Getenv("GROVE_TEST_REQUIRE_TS") == "1" {
			t.Fatal("GROVE_TEST_REQUIRE_TS=1 but GROVE_TEST_TYPESCRIPT is not set")
		}
		t.Skip("GROVE_TEST_TYPESCRIPT not set")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	files := map[string]string{
		"tsconfig.json": `{"compilerOptions":{"strict":true,"target":"es2020"},"include":["*.ts"]}`,
		"a.ts": `export function parse(s: string): string { return s; }
export class Room {
  bind(): void {}
  handler(): void {}
  own(): string { return parse("x"); }
  setup(): void {
    const o = JSON.parse("{}");
    this.handler = this.handler.bind(this);
  }
}
`,
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ts, filepath.Join(root, "node_modules", "typescript")); err != nil {
		t.Fatal(err)
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g, r, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	skipUnless(t, r.Native, "js-ts: full pass")
	syms, edges := g.Snapshot()
	name := map[string]string{}
	for _, s := range syms {
		name[s.ID] = s.QualifiedName
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.Type == core.EdgeCalls {
			got[name[e.From]+"→"+name[e.To]] = true
		}
	}
	for _, bad := range []string{"Room.setup→parse", "Room.setup→Room.bind"} {
		if got[bad] {
			t.Errorf("name guess survived the checker's resolution: %s", bad)
		}
	}
	if !got["Room.own→parse"] {
		t.Errorf("the real call own→parse is missing: %v", got)
	}
}

// JS/TS: a bare call never reaches a class method, and a local const/let
// binding shadows every other function of that name (oracle review
// 2026-10-15: socket.io run(), express send()).
func TestJSBareCallsDoNotBindMethods(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"a.ts": "export class Server {\n  cleanup(): void {}\n  attach(): void {}\n  start(): void {\n    const cleanup = () => {};\n    cleanup();\n  }\n}\n" +
			"export class Other {\n  go(): void {\n    attach();\n  }\n}\n",
		"b.js": "var send = require('send');\nvar res = {};\nres.send = function send(body) { return body; };\n" +
			"res.sendFile = function sendFile(p) {\n  var file = send(p);\n  return file;\n};\nmodule.exports = res;\n",
	})
	for k := range got {
		if strings.HasSuffix(k, "→Server.cleanup") || strings.HasSuffix(k, "→Server.attach") || strings.HasSuffix(k, "→res.send") {
			t.Errorf("bare call bound to a method: %s", k)
		}
	}
}

// C#: a call result's methods are narrowed by the producer. A library
// producer (no method of ours by that name) reaches only our extension
// methods; our producer's declared return type picks the class.
func TestCSharpCallResultReceivers(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"Lib.cs": `namespace N {
public class JProperty { public void Remove() {} }
public class JObject {
  public JProperty Property(string name) { return new JProperty(); }
}
public class JArrayTests { public void Remove() {} }
public static class Ext { public static int Count2(this System.Collections.IEnumerable e) { return 0; } }
}
`,
		"Use.cs": `namespace N {
using System.Linq;
public class Use {
  public void A(JObject o) { o.Property("x").Remove(); }
  public void B(int[] xs) { xs.Select(x => x).ToArray(); }
  public int C(int[] xs) { return xs.Where(x => x > 0).Count2(); }
}
}
`,
	})
	if got["Use.A→JArrayTests.Remove"] {
		t.Error("Property(...).Remove() bound to an unrelated class's Remove")
	}
	if !got["Use.A→JProperty.Remove"] {
		t.Errorf("Property(...).Remove() lost its real target: %v", got)
	}
	if !got["Use.C→Ext.Count2"] {
		t.Errorf("an extension method on a library result was dropped: %v", got)
	}
}

// Python: `import json` inside a package that has its own json subpackage
// is the standard library's (absolute import), and a dict-annotated
// parameter's methods are dict's (oracle review 2026-10-15: flask).
func TestPythonStdlibModuleAndBuiltinReceivers(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"pkg/__init__.py":      "",
		"pkg/json/__init__.py": "import json as _json\n\n\ndef loads(s):\n    return _json.loads(s)\n",
		"pkg/json/provider.py": "import json\n\n\nclass Provider:\n    def loads(self, s):\n        return json.loads(s)\n",
		"pkg/app.py": "class Scaffold:\n    def get(self, rule):\n        return rule\n\n\nclass App(Scaffold):\n" +
			"    def register(self, options: dict) -> str:\n        return options.get(\"name\", \"\")\n",
	})
	for k := range got {
		if strings.HasSuffix(k, "→loads") || strings.HasSuffix(k, "→Provider.loads") || k == "App.register→Scaffold.get" {
			t.Errorf("guess survived: %s", k)
		}
	}
}

// Python: `self.x = param` takes the parameter's annotation, and `self.name`
// in a class whose hierarchy declares no `name` is not another class's
// property (oracle review 2026-10-15: flask JSONTag, Blueprint.name).
func TestPythonSelfAttributes(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"pkg/m.py": "class Serializer:\n    def tag(self, v):\n        return v\n\n\nclass Tagger:\n    def tag(self, v):\n        return v\n\n\n" +
			"class Holder:\n    def __init__(self, serializer: Serializer) -> None:\n        self.serializer = serializer\n\n    def go(self, v):\n        return self.serializer.tag(v)\n\n\n" +
			"class App:\n    @property\n    def name(self):\n        return \"app\"\n\n\n" +
			"class Blueprint:\n    def __init__(self, name):\n        self.name = name\n\n    def check(self):\n        return self.name\n",
	})
	if got["Holder.go→Tagger.tag"] || !got["Holder.go→Serializer.tag"] {
		t.Errorf("self.serializer.tag not typed by the __init__ annotation: %v", got)
	}
	if got["Blueprint.check→App.name"] {
		t.Error("self.name bound to an unrelated class's property")
	}
}

// TS/JS call-result receivers: a library producer's result reaches none of
// our methods unless it returns an element (Map.get); our annotated
// producer picks its return type's method (oracle review 2026-10-15).
func TestTSCallResultReceivers(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"a.ts": "export class Op {\n  to(r: string): Op { return this; }\n}\nexport class Other {\n  to(r: string): void {}\n  filter(): void {}\n}\n" +
			"export class Ns {\n  createChild(n: string): void {}\n}\n" +
			"export class S {\n  parents: Map<string, Ns> = new Map();\n  newOp(): Op { return new Op(); }\n" +
			"  a(): void { this.newOp().to(\"room\"); }\n  b(xs: number[]): void { xs.map(x => x).filter(); }\n" +
			"  c(): void { this.parents.get(\"k\")!.createChild(\"n\"); }\n}\n",
	})
	if got["S.a→Other.to"] || !got["S.a→Op.to"] {
		t.Errorf("newOp().to not narrowed by the producer's return type: %v", got)
	}
	if got["S.b→Other.filter"] {
		t.Error("a library result's method bound to ours")
	}
	if !got["S.c→Ns.createChild"] {
		t.Errorf("map.get(k)!.createChild lost its target: %v", got)
	}
}

// TS: a base class imported from an npm package is not a same-named class
// of ours; one from a workspace package (a directory of that name) is.
func TestTSBaseFromPackage(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"pkgA/emitter.ts": "export class Emitter {\n  emit(ev: string): void {}\n}\n",
		"pkgB/transport.ts": "import { Emitter } from \"@socket.io/component-emitter\";\nexport class Client extends Emitter {\n  fire(): void { this.emit(\"x\"); }\n}\n",
		"engine/lib/base.ts": "export class Base {\n  emit(ev: string): void {}\n}\n",
		"pkgC/local.ts": "import { Base } from \"engine\";\nexport class Local extends Base {\n  fire(): void { this.emit(\"y\"); }\n}\n",
	})
	if got["Client.fire→Emitter.emit"] {
		t.Error("a package base class resolved to a same-named class of ours")
	}
	if !got["Local.fire→Base.emit"] {
		t.Errorf("a workspace package's base class was dropped: %v", got)
	}
}

// Objective-C: a class name or self in a class method sends the class
// method; self in an instance method the instance method.
func TestObjCClassAndInstanceSelectors(t *testing.T) {
	_, _, stored := callEdges(t, map[string]string{
		"L.h": "@interface L : NSObject\n+ (void)flushLog;\n- (void)flushLog;\n- (void)run;\n+ (void)go;\n@end\n",
		"L.m": "#import \"L.h\"\n@implementation L\n+ (void)flushLog {}\n- (void)flushLog {}\n" +
			"- (void)run {\n  [self flushLog];\n}\n+ (void)go {\n  [L flushLog];\n}\n@end\n",
	})
	per := map[string]int{}
	for _, e := range stored {
		if e.Type == core.EdgeCalls && strings.Contains(e.To, "flushLog") {
			per[stripSHA(e.From)]++
		}
	}
	for from, n := range per {
		if n != 1 {
			t.Errorf("%s calls %d flushLog methods, want 1", from, n)
		}
	}
	if len(per) != 2 {
		t.Errorf("want calls from run and go, got %v", per)
	}
}

// C#: a BCL-typed receiver reaches our extension methods on that type;
// `var` locals take a cast's type or our producer's return type; a call
// argument is typed by our method's return type.
func TestCSharpReceiverAndArgumentTyping(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"Lib.cs": `namespace N {
public static class TypeExtensions { public static bool IsEnum(this System.Type t) { return false; } }
public class JObject {
  public static JObject Parse(string s) { return new JObject(); }
  public void CreateReader() {}
}
public class JArray { public void CreateReader() {} }
public class W {
  public void WriteValue(byte[] b) {}
  public void WriteValue(string s) {}
  public void WriteValue(int i) {}
  public void WriteValue(long l) {}
  public void WriteValue(bool b) {}
  public void WriteValue(char c) {}
}
public class Util { public static byte[] HexToBytes(string s) { return null; } }
}
`,
		"Use.cs": `namespace N {
public class Use {
  public bool A(System.Type t) { return t.IsEnum(); }
  public void B(object token) { var o = (JObject)token; o.CreateReader(); var j = JObject.Parse("{}"); j.CreateReader(); }
  public void C(W w) { w.WriteValue(Util.HexToBytes("4A")); w.WriteValue("#" + 1); }
}
}
`,
	})
	if !got["Use.A→TypeExtensions.IsEnum"] {
		t.Errorf("extension on a BCL-typed receiver missing: %v", got)
	}
	if got["Use.B→JArray.CreateReader"] || !got["Use.B→JObject.CreateReader"] {
		t.Errorf("var typed by cast/producer not applied: %v", got)
	}
}

// C: a header function calling a static function each includer defines
// reaches every includer's copy (jansson util.h main -> run_tests), past
// the fan-out cap; a parameter named like a function is not a reference.
func TestCHeaderStaticCallsAndParameterShadowing(t *testing.T) {
	files := map[string]string{
		"util.h": "static void run_tests();\nint main(void) {\n    run_tests();\n    return 0;\n}\n",
		"err.c":  "void report(int position) {\n    use(position);\n}\nvoid use(int p) {}\n",
		"t.c":    "#include \"util.h\"\nstatic void position(void) {}\n",
	}
	for i := 0; i < 20; i++ {
		files["test_"+strconv.Itoa(i)+".c"] = "#include \"util.h\"\nstatic void run_tests() {}\n"
	}
	_, _, stored := callEdges(t, files)
	runs := 0
	for _, e := range stored {
		if e.Type != core.EdgeCalls {
			continue
		}
		if strings.HasPrefix(e.From, "util.h::main") && strings.Contains(e.To, "::run_tests") {
			runs++
		}
		if strings.HasPrefix(e.From, "err.c::report") && strings.HasPrefix(e.To, "t.c::position") {
			t.Error("parameter `position` read as a reference to another file's static function")
		}
	}
	if runs != 20 {
		t.Errorf("main reaches %d run_tests copies, want 20", runs)
	}
}

// Python: `(ctx := _cv.get(None))` with `_cv: ContextVar[Ctx]` types ctx.
func TestPythonContextVarWalrus(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/ctx.py":      "class Ctx:\n    def push(self):\n        return 1\n\n\nclass Other:\n    def push(self):\n        return 2\n",
		"pkg/globals.py":  "from contextvars import ContextVar\nfrom .ctx import Ctx\n\n_cv: ContextVar[Ctx] = ContextVar(\"x\")\n",
		"pkg/helpers.py": "from .globals import _cv\n\n\ndef stream():\n    if (ctx := _cv.get(None)) is not None:\n        ctx.push()\n",
	})
	if !got["stream→Ctx.push"] || got["stream→Other.push"] {
		t.Errorf("ctx from ContextVar[Ctx].get not typed: %v", got)
	}
}

// Go: a call through a function-valued field reaches the functions stored
// into that field elsewhere (cobra c.Args(c, args) -> NoArgs), in a full
// and an incremental build alike.
func TestGoFuncFieldCalls(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed")
	}
	files := map[string]string{
		"go.mod": "module m\n\ngo 1.22\n",
		"cmd.go": "package m\n\ntype Check func(n int) error\n\ntype Cmd struct{ Args Check }\n\nfunc (c *Cmd) Validate(n int) error {\n\treturn c.Args(n)\n}\n",
		"args.go": "package m\n\nfunc NoArgs(n int) error { return nil }\n\nfunc New() *Cmd { return &Cmd{Args: NoArgs} }\n",
	}
	got, diags, _ := callEdges(t, files)
	skipUnless(t, diags, "go")
	if !got["Cmd.Validate→NoArgs"] {
		t.Errorf("call through the Args field does not reach NoArgs: %v", got)
	}
	for i := 0; i < 40; i++ {
		n := strconv.Itoa(i)
		files["p"+n+"/f.go"] = "package p" + n + "\n\nfunc F() int { return 1 }\n"
	}
	incMatchesFull(t, files, map[string]string{
		"args.go": "package m\n\nfunc NoArgs(n int) error { return nil }\n\nfunc New() *Cmd { return &Cmd{} }\n",
	}, "go")
}

// Swift: a generic-specialized initializer is a call, and a protocol
// extension's member is reached through a conforming type.
func TestSwiftGenericInitAndProtocolExtension(t *testing.T) {
	got, _, _ := callEdges(t, map[string]string{
		"Sources/G.swift": "protocol Location { var path: String { get } }\nextension Location {\n    func delete() { print(path) }\n}\n" +
			"struct Storage<T> {\n    let path: String\n    init(path: String) { self.path = path }\n}\n" +
			"struct Folder: Location {\n    let path: String\n}\n" +
			"func make() -> Storage<Int> {\n    return Storage<Int>(path: \"a\")\n}\n" +
			"func cleanup(folder: Folder) {\n    folder.delete()\n}\n",
	})
	if !got["make→Storage.init"] {
		t.Errorf("generic initializer call missing: %v", got)
	}
	if !got["cleanup→Location.delete"] {
		t.Errorf("protocol extension member via conformance missing: %v", got)
	}
}
