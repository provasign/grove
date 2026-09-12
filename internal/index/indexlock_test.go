package index

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireIndexLockSerializesProcessesAndHonorsContext(t *testing.T) {
	root := t.TempDir()
	first, err := acquireIndexLock(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = first()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	if _, err := acquireIndexLock(ctx, root); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second lock error = %v, want context deadline exceeded", err)
	}

	if err := first(); err != nil {
		t.Fatal(err)
	}
	released = true
	second, err := acquireIndexLock(context.Background(), root)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	if err := second(); err != nil {
		t.Fatal(err)
	}
}
