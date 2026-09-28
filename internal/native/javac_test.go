package native

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeJDK(t *testing.T, version string) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"javac": "#!/bin/sh\necho 'javac " + version + "'\n",
		"java":  "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// The resolver runs as a single-file source program, which needs Java 11+.
func TestProbeJDKRequiresJava11(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake JDK")
	}
	if j := probeJDK(fakeJDK(t, "1.8.0_392")); j != nil {
		t.Fatalf("Java 8 accepted: %+v", j)
	}
	home := fakeJDK(t, "17.0.2")
	j := probeJDK(home)
	if j == nil || j.home != home || j.java != filepath.Join(home, "bin", "java") {
		t.Fatalf("Java 17 rejected or wrong launcher: %+v", j)
	}
	if probeJDK(t.TempDir()) != nil {
		t.Fatal("empty home accepted")
	}
}

// Source roots that redefine the same classes (guava's guava/ and
// android/guava/) land in separate batches; package-info.java files shared by
// every root do not count as conflicts.
func TestJavaBatchesSeparateDuplicateClassRoots(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := []string{
		"guava/src/com/x/A.java", "android/guava/src/com/x/A.java",
		"guava/src/com/x/package-info.java", "testlib/src/com/x/package-info.java",
		"testlib/src/com/x/T.java",
	}
	for _, f := range files {
		body := "package com.x;\n"
		if filepath.Base(f) != "package-info.java" {
			body += "class " + strings.TrimSuffix(filepath.Base(f), ".java") + " {}\n"
		}
		write(f, body)
	}
	batches := javaBatches(root, files)
	if len(batches) != 2 {
		t.Fatalf("want 2 batches, got %d: %+v", len(batches), batches)
	}
	batchOf := map[string]int{}
	for i, b := range batches {
		for _, f := range b.files {
			batchOf[f] = i
		}
	}
	if batchOf["guava/src/com/x/A.java"] == batchOf["android/guava/src/com/x/A.java"] {
		t.Errorf("duplicate classes share a batch: %+v", batches)
	}
	if n := len(batches[0].files) + len(batches[1].files); n != len(files) {
		t.Errorf("files attributed %d times, want %d", n, len(files))
	}
}

// The resolved classpath is reused while build files are unchanged (no Maven
// start per incremental index); editing a pom.xml changes the key.
func TestJavacClasspathCacheFollowsBuildFiles(t *testing.T) {
	root := t.TempDir()
	pom := filepath.Join(root, "pom.xml")
	if err := os.WriteFile(pom, []byte("<project/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	jar := filepath.Join(root, "lib.jar")
	if err := os.WriteFile(jar, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := &jdk{home: "/jdk"}
	key := buildFilesKey(root, j.home)
	if key == "" {
		t.Fatal("no key for a Maven project")
	}
	cache := filepath.Join(root, ".grove", "java-classpath.json")
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{"Key": key, "Classpath": jar})
	if err := os.WriteFile(cache, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if cp, _ := javacClasspathCached(context.Background(), j, root, t.TempDir()); cp != jar {
		t.Fatalf("cache hit returned %q, want %q", cp, jar)
	}
	if err := os.WriteFile(pom, []byte("<project><dependencies/></project>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if buildFilesKey(root, j.home) == key {
		t.Fatal("editing pom.xml did not change the classpath key")
	}
	if buildFilesKey(t.TempDir(), j.home) != "" {
		t.Fatal("a project without build files must not get a key")
	}
}
