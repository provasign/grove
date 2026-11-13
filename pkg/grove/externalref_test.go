package grove

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
	if inHold["go:example.com/lib/store#Box"] != 8 || inHold["go:example.com/lib/store#Opener"] != 9 || inHold["go:example.com/lib#Helper"] != 11 {
		t.Fatalf("type uses and value reference in hold = %v", inHold)
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
