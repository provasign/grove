package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Swift receivers the parser's last-segment callee hides (Files oracle
// review 2026-10-16): a static-property chain, a local typed by a throwing
// call's declared result, and Sequence's inferred Iterator.
func TestSwiftChainsCallResultsAndIterator(t *testing.T) {
	g := New()
	sw := func(kind core.SymbolKind, name, parent, sig, raw string, start, end int, sites ...core.CallSite) core.SymbolRecord {
		q := name
		if parent != "" {
			q = parent + "." + name
		}
		return core.SymbolRecord{ID: "F.swift::" + q + "@b", FilePath: "F.swift", BlobSHA: "b", Language: "swift", Kind: kind,
			Name: name, QualifiedName: q, ParentSymbol: parent, Signature: sig, RawText: raw,
			Span: core.LineRange{Start: start, End: end}, CallSites: sites}
	}
	syms := []core.SymbolRecord{
		sw(core.KindClass, "Folder", "", "final class Folder", "final class Folder {}", 1, 30),
		sw(core.KindField, "home", "Folder", "static var home: Folder {", "static var home: Folder { Folder() }", 2, 2),
		sw(core.KindMethod, "createSubfolder", "Folder", "func createSubfolder(named name: String) throws -> Folder", "func createSubfolder(named name: String) throws -> Folder { self }", 3, 3),
		sw(core.KindMethod, "createFile", "Folder", "func createFile(named name: String) throws -> File", "func createFile(named name: String) throws -> File { File() }", 4, 4),
		sw(core.KindClass, "File", "", "final class File", "final class File {}", 40, 50),
		sw(core.KindMethod, "delete", "File", "func delete() throws", "func delete() throws {}", 41, 41),
		sw(core.KindClass, "Other", "", "final class Other", "final class Other {}", 60, 70),
		sw(core.KindMethod, "delete", "Other", "func delete() throws", "func delete() throws {}", 61, 61),
		sw(core.KindMethod, "createSubfolder", "Other", "func createSubfolder(named name: String) throws -> Other", "func createSubfolder(named name: String) throws -> Other { self }", 62, 62),
		sw(core.KindStruct, "ChildSequence", "", "struct ChildSequence", "struct ChildSequence {}", 80, 99),
		sw(core.KindMethod, "makeIterator", "ChildSequence", "func makeIterator() -> ChildIterator<Child>", "func makeIterator() -> ChildIterator<Child> { ChildIterator() }", 81, 81),
		sw(core.KindStruct, "ChildIterator", "", "struct ChildIterator<Child: Location>: IteratorProtocol", "struct ChildIterator<Child: Location>: IteratorProtocol {}", 100, 120),
		sw(core.KindConstructor, "ChildIterator", "ChildIterator", "init(folder: Folder)", "init(folder: Folder) {}", 101, 101),
		sw(core.KindMethod, "next", "ChildIterator", "mutating func next() -> Child?", "mutating func next() -> Child? { nil }", 102, 102),
		sw(core.KindMethod, "last", "ChildSequence", "func last() -> Child?", "func last() -> Child? {\n    var iterator = Iterator(folder: folder)\n    return iterator.next()\n}", 83, 86,
			core.CallSite{Callee: "Iterator", Line: 84, Col: 20, Argc: 1, Args: []string{"folder:folder"}},
			core.CallSite{Callee: "iterator.next", Line: 85, Col: 21}),
		sw(core.KindMethod, "setUp", "Tests", "func setUp()", "func setUp() {\n    folder = try! Folder.home.createSubfolder(named: \".x\")\n    performTest {\n        let file = try folder.createFile(named: \"f\")\n        try file.delete()\n    }\n}", 200, 206,
			core.CallSite{Callee: "home.createSubfolder", Line: 201, Col: 31, Argc: 1, Args: []string{"named:"}},
			core.CallSite{Callee: "performTest", Line: 202, Col: 5},
			core.CallSite{Callee: "folder.createFile", Line: 203, Col: 31, Argc: 1, Args: []string{"named:"}},
			core.CallSite{Callee: "file.delete", Line: 204, Col: 18}),
		sw(core.KindClass, "Tests", "", "final class Tests", "final class Tests {\n    var folder: Folder!\n}", 190, 210),
		sw(core.KindField, "folder", "Tests", "var folder: Folder!", "var folder: Folder!", 191, 191),
	}
	g.Replace(syms, 2)
	for _, want := range [][2]string{
		{"F.swift::Tests.setUp@b", "F.swift::Folder.createSubfolder@b"},
		{"F.swift::Tests.setUp@b", "F.swift::File.delete@b"},
		{"F.swift::ChildSequence.last@b", "F.swift::ChildIterator.ChildIterator@b"},
		{"F.swift::ChildSequence.last@b", "F.swift::ChildIterator.next@b"},
	} {
		if !hasEdge(g, core.EdgeCalls, want[0], want[1]) {
			t.Errorf("missing %s -> %s", want[0], want[1])
		}
	}
	for _, wrong := range []string{"F.swift::Other.delete@b", "F.swift::Other.createSubfolder@b"} {
		if hasEdge(g, core.EdgeCalls, "F.swift::Tests.setUp@b", wrong) {
			t.Errorf("unrelated %s bound", wrong)
		}
	}
}
