package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func deadCodeFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		// main -> alive() ; orphan() unreached ; helperViaValue passed as
		// value (name in main's text, no call edge) ; ExportedIdle exported
		// and unreferenced ; deadCaller -> deadCallee cluster.
		{ID: "main.go::main@sha", FilePath: "main.go", Language: "go", Kind: core.KindFunction,
			Name: "main", QualifiedName: "pkg.main",
			RawText:   "func main() { alive(); register(helperViaValue) }",
			CallSites: []core.CallSite{{Callee: "alive", Line: 1, Argc: 0}}},
		{ID: "a.go::alive@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "alive", QualifiedName: "pkg.alive", RawText: "func alive() {}"},
		{ID: "a.go::orphan@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "orphan", QualifiedName: "pkg.orphan", RawText: "func orphan() {}"},
		{ID: "a.go::helperViaValue@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "helperViaValue", QualifiedName: "pkg.helperViaValue", RawText: "func helperViaValue() {}"},
		{ID: "b.go::ExportedIdle@sha", FilePath: "b.go", Language: "go", Kind: core.KindFunction,
			Name: "ExportedIdle", QualifiedName: "pkg.ExportedIdle", Exports: true,
			RawText: "func ExportedIdle() {}"},
		{ID: "c.go::deadCaller@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCaller", QualifiedName: "pkg.deadCaller",
			RawText:   "func deadCaller() { deadCallee() }",
			CallSites: []core.CallSite{{Callee: "deadCallee", Line: 1, Argc: 0}}},
		{ID: "c.go::deadCallee@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCallee", QualifiedName: "pkg.deadCallee", RawText: "func deadCallee() {}"},
	}, 3)
	return g
}

func TestDeadCodeBuckets(t *testing.T) {
	g := deadCodeFixture()
	r := g.DeadCode(nil)
	dead := names(r.Dead)
	if !dead["orphan"] {
		t.Errorf("Dead = %v, want orphan", dead)
	}
	if dead["alive"] || dead["main"] {
		t.Errorf("Dead = %v wrongly contains live code", dead)
	}
	// Passed as a value: no call edge, but the name occurs in main's text.
	if dead["helperViaValue"] {
		t.Errorf("Dead wrongly contains helperViaValue (referenced by value)")
	}
	// Transitively-dead cluster: only the top (deadCaller) is reported;
	// deadCallee stays because deadCaller's text still mentions it.
	if !dead["deadCaller"] || dead["deadCallee"] {
		t.Errorf("Dead = %v, want deadCaller only from the dead cluster", dead)
	}
	if exp := names(r.ExportedUnreferenced); !exp["ExportedIdle"] {
		t.Errorf("ExportedUnreferenced = %v, want ExportedIdle", exp)
	}
	if len(r.Caveats) == 0 {
		t.Error("Caveats must always be present")
	}
	if r.Considered == 0 || r.RootCount == 0 {
		t.Errorf("counters: considered=%d roots=%d", r.Considered, r.RootCount)
	}
}
