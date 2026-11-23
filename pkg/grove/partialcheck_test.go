package grove

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The analysed package's own type errors are reported, in the form an
// imported package's are: its edges come from a partial type-check
// (prism BUG-03). They used to be discarded, so only imports said so.
func TestIndexReportsAnalysedPackageTypeErrors(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/pc\n\ngo 1.26\n")
	write("a.go", "package pc\n\ntype P interface {\n\tChat()\n\tClose()\n}\n\ntype B struct{}\n\nfunc (B) Chat() {}\n\nfunc Run(p P) { p.Chat() }\n\nfunc Use() { Run(B{}) }\n")
	e, err := Open(t.Context(), Config{RepoRoot: root, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	res, err := e.Index(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range res.Native {
		if strings.HasPrefix(d, "go: project import example.com/pc type-checked partially: ") && strings.Contains(d, "missing method Close") {
			return
		}
	}
	t.Fatalf("no partial type-check diagnostic for the analysed package: %v", res.Native)
}
