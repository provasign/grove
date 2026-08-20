package grove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Mini-gin: the exact shapes that misclassified live (2026-08-20).
func TestRenamePlanPerSite_GinShapes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module mini\n\ngo 1.22\n")
	write("rw.go", `package mini

type ResponseWriter interface {
	Status() int
}

type responseWriter struct{ status int }

func (w *responseWriter) Status() int { return w.status }
`)
	write("ctx.go", `package mini

type Context struct {
	Writer    ResponseWriter
	writermem responseWriter
}

func (c *Context) Status(code int) { c.writermem.status = code }
`)
	write("use.go", `package mini

func handler(c *Context) {
	c.Status(200)
	_ = c.Writer.Status()
	_ = c.writermem.Status()
	var w ResponseWriter = &responseWriter{}
	_ = w.Status()
}
`)
	eng, err := Open(context.Background(), Config{RepoRoot: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	if _, err := eng.Index(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	// Family must NOT include Context.Status: its arity differs from the
	// interface member, and a PARSED zero-param list is evidence, not
	// absence of evidence (the paramTypesOf nil-vs-empty fix).
	ci, err := eng.ChangeImpact(context.Background(), "ResponseWriter.Status")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ci.Family {
		if s.QualifiedName == "Context.Status" {
			t.Fatalf("Context.Status(int) joined the zero-param interface member's family")
		}
	}

	r, err := eng.RenamePlan(context.Background(), "ResponseWriter.Status", "StatusCode")
	if err != nil {
		t.Fatal(err)
	}
	confirmed := map[string]bool{}
	for _, e := range r.Edits {
		confirmed[e.FilePath+":"+itoa(e.Line)] = true
		if e.FilePath == "ctx.go" && e.Line == 8 {
			t.Fatalf("Context.Status's own declaration wrongly renamed: %+v", e)
		}
	}
	// Every true site confirms per-site: interface-typed field chain
	// (c.Writer), concrete field chain (c.writermem), conversion-typed
	// local (w) — the shapes measured live on gin.
	for _, want := range []string{"rw.go:4", "rw.go:9", "use.go:5", "use.go:6", "use.go:8"} {
		if !confirmed[want] {
			t.Errorf("expected confirmed edit at %s; got %v", want, confirmed)
		}
	}
	if len(r.Ambiguous) != 0 {
		t.Errorf("per-site resolution should leave nothing ambiguous here: %+v", r.Ambiguous)
	}
	// Context.Status call is not a site: excluded entirely.
	if confirmed["use.go:4"] {
		t.Error("c.Status(200) is Context.Status — must be excluded, not confirmed")
	}
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
