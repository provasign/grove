//go:build unix

package index

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// A FIFO in the tree used to hang the walk forever: the shebang sniffer
// opened it for reading.
func TestIndexSkipsFIFO(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := New(parser.NewEngine(), st).IndexWithOptions(context.Background(), root, Options{})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("index hung on a FIFO")
	}
}
