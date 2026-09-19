package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Rules the second corpus per language surfaced (2026-09-19): each fixture
// is the minimal shape of a miss the first corpus never exercised.

// A Java constructor without an explicit super()/this() call invokes the
// superclass's parameterless constructor (javac emits it; commons-io had 89
// such misses). Nested helper types reuse simple names across a package,
// so the base is resolved from the constructor's own file first.
func TestJavaImplicitSuperConstructor(t *testing.T) {
	j := func(id, file string, kind core.SymbolKind, name, qualified, parent, sig string, sites ...core.CallSite) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "sha", Language: "java", Kind: kind,
			Name: name, QualifiedName: qualified, ParentSymbol: parent, Signature: sig, CallSites: sites}
	}
	g := New()
	syms := []core.SymbolRecord{
		j("a/AbstractFileFilter.java::AbstractFileFilter@sha", "a/AbstractFileFilter.java", core.KindClass, "AbstractFileFilter", "AbstractFileFilter", "", "public abstract class AbstractFileFilter implements IOFileFilter"),
		j("a/AbstractFileFilter.java::AbstractFileFilter.AbstractFileFilter@sha", "a/AbstractFileFilter.java", core.KindConstructor, "AbstractFileFilter", "AbstractFileFilter.AbstractFileFilter", "AbstractFileFilter", "public AbstractFileFilter()"),
		j("a/AgeFileFilter.java::AgeFileFilter@sha", "a/AgeFileFilter.java", core.KindClass, "AgeFileFilter", "AgeFileFilter", "", "public class AgeFileFilter extends AbstractFileFilter implements Serializable"),
		j("a/AgeFileFilter.java::AgeFileFilter.AgeFileFilter@sha", "a/AgeFileFilter.java", core.KindConstructor, "AgeFileFilter", "AgeFileFilter.AgeFileFilter", "AgeFileFilter", "public AgeFileFilter(Instant cutoff, boolean acceptOlder)"),
		// Two nested AbstractBuilder classes in sibling files; each
		// Builder extends its own file's.
		j("c/FilterChannel.java::FilterChannel.AbstractBuilder@sha", "c/FilterChannel.java", core.KindClass, "AbstractBuilder", "FilterChannel.AbstractBuilder", "FilterChannel", "public abstract static class AbstractBuilder<F, C, B>"),
		j("c/FilterChannel.java::FilterChannel.AbstractBuilder.AbstractBuilder@sha", "c/FilterChannel.java", core.KindConstructor, "AbstractBuilder", "FilterChannel.AbstractBuilder.AbstractBuilder", "AbstractBuilder", "protected AbstractBuilder()"),
		j("c/FilterByteChannel.java::FilterByteChannel.AbstractBuilder@sha", "c/FilterByteChannel.java", core.KindClass, "AbstractBuilder", "FilterByteChannel.AbstractBuilder", "FilterByteChannel", "public abstract static class AbstractBuilder<F, C, B>"),
		j("c/FilterByteChannel.java::FilterByteChannel.AbstractBuilder.AbstractBuilder@sha", "c/FilterByteChannel.java", core.KindConstructor, "AbstractBuilder", "FilterByteChannel.AbstractBuilder.AbstractBuilder", "AbstractBuilder", "protected AbstractBuilder()"),
		j("c/FilterByteChannel.java::FilterByteChannel.Builder@sha", "c/FilterByteChannel.java", core.KindClass, "Builder", "FilterByteChannel.Builder", "FilterByteChannel", "public static class Builder extends AbstractBuilder<FilterByteChannel<ByteChannel>, ByteChannel, Builder>"),
		j("c/FilterByteChannel.java::FilterByteChannel.Builder.Builder@sha", "c/FilterByteChannel.java", core.KindConstructor, "Builder", "FilterByteChannel.Builder.Builder", "Builder", "private Builder()"),
	}
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeCalls, "a/AgeFileFilter.java::AgeFileFilter.AgeFileFilter@sha", "a/AbstractFileFilter.java::AbstractFileFilter.AbstractFileFilter@sha") {
		t.Fatalf("a constructor without super() must call the superclass's parameterless constructor")
	}
	if !hasEdge(g, core.EdgeCalls, "c/FilterByteChannel.java::FilterByteChannel.Builder.Builder@sha", "c/FilterByteChannel.java::FilterByteChannel.AbstractBuilder.AbstractBuilder@sha") {
		t.Fatalf("a nested Builder must resolve its base AbstractBuilder in its own file")
	}
	if hasEdge(g, core.EdgeCalls, "c/FilterByteChannel.java::FilterByteChannel.Builder.Builder@sha", "c/FilterChannel.java::FilterChannel.AbstractBuilder.AbstractBuilder@sha") {
		t.Fatalf("a sibling file's same-named AbstractBuilder must not be the base")
	}
}

// Cargo integration tests are crates of their own: tests/tests.rs sees
// tests/testenv/mod.rs (fd had 93 such misses with a file-only scope).
func TestRustIntegrationTestsAreACrate(t *testing.T) {
	r := func(id, file string, kind core.SymbolKind, name, parent, sig, raw string, sites ...core.CallSite) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "sha", Language: "rust", Kind: kind,
			Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig, RawText: raw, CallSites: sites}
	}
	g := New()
	syms := []core.SymbolRecord{
		r("src/main.rs::main@sha", "src/main.rs", core.KindFunction, "main", "", "fn main()", ""),
		r("tests/testenv/mod.rs::TestEnv@sha", "tests/testenv/mod.rs", core.KindStruct, "TestEnv", "", "pub struct TestEnv", ""),
		r("tests/testenv/mod.rs::TestEnv.new@sha", "tests/testenv/mod.rs", core.KindConstructor, "new", "TestEnv", "pub fn new(dirs: &[&str], files: &[&str]) -> TestEnv", ""),
		r("tests/tests.rs::test_and_basic@sha", "tests/tests.rs", core.KindFunction, "test_and_basic", "", "fn test_and_basic()",
			"fn test_and_basic() { let env = TestEnv::new(DEFAULT_DIRS, DEFAULT_FILES); }",
			core.CallSite{Callee: "TestEnv.new", Line: 1, Argc: 2}),
	}
	g.Replace(syms, 3)
	if !hasEdge(g, core.EdgeCalls, "tests/tests.rs::test_and_basic@sha", "tests/testenv/mod.rs::TestEnv.new@sha") {
		t.Fatalf("tests/tests.rs must reach tests/testenv/mod.rs as one crate")
	}
}

// Swift: a typealias constructs the aliased type; a property declared on
// the enclosing type (or as a protocol requirement) types the receiver,
// generic arguments dropped; an in-repo extension of an external type is
// reachable from a receiver of that type (Files.swift: R 0.53 → 0.92).
func TestSwiftTypealiasPropertyAndExtension(t *testing.T) {
	s := func(id, file string, kind core.SymbolKind, name, parent, sig, raw string, sites ...core.CallSite) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: file, BlobSHA: "sha", Language: "swift", Kind: kind,
			Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig, RawText: raw, CallSites: sites}
	}
	g := New()
	syms := []core.SymbolRecord{
		s("F.swift::FilesError@sha", "F.swift", core.KindStruct, "FilesError", "", "public struct FilesError<Reason>: Error", ""),
		s("F.swift::FilesError.FilesError@sha", "F.swift", core.KindConstructor, "FilesError", "FilesError", "public init(path: String, reason: Reason)", ""),
		s("F.swift::LocationError@sha", "F.swift", core.KindType, "LocationError", "", "public typealias LocationError = FilesError<LocationErrorReason>", ""),
		s("F.swift::Storage@sha", "F.swift", core.KindStruct, "Storage", "", "struct Storage<Location: LocationKind>", ""),
		s("F.swift::Storage.move@sha", "F.swift", core.KindMethod, "move", "Storage", "func move(to newPath: String)", ""),
		s("F.swift::Location@sha", "F.swift", core.KindInterface, "Location", "", "public protocol Location", ""),
		s("F.swift::Location.storage@sha", "F.swift", core.KindField, "storage", "Location", "var storage: Storage<Self> { get }", ""),
		s("F.swift::String.appendingSuffixIfNeeded@sha", "F.swift", core.KindMethod, "appendingSuffixIfNeeded", "String", "func appendingSuffixIfNeeded(_ suffix: String) -> String", ""),
		s("F.swift::Location.rename@sha", "F.swift", core.KindMethod, "rename", "Location", "func rename(to newName: String) throws",
			"func rename(to newName: String) throws { let p = path.appendingSuffixIfNeeded(\"/\"); try storage.move(to: p); throw LocationError(path: path, reason: .cannotRenameRoot) }",
			core.CallSite{Callee: "path.appendingSuffixIfNeeded", Line: 1, Argc: 1, Args: []string{"_:#String"}},
			core.CallSite{Callee: "storage.move", Line: 1, Argc: 1, Args: []string{"to:p"}},
			core.CallSite{Callee: "LocationError", Line: 1, Argc: 2, Args: []string{"path:path", "reason:"}}),
		s("F.swift::Location.path@sha", "F.swift", core.KindField, "path", "Location", "var path: String { get }", ""),
	}
	g.Replace(syms, 1)
	for _, want := range []string{"F.swift::FilesError.FilesError@sha", "F.swift::Storage.move@sha", "F.swift::String.appendingSuffixIfNeeded@sha"} {
		if !hasEdge(g, core.EdgeCalls, "F.swift::Location.rename@sha", want) {
			t.Fatalf("rename must reach %s", want)
		}
	}
}
