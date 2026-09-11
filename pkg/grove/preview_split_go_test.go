package grove

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreviewGoMethodInDifferentFileThanType(t *testing.T) {
	root := t.TempDir()
	for path, src := range map[string]string{
		"go.mod":    "module example.com/audit\n\ngo 1.26\n",
		"type.go":   "package audit\ntype API struct{}\n",
		"method.go": "package audit\nfunc (API) Run(x, y int) int { return x+y }\n",
		"use.go":    "package audit\nfunc Use(a API) int { return a.Run(1) }\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(src), 0600); err != nil {
			t.Fatal(err)
		}
	}
	e, err := Open(t.Context(), Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if _, err := e.Index(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	r, failures, err := e.PreviewChangeImpacts(t.Context(), [][2]string{{"API.Run", "method.go"}}, map[string][]byte{
		"method.go": []byte("package audit\nfunc (API) Run(x int) int { return x }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if failures[0] != "" || len(r[0].Callers) != 1 || r[0].Callers[0].Name != "Use" {
		t.Fatalf("split-file Go impact lost: %v %+v", failures, r)
	}
}
