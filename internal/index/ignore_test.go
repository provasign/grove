package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndex_RespectsGroveIgnoreAndGitIgnore(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".groveignore"), []byte("ignored.go\nprivate/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("generated/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "private"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "generated"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"main.go":              "package main\nfunc Keep(){}\n",
		"ignored.go":           "package main\nfunc Ignored(){}\n",
		"private/secret.go":    "package private\nfunc Secret(){}\n",
		"generated/output.go":  "package generated\nfunc Generated(){}\n",
		"generated/output2.md": "# generated\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 1 {
		t.Fatalf("FilesUpdated = %d, want 1", res.FilesUpdated)
	}
}

func TestIndex_SkipsSensitivePaths(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()

	root := t.TempDir()
	files := map[string]string{
		"main.go":          "package main\nfunc Keep(){}\n",
		".env.local":       "TOKEN=secret\n",
		"credentials.json": `{"token":"secret"}`,
		"api-secret.yaml":  "token: secret\n",
		"private-key.pem":  "-----BEGIN PRIVATE KEY-----\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 1 {
		t.Fatalf("FilesUpdated = %d, want 1", res.FilesUpdated)
	}
}

func TestIgnoreRules_Negation(t *testing.T) {
	rules := parseIgnoreRules("*.go\n!keep.go\n")
	if !ignoredByRules("drop.go", false, rules) {
		t.Fatal("drop.go should be ignored")
	}
	if ignoredByRules("keep.go", false, rules) {
		t.Fatal("keep.go should be restored by negation")
	}
}
