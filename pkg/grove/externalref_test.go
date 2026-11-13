package grove

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/native"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func indexRepo(t *testing.T, root string) {
	t.Helper()
	ctx := context.Background()
	eng, err := Open(ctx, Config{RepoRoot: root, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
}

// A library and an application in separate repositories, each with its own
// index: the application's index must say where it calls the library, under
// the same key the library's index derives for the declaration.
func TestExternalRefsLinkTwoRepositories(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	ctx := context.Background()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeTree(t, lib, map[string]string{
		"go.mod": "module example.com/lib\n\ngo 1.22\n",
		"lib.go": "package lib\n\nfunc Helper() int { return 1 }\n\nfunc unused() {}\n",
		"store/store.go": "package store\n\ntype Box struct{}\n\nfunc (b *Box) Open() error { return nil }\n\n" +
			"type Opener interface{ Open() error }\n",
	})
	writeTree(t, app, map[string]string{
		"go.mod": "module example.com/app\n\ngo 1.22\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n",
		"main.go": `package main

import (
	"fmt"

	"example.com/lib"
	"example.com/lib/store"
)

func local() {}

func run(o store.Opener) {
	fmt.Println(lib.Helper())
	b := &store.Box{}
	_ = b.Open()
	_ = o.Open()
	local()
}

func main() { run(nil); _ = lib.Helper() }
`,
	})
	indexRepo(t, lib)
	indexRepo(t, app)

	// The library side: keys from its own symbols.
	lr, err := OpenReader(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()
	syms, err := lr.SymbolsNamed(ctx, []string{"Helper", "Box.Open"})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, s := range syms {
		if k, ok := GoSymbolKey(lib, s); ok {
			keys[s.QualifiedName] = k
		}
	}
	if keys["Helper"] != "go:example.com/lib#Helper" || keys["Box.Open"] != "go:example.com/lib/store#Box.Open" {
		t.Fatalf("declaration keys = %v", keys)
	}

	// The application side: its recorded calls under those keys.
	ar, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	refs, err := ar.ExternalRefs(ctx, []string{keys["Helper"], keys["Box.Open"], "go:example.com/lib/store#Opener.Open"})
	if err != nil {
		t.Fatal(err)
	}
	type site struct {
		key, from string
		line      int
	}
	got := map[site]bool{}
	for _, r := range refs {
		if r.File != "main.go" || r.Col == 0 {
			t.Fatalf("unexpected site %+v", r)
		}
		got[site{r.Key, r.From, r.Line}] = true
	}
	want := []site{
		{"go:example.com/lib#Helper", "run", 13},
		{"go:example.com/lib#Helper", "main", 20},
		{"go:example.com/lib/store#Box.Open", "run", 15},
		{"go:example.com/lib/store#Opener.Open", "run", 16},
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing %+v; got %v", w, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d sites, want %d: %v", len(got), len(want), got)
	}

	// Types named in a signature or a body, and a function used as a value.
	writeTree(t, app, map[string]string{"value.go": `package main

import (
	"example.com/lib"
	"example.com/lib/store"
)

type Wrapper struct {
	Inner *store.Box
}

func hold(b *store.Box) func() int {
	var o store.Opener = b
	_ = o
	return lib.Helper
}
`})
	indexRepo(t, app)
	ar2, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer ar2.Close()
	more, err := ar2.ExternalRefs(ctx, []string{"go:example.com/lib/store#Box", "go:example.com/lib/store#Opener", "go:example.com/lib#Helper"})
	if err != nil {
		t.Fatal(err)
	}
	inHold := map[string]int{}
	for _, r := range more {
		if r.File == "value.go" && r.From == "hold" {
			inHold[r.Key] = r.Line
		}
	}
	if inHold["go:example.com/lib/store#Box"] != 12 || inHold["go:example.com/lib/store#Opener"] != 13 || inHold["go:example.com/lib#Helper"] != 15 {
		t.Fatalf("type uses and value reference in hold = %v", inHold)
	}
	field := false
	for _, r := range more {
		field = field || (r.From == "Wrapper" && r.Key == "go:example.com/lib/store#Box" && r.Line == 9)
	}
	if !field {
		t.Fatalf("a struct field of another module's type was not recorded on the struct: %v", more)
	}
	typeSyms, _ := lr.SymbolsNamed(ctx, []string{"Box", "Opener"})
	for _, s := range typeSyms {
		if k, ok := GoSymbolKey(lib, s); !ok || k != "go:example.com/lib/store#"+s.Name {
			t.Fatalf("type declaration key for %s (%s) = %q, %v", s.Name, s.Kind, k, ok)
		}
	}

	// Standard library and in-repository calls are not recorded.
	none, err := ar.ExternalRefs(ctx, []string{"go:fmt#Println", "go:example.com/app#local"})
	if err != nil || len(none) != 0 {
		t.Fatalf("standard library or local calls recorded: %v %v", none, err)
	}
	// The library's own index holds no reference to itself.
	self, _ := lr.ExternalRefs(ctx, []string{keys["Helper"]})
	if len(self) != 0 {
		t.Fatalf("library recorded an external reference to its own function: %v", self)
	}
}

// A go.work above two checkouts is how they are built together. The
// application's go.mod pins a published version of the library; the call it
// makes exists only in the checkout. Analysis must follow the workspace, or
// that call is invisible.
func TestExternalRefsFollowParentGoWork(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	ctx := context.Background()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeTree(t, parent, map[string]string{"go.work": "go 1.22\n\nuse (\n\t./app\n\t./lib\n)\n"})
	writeTree(t, lib, map[string]string{
		"go.mod": "module example.com/wslib\n\ngo 1.22\n",
		"lib.go": "package wslib\n\nfunc OnlyInCheckout() int { return 1 }\n",
	})
	writeTree(t, app, map[string]string{
		"go.mod":  "module example.com/wsapp\n\ngo 1.22\n\nrequire example.com/wslib v0.1.0\n",
		"main.go": "package main\n\nimport \"example.com/wslib\"\n\nfunc main() {\n\tprintln(wslib.OnlyInCheckout())\n}\n",
	})
	indexRepo(t, app)
	r, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := r.ExternalRefs(ctx, []string{"go:example.com/wslib#OnlyInCheckout"})
	r.Close()
	if err != nil || len(refs) != 1 || refs[0].File != "main.go" || refs[0].Line != 6 {
		t.Fatalf("call into the workspace checkout not recorded: %v %v", refs, err)
	}

	// The library gains a function and the application starts calling it:
	// the cached listing must not hide the new call.
	writeTree(t, lib, map[string]string{"more.go": "package wslib\n\nfunc AddedLater() int { return 2 }\n"})
	writeTree(t, app, map[string]string{"more.go": "package main\n\nimport \"example.com/wslib\"\n\nfunc later() int { return wslib.AddedLater() }\n"})
	indexRepo(t, app)
	r, err = OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	refs, err = r.ExternalRefs(ctx, []string{"go:example.com/wslib#AddedLater"})
	if err != nil || len(refs) != 1 || refs[0].From != "later" {
		t.Fatalf("call added after a workspace member changed not recorded: %v %v", refs, err)
	}
}

// findTypeScript locates an installed typescript package to link into a
// test project: $GROVE_TEST_TYPESCRIPT, else one next to the test binary's
// working tree. The TypeScript pass needs it resolvable from the project.
func findTypeScript() string {
	candidates := []string{os.Getenv("GROVE_TEST_TYPESCRIPT")}
	if home, err := os.UserHomeDir(); err == nil {
		m, _ := filepath.Glob(filepath.Join(home, "*", "*", "node_modules", "typescript"))
		candidates = append(candidates, m...)
		m, _ = filepath.Glob(filepath.Join(home, "*", "node_modules", "typescript"))
		candidates = append(candidates, m...)
	}
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(c, "lib", "typescript.js")); err == nil {
			return c
		}
	}
	return ""
}

func TestExternalRefsTypeScriptPackages(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available")
	}
	tsDir := findTypeScript()
	if tsDir == "" {
		t.Skip("no typescript package found to link into the test project")
	}
	ctx := context.Background()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeTree(t, lib, map[string]string{
		"package.json": `{"name":"@acme/lib","version":"1.0.0","main":"src/index.ts","types":"src/index.ts"}`,
		"src/index.ts": "export function helper(): number { return 1 }\n\nexport class Box {\n  open(): number { return 2 }\n}\n",
	})
	writeTree(t, app, map[string]string{
		"package.json":  `{"name":"app","devDependencies":{"typescript":"5"},"dependencies":{"@acme/lib":"file:../lib"}}`,
		"tsconfig.json": `{"compilerOptions":{"strict":true,"module":"commonjs","target":"es2020","moduleResolution":"node"},"include":["src"]}`,
		"src/main.ts":   "import { helper, Box } from '@acme/lib'\n\nexport function run(): number {\n  const b = new Box()\n  return helper() + b.open() + parseInt('1')\n}\n",
	})
	for link, target := range map[string]string{
		filepath.Join(app, "node_modules", "typescript"):   tsDir,
		filepath.Join(app, "node_modules", "@acme", "lib"): lib,
	} {
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Skip("symlinks unavailable:", err)
		}
	}
	indexRepo(t, lib)
	indexRepo(t, app)

	lr, err := OpenReader(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()
	syms, err := lr.SymbolsNamed(ctx, []string{"helper", "Box.open"})
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]string{}
	for _, s := range syms {
		if k, ok := SymbolKey(lib, s); ok {
			keys[s.QualifiedName] = k
		}
	}
	if keys["helper"] != "npm:@acme/lib#helper" || keys["Box.open"] != "npm:@acme/lib#open" {
		t.Fatalf("declaration keys = %v", keys)
	}
	ar, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	refs, err := ar.ExternalRefs(ctx, []string{keys["helper"], keys["Box.open"]})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, r := range refs {
		if r.File != "src/main.ts" || r.From != "run" {
			t.Fatalf("unexpected site %+v", r)
		}
		got[r.Key] = r.Line
	}
	if got["npm:@acme/lib#helper"] != 5 || got["npm:@acme/lib#open"] != 5 || len(got) != 2 {
		t.Fatalf("TypeScript external references = %v (want helper and open on line 5)", got)
	}
	// The platform (parseInt from TypeScript's lib) is not recorded.
	if none, _ := ar.ExternalRefs(ctx, []string{"npm:typescript#parseInt"}); len(none) != 0 {
		t.Fatalf("a platform call was recorded: %v", none)
	}
}

func TestExternalRefsPythonImports(t *testing.T) {
	ctx := context.Background()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	writeTree(t, lib, map[string]string{
		"src/extlib/__init__.py": "from .models import Model\n",
		"src/extlib/models.py":   "class Model:\n    def save(self):\n        pass\n\n\ndef persist(m):\n    pass\n",
	})
	writeTree(t, app, map[string]string{
		"app/__init__.py": "",
		"app/local.py":    "def helper():\n    pass\n",
		"app/use.py": `import os
import extlib.models as em
from extlib import Model
from extlib.models import persist as keep
from .local import helper
from app.local import helper as again


def run():
    m = Model()
    keep(m)
    em.persist(m)
    helper()
    again()
    os.getcwd()
    return m.save()
`,
	})
	indexRepo(t, lib)
	indexRepo(t, app)

	lr, err := OpenReader(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()
	syms, _ := lr.SymbolsNamed(ctx, []string{"Model", "persist"})
	var keys []string
	for _, s := range syms {
		keys = append(keys, KeysFor(lib, s.Language, string(s.Kind), s.FilePath, s.QualifiedName)...)
	}
	// Model is declared in extlib.models and reachable as extlib.Model.
	for _, want := range []string{"py:extlib.models#Model", "py:extlib#Model", "py:extlib.models#persist"} {
		found := false
		for _, k := range keys {
			found = found || k == want
		}
		if !found {
			t.Fatalf("declaration keys %v lack %s", keys, want)
		}
	}
	ar, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	refs, err := ar.ExternalRefs(ctx, keys)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]int{}
	for _, r := range refs {
		if r.File != "app/use.py" || r.From != "run" {
			t.Fatalf("unexpected site %+v", r)
		}
		got[r.Key] = append(got[r.Key], r.Line)
	}
	if len(got["py:extlib#Model"]) != 1 || got["py:extlib#Model"][0] != 10 {
		t.Fatalf("Model() through the package re-export: %v", got)
	}
	if p := got["py:extlib.models#persist"]; len(p) != 2 || p[0] != 11 || p[1] != 12 {
		t.Fatalf("persist through an alias and through a module alias: %v", got)
	}
	// The project's own modules and the standard library are not recorded.
	all, _ := ar.AllExternalRefs(ctx)
	for _, r := range all {
		if r.Key == "py:os#getcwd" || r.Key == "py:app.local#helper" || r.Key == "py:.local#helper" {
			t.Fatalf("a standard library or project call was recorded: %+v", r)
		}
	}
}

func indexRepoLinked(t *testing.T, root string, linked ...string) {
	t.Helper()
	ctx := context.Background()
	eng, err := Open(ctx, Config{RepoRoot: root, OneShot: true, LinkedRoots: linked})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
}

// A Java library and an application in separate repositories. The
// library's jar is not on the application's classpath; its sources are
// read from the linked checkout, and javac's resolution gives each call
// and type use the key the library derives for the declaration.
func TestExternalRefsJava(t *testing.T) {
	if !native.HasJDK() {
		t.Skip("no JDK")
	}
	ctx := context.Background()
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	lib, app := filepath.Join(parent, "lib"), filepath.Join(parent, "app")
	pom := func(artifact string) string {
		return "<project><modelVersion>4.0.0</modelVersion><groupId>com.acme</groupId><artifactId>" + artifact + "</artifactId><version>1.0</version></project>\n"
	}
	writeTree(t, lib, map[string]string{
		"pom.xml": pom("lib"),
		"src/main/java/com/acme/lib/Box.java": `package com.acme.lib;

public class Box {
    public Box() {}
    public Box(int n) {}
    public int open() { return 1; }
    public static Box create() { return new Box(); }
    public static class Lid {
        public void lift() {}
    }
    public interface Opener { int open(); }
}
`,
	})
	writeTree(t, app, map[string]string{
		"pom.xml": pom("app"),
		"src/main/java/com/acme/app/Main.java": `package com.acme.app;

import com.acme.lib.Box;
import java.util.ArrayList;

public class Main implements Box.Opener {
    private Box held;
    public int open() { return 0; }
    static int run() {
        Box b = Box.create();
        Box.Lid lid = new Box.Lid();
        lid.lift();
        new ArrayList<String>().add("x");
        return b.open() + new Box(3).open();
    }
}
`,
	})
	indexRepo(t, lib)
	indexRepoLinked(t, app, lib)

	lr, err := OpenReader(lib)
	if err != nil {
		t.Fatal(err)
	}
	defer lr.Close()
	syms, err := lr.AllSymbols(ctx)
	if err != nil {
		t.Fatal(err)
	}
	keyOf := map[string]string{}
	for _, s := range syms {
		if k, ok := SymbolKey(lib, s); ok {
			keyOf[string(s.Kind)+" "+s.QualifiedName] = k
		}
	}
	for name, want := range map[string]string{
		"class Box":            "jvm:com.acme.lib#Box",
		"constructor Box.Box":  "jvm:com.acme.lib#Box.Box",
		"method Box.open":      "jvm:com.acme.lib#Box.open",
		"method Box.create":    "jvm:com.acme.lib#Box.create",
		"class Box.Lid":        "jvm:com.acme.lib#Box.Lid",
		"method Box.Lid.lift":  "jvm:com.acme.lib#Box.Lid.lift",
		"interface Box.Opener": "jvm:com.acme.lib#Box.Opener",
	} {
		if keyOf[name] != want {
			t.Fatalf("declaration key for %q = %q, want %q (all: %v)", name, keyOf[name], want, keyOf)
		}
	}

	ar, err := OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer ar.Close()
	all, err := ar.AllExternalRefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string][]int{} // key -> lines
	froms := map[string]string{}
	for _, r := range all {
		if r.File != "src/main/java/com/acme/app/Main.java" {
			t.Fatalf("unexpected file in %+v", r)
		}
		lines[r.Key] = append(lines[r.Key], r.Line)
		froms[r.Key+"@"+itoaTest(r.Line)] = r.From
	}
	has := func(key string, line int) bool {
		for _, l := range lines[key] {
			if l == line {
				return true
			}
		}
		return false
	}
	for _, want := range []struct {
		key  string
		line int
	}{
		{"jvm:com.acme.lib#Box.create", 10},   // static call
		{"jvm:com.acme.lib#Box.Lid.Lid", 11},  // nested constructor
		{"jvm:com.acme.lib#Box.Lid.lift", 12}, // method on a nested type
		{"jvm:com.acme.lib#Box.open", 14},     // instance method
		{"jvm:com.acme.lib#Box.Box", 14},      // overloaded constructor
		{"jvm:com.acme.lib#Box.Opener", 6},    // implements clause
		{"jvm:com.acme.lib#Box", 7},           // field type
		{"jvm:com.acme.lib#Box", 10},          // local variable type and static receiver
		{"jvm:com.acme.lib#Box.Lid", 11},      // nested type use
	} {
		if !has(want.key, want.line) {
			t.Errorf("missing %s at line %d; recorded: %v", want.key, want.line, lines)
		}
	}
	if froms["jvm:com.acme.lib#Box.create@10"] != "Main.run" {
		t.Errorf("call attributed to %q, want Main.run", froms["jvm:com.acme.lib#Box.create@10"])
	}
	if froms["jvm:com.acme.lib#Box.Opener@6"] != "Main" {
		t.Errorf("implements clause attributed to %q, want the class Main", froms["jvm:com.acme.lib#Box.Opener@6"])
	}
	for key := range lines {
		if strings.HasPrefix(key, "jvm:java.") {
			t.Errorf("a JDK reference was recorded: %s", key)
		}
	}
	// The library's own index records nothing about itself.
	if self, _ := lr.AllExternalRefs(ctx); len(self) != 0 {
		t.Errorf("library recorded external references to its own types: %v", self)
	}
}

func itoaTest(n int) string { return strconv.Itoa(n) }
