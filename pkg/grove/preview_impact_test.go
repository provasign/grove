package grove

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
)

func TestPreviewImpactRestoresBaseWithoutChangingIndex(t *testing.T) {
	root := t.TempDir()
	current := "class Base:\n    def run(self, x): pass\nclass Child(Base):\n    def run(self, x, y): pass\nclass Decoy:\n    def run(self, x): pass\n"
	if err := os.WriteFile(filepath.Join(root, "api.py"), []byte(current), 0600); err != nil {
		t.Fatal(err)
	}
	e, err := Open(t.Context(), Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Index(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	before, edges, err := e.SnapshotGraph(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	base := "class Base:\n    def run(self, x): pass\nclass Child(Base):\n    def run(self, x): pass\nclass Decoy:\n    def run(self, x): pass\n"
	r, failures, err := e.PreviewChangeImpacts(t.Context(), [][2]string{{"Child.run", "api.py"}}, map[string][]byte{"api.py": []byte(base)})
	if err != nil || failures[0] != "" {
		t.Fatalf("preview: %v %v", err, failures)
	}
	if len(r[0].Supers) != 1 || r[0].Supers[0].QualifiedName != "Base.run" {
		t.Fatalf("lost base contract: %v", r[0])
	}
	for _, s := range r[0].Sites() {
		if s.QualifiedName == "Decoy.run" {
			t.Fatal("unrelated signature admitted")
		}
	}
	after, afterEdges, _ := e.SnapshotGraph(t.Context())
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(edges, afterEdges) {
		t.Fatal("preview mutated graph")
	}
	b, _ := os.ReadFile(filepath.Join(root, "api.py"))
	if string(b) != current {
		t.Fatal("preview modified source")
	}
}

func TestResolverUpgradeDiscardsPersistedFalsePythonCall(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "api.py"), []byte("class A:\n    def run(self): pass\ndef use(x):\n    return x.run()\n"), 0600)
	e, err := Open(t.Context(), Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Index(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	syms, _, _ := e.SnapshotGraph(t.Context())
	var use, run string
	for _, s := range syms {
		if s.Name == "use" {
			use = s.ID
		}
		if s.Name == "run" {
			run = s.ID
		}
	}
	if use == "" || run == "" {
		t.Fatal("missing test symbols")
	}
	if err := e.store.ReplaceEdges(t.Context(), []core.Edge{{From: use, To: run, Type: core.EdgeCalls, Source: core.EvidenceSourceNative, Confidence: .98}}); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetMeta(t.Context(), "resolver-version", "old"); err != nil {
		t.Fatal(err)
	}
	if stale, err := e.IndexNeedsRefresh(t.Context()); err != nil || !stale {
		t.Fatalf("old resolver version must require reindex: stale=%v err=%v", stale, err)
	}
	e.Close()
	e, err = Open(t.Context(), Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	_, edges, err := e.SnapshotGraph(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range edges {
		if edge.Type == core.EdgeCalls && edge.Source == core.EvidenceSourceNative {
			t.Fatal("stale native Python call survived open")
		}
	}
	result, err := e.Index(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.FilesUpdated == 0 {
		t.Fatal("upgrade incorrectly reused unchanged index")
	}
	version, _, _ := e.store.GetMeta(t.Context(), "resolver-version")
	if version != graph.ResolverVersion {
		t.Fatalf("version=%s", version)
	}
	if stale, err := e.IndexNeedsRefresh(t.Context()); err != nil || stale {
		t.Fatalf("reindexed resolver version must be current: stale=%v err=%v", stale, err)
	}
}
