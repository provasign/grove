package native

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"reflect"
	"sort"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestGoInterfaceDispatchMethodSets(t *testing.T) {
	const src = `package coverage
import "net/http"
import "io"
type Contract interface { http.CloseNotifier; io.Writer }
type Alias = Contract
type base struct{}
func (*base) CloseNotify() <-chan bool { return nil }
type promoted struct { *base; io.Writer }
type value struct { io.Writer }
func (value) CloseNotify() <-chan bool { return nil }
type wrongReturn struct { io.Writer }
func (*wrongReturn) CloseNotify() chan bool { return nil }
type wrongParam struct { io.Writer }
func (*wrongParam) CloseNotify(int) <-chan bool { return nil }
type wrongCase struct { io.Writer }
func (*wrongCase) Closenotify() <-chan bool { return nil }
type missingMethod struct{}
func (*missingMethod) CloseNotify() <-chan bool { return nil }
type shadow struct { *promoted }
func (*shadow) CloseNotify() string { return "" }
type left struct{}
func (*left) CloseNotify() <-chan bool { return nil }
type right struct{}
func (*right) CloseNotify() <-chan bool { return nil }
type ambiguous struct { *left; *right; io.Writer }
func Call(c Contract) { c.CloseNotify(); c.CloseNotify() }
func CallAlias(c Alias) { c.CloseNotify() }
func Expression(c Contract) { Contract.CloseNotify(c) }
func CallConcrete(c *promoted) { c.CloseNotify() }
`
	got := goDispatchFixture(t, src, false)
	want := map[string][]string{
		"Call":         {"base.CloseNotify", "value.CloseNotify", "base.CloseNotify", "value.CloseNotify"},
		"CallAlias":    {"base.CloseNotify", "value.CloseNotify"},
		"Expression":   {"base.CloseNotify", "value.CloseNotify"},
		"CallConcrete": nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("targets: got %v, want %v", got, want)
	}
}

func TestGoInterfaceDispatchRejectsUnknownSignatures(t *testing.T) {
	got := goDispatchFixture(t, `package coverage
import "missing.invalid/pkg"
type Contract interface { Send(pkg.Message) }
type writer struct{}
func (*writer) Send(pkg.Message) {}
func Call(c Contract) { c.Send(pkg.Message{}) }
`, true)
	if len(got["Call"]) != 0 {
		t.Fatalf("unknown parameter types created dispatch targets: %v", got)
	}
}

func TestGoInterfaceDispatchRejectsUnknownEmbeddedAndNamedTypes(t *testing.T) {
	for _, contract := range []string{
		"type Contract interface { pkg.Unknown; Send() }\ntype writer struct{}\nfunc (*writer) Send() {}",
		"type Message pkg.Unknown\ntype Contract interface { Send(Message) }\ntype writer struct{}\nfunc (*writer) Send(Message) {}",
	} {
		src := "package coverage\nimport \"missing.invalid/pkg\"\n" + contract + "\nfunc Call(c Contract) { c.Send() }"
		got := goDispatchFixture(t, src, true)
		if len(got["Call"]) != 0 {
			t.Errorf("unknown types created dispatch targets: %v", got)
		}
	}
}

func TestGoInterfaceDispatchSurvivesUnrelatedImportError(t *testing.T) {
	got := goDispatchFixture(t, `package coverage
import "missing.invalid/pkg"
var unused pkg.Unknown
type Contract interface { Send() }
type writer struct{}
func (*writer) Send() {}
func Call(c Contract) { c.Send() }
`, true)
	if !reflect.DeepEqual(got["Call"], []string{"writer.Send"}) {
		t.Fatalf("valid signature lost because of an unrelated error: %v", got)
	}
}

func TestGoInterfaceDispatchUsesExactSourceFile(t *testing.T) {
	got := goDispatchFixture(t, `package coverage
type Contract interface { Send() }
type writer struct{}
func (*writer) Send() {}
func Call(c Contract) { c.Send() }
`, false, core.SymbolRecord{ID: "wrong-file", Name: "Send", ParentSymbol: "writer",
		FilePath: "external_test.go", Kind: core.KindMethod, Language: "go"})
	if !reflect.DeepEqual(got["Call"], []string{"writer.Send"}) {
		t.Fatalf("native object bound to same-name method in another file: %v", got)
	}
}

func goDispatchFixture(t *testing.T, src string, wantTypeError bool, extra ...core.SymbolRecord) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	config := types.Config{Importer: importer.Default(), Error: func(error) {}}
	pkg, err := config.Check("coverage", fset, []*ast.File{file}, info)
	if (err != nil) != wantTypeError {
		t.Fatalf("type check: %v, want error %v", err, wantTypeError)
	}
	var symbols []core.SymbolRecord
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if typ, ok := spec.(*ast.TypeSpec); ok {
					symbols = append(symbols, core.SymbolRecord{ID: typ.Name.Name, Name: typ.Name.Name,
						FilePath: "fixture.go", Kind: core.KindType, Language: "go"})
				}
			}
		case *ast.FuncDecl:
			recv := goReceiverName(d)
			id := d.Name.Name
			if recv != "" {
				id = recv + "." + id
			}
			symbols = append(symbols, core.SymbolRecord{ID: id, Name: d.Name.Name,
				ParentSymbol: recv, FilePath: "fixture.go", Kind: core.KindMethod, Language: "go"})
		}
	}
	dispatch := newGoInterfaceDispatch(pkg, ".", ".", fset, newGoSymbolIndex(append(symbols, extra...)))
	got := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		got[fn.Name.Name] = nil
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if call, ok := node.(*ast.CallExpr); ok {
				var targets []string
				for _, target := range dispatch.targets(call.Fun, info) {
					targets = append(targets, target.ID)
				}
				sort.Strings(targets)
				got[fn.Name.Name] = append(got[fn.Name.Name], targets...)
			}
			return true
		})
	}
	return got
}
