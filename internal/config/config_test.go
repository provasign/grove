package config

import (
	"path/filepath"
	"testing"
)

func TestResolveNormalizesRoot(t *testing.T) {
	root := t.TempDir()
	cfg, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Root) {
		t.Fatalf("root is not absolute: %s", cfg.Root)
	}
	if cfg.StorePath != filepath.Join(cfg.Root, ".grove", "grove.db") {
		t.Fatalf("unexpected store path: %s", cfg.StorePath)
	}
}

func TestResolveRejectsMissingRoot(t *testing.T) {
	if _, err := Resolve(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing root error")
	}
}
