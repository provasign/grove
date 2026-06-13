package eval

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// C# call-edge ground truth from the Roslyn semantic model. A small dotnet
// program (eval/cstruth) builds one compilation from every .cs file under
// the repo and resolves each invocation/object-creation to its symbol;
// edges between two in-repo declarations are the truth — the same altitude
// as the TypeScript compiler-API oracle.
//
// The dotnet SDK is located via $DOTNET_ROOT, PATH, or ~/.dotnet. The
// compiled oracle assembly is found via $GROVE_EVAL_CSTRUTH or, by default,
// eval/cstruth/bin/cstruth.dll relative to this package.
func CSharpCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	dotnet, err := dotnetBin()
	if err != nil {
		return TruthFile{}, nil, err
	}
	dll, err := cstruthDLL()
	if err != nil {
		return TruthFile{}, nil, err
	}

	tmp, err := os.MkdirTemp("", "grove-cs-truth-*")
	if err != nil {
		return TruthFile{}, nil, err
	}
	defer os.RemoveAll(tmp)
	outPath := filepath.Join(tmp, "truth.jsonl")

	cmd := exec.Command(dotnet, dll, root, outPath)
	cmd.Env = append(os.Environ(), "DOTNET_CLI_TELEMETRY_OPTOUT=1", "DOTNET_NOLOGO=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return TruthFile{}, nil, fmt.Errorf("cstruth: %w\n%s", err, tail(out))
	}
	return ReadTruth(outPath)
}

// dotnetBin locates the dotnet host.
func dotnetBin() (string, error) {
	if p, err := exec.LookPath("dotnet"); err == nil {
		return p, nil
	}
	for _, cand := range []string{
		os.Getenv("DOTNET_ROOT") + "/dotnet",
		os.Getenv("HOME") + "/.dotnet/dotnet",
		"/usr/local/share/dotnet/dotnet",
		"/usr/lib/dotnet/dotnet",
	} {
		if cand == "/dotnet" {
			continue
		}
		if st, err := os.Stat(cand); err == nil && !st.IsDir() {
			return cand, nil
		}
	}
	return "", fmt.Errorf("dotnet not found (PATH, $DOTNET_ROOT, ~/.dotnet)")
}

// cstruthDLL locates the compiled oracle assembly.
func cstruthDLL() (string, error) {
	if env := os.Getenv("GROVE_EVAL_CSTRUTH"); env != "" {
		return env, nil
	}
	// Relative to the eval module root (this file's package dir at runtime
	// is the cwd of `go run`/the built binary's invocation, so try common
	// locations).
	for _, cand := range []string{
		"cstruth/bin/cstruth.dll",
		"eval/cstruth/bin/cstruth.dll",
	} {
		if abs, err := filepath.Abs(cand); err == nil {
			if st, err := os.Stat(abs); err == nil && !st.IsDir() {
				return abs, nil
			}
		}
	}
	return "", fmt.Errorf("cstruth.dll not found (build eval/cstruth or set $GROVE_EVAL_CSTRUTH)")
}
