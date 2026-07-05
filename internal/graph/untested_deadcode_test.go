package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// untestedFixture: interface Codec.encode with two implementors and two
// callers; a test covers JsonCodec.encode (directly) and Publisher.publish
// (whose closure includes encode's caller) — XmlCodec.encode and
// Archiver.archive have no covering test.
func untestedFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Codec.java::Codec@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindInterface,
			Name: "Codec", QualifiedName: "Codec", Signature: "public interface Codec"},
		{ID: "Codec.java::Codec.encode@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Codec.encode", ParentSymbol: "Codec",
			Signature: "byte[] encode(Object value)"},

		{ID: "JsonCodec.java::JsonCodec@sha", FilePath: "JsonCodec.java", Language: "java", Kind: core.KindClass,
			Name: "JsonCodec", QualifiedName: "JsonCodec", Signature: "public class JsonCodec implements Codec"},
		{ID: "JsonCodec.java::JsonCodec.encode@sha", FilePath: "JsonCodec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "JsonCodec.encode", ParentSymbol: "JsonCodec",
			Signature: "public byte[] encode(Object value)",
			RawText:   "public byte[] encode(Object value) { return null; }"},

		{ID: "XmlCodec.java::XmlCodec@sha", FilePath: "XmlCodec.java", Language: "java", Kind: core.KindClass,
			Name: "XmlCodec", QualifiedName: "XmlCodec", Signature: "public class XmlCodec implements Codec"},
		{ID: "XmlCodec.java::XmlCodec.encode@sha", FilePath: "XmlCodec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "XmlCodec.encode", ParentSymbol: "XmlCodec",
			Signature: "public byte[] encode(Object value)",
			RawText:   "public byte[] encode(Object value) { return new byte[0]; }"},

		{ID: "Publisher.java::Publisher@sha", FilePath: "Publisher.java", Language: "java", Kind: core.KindClass,
			Name: "Publisher", QualifiedName: "Publisher", Signature: "public class Publisher"},
		{ID: "Publisher.java::Publisher.publish@sha", FilePath: "Publisher.java", Language: "java", Kind: core.KindMethod,
			Name: "publish", QualifiedName: "Publisher.publish", ParentSymbol: "Publisher",
			Signature: "void publish(Codec c)",
			RawText:   "void publish(Codec c) { c.encode(payload); }",
			CallSites: []core.CallSite{{Callee: "c.encode", Line: 1, Argc: 1}},
			Imports:   []string{"Codec"}},

		{ID: "Archiver.java::Archiver@sha", FilePath: "Archiver.java", Language: "java", Kind: core.KindClass,
			Name: "Archiver", QualifiedName: "Archiver", Signature: "public class Archiver"},
		{ID: "Archiver.java::Archiver.archive@sha", FilePath: "Archiver.java", Language: "java", Kind: core.KindMethod,
			Name: "archive", QualifiedName: "Archiver.archive", ParentSymbol: "Archiver",
			Signature: "void archive(Codec c)",
			RawText:   "void archive(Codec c) { c.encode(payload); }",
			CallSites: []core.CallSite{{Callee: "c.encode", Line: 1, Argc: 1}},
			Imports:   []string{"Codec"}},

		{ID: "PublisherTest.java::PublisherTest@sha", FilePath: "src/test/PublisherTest.java", Language: "java", Kind: core.KindClass,
			Name: "PublisherTest", QualifiedName: "PublisherTest", Signature: "public class PublisherTest"},
		{ID: "PublisherTest.java::PublisherTest.testPublish@sha", FilePath: "src/test/PublisherTest.java", Language: "java", Kind: core.KindMethod,
			Name: "testPublish", QualifiedName: "PublisherTest.testPublish", ParentSymbol: "PublisherTest",
			Signature: "void testPublish()",
			RawText:   "void testPublish() { new Publisher().publish(new JsonCodec()); }",
			CallSites: []core.CallSite{{Callee: "publish", Line: 1, Argc: 1}},
			Imports:   []string{"Publisher", "JsonCodec"}},
	}, 3)
	return g
}

func TestUntestedSurfacePartition(t *testing.T) {
	g := untestedFixture()
	r, err := g.UntestedSurface("Codec.encode")
	if err != nil {
		t.Fatalf("UntestedSurface: %v", err)
	}
	if r.TotalSites == 0 || r.TotalSites != len(r.Untested)+len(r.Covered) {
		t.Fatalf("partition does not cover the change-set: total=%d untested=%d covered=%d",
			r.TotalSites, len(r.Untested), len(r.Covered))
	}
	un := names(r.Untested)
	var cov []string
	for _, c := range r.Covered {
		cov = append(cov, c.Symbol.QualifiedName)
		if c.TestCount == 0 || len(c.Tests) == 0 {
			t.Errorf("covered site %s has no test evidence", c.Symbol.QualifiedName)
		}
	}
	// Archiver.archive has no covering test anywhere in its closure.
	if !un["archive"] {
		t.Errorf("Untested = %v, want archive uncovered (covered: %v)", un, cov)
	}
	// Publisher.publish is exercised by testPublish directly.
	found := false
	for _, c := range r.Covered {
		if c.Symbol.Name == "publish" {
			found = true
		}
	}
	if !found {
		t.Errorf("publish should be covered via testPublish; covered=%v untested=%v", cov, un)
	}
}

func deadCodeFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		// main -> alive() ; orphan() unreached ; helperViaValue passed as
		// value (name in main's text, no call edge) ; ExportedIdle exported
		// and unreferenced ; deadCaller -> deadCallee cluster.
		{ID: "main.go::main@sha", FilePath: "main.go", Language: "go", Kind: core.KindFunction,
			Name: "main", QualifiedName: "pkg.main",
			RawText:   "func main() { alive(); register(helperViaValue) }",
			CallSites: []core.CallSite{{Callee: "alive", Line: 1, Argc: 0}}},
		{ID: "a.go::alive@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "alive", QualifiedName: "pkg.alive", RawText: "func alive() {}"},
		{ID: "a.go::orphan@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "orphan", QualifiedName: "pkg.orphan", RawText: "func orphan() {}"},
		{ID: "a.go::helperViaValue@sha", FilePath: "a.go", Language: "go", Kind: core.KindFunction,
			Name: "helperViaValue", QualifiedName: "pkg.helperViaValue", RawText: "func helperViaValue() {}"},
		{ID: "b.go::ExportedIdle@sha", FilePath: "b.go", Language: "go", Kind: core.KindFunction,
			Name: "ExportedIdle", QualifiedName: "pkg.ExportedIdle", Exports: true,
			RawText: "func ExportedIdle() {}"},
		{ID: "c.go::deadCaller@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCaller", QualifiedName: "pkg.deadCaller",
			RawText:   "func deadCaller() { deadCallee() }",
			CallSites: []core.CallSite{{Callee: "deadCallee", Line: 1, Argc: 0}}},
		{ID: "c.go::deadCallee@sha", FilePath: "c.go", Language: "go", Kind: core.KindFunction,
			Name: "deadCallee", QualifiedName: "pkg.deadCallee", RawText: "func deadCallee() {}"},
	}, 3)
	return g
}

func TestDeadCodeBuckets(t *testing.T) {
	g := deadCodeFixture()
	r := g.DeadCode(nil)
	dead := names(r.Dead)
	if !dead["orphan"] {
		t.Errorf("Dead = %v, want orphan", dead)
	}
	if dead["alive"] || dead["main"] {
		t.Errorf("Dead = %v wrongly contains live code", dead)
	}
	// Passed as a value: no call edge, but the name occurs in main's text.
	if dead["helperViaValue"] {
		t.Errorf("Dead wrongly contains helperViaValue (referenced by value)")
	}
	// Transitively-dead cluster: only the top (deadCaller) is reported;
	// deadCallee stays because deadCaller's text still mentions it.
	if !dead["deadCaller"] || dead["deadCallee"] {
		t.Errorf("Dead = %v, want deadCaller only from the dead cluster", dead)
	}
	if exp := names(r.ExportedUnreferenced); !exp["ExportedIdle"] {
		t.Errorf("ExportedUnreferenced = %v, want ExportedIdle", exp)
	}
	if len(r.Caveats) == 0 {
		t.Error("Caveats must always be present")
	}
	if r.Considered == 0 || r.RootCount == 0 {
		t.Errorf("counters: considered=%d roots=%d", r.Considered, r.RootCount)
	}
}
