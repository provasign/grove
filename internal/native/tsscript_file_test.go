package native

import (
	"os"
	"strings"
	"testing"
)

// The resolver script reaches node through a file, not argv: Windows caps a
// command line at 32767 characters and the script outgrew it (2026-10-12).
// The file is rewritten if a temp cleaner removed it.
func TestTSScriptLoadedFromPrivateFile(t *testing.T) {
	if len(tsScriptLoader) > 200 {
		t.Fatalf("the -e loader must stay tiny, got %d characters", len(tsScriptLoader))
	}
	env, err := tsScriptEnv()
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimPrefix(env, "GROVE_TS_SCRIPT=")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != tsScript {
		t.Fatalf("script file %s does not hold the resolver script (err %v)", path, err)
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("script file is readable by others: %v", info.Mode())
	}
	os.Remove(path)
	env2, err := tsScriptEnv()
	if err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(strings.TrimPrefix(env2, "GROVE_TS_SCRIPT=")); err != nil || string(data) != tsScript {
		t.Fatalf("removed script file was not rewritten: %v", err)
	}
}
