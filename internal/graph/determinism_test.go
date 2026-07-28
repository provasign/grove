package graph

import (
	"encoding/json"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// determinismFixture: one production function covered by six tests that all
// live in the SAME file with the SAME span start (0) — maximally tie-prone
// for any non-total sort key. Go's randomized map iteration then surfaces
// any order/content leak within a few repeated calls.
func determinismFixture() *CodeGraph {
	g := New()
	syms := []core.SymbolRecord{
		{ID: "svc.go::Svc@sha", FilePath: "svc.go", Language: "go", Kind: core.KindStruct,
			Name: "Svc", QualifiedName: "pkg.Svc", RawText: "type Svc struct{}"},
		{ID: "svc.go::Svc.Handle@sha", FilePath: "svc.go", Language: "go", Kind: core.KindMethod,
			Name: "Handle", QualifiedName: "pkg.Svc.Handle", ParentSymbol: "Svc",
			RawText: "func (s *Svc) Handle() {}"},
	}
	// Six tests, one shared file, identical spans, names that collide on the
	// bare-Name tiebreak the old AffectedTests sort used.
	for _, q := range []string{"A.TestHandle", "B.TestHandle", "C.TestHandle", "D.TestHandle", "E.TestHandle", "F.TestHandle"} {
		syms = append(syms, core.SymbolRecord{
			ID: "svc_test.go::" + q + "@sha", FilePath: "svc_test.go", Language: "go",
			Kind: core.KindFunction, Name: "TestHandle", QualifiedName: q,
			RawText:   "func TestHandle() { Handle() }",
			CallSites: []core.CallSite{{Callee: "Handle", Line: 1, Argc: 0}},
		})
	}
	g.Replace(syms, 2)
	return g
}

func snapshotJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestTestsClosureOutputsAreDeterministic pins run-to-run determinism of
// every tests-closure query surface: repeated identical calls must return
// byte-identical results (order AND content — UntestedSurface's per-site
// test sample was previously capped in map-iteration order).
func TestTestsClosureOutputsAreDeterministic(t *testing.T) {
	g := determinismFixture()

	wantAffected := snapshotJSON(t, g.AffectedTests([]string{"svc.go"}))
	wantTestsFor := snapshotJSON(t, g.TestsFor("Handle"))
	wantSymbol := snapshotJSON(t, g.TestsForSymbol("svc.go::Svc.Handle@sha", PolicyTests))
	ur, err := g.UntestedSurface("Svc.Handle")
	if err != nil {
		t.Fatalf("UntestedSurface: %v", err)
	}
	wantUntested := snapshotJSON(t, ur)

	// Sanity: the cap must actually be exercised (6 covering tests > cap).
	capped := false
	for _, c := range ur.Covered {
		if c.TestCount > len(c.Tests) {
			capped = true
		}
	}
	if !capped {
		t.Fatalf("fixture does not exercise the per-site test cap: %+v", ur.Covered)
	}

	for i := 0; i < 25; i++ {
		if got := snapshotJSON(t, g.AffectedTests([]string{"svc.go"})); got != wantAffected {
			t.Fatalf("AffectedTests unstable at iteration %d:\n%s\nvs\n%s", i, got, wantAffected)
		}
		if got := snapshotJSON(t, g.TestsFor("Handle")); got != wantTestsFor {
			t.Fatalf("TestsFor unstable at iteration %d", i)
		}
		if got := snapshotJSON(t, g.TestsForSymbol("svc.go::Svc.Handle@sha", PolicyTests)); got != wantSymbol {
			t.Fatalf("TestsForSymbol unstable at iteration %d", i)
		}
		u, err := g.UntestedSurface("Svc.Handle")
		if err != nil {
			t.Fatalf("UntestedSurface: %v", err)
		}
		if got := snapshotJSON(t, u); got != wantUntested {
			t.Fatalf("UntestedSurface unstable at iteration %d:\n%s\nvs\n%s", i, got, wantUntested)
		}
	}
}
