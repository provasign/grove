// Negative same-name scope fixtures: a call to a method/function whose name
// also exists in an unrelated, out-of-scope package must NOT produce a cross
// edge. These lock in current cross-scope precision for the languages touched
// by the receiver-narrowing unification (Phase 4) so that refactor cannot
// silently regress them. PHP/C#/C/C++ same-namespace negatives live with the
// Phase 3 scope work, where the repo-wide → namespace/include scope change
// makes them pass.
package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestCalls_JavaSameNameNonImportedPackageDrops(t *testing.T) {
	// Caller in com/a holds a Repo (local type Repo) and calls r.save(). A
	// same-named Repo.save in com/b is neither imported nor same-package, so it
	// must not be linked; the in-package Repo.save is the only target.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "com/a/Caller.java::Caller.run@sha", FilePath: "com/a/Caller.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "run", QualifiedName: "Caller.run", ParentSymbol: "Caller",
			RawText:   "void run() { Repo r = new Repo(); r.save(); }",
			CallSites: []core.CallSite{{Callee: "r.save", Line: 1, Argc: 0}}},
		{ID: "com/a/Repo.java::Repo.save@sha", FilePath: "com/a/Repo.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "save", QualifiedName: "Repo.save", ParentSymbol: "Repo"},
		{ID: "com/b/Repo.java::Repo.save@sha", FilePath: "com/b/Repo.java", BlobSHA: "sha",
			Language: "java", Kind: core.KindMethod, Name: "save", QualifiedName: "Repo.save", ParentSymbol: "Repo"},
	}, 3)

	if !hasEdge(g, core.EdgeCalls, "com/a/Caller.java::Caller.run@sha", "com/a/Repo.java::Repo.save@sha") {
		t.Fatalf("missing in-package calls edge run→com/a Repo.save")
	}
	if hasEdge(g, core.EdgeCalls, "com/a/Caller.java::Caller.run@sha", "com/b/Repo.java::Repo.save@sha") {
		t.Fatalf("calls edge MUST NOT cross to non-imported com/b Repo.save")
	}
}

func TestCalls_RustSameNameOtherModuleDrops(t *testing.T) {
	// A free function call resolves within the crate, but a same-named function
	// in an unrelated module reached only through a non-matching qualifier must
	// not be linked. Caller calls parse::run(); only parse.rs's run is in module
	// scope, other.rs's run is not.
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/main.rs::drive@sha", FilePath: "src/main.rs", BlobSHA: "sha",
			Language: "rust", Kind: core.KindFunction, Name: "drive", QualifiedName: "drive",
			RawText:   "fn drive() { parse::run(); }",
			CallSites: []core.CallSite{{Callee: "parse.run", Line: 1, Argc: 0}}},
		{ID: "src/parse.rs::run@sha", FilePath: "src/parse.rs", BlobSHA: "sha",
			Language: "rust", Kind: core.KindFunction, Name: "run", QualifiedName: "run"},
		{ID: "src/other.rs::run@sha", FilePath: "src/other.rs", BlobSHA: "sha",
			Language: "rust", Kind: core.KindFunction, Name: "run", QualifiedName: "run"},
	}, 3)

	if !hasEdge(g, core.EdgeCalls, "src/main.rs::drive@sha", "src/parse.rs::run@sha") {
		t.Fatalf("missing module-scoped calls edge drive→parse.rs run")
	}
	if hasEdge(g, core.EdgeCalls, "src/main.rs::drive@sha", "src/other.rs::run@sha") {
		t.Fatalf("calls edge MUST NOT reach other.rs run via parse:: qualifier")
	}
}
