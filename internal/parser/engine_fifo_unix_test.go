//go:build unix

package parser

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestFileBlobSHA_RejectsFifo guards the specific DoS: a named pipe named like
// source made the pre-guard os.ReadFile block forever, hanging the index. The
// IsRegular check must reject it fast. Unix-only (syscall.Mkfifo).
func TestFileBlobSHA_RejectsFifo(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "x.go")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, err := FileBlobSHA(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FileBlobSHA(fifo) returned nil — should reject non-regular files")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("FileBlobSHA(fifo) blocked — non-regular-file guard missing (index hang)")
	}
}
