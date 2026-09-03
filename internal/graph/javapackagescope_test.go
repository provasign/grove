package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// TestJavaSamePackageAcrossSourceRoots: Java's package identity spans
// Maven/Gradle source roots — src/test/java/com/x and src/main/java/com/x
// are ONE package, and test classes conventionally sit in the package of
// the code under test precisely so no import is needed. Same-directory-only
// scope made every such caller invisible: measured 2026-09-02 (dubbo,
// MetadataInfo.ServiceInfo.getMethodParameter), change-impact claimed
// "callers (2), completeness: closed" while MetadataInfoTest called the
// method twice. The fixture reproduces the minimal shape: a nested class's
// method, one same-package caller in a DIFFERENT source root with no
// import, one explicitly-importing caller in another package.
func TestJavaSamePackageAcrossSourceRoots(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "src/main/java/com/x/Meta.java::Meta@sha", FilePath: "src/main/java/com/x/Meta.java", Language: "java", Kind: core.KindClass,
			Name: "Meta", QualifiedName: "Meta", Signature: "public class Meta"},
		{ID: "src/main/java/com/x/Meta.java::Inner@sha", FilePath: "src/main/java/com/x/Meta.java", Language: "java", Kind: core.KindClass,
			Name: "Inner", QualifiedName: "Meta.Inner", ParentSymbol: "Meta",
			Signature: "public static class Inner"},
		{ID: "src/main/java/com/x/Meta.java::Inner.getThing@sha", FilePath: "src/main/java/com/x/Meta.java", Language: "java", Kind: core.KindMethod,
			Name: "getThing", QualifiedName: "Meta.Inner.getThing", ParentSymbol: "Inner",
			Signature: "public String getThing(String method, String key, String def)",
			RawText:   "public String getThing(String method, String key, String def) { return def; }"},
		// Same package (com.x), DIFFERENT source root, no import — the
		// idiomatic Java test caller.
		{ID: "src/test/java/com/x/MetaTest.java::MetaTest@sha", FilePath: "src/test/java/com/x/MetaTest.java", Language: "java", Kind: core.KindClass,
			Name: "MetaTest", QualifiedName: "MetaTest", Signature: "public class MetaTest"},
		{ID: "src/test/java/com/x/MetaTest.java::MetaTest.testDirect@sha", FilePath: "src/test/java/com/x/MetaTest.java", Language: "java", Kind: core.KindMethod,
			Name: "testDirect", QualifiedName: "MetaTest.testDirect", ParentSymbol: "MetaTest",
			Signature: "void testDirect()",
			RawText:   "void testDirect() { Meta.Inner inner2 = make(); String s = inner2.getThing(\"a\", \"b\", \"c\"); }",
			CallSites: []core.CallSite{{Callee: "inner2.getThing", Line: 1, Argc: 3}}},
	}, 3)

	r, err := g.ChangeImpact("Meta.Inner.getThing")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	found := false
	for _, c := range r.Callers {
		if c.QualifiedName == "MetaTest.testDirect" {
			found = true
		}
	}
	if !found {
		t.Fatalf("same-package cross-source-root test caller missing from callers: %+v", r.Callers)
	}
}

// TestJavaPackageSuffix pins the source-root marker parsing.
func TestJavaPackageSuffix(t *testing.T) {
	cases := []struct {
		dir  string
		want string
		ok   bool
	}{
		{"src/main/java/com/x", "com/x", true},
		{"src/test/java/com/x", "com/x", true},
		{"mod/dubbo-api/src/main/java/org/apache", "org/apache", true},
		{"src/it/java/com/y", "com/y", true},
		{"lib/nested/other", "", false},
		{"srcx/main/java/com", "", false},
	}
	for _, c := range cases {
		got, ok := javaPackageSuffix(c.dir)
		if got != c.want || ok != c.ok {
			t.Errorf("javaPackageSuffix(%q) = (%q, %v), want (%q, %v)", c.dir, got, ok, c.want, c.ok)
		}
	}
}

// TestChangeImpactScoped_FileDisambiguatesSameNamedTypes: BACKLOG addendum
// #5 (grove repo, 2026-09-02) — "Engine.Query" merged pkg/grove/grove.go's
// Engine with internal/embeddings/model2vec's Engine into one closure whose
// callers all belonged to the type the agent was NOT asking about; it
// distrusted the answer and re-derived it manually. file= scopes the seed
// types to a declaring-file fragment.
func TestChangeImpactScoped_FileDisambiguatesSameNamedTypes(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "pkg/g/g.go::Engine@sha", FilePath: "pkg/g/g.go", Language: "go", Kind: core.KindStruct,
			Name: "Engine", QualifiedName: "Engine", Signature: "type Engine struct"},
		{ID: "pkg/g/g.go::Engine.Query@sha", FilePath: "pkg/g/g.go", Language: "go", Kind: core.KindMethod,
			Name: "Query", QualifiedName: "Engine.Query", ParentSymbol: "Engine",
			Signature: "func (e *Engine) Query(q string) []string"},
		{ID: "internal/emb/model.go::Engine@sha", FilePath: "internal/emb/model.go", Language: "go", Kind: core.KindStruct,
			Name: "Engine", QualifiedName: "Engine", Signature: "type Engine struct"},
		{ID: "internal/emb/model.go::Engine.Query@sha", FilePath: "internal/emb/model.go", Language: "go", Kind: core.KindMethod,
			Name: "Query", QualifiedName: "Engine.Query", ParentSymbol: "Engine",
			Signature: "func (e *Engine) Query(v []float32) []int"},
	}, 3)

	// Unscoped: both same-named types seed (existing behavior, unchanged).
	r, err := g.ChangeImpact("Engine.Query")
	if err != nil {
		t.Fatalf("ChangeImpact: %v", err)
	}
	if len(r.Declarations) != 2 {
		t.Fatalf("unscoped declarations = %d, want 2 (both types)", len(r.Declarations))
	}

	// Scoped: only the matching file's type.
	r, err = g.ChangeImpactScoped("Engine.Query", "pkg/g")
	if err != nil {
		t.Fatalf("ChangeImpactScoped: %v", err)
	}
	if len(r.Declarations) != 1 || r.Declarations[0].FilePath != "pkg/g/g.go" {
		t.Fatalf("scoped declarations = %+v, want exactly pkg/g/g.go", r.Declarations)
	}

	// A scope matching nothing errors with guidance instead of silently
	// widening back out.
	if _, err := g.ChangeImpactScoped("Engine.Query", "no/such/dir"); err == nil {
		t.Fatal("scope matching no declaration must error, not widen")
	}
}
