package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// zod default.test.ts (2026-10-16): the parser guessed result1.get(...) as
// $ZodRegistry.get at line 358 (no column); tsc resolved it to Map.get at
// 358:18. The guess is covered by the compiler's mark.
func TestCallMarksCoverColumnlessGuess(t *testing.T) {
	m := callMarks{"f::top": {"get": {"358:18": true, "361:18": true}}}
	guess := core.Edge{From: "f::top", To: "r.ts::Registry.get@b", Type: core.EdgeCalls, Lines: "358,361"}
	if !m.covers(guess) {
		t.Errorf("columnless guess on marked lines not covered")
	}
	guess.Lines = "358,362"
	if m.covers(guess) {
		t.Errorf("guess with an unmarked line covered")
	}
}

// hono Context.header reads options?.append (SetHeadersOptions.append) and
// calls headers.append(...) (Headers.append) a line later; h3's tracing
// tests build H3Route and other handler: literals in one <top-level>. With
// positioned compiler edges the verdict is per line.
func TestCompilerVerdictPerLine(t *testing.T) {
	c := &memberClassifier{
		nativeOurs:        map[string]bool{"hdr": true, "top": true},
		nativeOthers:      map[string]bool{"top": true},
		nativeOursLines:   map[string]map[int]bool{},
		nativeOthersLines: map[string]map[int]bool{},
	}
	noteEdgeLines(c.nativeOursLines, core.Edge{From: "hdr", Lines: "526:25"})
	noteEdgeLines(c.nativeOursLines, core.Edge{From: "top", Lines: "768:9,832:9"})
	noteEdgeLines(c.nativeOthersLines, core.Edge{From: "top", Lines: "54:7"})
	hdr := &core.SymbolRecord{ID: "hdr"}
	top := &core.SymbolRecord{ID: "top"}
	for _, tc := range []struct {
		e    *core.SymbolRecord
		line int
		want verdict
	}{
		{hdr, 526, vConfirm},
		{hdr, 527, vAmbiguous},
		{top, 768, vConfirm},
		{top, 54, vExclude},
		{top, 100, vAmbiguous},
	} {
		if v, _ := c.compilerVerdict(tc.e, tc.line, vAmbiguous, "heuristic"); v != tc.want {
			t.Errorf("%s:%d: verdict %v, want %v", tc.e.ID, tc.line, v, tc.want)
		}
	}
}
