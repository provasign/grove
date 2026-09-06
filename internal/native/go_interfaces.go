package native

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/provasign/grove/internal/core"
)

type goDispatchCandidate struct {
	typ     types.Type
	methods *types.MethodSet
}

// The native pass can prove dispatch to local implementations even when the
// interface embeds external methods that have no indexed declaration. This is
// a possible-target set, not a claim about the receiver's runtime value.
type goInterfaceDispatch struct {
	pkg        *types.Package
	root       string
	dir        string
	fset       *token.FileSet
	index      goSymbolIndex
	candidates []goDispatchCandidate
	cache      map[*types.Interface]map[string][]core.SymbolRecord
}

func newGoInterfaceDispatch(pkg *types.Package, root, dir string, fset *token.FileSet, index goSymbolIndex) *goInterfaceDispatch {
	d := &goInterfaceDispatch{pkg: pkg, root: root, dir: dir, fset: fset, index: index, cache: map[*types.Interface]map[string][]core.SymbolRecord{}}
	if pkg == nil {
		return d
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || obj.IsAlias() {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok || named.TypeParams().Len() > 0 {
			continue
		}
		if _, ok := named.Underlying().(*types.Interface); ok {
			continue
		}
		if _, ok := index.byType[dir+"\x00"+name]; !ok {
			continue
		}
		// *T includes value- and pointer-receiver methods and correctly handles
		// promotion, shadowing, and ambiguous embedded selectors.
		ptr := types.NewPointer(named)
		d.candidates = append(d.candidates, goDispatchCandidate{ptr, types.NewMethodSet(ptr)})
	}
	return d
}

func (d *goInterfaceDispatch) targets(expr ast.Expr, info *types.Info) []core.SymbolRecord {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return nil
	}
	selection := info.Selections[selector]
	if selection == nil {
		return nil
	}
	iface, ok := selection.Recv().Underlying().(*types.Interface)
	if !ok || !iface.IsMethodSet() {
		return nil
	}
	methods, cached := d.cache[iface]
	if !cached {
		methods = d.implementations(iface)
		d.cache[iface] = methods
	}
	return methods[selection.Obj().Id()]
}

func (d *goInterfaceDispatch) implementations(iface *types.Interface) map[string][]core.SymbolRecord {
	result := map[string][]core.SymbolRecord{}
	if !goDispatchTypeValid(iface, map[types.Type]bool{}) {
		return result
	}
	seen := map[string]bool{}
	for _, candidate := range d.candidates {
		if !types.Implements(candidate.typ, iface) {
			continue
		}
		for i := 0; i < iface.NumMethods(); i++ {
			method := iface.Method(i)
			selection := candidate.methods.Lookup(method.Pkg(), method.Name())
			if selection == nil {
				continue
			}
			fn, ok := selection.Obj().(*types.Func)
			if !ok || fn.Pkg() != d.pkg {
				continue // no guessing a local symbol for an external declaration
			}
			file, ok := relFile(d.root, d.fset.PositionFor(fn.Pos(), false).Filename)
			if !ok {
				continue
			}
			symbol, ok := d.index.byFileFunc[file+"\x00"+goCallableKey(d.dir, goFuncReceiverName(fn), fn.Name())]
			if !ok || seen[symbol.ID] {
				continue
			}
			seen[symbol.ID] = true
			result[method.Id()] = append(result[method.Id()], symbol)
		}
	}
	return result
}

// A best-effort type check can retain signatures containing Invalid after an
// import error. Two unknown types must not become evidence of compatibility.
func goDispatchTypeValid(typ types.Type, seen map[types.Type]bool) bool {
	if typ == nil {
		return false
	}
	if seen[typ] {
		return true
	}
	seen[typ] = true
	valid := func(t types.Type) bool { return goDispatchTypeValid(t, seen) }
	switch t := typ.(type) {
	case *types.Basic:
		return t.Kind() != types.Invalid
	case *types.Pointer:
		return valid(t.Elem())
	case *types.Slice:
		return valid(t.Elem())
	case *types.Array:
		return valid(t.Elem())
	case *types.Chan:
		return valid(t.Elem())
	case *types.Map:
		return valid(t.Key()) && valid(t.Elem())
	case *types.Alias:
		return valid(types.Unalias(t))
	case *types.Named:
		for i := 0; i < t.TypeArgs().Len(); i++ {
			if !valid(t.TypeArgs().At(i)) {
				return false
			}
		}
		return valid(t.Underlying())
	case *types.Signature:
		return valid(t.Params()) && valid(t.Results())
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if !valid(t.At(i).Type()) {
				return false
			}
		}
		return true
	case *types.Interface:
		if !t.IsMethodSet() {
			return false
		}
		for i := 0; i < t.NumEmbeddeds(); i++ {
			if !valid(t.EmbeddedType(i)) {
				return false
			}
		}
		for i := 0; i < t.NumMethods(); i++ {
			if !valid(t.Method(i).Type()) {
				return false
			}
		}
		return true
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			if !valid(t.Field(i).Type()) {
				return false
			}
		}
		return true
	default:
		return false
	}
}
