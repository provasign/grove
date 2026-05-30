package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolve_Defaults(t *testing.T) {
	c, err := Resolve("", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Port != DefaultPort {
		t.Errorf("port=%d", c.Port)
	}
	if c.Root == "" {
		t.Error("empty root")
	}
}

func TestResolve_MissingDir(t *testing.T) {
	if _, err := Resolve(filepath.Join(t.TempDir(), "nope"), 0); err == nil {
		t.Error("expected stat error")
	}
}

func TestResolve_NotADirectory(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(p, 0); err == nil {
		t.Error("expected not-a-directory error")
	}
}
