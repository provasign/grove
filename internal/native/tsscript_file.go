package native

import (
	"os"
	"sync"
)

// tsScriptLoader is what node runs with -e: it evaluates the resolver script
// from the file tsScriptEnv names. The script itself no longer fits on a
// Windows command line (32767 characters; it reached 34k with the
// compiler-declared pass, 2026-10-12: "The filename or extension is too
// long"). A direct eval runs in the -e module's scope, so require() still
// resolves typescript from node's working directory, exactly as when the
// script was the -e argument (the repo root, or the neutral directory in
// untrusted mode).
const tsScriptLoader = `eval(require('fs').readFileSync(process.env.GROVE_TS_SCRIPT, 'utf8'))`

var tsScriptFile struct {
	sync.Mutex
	path string
}

// tsScriptEnv returns the GROVE_TS_SCRIPT variable naming a private temporary
// copy of the resolver script: a random name with owner-only permissions,
// never a predictable shared path another user could plant. It is written once
// per process and again if a temp cleaner removed it under a long-lived engine.
func tsScriptEnv() (string, error) {
	tsScriptFile.Lock()
	defer tsScriptFile.Unlock()
	if tsScriptFile.path != "" {
		if _, err := os.Stat(tsScriptFile.path); err == nil {
			return "GROVE_TS_SCRIPT=" + tsScriptFile.path, nil
		}
	}
	f, err := os.CreateTemp("", "grove-ts-*.js")
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(tsScript); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	tsScriptFile.path = f.Name()
	return "GROVE_TS_SCRIPT=" + tsScriptFile.path, nil
}
