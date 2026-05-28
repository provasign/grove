package config

import (
	"path/filepath"
	"testing"
)

func TestResolveNormalizesRootAndDefaultPort(t *testing.T) {
	root := t.TempDir()
	cfg, err := Resolve(root, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Root) {
		t.Fatalf("root is not absolute: %s", cfg.Root)
	}
	if cfg.Port != DefaultPort {
		t.Fatalf("port = %d, want %d", cfg.Port, DefaultPort)
	}
	if cfg.StorePath != filepath.Join(cfg.Root, ".grove", "grove.db") {
		t.Fatalf("unexpected store path: %s", cfg.StorePath)
	}
}

func TestResolveRejectsMissingRoot(t *testing.T) {
	if _, err := Resolve(filepath.Join(t.TempDir(), "missing"), 0); err == nil {
		t.Fatal("expected missing root error")
	}
}
