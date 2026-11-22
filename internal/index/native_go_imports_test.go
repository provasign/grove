package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// Go import edges are per file and positioned: only the file that imports a
// package links to it, at the import's line, and never to the package's
// tests. They were package-level (every file to every file, tests included,
// no position) until 2026-11-22.
func TestGoImportEdgesArePerFileWithPosition(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod":                  "module example.com/imp\n\ngo 1.22\n",
		"version/version.go":      "package version\n\nvar Version = \"1\"\n",
		"version/version_test.go": "package version\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {}\n",
		"cli/commands.go":         "package cli\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/imp/version\"\n)\n\nfunc Run() { fmt.Println(version.Version) }\n",
		"cli/root.go":             "package cli\n\nfunc Root() {}\n",
	} {
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
	skipUnless(t, r.Native, "go list resolved")
	_, edges := g.Snapshot()
	var got []string
	for _, e := range edges {
		if e.Type == core.EdgeImports && e.Source == core.EvidenceSourceNative && e.From != "" && filepath.Dir(e.From[len("file:"):]) == "cli" {
			got = append(got, e.From+" -> "+e.To+" @"+e.Lines)
		}
	}
	sort.Strings(got)
	want := "file:cli/commands.go -> file:version/version.go @6:2"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("cli import edges = %v, want [%s]", got, want)
	}
}
