package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func pySym(kind core.SymbolKind, file, name, parent, raw string, start, end int, attrs ...core.CallSite) core.SymbolRecord {
	q := name
	if parent != "" {
		q = parent + "." + name
	}
	return core.SymbolRecord{ID: file + "::" + q + "@b", FilePath: file, BlobSHA: "b", Language: "python", Kind: kind,
		Name: name, QualifiedName: q, ParentSymbol: parent, RawText: raw, Signature: raw,
		Span: core.LineRange{Start: start, End: end}, AttrSites: attrs}
}

// Python class-level behaviour without call syntax (flask oracle review
// 2026-10-16): a descriptor attribute's read runs __get__; a class
// attribute holding a class is constructed by calling it; a method alias
// (`tag = to_json`) is that subclass's override, and a subclass that
// overrides the template method never runs the base's self.x() dispatch.
func TestPythonDescriptorsAliasesAndClassAttributes(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		pySym(core.KindClass, "config.py", "ConfigAttribute", "", "class ConfigAttribute:\n    def __get__(self, obj, owner=None): ...", 1, 4),
		pySym(core.KindMethod, "config.py", "__get__", "ConfigAttribute", "def __get__(self, obj, owner=None): ...", 2, 2),
		pySym(core.KindMethod, "config.py", "__set__", "ConfigAttribute", "def __set__(self, obj, value): ...", 3, 3),
		pySym(core.KindClass, "app.py", "Provider", "", "class Provider:\n    def __init__(self, app): ...", 1, 2),
		pySym(core.KindMethod, "app.py", "__init__", "Provider", "def __init__(self, app): ...", 2, 2),
		pySym(core.KindClass, "app.py", "App", "", "class App:\n    testing = ConfigAttribute(\"TESTING\")\n    provider_class: type[Provider] = Provider\n    def run(self):\n        if self.testing:\n            self.json = self.provider_class(self)", 4, 10),
		pySym(core.KindField, "app.py", "testing", "App", "testing = ConfigAttribute(\"TESTING\")", 5, 5),
		pySym(core.KindField, "app.py", "provider_class", "App", "provider_class: type[Provider] = Provider", 6, 6),
		pySym(core.KindMethod, "app.py", "run", "App", "def run(self):\n        if self.testing:\n            self.json = self.provider_class(self)", 7, 9,
			core.CallSite{Callee: "self.testing", Line: 8}),
		pySym(core.KindClass, "tag.py", "JSONTag", "", "class JSONTag:", 1, 6),
		pySym(core.KindMethod, "tag.py", "tag", "JSONTag", "def tag(self, value):\n        return self.to_json(value)", 2, 3),
		pySym(core.KindMethod, "tag.py", "to_json", "JSONTag", "def to_json(self, value):\n        raise NotImplementedError", 4, 5),
		pySym(core.KindClass, "tag.py", "PassDict", "", "class PassDict(JSONTag):", 10, 15),
		pySym(core.KindMethod, "tag.py", "to_json", "PassDict", "def to_json(self, value):\n        return value", 11, 12),
		pySym(core.KindField, "tag.py", "tag", "PassDict", "tag = to_json", 14, 14),
		pySym(core.KindClass, "tag.py", "Serializer", "", "class Serializer:\n    def __init__(self):\n        self.order: list[JSONTag] = []", 20, 30),
		pySym(core.KindMethod, "tag.py", "tag", "Serializer", "def tag(self, value):\n        for t in self.order:\n            return t.tag(value)", 24, 26),
	}
	syms[8].CallSites = []core.CallSite{{Callee: "self.provider_class", Line: 9, Argc: 1, Args: []string{"self"}}}
	syms[10].CallSites = []core.CallSite{{Callee: "self.to_json", Line: 3, Argc: 1, Args: []string{"value"}}}
	syms[16].CallSites = []core.CallSite{{Callee: "t.tag", Line: 26, Argc: 1, Args: []string{"value"}}}
	for i := range syms {
		syms[i].Imports = []string{"config.py", "app.py", "tag.py"}
	}
	g.Replace(syms, 2)
	if !hasEdge(g, core.EdgeCalls, "app.py::App.run@b", "config.py::ConfigAttribute.__get__@b") {
		t.Errorf("self.testing (a ConfigAttribute descriptor) must reach __get__")
	}
	if !hasEdge(g, core.EdgeCalls, "app.py::App.run@b", "app.py::Provider.__init__@b") {
		t.Errorf("self.provider_class(self) must construct the held class")
	}
	if !hasEdge(g, core.EdgeCalls, "tag.py::Serializer.tag@b", "tag.py::PassDict.to_json@b") {
		t.Errorf("t.tag(value) on a JSONTag element must reach PassDict's alias tag = to_json")
	}
	if hasEdge(g, core.EdgeCalls, "tag.py::JSONTag.tag@b", "tag.py::PassDict.to_json@b") {
		t.Errorf("PassDict overrides tag: JSONTag.tag's self.to_json never runs on it")
	}
}
