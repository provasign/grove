package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// BUG-03 (prism repo, 2026-11-22): Provider gained Close() and only ollama
// implements it. go vet rejects anthropic and openai, which are returned as a
// Provider; missing-implementations reported zero missing because types that
// no longer satisfy an interface had no implements edge. A type that is never
// used as the interface (unrelated) is not claimed.
func TestGoMissingImplementationsAfterInterfaceGainsMethod(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod": "module example.com/goevo\n\ngo 1.22\n",
		"prov/prov.go": `package prov

type Provider interface {
	Name() string
	Close() error
}

type ollama struct{}

func (ollama) Name() string { return "ollama" }
func (ollama) Close() error { return nil }

type anthropic struct{}

func (*anthropic) Name() string { return "anthropic" }

type openai struct{}

func (openai) Name() string { return "openai" }

type unrelated struct{}

func (unrelated) Name() string { return "x" }

func New(kind string) Provider {
	switch kind {
	case "a":
		return &anthropic{}
	}
	return ollama{}
}
`,
		"app/app.go": `package app

import "example.com/goevo/prov"

var registry = map[string]prov.Provider{}

func Register(name string, p prov.Provider) { registry[name] = p }
`,
		"app/openai.go": `package app

import "example.com/goevo/prov"

func init() { Register("openai", prov.OpenAI()) }
`,
		"prov/openai.go": `package prov

func OpenAI() openai { return openai{} }
`,
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

	res, err := g.MissingImplementations("Provider.Close")
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, m := range res.Missing {
		missing = append(missing, m.Name)
	}
	sort.Strings(missing)
	if len(missing) != 2 || missing[0] != "anthropic" || missing[1] != "openai" {
		t.Fatalf("missing = %v, want [anthropic openai] (unrelated is never used as a Provider)\ndiagnostics: %v", missing, r.Native)
	}
	if res.ImplementedCount != 1 {
		t.Errorf("implemented = %d, want 1 (ollama)", res.ImplementedCount)
	}

	// The same claims keep the rest of the family: a signature change to
	// Name reaches every provider, not just the one that still compiles.
	ci, err := g.ChangeImpact("Provider.Name")
	if err != nil {
		t.Fatal(err)
	}
	var family []string
	for _, m := range ci.Family {
		family = append(family, m.QualifiedName)
	}
	sort.Strings(family)
	want := []string{"anthropic.Name", "ollama.Name", "openai.Name"}
	if len(family) != 3 || family[0] != want[0] || family[1] != want[1] || family[2] != want[2] {
		t.Errorf("Provider.Name family = %v, want %v", family, want)
	}
}
