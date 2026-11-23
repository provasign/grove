package native

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SEC-01: an untrusted root never runs the repository's own gradlew, and
// trust is per root.
func TestUntrustedRootDoesNotRunGradlew(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "ran")
	if err := os.WriteFile(filepath.Join(root, "build.gradle"), []byte("// gradle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "gradlew"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetUntrusted(root, true)
	defer SetUntrusted(root, false)
	if !untrustedMode(root) || untrustedMode(t.TempDir()) {
		t.Fatal("trust must be per root")
	}
	cp, why := javacClasspath(context.Background(), nil, root, t.TempDir())
	if cp != "" || !strings.Contains(why, "untrusted") {
		t.Fatalf("classpath %q, reason %q", cp, why)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("untrusted index ran the repository's gradlew")
	}
	if !untrustedMode(filepath.Join(root, ".")) {
		t.Fatal("root matching must ignore a trailing ./")
	}
}
