package graph

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
)

// A subclass with its base's short name (werkzeug: wrappers.Response over
// sansio Response, imported under an alias) is the walked type itself, not
// a dispatch target. Keying shared names by file (php-parser's Stmt\Const_
// vs Node\Const_) had made it one, displacing typed calls' direct binding
// (replay 2026-10-18: Response.get_data callers turned "by name").
func TestSubclassOverridesSkipsSameNamedSubclass(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pkg/__init__.py":          "",
		"pkg/sansio/__init__.py":   "",
		"pkg/wrappers/__init__.py": "",
		"pkg/sansio/response.py":   "class Response:\n    def get_data(self):\n        return b''\n",
		"pkg/wrappers/response.py": "from ..sansio.response import Response as _SansIOResponse\n\n\nclass Response(_SansIOResponse):\n    def get_data(self):\n        return b'x'\n",
	}
	var syms []core.SymbolRecord
	eng := parser.NewEngine()
	for _, rel := range []string{"pkg/__init__.py", "pkg/sansio/__init__.py", "pkg/wrappers/__init__.py", "pkg/sansio/response.py", "pkg/wrappers/response.py"} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(files[rel]), 0o644)
		s, _ := eng.ExtractFile(p, root)
		syms = append(syms, s...)
	}
	idx := newEdgeIndex(syms)
	if b := baseClassesInFileFor(idx, "python", "Response", "pkg/wrappers/response.py"); len(b) != 1 || b[0] != "Response" {
		t.Fatalf("fixture: wrappers Response should subclass Response, bases %v", b)
	}
	if got := subclassOverrides(idx, "python", "Response", "get_data", ""); len(got) != 0 {
		t.Errorf("same-named subclass returned as an override: %s", got[0].FilePath)
	}
}
