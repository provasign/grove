package index

import (
	"reflect"
	"testing"
)

func TestFirstIndexWording(t *testing.T) {
	in := []string{
		"go: skipped: no go.mod or go.work (previous native edges carried forward)",
		"java: timed out after 5s — previous native edges carried forward",
		"python: resolved 3 edge(s)",
	}
	want := []string{
		"go: skipped: no go.mod or go.work (no native edges yet)",
		"java: timed out after 5s — no native edges yet",
		"python: resolved 3 edge(s)",
	}
	if got := firstIndexWording(true, in); !reflect.DeepEqual(got, want) {
		t.Fatalf("first index: %q", got)
	}
	if got := firstIndexWording(false, in); !reflect.DeepEqual(got, in) {
		t.Fatalf("later index changed: %q", got)
	}
}
