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
	rng    []int
	symbol string
	roles  uint64
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
	for _, d := range docs {
		for s := range d.infos {
			if strings.Contains(s, "file:") || strings.Contains(s, "<") {
				t.Errorf("synthetic symbol exported: %s", s)
			}
		}
	}
}
