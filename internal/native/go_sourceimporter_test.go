package native

import (
	"context"
	"go/types"
	"os"
	"path/filepath"
	"testing"
)

// Dependencies typed from source (the cold-build-cache path) carry the same
// declarations export data would: net/http, which pulls in cgo-using net,
// type-checks with function bodies ignored and C faked.
func TestGoSourceImporterTypesDependencies(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex\n\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nimport \"net/http\"\n\nfunc main() { _ = http.Client{} }\n"), 0o644)
	imp := goSourceDeps(context.Background(), root)
	if imp == nil {
		t.Skip("go list unavailable")
	}
	pkg, err := imp.Import("net/http")
	if err != nil {
		t.Fatal(err)
	}
	obj := pkg.Scope().Lookup("Client")
	if _, ok := obj.(*types.TypeName); !ok {
		t.Fatalf("http.Client not typed from source: %v", obj)
	}
	if m, _, _ := types.LookupFieldOrMethod(obj.Type(), true, pkg, "Do"); m == nil {
		t.Fatal("(*http.Client).Do missing")
	}
}
