package scipexport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/provasign/grove/internal/core"
)

type decodedOcc struct {
	rng, enc []int
	symbol   string
	roles    uint64
}

type decodedDoc struct {
	path, language string
	occs           []decodedOcc
	infos          map[string][]string // symbol -> implementation relationships
	kinds          map[string]uint64
}

func fields(t *testing.T, b []byte, visit func(num protowire.Number, typ protowire.Type, v []byte, n uint64)) {
	t.Helper()
	for len(b) > 0 {
		num, typ, l := protowire.ConsumeTag(b)
		if l < 0 {
			t.Fatalf("bad tag")
		}
		b = b[l:]
		switch typ {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				t.Fatalf("bad bytes")
			}
			visit(num, typ, v, 0)
			b = b[m:]
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				t.Fatalf("bad varint")
			}
			visit(num, typ, nil, v)
			b = b[m:]
		default:
			t.Fatalf("unexpected wire type %v", typ)
		}
	}
}

func decode(t *testing.T, raw []byte) (tool string, docs []decodedDoc) {
	fields(t, raw, func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
		switch num {
		case indexMetadata:
			fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
				if num == metaToolInfo {
					fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
						if num == toolName {
							tool = string(v)
						}
					})
				}
			})
		case indexDocuments:
			d := decodedDoc{infos: map[string][]string{}, kinds: map[string]uint64{}}
			fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
				switch num {
				case docPath:
					d.path = string(v)
				case docLanguage:
					d.language = string(v)
				case docOccurrences:
					var o decodedOcc
					fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, n uint64) {
						switch num {
						case occRange:
							for len(v) > 0 {
								x, m := protowire.ConsumeVarint(v)
								o.rng = append(o.rng, int(x))
								v = v[m:]
							}
						case occEnclosingRange:
							for len(v) > 0 {
								x, m := protowire.ConsumeVarint(v)
								o.enc = append(o.enc, int(x))
								v = v[m:]
							}
						case occSymbol:
							o.symbol = string(v)
						case occRoles:
							o.roles = n
						}
					})
					d.occs = append(d.occs, o)
				case docSymbols:
					var sym string
					var rels []string
					var kind uint64
					fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, n uint64) {
						switch num {
						case infoSymbol:
							sym = string(v)
						case infoKind:
							kind = n
						case infoRelations:
							fields(t, v, func(num protowire.Number, _ protowire.Type, v []byte, _ uint64) {
								if num == relSymbol {
									rels = append(rels, string(v))
								}
							})
						}
					})
					d.infos[sym] = rels
					d.kinds[sym] = kind
				}
			})
			docs = append(docs, d)
		}
	})
	return tool, docs
}

// A Go interface, an implementation and a caller: the export has one
// document per file, a definition at each name, a reference at the resolved
// call position, a type reference, and the implementation relationship.
func TestExportSCIP(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"shape.go": "package app\n\ntype Shape interface {\n\tArea() int\n}\n",
		"square.go": "package app\n\ntype Square struct{ n int }\n\nfunc (s Square) Area() int { return s.n * s.n }\n\n" +
			"func total(s Shape) int { return s.Area() + helper() }\n\nfunc helper() int { return 1 }\n",
	}
	for p, body := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sym := func(file, name, qual, parent string, kind core.SymbolKind, start, end int, calls ...core.CallSite) core.SymbolRecord {
		return core.SymbolRecord{ID: file + "::" + qual, FilePath: file, Language: "go", Kind: kind, Name: name,
			QualifiedName: qual, ParentSymbol: parent, Span: core.LineRange{Start: start, End: end}, CallSites: calls}
	}
	symbols := []core.SymbolRecord{
		sym("shape.go", "Shape", "Shape", "", core.KindInterface, 3, 5),
		sym("shape.go", "Area", "Shape.Area", "Shape", core.KindMethod, 4, 4),
		sym("square.go", "Square", "Square", "", core.KindStruct, 3, 3),
		sym("square.go", "Area", "Square.Area", "Square", core.KindMethod, 5, 5),
		sym("square.go", "total", "total", "", core.KindFunction, 7, 7, core.CallSite{Callee: "helper", Line: 7}),
		sym("square.go", "helper", "helper", "", core.KindFunction, 9, 9),
		{ID: "file:square.go", FilePath: "square.go", Kind: core.KindFile, Name: "square.go"},
	}
	edges := []core.Edge{
		{From: "square.go::total", To: "shape.go::Shape.Area", Type: core.EdgeCalls, Lines: "7:36"},
		{From: "square.go::total", To: "square.go::helper", Type: core.EdgeCalls},
		{From: "square.go::total", To: "shape.go::Shape", Type: core.EdgeUsesType},
		{From: "square.go::Square.Area", To: "shape.go::Shape.Area", Type: core.EdgeOverrides},
		{From: "square.go::Square", To: "shape.go::Shape", Type: core.EdgeImplements},
		{From: "file:square.go", To: "square.go::helper", Type: core.EdgeContains},
	}
	raw, err := Export(symbols, edges, Options{Root: root, ToolName: "prism", ToolVersion: "test"})
	if err != nil {
		t.Fatal(err)
	}
	tool, docs := decode(t, raw)
	if tool != "prism" || len(docs) != 2 || docs[0].path != "shape.go" || docs[1].path != "square.go" || docs[1].language != "Go" {
		t.Fatalf("tool %q docs %+v", tool, docs)
	}
	shapeArea := "prism . . . `shape.go`/Shape#Area()."
	shape := "prism . . . `shape.go`/Shape#"
	helper := "prism . . . `square.go`/helper()."
	find := func(d decodedDoc, symbol string, line int, roles uint64) []int {
		for _, o := range d.occs {
			if o.symbol == symbol && o.rng[0] == line && o.roles == roles {
				return o.rng
			}
		}
		return nil
	}
	if r := find(docs[0], shapeArea, 3, roleDefinition); r == nil || r[1] != 1 || r[2] != 5 {
		t.Errorf("Shape.Area definition = %v; occs %+v", r, docs[0].occs)
	}
	sq := docs[1]
	if r := find(sq, shapeArea, 6, 0); r == nil || r[1] != 35 {
		t.Errorf("resolved call reference = %v; occs %+v", r, sq.occs)
	}
	if r := find(sq, helper, 6, 0); r == nil || r[1] != 44 {
		t.Errorf("call-site reference to helper = %v; occs %+v", r, sq.occs)
	}
	if r := find(sq, shape, 6, 0); r == nil || r[1] != 13 {
		t.Errorf("type reference = %v; occs %+v", r, sq.occs)
	}
	if rels := sq.infos["prism . . . `square.go`/Square#Area()."]; len(rels) != 1 || rels[0] != shapeArea {
		t.Errorf("override relationship = %v", rels)
	}
	if rels := sq.infos["prism . . . `square.go`/Square#"]; len(rels) != 1 || rels[0] != shape {
		t.Errorf("implements relationship = %v", rels)
	}
	if sq.kinds[helper] != 17 || docs[0].kinds[shape] != 21 {
		t.Errorf("kinds = %v %v", sq.kinds, docs[0].kinds)
	}
	// Every definition's enclosing range contains the definition (scip.proto).
	// v0.64.7 wrote [line, 0, line, 0] for a one-line function: empty.
	expand := func(r []int) [4]int {
		if len(r) == 3 {
			return [4]int{r[0], r[1], r[0], r[2]}
		}
		return [4]int{r[0], r[1], r[2], r[3]}
	}
	before := func(l1, c1, l2, c2 int) bool { return l1 < l2 || (l1 == l2 && c1 <= c2) }
	defs := 0
	for _, d := range docs {
		for _, o := range d.occs {
			if o.roles&roleDefinition == 0 {
				continue
			}
			defs++
			if len(o.enc) != 3 && len(o.enc) != 4 {
				t.Errorf("%s: definition %s has no enclosing range", d.path, o.symbol)
				continue
			}
			e, r := expand(o.enc), expand(o.rng)
			if !before(e[0], e[1], r[0], r[1]) || !before(r[2], r[3], e[2], e[3]) || !before(e[0], e[1], e[2], e[3]) || (e[0] == e[2] && e[1] == e[3]) {
				t.Errorf("%s: enclosing %v does not contain %s at %v", d.path, o.enc, o.symbol, o.rng)
			}
		}
	}
	if defs != 6 {
		t.Errorf("definitions = %d, want 6", defs)
	}
	if e := expand(func() []int {
		for _, o := range docs[0].occs {
			if o.symbol == shape && o.roles&roleDefinition != 0 {
				return o.enc
			}
		}
		return []int{0, 0, 0}
	}()); e != [4]int{2, 0, 4, 1} {
		t.Errorf("Shape's enclosing range = %v, want the whole declaration [2 0 4 1]", e)
	}
	for _, d := range docs {
		for s := range d.infos {
			if strings.Contains(s, "file:") || strings.Contains(s, "<") {
				t.Errorf("synthetic symbol exported: %s", s)
			}
		}
	}
}

// Overloads (same qualified name, one file) get SCIP's method disambiguator in
// source order, and Go's blank identifier gets no symbol: the index had
// duplicate SymbolInformation for every Java overload set and every `var _ X`.
func TestExportSCIPOverloadsAndBlank(t *testing.T) {
	root := t.TempDir()
	src := "class M {\n  void read(int a) {}\n  void read(String s) {}\n  void read() {}\n}\n"
	if err := os.WriteFile(filepath.Join(root, "M.java"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.go"), []byte("package b\n\nvar _ = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := func(id string, line int) core.SymbolRecord {
		return core.SymbolRecord{ID: id, FilePath: "M.java", Language: "java", Kind: core.KindMethod, Name: "read",
			QualifiedName: "M.read", ParentSymbol: "M", Span: core.LineRange{Start: line, End: line}}
	}
	syms := []core.SymbolRecord{
		m("M.java::M.read#3", 3), m("M.java::M.read#2", 2), m("M.java::M.read#4", 4),
		{ID: "b.go::_", FilePath: "b.go", Language: "go", Kind: core.KindVariable, Name: "_", QualifiedName: "_", Span: core.LineRange{Start: 3, End: 3}},
	}
	raw, err := Export(syms, nil, Options{Root: root, ToolName: "prism"})
	if err != nil {
		t.Fatal(err)
	}
	_, docs := decode(t, raw)
	if len(docs) != 1 {
		t.Fatalf("docs = %+v (the blank identifier must not make a document)", docs)
	}
	for _, want := range []string{"M#read().", "M#read(+1).", "M#read(+2)."} {
		if _, ok := docs[0].infos["prism . . . `M.java`/"+want]; !ok {
			t.Errorf("missing %s in %v", want, docs[0].infos)
		}
	}
	for _, o := range docs[0].occs {
		if o.symbol == "prism . . . `M.java`/M#read()." && o.rng[0] != 1 {
			t.Errorf("read() is the first overload by line, got line %d", o.rng[0])
		}
	}
}
