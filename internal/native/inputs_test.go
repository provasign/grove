package native

import (
	"os/exec"
	"testing"
)

// A compiler-input change drops the resident state of that analyzer only.
func TestInvalidateResidentStopsOnlyNamedAnalyzers(t *testing.T) {
	start := func() *exec.Cmd { return exec.Command("true") }
	residentWorkerFor("ts\x00/r\x00/r", "/r", "m", start)
	residentWorkerFor("java\x00/r\x00java", "/r", "m", start)
	residentWorkerFor("ts\x00/other\x00/other", "/other", "m", start)
	defer StopTSWorkers("")
	InvalidateResident("/r", []string{"js-ts"})
	residentWorkers.Lock()
	defer residentWorkers.Unlock()
	if _, ok := residentWorkers.m["ts\x00/r\x00/r"]; ok {
		t.Error("TypeScript worker of the root survived")
	}
	if _, ok := residentWorkers.m["java\x00/r\x00java"]; !ok {
		t.Error("Java worker stopped by a TypeScript config change")
	}
	if _, ok := residentWorkers.m["ts\x00/other\x00/other"]; !ok {
		t.Error("another root's worker stopped")
	}
}
