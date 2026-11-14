package native

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func fakeToolchain(t *testing.T, modCache, version string) {
	t.Helper()
	bin := filepath.Join(modCache, "golang.org", "toolchain@v0.0.1-"+version+"."+runtime.GOOS+"-"+runtime.GOARCH, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The oldest cached toolchain that satisfies the requirement is used; older
// ones and other platforms are not.
func TestCachedGoToolchainPicksOldestSatisfying(t *testing.T) {
	cache := t.TempDir()
	fakeToolchain(t, cache, "go1.26.9")
	fakeToolchain(t, cache, "go1.27.4")
	fakeToolchain(t, cache, "go1.27.10")
	fakeToolchain(t, cache, "go1.28.0")
	other := filepath.Join(cache, "golang.org", "toolchain@v0.0.1-go1.27.1.plan9-mips", "bin")
	_ = os.MkdirAll(other, 0o755)
	_ = os.WriteFile(filepath.Join(other, "go"), nil, 0o755)

	tc, ok := cachedGoToolchain(cache, "go1.27")
	if !ok || tc.version != "go1.27.4" {
		t.Fatalf("got %+v ok=%v, want go1.27.4", tc, ok)
	}
	if _, ok := cachedGoToolchain(cache, "go1.29"); ok {
		t.Fatal("no cached toolchain satisfies go1.29")
	}
	if _, ok := cachedGoToolchain("", "go1.27"); ok {
		t.Fatal("no module cache, no toolchain")
	}
}

// The requirement is the newest of the go and toolchain lines.
func TestGoRequiredVersion(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex\n\ngo 1.26.4\n\ntoolchain go1.27.2\n"), 0o644)
	if got := goRequiredVersion(root); got != "go1.27.2" {
		t.Fatalf("got %q, want go1.27.2", got)
	}
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex\n\ngo 1.27\n"), 0o644)
	if got := goRequiredVersion(root); got != "go1.27" {
		t.Fatalf("got %q, want go1.27", got)
	}
}
