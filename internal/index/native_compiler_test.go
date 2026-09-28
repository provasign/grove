package index

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// compilerFixture indexes files under a fresh root and returns native edges
// keyed "from→to type[reason]" by symbol name, plus the run's diagnostics.
func compilerFixture(t *testing.T, files map[string]string) (map[string]bool, []string, *store.Store) {
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
	return compilerIndex(t, root)
}

func compilerIndex(t *testing.T, root string) (map[string]bool, []string, *store.Store) {
	t.Helper()
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
		name[s.ID] = s.Name
	}
	got := map[string]bool{}
	for _, e := range edges {
		if e.Source != core.EvidenceSourceNative {
			continue
		}
		k := name[e.From] + "→" + name[e.To] + " " + string(e.Type)
		got[k] = true
		if e.Reason != "" {
			got[k+"["+string(e.Reason)+"]"] = true
		}
	}
	return got, r.Native, st
}

func wantEdges(t *testing.T, got map[string]bool, diags []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing native edge %q\ndiagnostics: %v", w, diags)
		}
	}
}

func skipUnless(t *testing.T, diags []string, ranMarker string) {
	t.Helper()
	for _, d := range diags {
		if strings.Contains(d, ranMarker) {
			return
		}
	}
	t.Skipf("compiler pass did not run here (want %q): %v", ranMarker, diags)
}

// javac resolves calls through inheritance, lambdas, and method references,
// and field reads/writes, from the project's own sources.
func TestJavacResolverCallsAndFields(t *testing.T) {
	got, diags, _ := compilerFixture(t, map[string]string{
		"src/main/java/p/Base.java": `package p;
public class Base {
  protected int count;
  public void inc() { count++; }
}
`,
		"src/main/java/p/Svc.java": `package p;
import java.util.List;
public class Svc extends Base {
  public void direct() { inc(); }
  public void viaLambda(List<String> xs) { xs.forEach(x -> helper(x)); }
  public void viaRef(List<String> xs) { xs.forEach(this::helper); }
  public int reader() { return count; }
  public void writer() { count = 3; }
  void helper(String s) {}
}
`,
	})
	skipUnless(t, diags, "javac attributed")
	wantEdges(t, got, diags,
		"direct→inc calls",
		"viaLambda→helper calls[lambda-body]",
		"viaRef→helper calls[method-ref]",
		"reader→count reads",
		"writer→count writes",
		"inc→count writes",
	)
}

// The TypeScript checker attributes property reads, writes, and
// object-literal keys to the declaring member.
func TestTSNativeMemberReferences(t *testing.T) {
	ts := os.Getenv("GROVE_TEST_TYPESCRIPT") // a node_modules/typescript package dir
	if ts == "" {
		t.Skip("GROVE_TEST_TYPESCRIPT not set")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ts, filepath.Join(root, "node_modules", "typescript")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"tsconfig.json": `{"compilerOptions":{"strict":true,"target":"es2020"},"include":["*.ts"]}`,
		"conn.ts": `export interface ConnInfo { remote: string }
export class Conn {
  info: ConnInfo = { remote: "" }
}
export function readRemote(c: Conn): string { return c.info.remote }
export function setInfo(c: Conn) { c.info = { remote: "x" } }
`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, diags, _ := compilerIndex(t, root)
	skipUnless(t, diags, "js-ts")
	wantEdges(t, got, diags,
		"readRemote→info reads",
		"readRemote→remote reads",
		"setInfo→info writes",
		"setInfo→remote writes", // object-literal key typed by the contextual ConnInfo
	)
}

// Two ways a TypeScript file used to get no compiler facts: a script config
// run by tsx/bun (allowImportingTsExtensions under NodeNext) whose imports
// of extensionless project sources resolved to any, and a file no config
// includes at all (now checked in an inferred project).
func TestTSScriptConfigsAndFilesOutsideEveryConfig(t *testing.T) {
	ts := os.Getenv("GROVE_TEST_TYPESCRIPT")
	if ts == "" {
		t.Skip("GROVE_TEST_TYPESCRIPT not set")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not found")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ts, filepath.Join(root, "node_modules", "typescript")); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"package.json":        `{"name":"p","type":"module"}`,
		"tsconfig.json":       `{"compilerOptions":{"strict":true,"target":"es2022","module":"esnext","moduleResolution":"bundler"},"include":["src"]}`,
		"src/router.ts":       "export class Router {\n  match(path: string): number { return path.length }\n}\n",
		"src/index.ts":        "export { Router } from './router'\n",
		"bench/tsconfig.json": `{"compilerOptions":{"allowImportingTsExtensions":true,"module":"NodeNext"},"include":["./src"]}`,
		"bench/src/run.mts":   "import { Router } from '../../src/index.ts'\nconst r = new Router()\nr.match('/a')\n",
		"tools/use.ts":        "import { Router } from '../src/router'\nexport function useRouter(): number { return new Router().match('/b') }\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, diags, _ := compilerIndex(t, root)
	skipUnless(t, diags, "js-ts")
	wantEdges(t, got, diags,
		"<top-level>→match calls", // bench/src/run.mts under the tsx-style config
		"useRouter→match calls",   // tools/use.ts, outside every config
	)
}

// A project package with a type error still loads for interface dispatch:
// its interface reaches the implementers, and the diagnostic says the load
// was partial (it used to drop the package and every dispatch edge with it).
func TestGoPartialPackageKeepsCompilerEdges(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	got, diags, _ := compilerFixture(t, map[string]string{
		"go.mod": "module partial\n\ngo 1.22\n",
		"lib/lib.go": `package lib

type Doer interface{ Do() }

func Broken() int { return undefinedThing() }
`,
		"app/app.go": `package app

import "partial/lib"

type impl struct{}

func (impl) Do() {}

func Use(d lib.Doer) { d.Do() }

var _ lib.Doer = impl{}
`,
	})
	wantEdges(t, got, diags, "Use→Do calls")
	partial := false
	for _, d := range diags {
		if strings.Contains(d, "type-checked partially") {
			partial = true
		}
	}
	if !partial {
		t.Errorf("no partial type-check diagnostic: %v", diags)
	}
}

// A package importing a module dependency type-checks fully once the module
// is available: dependencies load from `go list -export` data. The GOPATH-style
// default importer never found module dependencies, so every such package was
// type-checked partially and flagged as missing dependencies.
func TestGoModuleDependencyTypeChecksFully(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	got, diags, _ := compilerFixture(t, map[string]string{
		"go.mod":         "module withdep\n\ngo 1.22\n\nrequire example.com/third v0.0.0\n\nreplace example.com/third => ./third\n",
		"third/go.mod":   "module example.com/third\n\ngo 1.22\n",
		"third/third.go": "package third\n\ntype Value struct{ N int }\n",
		"lib/lib.go": `package lib

import "example.com/third"

type Doer interface{ Do() third.Value }
`,
		"app/app.go": `package app

import (
	"example.com/third"
	"withdep/lib"
)

type impl struct{}

func (impl) Do() third.Value { return third.Value{} }

func Use(d lib.Doer) { d.Do() }

var _ lib.Doer = impl{}
`,
	})
	wantEdges(t, got, diags, "Use→Do calls")
	for _, d := range diags {
		if strings.Contains(d, "type-checked partially") || strings.Contains(d, "could not import") {
			t.Errorf("module dependency not loaded: %s", d)
		}
	}
}

// A syntax error in one file mid-edit keeps compiler edges for the rest of
// the package, and for the declarations the parser could still read in the
// broken file (the package used to be skipped whole).
func TestGoSyntaxErrorKeepsPackageCompilerEdges(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	got, diags, _ := compilerFixture(t, map[string]string{
		"go.mod": "module midedit\n\ngo 1.22\n",
		"lib/ok.go": `package lib

type Store struct{}

func (Store) Save() {}

func Use(s Store) { s.Save() }
`,
		"lib/broken.go": `package lib

func Helper(s Store) { s.Save() }

func Editing() {
	x := 
}
`,
	})
	wantEdges(t, got, diags, "Use→Save calls", "Helper→Save calls")
}

// An incremental Java index re-attributes only the edited package, keeps
// every other package's compiler edges (including edges INTO the edited file,
// whose symbol IDs all change), and ends up identical to a full index of the
// same tree.
func TestJavacIncrementalMatchesFullIndex(t *testing.T) {
	files := map[string]string{
		"src/main/java/a/Util.java": `package a;
public class Util {
  public static int check(int x) { return x; }
  public static int check(int x, int y) { return x + y; }
}
`,
		"src/main/java/b/Client.java": `package b;
import a.Util;
public class Client {
  public int one() { return Util.check(1); }
  public int two() { return Util.check(1, 2); }
}
`,
		"src/main/java/c/Other.java": `package c;
public class Other { public int n() { return 3; } }
`,
		"src/main/java/c/Other2.java": `package c;
public class Other2 { public int m() { return new Other().n(); } }
`,
	}
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
	_, diags, st := compilerIndex(t, root)
	skipUnless(t, diags, "javac attributed")
	st.Close()

	// Edit a/Util.java: a new overload inserted first shifts the #N overload
	// suffixes, so the remap must match by signature, not position.
	write("src/main/java/a/Util.java", `package a;
public class Util {
  public static int check(String s) { return s.length(); }
  public static int check(int x) { return x; }
  public static int check(int x, int y) { return x + y; }
}
`)
	inc, incDiags, st2 := compilerIndex(t, root)
	st2.Close()
	scoped := false
	for _, d := range incDiags {
		if strings.Contains(d, "scoped to 1 affected package dir") {
			scoped = true
		}
	}
	if !scoped {
		t.Errorf("incremental run was not scoped to the edited package: %v", incDiags)
	}
	wantEdges(t, inc, incDiags, "one→check calls", "two→check calls", "m→n calls")

	if err := os.RemoveAll(filepath.Join(root, ".grove")); err != nil {
		t.Fatal(err)
	}
	full, fullDiags, st3 := compilerIndex(t, root)
	st3.Close()
	for k := range full {
		if !inc[k] {
			t.Errorf("full index has %q, incremental does not\nfull: %v\ninc: %v", k, fullDiags, incDiags)
		}
	}
	for k := range inc {
		if !full[k] {
			t.Errorf("incremental index has %q, full does not", k)
		}
	}
}

// The first index records which compiler passes completed a full run, so a
// later incremental index knows the baseline exists.
func TestNativeCompleteRecordedAfterFirstIndex(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	_, diags, st := compilerFixture(t, map[string]string{
		"go.mod":  "module done\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() { run() }\n\nfunc run() {}\n",
	})
	raw, ok, err := st.GetMeta(context.Background(), core.MetaNativeComplete)
	if err != nil || !ok {
		t.Fatalf("meta %s missing (ok=%v err=%v); diagnostics: %v", core.MetaNativeComplete, ok, err, diags)
	}
	var done []string
	if err := json.Unmarshal([]byte(raw), &done); err != nil {
		t.Fatalf("meta %q: %v", raw, err)
	}
	if !strings.Contains(strings.Join(done, ","), "go") {
		t.Fatalf("go not recorded complete: %v", done)
	}
}
