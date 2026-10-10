package graph

import (
	"fmt"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Whole-repo-scope languages (C#, PHP, C/C++, Swift, ObjC) and Rust crates
// share ONE scope set across their files instead of materializing a copy
// per file: a per-file copy is O(files²) — 25,849² map entries (~19 GB) on
// a production C# monorepo, and 37% of the resolve-phase heap on a
// 971-file repo. Pin the sharing so it cannot silently regress.
func TestImportedFiles_WholeRepoScopeIsShared(t *testing.T) {
	var syms []core.SymbolRecord
	for i := 0; i < 50; i++ {
		f := fmt.Sprintf("src/File%d.cs", i)
		syms = append(syms, core.SymbolRecord{
			ID: f + "::C" + fmt.Sprint(i) + "@sha", FilePath: f, BlobSHA: "sha", Language: "csharp",
			Kind: core.KindClass, Name: "C" + fmt.Sprint(i), QualifiedName: "C" + fmt.Sprint(i),
		})
	}
	idx := newEdgeIndex(syms)
	a := idx.importedFiles("src/File0.cs")
	b := idx.importedFiles("src/File49.cs")
	if len(a) != 50 || len(b) != 50 {
		t.Fatalf("whole-repo scope sizes = %d, %d; want 50", len(a), len(b))
	}
	if fmt.Sprintf("%p", a) != fmt.Sprintf("%p", b) {
		t.Fatalf("C# files must share one scope map, got two allocations")
	}
}

func TestImportedFiles_RustCrateScopeIsSharedPerCrate(t *testing.T) {
	var syms []core.SymbolRecord
	for i := 0; i < 20; i++ {
		f := fmt.Sprintf("crates/a/src/m%d.rs", i)
		if i == 0 {
			f = "crates/a/src/lib.rs" // the crate root buildRustCrates keys on
		}
		syms = append(syms, core.SymbolRecord{
			ID: f + "::f" + fmt.Sprint(i) + "@sha", FilePath: f, BlobSHA: "sha", Language: "rust",
			Kind: core.KindFunction, Name: "f" + fmt.Sprint(i), QualifiedName: "f" + fmt.Sprint(i),
		})
	}
	idx := newEdgeIndex(syms)
	if idx.rustCrateOfFile == nil {
		t.Fatal("crate map not built: lib.rs must mark a crate root")
	}
	a := idx.importedFiles("crates/a/src/lib.rs")
	b := idx.importedFiles("crates/a/src/m19.rs")
	if _, ok := a["crates/a/src/m19.rs"]; !ok {
		t.Fatalf("crate scope must include sibling modules: %v", a)
	}
	if fmt.Sprintf("%p", a) != fmt.Sprintf("%p", b) {
		t.Fatalf("files of one crate must share one scope map")
	}
}

// A library's own `mod tests` paths (`tests::TempDir`) must not pull an
// integration-test directory (the workspace's tests/, another crate's
// crates/x/tests/) into its scope. Those targets are crates nothing can
// name; before, every one registered as crate "tests" and the map
// iteration picked which one a library reached (ripgrep's ignore crate
// got tests/ on one run and crates/ignore/tests on the next).
func TestImportedFiles_RustTestTargetsAreNotNamedCrates(t *testing.T) {
	mk := func(f, name, raw string) core.SymbolRecord {
		return core.SymbolRecord{
			ID: f + "::" + name + "@sha", FilePath: f, BlobSHA: "sha", Language: "rust",
			Kind: core.KindFunction, Name: name, QualifiedName: name, RawText: raw,
		}
	}
	for run := 0; run < 20; run++ {
		idx := newEdgeIndex([]core.SymbolRecord{
			mk("crates/ignore/src/lib.rs", "run", "fn run() {\n    let d = tests::TempDir::new();\n}"),
			mk("crates/ignore/src/walk.rs", "walk", "fn walk() {}"),
			mk("crates/ignore/tests/matched.rs", "matched", "fn matched() {}"),
			mk("crates/matcher/src/lib.rs", "find", "fn find() {}"),
			mk("crates/matcher/tests/util.rs", "util", "fn util() {}"),
			mk("tests/json.rs", "json", "fn json() {}"),
		})
		scope := idx.importedFiles("crates/ignore/src/lib.rs")
		for _, f := range []string{"tests/json.rs", "crates/ignore/tests/matched.rs", "crates/matcher/tests/util.rs"} {
			if _, ok := scope[f]; ok {
				t.Fatalf("run %d: ignore's scope reached test target %s: %v", run, f, sortedKeys(scope))
			}
		}
		if _, ok := scope["crates/ignore/src/walk.rs"]; !ok {
			t.Fatalf("run %d: crate scope lost its own module: %v", run, sortedKeys(scope))
		}
	}
}
