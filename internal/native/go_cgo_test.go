package native

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeCgoModule writes a module whose binding package has only cgo files
// (GoFiles=[], CgoFiles=[binding.go]) and returns go list's JSON for it, or
// skips when go or cgo is unavailable.
func writeCgoModule(t *testing.T) (string, []byte) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "binding"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod": "module audit\n\ngo 1.22\n",
		"binding/binding.go": "package binding\n\n// #include <stdlib.h>\nimport \"C\"\n\n" +
			"type Thing struct{ n C.int }\n\nfunc (t Thing) Do() int { return int(t.n) }\n\nfunc New() Thing { return Thing{} }\n",
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(rel)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("go", "list", "-json", "./binding")
	cmd.Dir = root
	raw, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var listed struct{ GoFiles, CgoFiles []string }
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.CgoFiles) == 0 {
		t.Skip("cgo unavailable (CGO_ENABLED=0 or no C compiler)")
	}
	if len(listed.GoFiles) != 0 {
		t.Fatalf("fixture expects a cgo-only package, go list GoFiles=%v", listed.GoFiles)
	}
	return root, raw
}

// Stack audit 2026-10-20 #4: a local package with only cgo files loaded as an
// empty package named "" because the project importer read GoFiles only.
func TestGoProjectImporterIncludesCgoDeclarations(t *testing.T) {
	_, raw := writeCgoModule(t)
	var meta goListPackage
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	pkg, err := newGoProjectImporter([]goListPackage{meta}, goDeps{}).Import(meta.ImportPath)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Name() != "binding" || pkg.Scope().Lookup("Thing") == nil || pkg.Scope().Lookup("New") == nil {
		t.Fatalf("cgo package lost its Go declarations: name=%q scope=%v", pkg.Name(), pkg.Scope().Names())
	}
}
