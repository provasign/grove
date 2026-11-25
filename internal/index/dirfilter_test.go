package index

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirFilter(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"src/a.go", "node_modules/x/i.js", "out/o.txt", "src/gen/g.go", "src/keep.log", "src/b.log"} {
		if err := os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, p), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, body string) {
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitignore", "out/\n*.log\n")
	write("src/.gitignore", "gen/\n!keep.log\n")

	f := NewDirFilter(root)
	j := func(p string) string { return filepath.Join(root, p) }
	if !f.Enter(root) || f.Enter(j("node_modules")) || f.Enter(j("out")) {
		t.Fatal("root must be entered; node_modules and out/ skipped")
	}
	if !f.Enter(j("src")) {
		t.Fatal("src skipped")
	}
	if f.Enter(j("src/gen")) {
		t.Fatal("nested .gitignore not applied to src/gen")
	}
	if f.FileIgnored(j("src/a.go")) || !f.FileIgnored(j("src/b.log")) || f.FileIgnored(j("src/keep.log")) {
		t.Fatal("file rules wrong")
	}
	if !f.FileIgnored(filepath.Join(filepath.Dir(root), "elsewhere.go")) || f.Enter(filepath.Dir(root)) {
		t.Fatal("paths outside the root must be ignored")
	}
}
