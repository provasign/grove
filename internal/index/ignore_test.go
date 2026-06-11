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

func TestIgnoreRules_DoubleStar(t *testing.T) {
	rules := parseIgnoreRules("**/build/**\ndocs/**/*.tmp\nsrc/**/gen\n")
	cases := []struct {
		rel     string
		isDir   bool
		ignored bool
	}{
		{"build/out.go", false, true},
		{"a/b/build/out.go", false, true},
		{"buildx/out.go", false, false},
		{"docs/a/b/x.tmp", false, true},
		{"docs/x.tmp", false, true},
		{"docs/x.md", false, false},
		{"src/a/gen", true, true},
		{"src/a/gen/deep.go", false, true},
		{"src/gen", true, true},
	}
	for _, c := range cases {
		if got := ignoredByRules(c.rel, c.isDir, rules); got != c.ignored {
			t.Errorf("ignored(%q, dir=%v) = %v, want %v", c.rel, c.isDir, got, c.ignored)
		}
	}
}

// TestIndex_NestedGitignore: a .gitignore inside a subdirectory applies
// relative to that directory, and deeper rules override shallower ones.
func TestIndex_NestedGitignore(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()

	root := t.TempDir()
	mk := func(rel, body string) {
		t.Helper()
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Root ignores *.gen.go everywhere; sub/ ignores its own scratch dir
	// but un-ignores one generated file.
	mk(".gitignore", "*.gen.go\n")
	mk("sub/.gitignore", "scratch/\n!important.gen.go\n")
	mk("main.go", "package main\nfunc Keep(){}\n")
	mk("dropped.gen.go", "package main\nfunc Dropped(){}\n")
	mk("sub/code.go", "package sub\nfunc Sub(){}\n")
	mk("sub/important.gen.go", "package sub\nfunc Important(){}\n")
	mk("sub/scratch/junk.go", "package scratch\nfunc Junk(){}\n")
	mk("other/scratch.go", "package other\nfunc NotScratch(){}\n") // "scratch/" rule must not leak out of sub/

	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	// Indexed: main.go, sub/code.go, sub/important.gen.go, other/scratch.go.
	if res.FilesUpdated != 4 {
		t.Fatalf("FilesUpdated = %d, want 4 (errors=%v)", res.FilesUpdated, res.Errors)
	}
}
