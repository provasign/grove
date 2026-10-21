package index

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/store"
)

func verdictStore(t *testing.T, stored []string) *store.Store {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if stored != nil {
		persistVerdict(t, st, stored)
	}
	return st
}

func persistVerdict(t *testing.T, st *store.Store, lines []string) {
	t.Helper()
	raw, _ := json.Marshal(lines)
	if err := st.SetMeta(context.Background(), core.MetaIndexNative, string(raw)); err != nil {
		t.Fatal(err)
	}
}

// Stack audit 2026-10-20 #10: re-carrying an unchanged diagnostic must not
// grow the verdict (it went 2, 4, 6, 8, 10 lines).
func TestCarryForwardVerdictDoesNotGrow(t *testing.T) {
	ctx := context.Background()
	initial := []string{"csharp: full pass over 1 file(s) took 30ms", "csharp: no edges produced — previous native edges carried forward"}
	st := verdictStore(t, initial)
	for round := 1; round <= 4; round++ {
		next := carryForwardVerdict(ctx, st, initial)
		persistVerdict(t, st, next)
		if !reflect.DeepEqual(next, initial) {
			t.Fatalf("round %d: verdict = %q, want the current attempt only %q", round, next, initial)
		}
	}
}

// A good pass, then repeated carries (unchanged, then unavailable): the
// last good pass is reported once, followed by the current reason only;
// earlier attempts are superseded, not accumulated.
func TestCarryForwardVerdictKeepsLastGoodOnce(t *testing.T) {
	ctx := context.Background()
	st := verdictStore(t, nil)
	good := []string{"java: full pass over 3 file(s) took 2s", "java: javac attributed 3 file(s)", "go: full pass over 1 file(s) took 1s", "go: resolved 4 native call edge(s)"}
	persistVerdict(t, st, carryForwardVerdict(ctx, st, good))

	unchanged := []string{"java: skipped: no changed files in its languages (previous edges carried forward)", "go: incremental pass over 1 file(s) took 1s", "go: resolved 5 native call edge(s)"}
	got := carryForwardVerdict(ctx, st, unchanged)
	persistVerdict(t, st, got)
	want := []string{good[0], good[1], unchanged[0], unchanged[1], unchanged[2]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unchanged carry:\n got %q\nwant %q", got, want)
	}

	for round := 0; round < 3; round++ {
		unavailable := []string{"java: skipped: no JDK 11+ found (previous native edges carried forward)", "go: skipped: no changed files in its languages (previous edges carried forward)"}
		got = carryForwardVerdict(ctx, st, unavailable)
		persistVerdict(t, st, got)
		want = []string{good[0], good[1], unavailable[0], unchanged[1], unchanged[2], unavailable[1]}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("round %d unavailable carry:\n got %q\nwant %q", round, got, want)
		}
	}

	// Disabled: its edges are dropped, so nothing of the old pass is claimed.
	got = carryForwardVerdict(ctx, st, []string{"java: skipped: disabled by config", "go: skipped: no changed files in its languages (previous edges carried forward)"})
	persistVerdict(t, st, got)
	got = carryForwardVerdict(ctx, st, []string{"java: skipped: no JDK 11+ found (previous native edges carried forward)"})
	if want := []string{"java: skipped: no JDK 11+ found (previous native edges carried forward)"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("after disable: got %q want %q", got, want)
	}
}

// An index written before the last-good key existed: its verdict holds the
// old accumulated history. The success lines are recovered once, the
// repeated attempt lines are dropped.
func TestCarryForwardVerdictMigratesAccumulatedHistory(t *testing.T) {
	ctx := context.Background()
	old := []string{
		"csharp: full pass over 9 file(s) took 1s", "csharp: resolved 12 native call edge(s)",
		"csharp: full pass over 9 file(s) took 1s", "csharp: resolved 12 native call edge(s)",
		"csharp: no edges produced — previous native edges carried forward",
		"csharp: no edges produced — previous native edges carried forward",
	}
	st := verdictStore(t, old)
	current := []string{"csharp: skipped: no changed files in its languages (previous edges carried forward)"}
	got := carryForwardVerdict(ctx, st, current)
	want := []string{"csharp: resolved 12 native call edge(s)", current[0]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q want %q", got, want)
	}
}
