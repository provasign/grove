package store

import (
	"context"
	"testing"
)

func TestAcquireAndReleaseLocks(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	locks, err := st.AcquireLocks(context.Background(), "intent-a", []string{"lock-a"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(locks) != 1 || locks[0].IntentID != "intent-a" {
		t.Fatalf("unexpected locks: %#v", locks)
	}
	if _, err := st.AcquireLocks(context.Background(), "intent-b", []string{"lock-a"}, 0); err == nil {
		t.Fatal("expected lock conflict")
	}
	released, err := st.ReleaseLocks(context.Background(), "intent-a")
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
}
