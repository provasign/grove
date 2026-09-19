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
