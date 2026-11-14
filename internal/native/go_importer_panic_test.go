package native

import (
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type panickingImporter struct{ calls int }

func (p *panickingImporter) Import(string) (*types.Package, error) {
	p.calls++
	panic("unsupported version: 3") // what internal/pkgbits says for newer export data
}

// Export data from a newer Go than this binary panics in the stdlib reader
// (prism built with go1.26, go1.27 on PATH). The import must not crash the
// index: it reports the error once and types dependencies from source.
func TestGoProjectImporterRecoversFromExportDataPanic(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module ex\n\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nimport \"strings\"\n\nfunc main() { _ = strings.ToUpper(\"x\") }\n"), 0o644)
	bad := &panickingImporter{}
	p := newGoProjectImporter(nil, goDeps{})
	p.fallback, p.root = bad, root
	pkg, err := p.Import("strings")
	if !strings.Contains(p.exportUnreadable, "unsupported version") {
		t.Fatalf("panic not recorded: %q", p.exportUnreadable)
	}
	if _, ok := p.fallback.(failingImporter); ok {
		t.Skip("go list unavailable; source fallback not built")
	}
	if err != nil || pkg == nil || pkg.Scope().Lookup("ToUpper") == nil {
		t.Fatalf("strings not typed from source after the panic: pkg=%v err=%v", pkg, err)
	}
	if _, err := p.Import("unicode"); err != nil {
		t.Fatalf("second import: %v", err)
	}
	if bad.calls != 1 {
		t.Fatalf("panicking importer called %d times, want 1", bad.calls)
	}
}

// With no source fallback either, every import fails with the recorded error.
func TestGoProjectImporterPanicWithoutSourceFallback(t *testing.T) {
	p := newGoProjectImporter(nil, goDeps{})
	p.fallback, p.root = &panickingImporter{}, filepath.Join(t.TempDir(), "missing")
	if _, err := p.Import("strings"); err == nil || !strings.Contains(err.Error(), "unreadable by this build") {
		t.Fatalf("err = %v", err)
	}
	if _, err := p.Import("fmt"); err == nil {
		t.Fatal("expected an error after the source fallback failed")
	}
}
