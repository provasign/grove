package index

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Stack audit 2026-10-20 #4: a cgo-only package (GoFiles=[], CgoFiles=[...])
// lost its Go declarations to importers, and its own calls were never
// type-checked. Both sides now resolve, positioned in the user's source.
func TestGoNativeAnalyzesCgoOnlyPackage(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	if out, err := exec.Command("go", "env", "CGO_ENABLED").Output(); err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Skip("cgo disabled")
	}
	got, diags, st := compilerFixture(t, map[string]string{
		"go.mod": "module audit\n\ngo 1.22\n",
		"binding/binding.go": "package binding\n\n// #include <stdlib.h>\nimport \"C\"\n\n" +
			"type Thing struct{ n C.int }\n\nfunc (t Thing) Do() int { return int(t.n) }\n\n" +
			"func helper() {}\n\nfunc New() Thing {\n\thelper()\n\treturn Thing{}\n}\n\n" +
			"type Doer interface{ Do() int }\n",
		"main.go": "package main\n\nimport \"audit/binding\"\n\nfunc main() {\n\tvar d binding.Doer = binding.New()\n\td.Do()\n}\n",
	})
	skipUnless(t, diags, "go: resolved ")
	for _, d := range diags {
		if strings.Contains(d, "invalid package name") || strings.Contains(d, "project import audit/binding") {
			t.Errorf("cgo package failed to load: %s", d)
		}
	}
	wantEdges(t, got, diags, "main→New calls", "main→Do calls", "New→helper calls")
	edges, err := st.AllEdges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	positioned := false
	for _, e := range edges {
		if e.Source == core.EvidenceSourceNative && e.Type == core.EdgeCalls && strings.Contains(e.To, "helper") && e.Lines != "" {
			// helper() is on line 13 of binding.go as written.
			positioned = true
			if e.Lines != "13:2" {
				t.Errorf("cgo call position not in user source coordinates: %q", e.Lines)
			}
		}
	}
	if !positioned {
		t.Logf("no positioned New→helper edge to check (positions: %v)", diags)
	}
}
