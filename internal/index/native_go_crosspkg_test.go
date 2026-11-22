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

// Two packages declare Server.Handler. Every call in app is on an a.Server
// except the last, which is on a b.Server. The compiler pass must resolve
// each one to its own package's method, with the call's position, and the
// name-based pass must not leave an edge to the other package's method.
// Before 2026-11-22 the compiler pass resolved no cross-package method call
// at all; name matching linked each call to both methods, and change-impact
// still called the result compiler-backed and closed.
func TestGoCrossPackageMethodCallsResolveToTheirOwnPackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"go.mod": "module example.com/gox\n\ngo 1.22\n",
		"a/a.go": `package a

type Server struct{}

func New() *Server { return &Server{} }

func (s *Server) Handler() int { return 1 }
`,
		"b/b.go": `package b

type Server struct{}

func (s *Server) Handler() int { return 2 }
`,
		"app/app.go": `package app

import (
	"example.com/gox/a"
	"example.com/gox/b"
)

type holder struct{ v int }

func Use() int {
	srv := a.New()
	h := holder{v: srv.Handler()}
	return h.v + a.New().Handler() + (&b.Server{}).Handler()
}
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
	syms, edges := g.Snapshot()
	file := map[string]string{}
	for _, s := range syms {
		file[s.ID] = s.FilePath + ":" + s.QualifiedName
	}
	got := map[string][]string{} // target -> "source lines"
	for _, e := range edges {
		if e.Type != core.EdgeCalls || file[e.From] != "app/app.go:Use" {
			continue
		}
		got[file[e.To]] = append(got[file[e.To]], string(e.Source)+" "+e.Lines)
	}
	for k := range got {
		sort.Strings(got[k])
	}
	want := map[string][]string{
		"a/a.go:New":            {"native 11:11,13:17"},
		"a/a.go:Server.Handler": {"native 12:21,13:23"},
		"b/b.go:Server.Handler": {"native 13:49"},
	}
	for target, w := range want {
		if len(got[target]) != 1 || got[target][0] != w[0] {
			t.Errorf("Use → %s: got %v, want %v\ndiagnostics: %v", target, got[target], w, r.Native)
		}
	}
	for target := range got {
		if _, ok := want[target]; !ok {
			t.Errorf("unexpected call edge Use → %s: %v", target, got[target])
		}
	}
}
