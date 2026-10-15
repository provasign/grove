//go:build !windows

package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestIndex_SkipsUnsupportedAndDirs(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "vendor"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "dist"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, ".grove"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "a.go"), []byte("package x"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "image.png"), []byte("\x89PNG\r\n"), 0o644) // binary — unsupported
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc M(){}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "readme.md"), []byte("# Project\n"), 0o644) // plaintext — indexed
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 2 { // main.go + readme.md
		t.Errorf("want 2 updated, got %d", res.FilesUpdated)
	}
}

func TestIndex_WalkErr(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	sub := filepath.Join(root, "denied")
	_ = os.MkdirAll(sub, 0o755)
	_ = os.WriteFile(filepath.Join(sub, "a.go"), []byte("package x"), 0o644)
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Skip("filesystem did not error on chmod 000")
	}
}

func TestIndex_PrunesDeleted(t *testing.T) {
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	p := filepath.Join(root, "a.go")
	_ = os.WriteFile(p, []byte("package x\nfunc A(){}\n"), 0o644)
	if _, _, err := idx.Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesPruned != 1 {
		t.Errorf("want pruned=1, got %d", res.FilesPruned)
	}
}

// bin/dist/target hold build output unless git tracks files in them: Cargo's
// src/bin/*.rs and a C project's tests/bin/*.c are source. Skipping by name
// alone left them out of the index (jansson test/bin, 2026-10-14).
func TestIndex_TrackedBuildNamedDirsAreIndexed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	idx, cleanup := newIdx(t)
	defer cleanup()
	root := t.TempDir()
	write := func(rel, body string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(root, rel)), 0o755)
		_ = os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644)
	}
	write("main.go", "package main\nfunc M(){}\n")
	write("src/bin/tool.go", "package main\nfunc Tool(){}\n")
	write("target/gen.go", "package gen\nfunc Gen(){}\n")
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("init", "-q")
	git("add", "main.go", "src/bin/tool.go")
	git("commit", "-q", "-m", "x")
	write("dist/out.go", "package dist\nfunc Out(){}\n") // untracked build output
	_, res, err := idx.Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesUpdated != 2 { // main.go + tracked src/bin/tool.go; untracked target/ and dist/ skipped
		t.Errorf("want 2 updated (main.go, src/bin/tool.go), got %d", res.FilesUpdated)
	}
}
