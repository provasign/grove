package native

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A repo that never depended on typescript is not "missing dependencies":
// the checker does not apply there, and the reason must say so.
func TestJSTSAvailabilityPlainJavaScript(t *testing.T) {
	if !commandExists("node") {
		t.Skip("node not on PATH")
	}
	write := func(dir, name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for name, tc := range map[string]struct {
		files map[string]string
		plain bool
	}{
		"plain js":                        {map[string]string{"package.json": `{"name":"x","dependencies":{"debug":"4"}}`, "index.js": "module.exports = 1\n"}, true},
		"typescript devDependency":        {map[string]string{"package.json": `{"devDependencies":{"typescript":"5"}}`, "index.js": ""}, false},
		"nested tsconfig":                 {map[string]string{"package.json": `{}`, "pkg/a/tsconfig.json": `{}`}, false},
		"jsconfig":                        {map[string]string{"package.json": `{}`, "jsconfig.json": `{}`}, false},
		"typescript only in node_modules": {map[string]string{"package.json": `{}`, "node_modules/x/package.json": `{"dependencies":{"typescript":"5"}}`}, true},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for f, body := range tc.files {
				write(dir, f, body)
			}
			if got := plainJavaScriptProject(dir); got != tc.plain {
				t.Fatalf("plainJavaScriptProject = %v, want %v", got, tc.plain)
			}
			av := jsTSAnalyzer{}.Available(context.Background(), dir)
			if av.Available {
				t.Skip("typescript is resolvable from the temp dir on this machine")
			}
			if (av.Reason == tsPlainJSReason) != tc.plain {
				t.Fatalf("reason %q, plain=%v", av.Reason, tc.plain)
			}
		})
	}
}
