package grove

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/parser"
)

// REL-06: an index stamped by another extractor version is rebuilt by
// IndexIfStale, the path Prism takes on every command, and is restamped.
func TestIndexIfStaleRebuildsOnExtractorVersionBump(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\n\nfunc B() { A() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := Open(t.Context(), Config{RepoRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { e.Close() }()
	if _, err := e.Index(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := e.store.SetMeta(t.Context(), "extractor-version", "previous"); err != nil {
		t.Fatal(err)
	}
	ran := false
	res, err := e.IndexIfStale(t.Context(), root, func(Status) { ran = true })
	if err != nil {
		t.Fatal(err)
	}
	if !ran || res.AlreadyCurrent {
		t.Fatalf("an older extractor stamp did not trigger a rebuild: ran=%v current=%v", ran, res.AlreadyCurrent)
	}
	if v, _, _ := e.store.GetMeta(t.Context(), "extractor-version"); v != parser.ExtractorVersion {
		t.Fatalf("restamped %q, want %q", v, parser.ExtractorVersion)
	}
	res, err = e.IndexIfStale(t.Context(), root, nil)
	if err != nil || !res.AlreadyCurrent {
		t.Fatalf("second call rebuilt again: %+v %v", res, err)
	}
}
