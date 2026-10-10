package graph

import (
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A caller with any resolved edge into the change set is certain, even when
// name matching adds more targets for it; only callers reached solely by
// name-derived edges are name-matched.
func TestCallerEvidenceNameMatchedOnlyWhenNoCertainEdge(t *testing.T) {
	ev := callerEvidence{}
	ev.note(core.Edge{From: "createHonoRouter", Source: core.EvidenceSourceHeuristic})
	ev.note(core.Edge{From: "createHonoRouter", Source: core.EvidenceSourceNative})
	ev.note(core.Edge{From: "createHonoRouter", Source: core.EvidenceSourceHeuristic})
	ev.note(core.Edge{From: "dispatch", Source: core.EvidenceSourceNative})
	ev.note(core.Edge{From: "denoBench", Source: core.EvidenceSourceHeuristic})
	ev.note(core.Edge{From: "denoBench", Source: core.EvidenceSourceRegex})
	if got, want := ev.nameMatched(), []string{"denoBench"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nameMatched = %v, want %v", got, want)
	}
	if got := (callerEvidence{"a": true}).nameMatched(); len(got) != 0 {
		t.Fatalf("all-certain callers reported name-matched: %v", got)
	}
}
