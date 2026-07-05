package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// renamePlanFixture: interface Store { load(String) } with
//   - DiskStore implements Store, declares load          -> decl edit
//   - readAll calls DiskStore.load twice                 -> confirmed call edits
//   - syncBoth calls DiskStore.load AND Cache.load       -> ambiguous (same-named non-family callee)
func renamePlanFixture() *CodeGraph {
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{
		{ID: "Store.java::Store@sha", FilePath: "Store.java", Language: "java", Kind: core.KindInterface,
			Name: "Store", QualifiedName: "Store", Signature: "public interface Store",
			Span: core.LineRange{Start: 1, End: 4}},
		{ID: "Store.java::Store.load@sha", FilePath: "Store.java", Language: "java", Kind: core.KindMethod,
			Name: "load", QualifiedName: "Store.load", ParentSymbol: "Store",
			Signature: "byte[] load(String key)", RawText: "byte[] load(String key);",
			Span: core.LineRange{Start: 2, End: 2}},

		{ID: "DiskStore.java::DiskStore@sha", FilePath: "DiskStore.java", Language: "java", Kind: core.KindClass,
			Name: "DiskStore", QualifiedName: "DiskStore",
			Signature: "public class DiskStore implements Store",
			Span:      core.LineRange{Start: 1, End: 10}},
		{ID: "DiskStore.java::DiskStore.load@sha", FilePath: "DiskStore.java", Language: "java", Kind: core.KindMethod,
			Name: "load", QualifiedName: "DiskStore.load", ParentSymbol: "DiskStore",
			Signature: "public byte[] load(String key)",
			RawText:   "@Override\npublic byte[] load(String key) {\n    return super.load(key);\n}",
			Span:      core.LineRange{Start: 4, End: 7},
			CallSites: []core.CallSite{{Callee: "super.load", Line: 6, Argc: 1}}},

		{ID: "Cache.java::Cache@sha", FilePath: "Cache.java", Language: "java", Kind: core.KindClass,
			Name: "Cache", QualifiedName: "Cache", Signature: "public class Cache",
			Span: core.LineRange{Start: 1, End: 8}},
		{ID: "Cache.java::Cache.load@sha", FilePath: "Cache.java", Language: "java", Kind: core.KindMethod,
			Name: "load", QualifiedName: "Cache.load", ParentSymbol: "Cache",
			Signature: "public byte[] load(String key)",
			RawText:   "public byte[] load(String key) {\n    return mem.get(key);\n}",
			Span:      core.LineRange{Start: 3, End: 5}},

		{ID: "App.java::App.readAll@sha", FilePath: "App.java", Language: "java", Kind: core.KindFunction,
			Name: "readAll", QualifiedName: "App.readAll",
			RawText: "void readAll(DiskStore ds) {\n    byte[] a = ds.load(\"a\");\n    byte[] b = ds.load(\"b\");\n}",
			Span:    core.LineRange{Start: 10, End: 13},
			CallSites: []core.CallSite{
				{Callee: "ds.load", Line: 11, Argc: 1},
				{Callee: "ds.load", Line: 12, Argc: 1},
			}},
		{ID: "App.java::App.syncBoth@sha", FilePath: "App.java", Language: "java", Kind: core.KindFunction,
			Name: "syncBoth", QualifiedName: "App.syncBoth",
			RawText: "void syncBoth(DiskStore ds, Cache c) {\n    ds.load(\"x\");\n    c.load(\"x\");\n}",
			Span:    core.LineRange{Start: 20, End: 23},
			CallSites: []core.CallSite{
				{Callee: "ds.load", Line: 21, Argc: 1},
				{Callee: "c.load", Line: 22, Argc: 1},
			}},
	}, []core.Edge{
		{From: "App.java::App.readAll@sha", To: "DiskStore.java::DiskStore.load@sha", Type: core.EdgeCalls, Confidence: 1},
		{From: "App.java::App.syncBoth@sha", To: "DiskStore.java::DiskStore.load@sha", Type: core.EdgeCalls, Confidence: 1},
		{From: "App.java::App.syncBoth@sha", To: "Cache.java::Cache.load@sha", Type: core.EdgeCalls, Confidence: 1},
	}, 4)
	return g
}

func TestRenamePlan(t *testing.T) {
	g := renamePlanFixture()
	r, err := g.RenamePlan("Store.load", "fetch")
	if err != nil {
		t.Fatalf("RenamePlan: %v", err)
	}

	byLine := map[string]RenameEdit{}
	for _, e := range r.Edits {
		byLine[e.FilePath+":"+itoa(e.Line)] = e
	}

	// Declaration edits: interface decl line 2, override signature line 5
	// (span starts at 4 with @Override; the name is on the second line).
	if e, ok := byLine["Store.java:2"]; !ok || !strings.Contains(e.After, "fetch(String key)") {
		t.Errorf("missing/wrong interface decl edit: %+v", e)
	}
	if e, ok := byLine["DiskStore.java:5"]; !ok || !strings.Contains(e.After, "public byte[] fetch(String key)") {
		t.Errorf("missing/wrong override decl edit: %+v", e)
	}

	// Confirmed caller edits: readAll lines 11 and 12.
	for _, ln := range []string{"App.java:11", "App.java:12"} {
		if e, ok := byLine[ln]; !ok || !strings.Contains(e.After, "ds.fetch(") {
			t.Errorf("missing/wrong confirmed call edit at %s: %+v", ln, e)
		}
	}

	// The delegating override's own super.load(key) call (line 6) must be
	// edited too — applying a plan that renames the declaration but not the
	// delegation breaks the build.
	if e, ok := byLine["DiskStore.java:6"]; !ok || !strings.Contains(e.After, "super.fetch(key)") {
		t.Errorf("missing/wrong delegating-override call edit: %+v", e)
	}

	// syncBoth calls both DiskStore.load and Cache.load -> its lines are
	// ambiguous, and Cache.load (non-family) must NOT appear anywhere.
	if len(r.Ambiguous) != 2 {
		t.Fatalf("Ambiguous = %d edits, want 2 (syncBoth lines): %+v", len(r.Ambiguous), r.Ambiguous)
	}
	for _, e := range r.Edits {
		if e.SiteID == "App.java::App.syncBoth@sha" {
			t.Errorf("syncBoth line wrongly confirmed: %+v", e)
		}
		if e.FilePath == "Cache.java" {
			t.Errorf("non-family Cache.load wrongly edited: %+v", e)
		}
	}

	if r.SitesTotal == 0 || r.Completeness != "closed" {
		t.Errorf("SitesTotal=%d Completeness=%q", r.SitesTotal, r.Completeness)
	}

	// Guard: same-name new name rejected.
	if _, err := g.RenamePlan("Store.load", "load"); err == nil {
		t.Error("expected error for identical new name")
	}
}

// String-literal and multi-occurrence lines must never land in confirmed
// Edits — a blanket line rewrite cannot be attributed per-occurrence.
func TestRenamePlanOccurrenceAmbiguity(t *testing.T) {
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{
		{ID: "S.java::S@sha", FilePath: "S.java", Language: "java", Kind: core.KindInterface,
			Name: "S", QualifiedName: "S", Signature: "public interface S",
			Span: core.LineRange{Start: 1, End: 3}},
		{ID: "S.java::S.save@sha", FilePath: "S.java", Language: "java", Kind: core.KindMethod,
			Name: "save", QualifiedName: "S.save", ParentSymbol: "S",
			Signature: "void save()", RawText: "void save();",
			Span: core.LineRange{Start: 2, End: 2}},
		{ID: "A.java::A.log@sha", FilePath: "A.java", Language: "java", Kind: core.KindFunction,
			Name: "log", QualifiedName: "A.log",
			RawText: "void log(S s) {\n    audit(\"save()\"); s.save();\n}",
			Span:    core.LineRange{Start: 10, End: 12},
			CallSites: []core.CallSite{{Callee: "s.save", Line: 11, Argc: 0}}},
	}, []core.Edge{
		{From: "A.java::A.log@sha", To: "S.java::S.save@sha", Type: core.EdgeCalls, Confidence: 1},
	}, 2)
	r, err := g.RenamePlan("S.save", "persist")
	if err != nil {
		t.Fatalf("RenamePlan: %v", err)
	}
	for _, e := range r.Edits {
		if e.FilePath == "A.java" && e.Line == 11 {
			t.Errorf("string-contaminated line wrongly confirmed: %+v", e)
		}
	}
	found := false
	for _, e := range r.Ambiguous {
		if e.FilePath == "A.java" && e.Line == 11 {
			found = true
			if strings.Contains(e.After, `audit("persist()")`) {
				// the rewrite text may touch the literal — that is exactly
				// why the line is ambiguous, not confirmed
				_ = e
			}
		}
	}
	if !found {
		t.Error("contaminated line missing from Ambiguous")
	}
}
