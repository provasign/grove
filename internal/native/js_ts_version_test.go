package native

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func fakeTypeScript(t *testing.T, version string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"package.json":                              `{"name":"app","devDependencies":{"typescript":"^` + version + `"}}`,
		"tsconfig.json":                             `{}`,
		"node_modules/typescript/package.json":      `{"name":"typescript","version":"` + version + `","main":"lib/typescript.js"}`,
		"node_modules/typescript/lib/typescript.js": "module.exports = {};\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// BUG-06: TypeScript 7 (the Go port) has no JS compiler API. It is
// skipped with a named reason instead of crashing the analyzer.
func TestJSTSAvailableByTypeScriptMajor(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed")
	}
	t.Setenv("GROVE_UNTRUSTED", "")
	if a := (jsTSAnalyzer{}).Available(context.Background(), fakeTypeScript(t, "5.9.3")); !a.Available {
		t.Fatalf("typescript 5: %+v", a)
	}
	a := (jsTSAnalyzer{}).Available(context.Background(), fakeTypeScript(t, "7.0.2"))
	if a.Available || !strings.Contains(a.Reason, "typescript 7.0.2 is the native (Go) compiler") || !strings.Contains(a.Reason, "typescript@5") {
		t.Fatalf("typescript 7: %+v", a)
	}
}

func TestTSBootstrapFailureIsOneLineWithLog(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".grove"), 0o700); err != nil {
		t.Fatal(err)
	}
	trace := "/x/script.js:12\n    at foo\nTypeError: Cannot read properties of undefined (reading 'fileExists')\n    at Object.<anonymous> (/x/script.js:12:9)\n    at Module._compile"
	msg := tsBootstrapFailure(root, errors.New("exit status 1"), trace)
	if strings.Contains(msg, "\n") || !strings.Contains(msg, "TypeError: Cannot read properties of undefined") || !strings.Contains(msg, "ts-bootstrap.log") {
		t.Fatalf("msg = %q", msg)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".grove", "ts-bootstrap.log")); err != nil || !strings.Contains(string(data), "Module._compile") {
		t.Fatalf("log: %v %q", err, data)
	}
}
