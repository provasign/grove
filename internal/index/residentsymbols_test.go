package index

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// Reusing resident symbols for unchanged files must yield exactly what a
// full AllSymbols load returns, across edits, additions and deletions; a
// baseline that does not match the store must fall back (nil).
func TestResidentSymbolsMatchFullLoad(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/r\n\ngo 1.22\n")
	for i := 0; i < 12; i++ {
		write("p"+strconv.Itoa(i)+"/p.go", "package p"+strconv.Itoa(i)+"\n\nfunc A() int { return B() }\n\nfunc B() int { return "+strconv.Itoa(i)+" }\n")
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	idx := New(parser.NewEngine(), st)
	if _, _, err := idx.IndexWithOptions(ctx, root, Options{}); err != nil {
		t.Fatal(err)
	}
	baseline, err := st.AllSymbols(ctx)
	if err != nil {
		t.Fatal(err)
	}
	steps := []func(){
		func() {
			write("p3/p.go", "package p3\n\nfunc A() int { return B() + 1 }\n\nfunc B() int { return 3 }\n\nfunc C() {}\n")
		},
		func() { write("p20/new.go", "package p20\n\nfunc N() {}\n") },
		func() { _ = os.RemoveAll(filepath.Join(root, "p5")) },
	}
	for n, step := range steps {
		step()
		_, res, err := idx.IndexWithOptions(ctx, root, Options{PrevSymbols: baseline, PrevEdges: []core.Edge{}})
		if err != nil {
			t.Fatal(err)
		}
		full, err := st.AllSymbols(ctx)
		if err != nil {
			t.Fatal(err)
		}
		fileMeta, err := st.AllFileMeta(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var reload []string
		for _, f := range []string{"p3/p.go", "p20/new.go", "p5/p.go"} {
			reload = append(reload, f)
		}
		got, err := idx.residentSymbols(ctx, baseline, fileMeta, reload)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, full) {
			t.Fatalf("step %d: resident symbols differ from a full load (%d vs %d); %v", n, len(got), len(full), res.Native)
		}
		baseline = full
	}

	// A stale baseline (another process re-indexed p1) must not be reused.
	stale := append([]core.SymbolRecord(nil), baseline...)
	for i := range stale {
		if stale[i].FilePath == "p1/p.go" {
			stale[i].BlobSHA = "stale"
		}
	}
	fileMeta, _ := st.AllFileMeta(ctx)
	if got, _ := idx.residentSymbols(ctx, stale, fileMeta, nil); got != nil {
		t.Fatal("stale baseline was reused instead of falling back to a full load")
	}
	// A baseline missing a file's symbols fails the count check.
	if got, _ := idx.residentSymbols(ctx, baseline[1:], fileMeta, nil); got != nil {
		t.Fatal("incomplete baseline was reused instead of falling back to a full load")
	}
}
