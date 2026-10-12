package grove

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// A resident engine answers an edit from the parse and runs the compiler
// pass in the background (Config.DeferNative). The edit is reported pending,
// the pass drains it, and the result is the store a cold, fully compiler-
// backed index of the same tree would hold: same symbols, same edges.
func TestDeferredCompilerPassConvergesToColdIndex(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("no javac on PATH")
	}
	ctx := context.Background()
	write := func(root, rel, body string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := map[string]string{
		"p/Adapter.java": "package p;\n\npublic abstract class Adapter {\n  public abstract void write(Object v);\n}\n",
		"p/Plain.java":   "package p;\n\npublic class Plain extends Adapter {\n  @Override\n  public void write(Object v) {}\n}\n",
		"p/Use.java":     "package p;\n\npublic class Use {\n  void run(Adapter a) { a.write(1); }\n}\n",
	}
	root := t.TempDir()
	for rel, body := range base {
		write(root, rel, body)
	}
	on := true
	eng, err := Open(ctx, Config{RepoRoot: root, DeferNative: &on})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, ""); err != nil { // cold: synchronous compiler pass
		t.Fatal(err)
	}
	if p := eng.NativePending(); len(p) != 0 {
		t.Fatalf("a cold index must not defer: pending %v", p)
	}

	// The edit: an override only the compiler pass declares correctly when
	// the parser misses it, plus a new caller.
	write(root, "p/Registry.java", "package p;\n\npublic class Registry {\n  public static final Adapter ANON =\n      new Adapter() {\n        @Override\n        public void write(Object v) {}\n      };\n}\n")
	write(root, "p/Use.java", "package p;\n\npublic class Use {\n  void run(Adapter a) { a.write(1); Registry.ANON.write(2); }\n}\n")
	res, err := eng.Index(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NativePending) == 0 {
		t.Fatalf("an incremental Java edit should be compiler-pending, got %+v", res)
	}
	// Nothing changed since: the call answers without a new pass.
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if !eng.WaitNative(ctx, 2*time.Minute) {
		t.Fatalf("compiler pass did not drain: pending %v", eng.NativePending())
	}

	cold := t.TempDir()
	for rel, body := range base {
		write(cold, rel, body)
	}
	for _, rel := range []string{"p/Registry.java", "p/Use.java"} {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		write(cold, rel, string(data))
	}
	ref, err := Open(ctx, Config{RepoRoot: cold, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ref.Close()
	if _, err := ref.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}

	gotSyms, gotEdges, err := eng.SnapshotGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantSyms, wantEdges, err := ref.SnapshotGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(syms []Symbol) []string {
		out := make([]string, 0, len(syms))
		for _, s := range syms {
			out = append(out, s.ID)
		}
		sort.Strings(out)
		return out
	}
	edges := func(es []Edge) []string {
		out := make([]string, 0, len(es))
		for _, e := range es {
			out = append(out, e.From+" "+string(e.Type)+" "+e.To+" "+string(e.Source))
		}
		sort.Strings(out)
		return out
	}
	if g, w := ids(gotSyms), ids(wantSyms); !equalStrings(g, w) {
		t.Fatalf("symbols differ from a cold index:\n got %v\nwant %v", g, w)
	}
	if g, w := edges(gotEdges), edges(wantEdges); !equalStrings(g, w) {
		t.Fatalf("edges differ from a cold index:\n got %d %v\nwant %d %v", len(g), g, len(w), w)
	}
	// The store must hold the same thing: a new engine rehydrates from it.
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, Config{RepoRoot: root, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	storedSyms, storedEdges, err := reopened.SnapshotGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g, w := ids(storedSyms), ids(wantSyms); !equalStrings(g, w) {
		t.Fatalf("stored symbols differ from a cold index:\n got %v\nwant %v", g, w)
	}
	if g, w := edges(storedEdges), edges(wantEdges); !equalStrings(g, w) {
		t.Fatalf("stored edges differ from a cold index:\n got %d %v\nwant %d %v", len(g), g, len(w), w)
	}
	eng = reopened
	// And the anonymous override is in Adapter.write's family.
	r, err := eng.ChangeImpact(ctx, "Adapter.write")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range r.Family {
		if s.FilePath == "p/Registry.java" {
			found = true
		}
	}
	if !found {
		t.Fatalf("anonymous override missing from the family after the compiler pass: %+v", r.Family)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
