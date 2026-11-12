package grove

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderAnswersWithoutTouchingTheRepository(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	if _, err := OpenReader(root); err == nil {
		t.Fatal("OpenReader succeeded on a repository with no index")
	}
	if _, err := os.Stat(filepath.Join(root, ".grove")); err == nil {
		t.Fatal("OpenReader created .grove in an unindexed repository")
	}

	src := "package lib\n\ntype Box struct{}\n\nfunc (b *Box) Open() {}\n\nfunc Helper() {}\n"
	if err := os.WriteFile(filepath.Join(root, "lib.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	eng, err := Open(ctx, Config{RepoRoot: root, OneShot: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Index(ctx, root); err != nil {
		t.Fatal(err)
	}
	if err := eng.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	syms, err := r.SymbolsNamed(ctx, []string{"Helper", "Box.Open", "Absent"})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, s := range syms {
		got[s.QualifiedName] = s.FilePath
	}
	if got["Helper"] != "lib.go" || got["Box.Open"] != "lib.go" || len(got) != 2 {
		t.Fatalf("SymbolsNamed = %v; want Helper and Box.Open in lib.go", got)
	}
	if sha, ok, err := r.FileBlobSHA(ctx, "lib.go"); err != nil || !ok || sha == "" {
		t.Fatalf("FileBlobSHA = %q, %v, %v", sha, ok, err)
	}
}
