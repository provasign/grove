package graph

import (
	"strconv"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// relFixture builds a graph from symbols and explicit edges.
type relFixture struct {
	syms  []core.SymbolRecord
	edges []core.Edge
}

func (f *relFixture) typ(file, name string, line int) string {
	id := file + "::" + name
	f.syms = append(f.syms, core.SymbolRecord{ID: id, FilePath: file, Language: "java", Kind: core.KindClass,
		Name: name, QualifiedName: name, Span: core.LineRange{Start: line, End: line + 200}})
	return id
}

func (f *relFixture) method(file, owner, name string, line int) string {
	id := file + "::" + owner + "." + name + "@" + strconv.Itoa(line)
	f.syms = append(f.syms, core.SymbolRecord{ID: id, FilePath: file, Language: "java", Kind: core.KindMethod,
		Name: name, QualifiedName: owner + "." + name, ParentSymbol: owner,
		Signature: "public void " + name + "(Object a)", Span: core.LineRange{Start: line, End: line + 5}})
	f.edges = append(f.edges, core.Edge{From: file + "::" + owner, To: id, Type: core.EdgeContains, Confidence: 1})
	return id
}

func (f *relFixture) call(from, to string) {
	f.edges = append(f.edges, core.Edge{From: from, To: to, Type: core.EdgeCalls, Confidence: 1})
}

func (f *relFixture) extends(sub, super string) {
	f.edges = append(f.edges, core.Edge{From: sub, To: super, Type: core.EdgeExtends, Confidence: 1})
}

func (f *relFixture) graph() *CodeGraph {
	g := New()
	g.ReplaceWithStoredEdges(f.syms, f.edges, 1)
	return g
}

func relatedByName(sites []RelatedSite) map[string]RelatedSite {
	out := map[string]RelatedSite{}
	for _, s := range sites {
		out[s.Symbol.QualifiedName] = s
	}
	return out
}

// jackson-databind pr6061 shape: writeBinary -> writePOJO -> ctx.writePOJO;
// writeEmbeddedObject and writeTree also call ctx.writePOJO. change_impact
// on writeBinary must name them as related, never as change-set sites.
func TestRelatedSitesCoCallersOfSameFileEffect(t *testing.T) {
	f := &relFixture{}
	const file = "TreeBuildingGenerator.java"
	f.typ(file, "TreeBuildingGenerator", 1)
	f.typ(file, "TreeWriteContext", 600)
	writeBinary := f.method(file, "TreeBuildingGenerator", "writeBinary", 495)
	writePOJO := f.method(file, "TreeBuildingGenerator", "writePOJO", 456)
	writeTree := f.method(file, "TreeBuildingGenerator", "writeTree", 475)
	embedded := f.method(file, "TreeBuildingGenerator", "writeEmbeddedObject", 541)
	ctxPOJO := f.method(file, "TreeWriteContext", "writePOJO", 610)
	f.typ("Serializer.java", "Serializer", 1)
	serialize := f.method("Serializer.java", "Serializer", "serialize", 10)
	f.call(writeBinary, writePOJO)
	f.call(writePOJO, ctxPOJO)
	f.call(writeTree, ctxPOJO)
	f.call(embedded, ctxPOJO)
	f.call(serialize, writeBinary)
	g := f.graph()

	r, err := g.ChangeImpact("TreeBuildingGenerator.writeBinary")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sites() {
		if s.Name == "writeEmbeddedObject" || s.Name == "writeTree" {
			t.Fatalf("related site leaked into the change set: %v", s.QualifiedName)
		}
	}
	rel := relatedByName(g.RelatedSites(r))
	for _, want := range []string{"TreeBuildingGenerator.writeEmbeddedObject", "TreeBuildingGenerator.writeTree"} {
		site, ok := rel[want]
		if !ok {
			t.Fatalf("related = %v, want %s", rel, want)
		}
		if site.Relation != "co-caller" || site.Via != "TreeWriteContext.writePOJO" ||
			!strings.Contains(site.Detail, "writeBinary via writePOJO:456") {
			t.Fatalf("%s = %+v", want, site)
		}
	}
	if _, ok := rel["Serializer.serialize"]; ok {
		t.Fatal("a caller of the target is in the change set, not related")
	}
}

// jackson-databind pr5977 shape: three createContextual overrides under
// StdContainerSerializer call its _hasDynamicTypingOverride helper;
// ObjectArraySerializer's override does not.
func containerFixture() (*CodeGraph, map[string]string) {
	f := &relFixture{}
	ids := map[string]string{}
	base := f.typ("StdContainerSerializer.java", "StdContainerSerializer", 1)
	helper := f.method("StdContainerSerializer.java", "StdContainerSerializer", "_hasDynamicTypingOverride", 154)
	shared := f.method("StdContainerSerializer.java", "StdContainerSerializer", "findContentSerializer", 170)
	ids["helper"] = helper
	for i, name := range []string{"AsArraySerializerBase", "MapSerializer", "MapEntrySerializer", "ObjectArraySerializer", "BooleanSerializer"} {
		file := name + ".java"
		sub := f.typ(file, name, 1)
		f.extends(sub, base)
		m := f.method(file, name, "createContextual", 100+i)
		ids[name] = m
		if name != "BooleanSerializer" {
			f.call(m, shared) // every container serializer resolves content
		}
		if name != "ObjectArraySerializer" && name != "BooleanSerializer" {
			f.call(m, helper)
		}
	}
	return f.graph(), ids
}

func TestRelatedSitesPeerLacksSharedHelper(t *testing.T) {
	g, _ := containerFixture()
	for _, query := range []string{"StdContainerSerializer._hasDynamicTypingOverride", "AsArraySerializerBase.createContextual"} {
		t.Run(query, func(t *testing.T) {
			r, err := g.ChangeImpact(query)
			if err != nil {
				t.Fatal(err)
			}
			sites := g.RelatedSites(r)
			if len(sites) == 0 || sites[0].Symbol.QualifiedName != "ObjectArraySerializer.createContextual" || sites[0].Relation != "peer-lacks" {
				t.Fatalf("related = %+v, want ObjectArraySerializer.createContextual ranked first as peer-lacks", sites)
			}
			if !strings.Contains(sites[0].Detail, "does not call _hasDynamicTypingOverride") {
				t.Fatalf("detail = %q", sites[0].Detail)
			}
			if len(sites) > maxRelatedSites {
				t.Fatalf("related group unbounded: %d", len(sites))
			}
		})
	}
}

func TestRelatedSitesTargetLacksSharedHelper(t *testing.T) {
	g, _ := containerFixture()
	r, err := g.ChangeImpact("ObjectArraySerializer.createContextual")
	if err != nil {
		t.Fatal(err)
	}
	sites := g.RelatedSites(r)
	if len(sites) == 0 || sites[0].Relation != "target-lacks" || sites[0].Via != "StdContainerSerializer._hasDynamicTypingOverride" {
		t.Fatalf("related = %+v, want target-lacks _hasDynamicTypingOverride", sites)
	}
	if !strings.Contains(sites[0].Detail, "3 of 5 createContextual methods under StdContainerSerializer") {
		t.Fatalf("detail = %q", sites[0].Detail)
	}
}

// A helper every peer calls is not a gap, and data/type results carry none.
func TestRelatedSitesQuietWhenNoGap(t *testing.T) {
	f := &relFixture{}
	base := f.typ("Base.java", "Base", 1)
	helper := f.method("Base.java", "Base", "helper", 10)
	for i, name := range []string{"A", "B", "C"} {
		sub := f.typ(name+".java", name, 1)
		f.extends(sub, base)
		f.call(f.method(name+".java", name, "run", 20+i), helper)
	}
	g := f.graph()
	r, err := g.ChangeImpact("A.run")
	if err != nil {
		t.Fatal(err)
	}
	if sites := g.RelatedSites(r); len(sites) != 0 {
		t.Fatalf("related = %+v, want none", sites)
	}
	if sites := g.RelatedSites(&ChangeImpactResult{Completeness: "type-level"}); sites != nil {
		t.Fatalf("type-level result grew related sites: %+v", sites)
	}
}
