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
	symbol  core.SymbolRecord
}

type goDispatchInterface struct {
	typ    *types.Interface
	symbol core.SymbolRecord
}

// The native pass can prove dispatch to local implementations even when the
// interface embeds external methods that have no indexed declaration. This is
// a possible-target set, not a claim about the receiver's runtime value.
type goInterfaceDispatch struct {
	pkg             *types.Package
	root            string
	dir             string
	fset            *token.FileSet
	index           goSymbolIndex
	candidates      []goDispatchCandidate
	interfaces      []goDispatchInterface
	interfaceByType map[*types.Interface]core.SymbolRecord
	cache           map[*types.Interface]map[string][]core.SymbolRecord
}

func newGoInterfaceDispatch(pkg *types.Package, root, dir string, fset *token.FileSet, index goSymbolIndex, pkgDirsByImport map[string][]string) *goInterfaceDispatch {
	d := &goInterfaceDispatch{pkg: pkg, root: root, dir: dir, fset: fset, index: index,
		interfaceByType: map[*types.Interface]core.SymbolRecord{}, cache: map[*types.Interface]map[string][]core.SymbolRecord{}}
	if pkg == nil {
		return d
	}
	d.addInterfaces(pkg, dir)
	for _, imported := range pkg.Imports() {
		for _, importedDir := range pkgDirsByImport[imported.Path()] {
			d.addInterfaces(imported, importedDir)
		}
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
		symbol, ok := index.byType[dir+"\x00"+name]
		if !ok {
			continue
		}
		// *T includes value- and pointer-receiver methods and correctly handles
		// promotion, shadowing, and ambiguous embedded selectors.
		ptr := types.NewPointer(named)
		d.candidates = append(d.candidates, goDispatchCandidate{ptr, types.NewMethodSet(ptr), symbol})
	}
	return d
}

func (d *goInterfaceDispatch) addInterfaces(pkg *types.Package, dir string) {
	if pkg == nil {
		return
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
		iface, ok := named.Underlying().(*types.Interface)
		if !ok {
			continue
		}
		if symbol, found := d.index.byType[dir+"\x00"+name]; found {
			d.interfaces = append(d.interfaces, goDispatchInterface{iface, symbol})
			d.interfaceByType[iface] = symbol
		}
	}
}

func (d *goInterfaceDispatch) interfaceAnchor(expr ast.Expr, info *types.Info) (string, bool) {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	selection := info.Selections[selector]
	if selection == nil {
		return "", false
	}
	iface, ok := selection.Recv().Underlying().(*types.Interface)
	if !ok || !iface.IsMethodSet() || !goDispatchTypeValid(iface, map[types.Type]bool{}) {
		return "", false
	}
	symbol, ok := d.interfaceByType[iface]
	if !ok {
		return "", false
	}
	return symbol.ID + "#" + selection.Obj().Name(), true
}

func (d *goInterfaceDispatch) contractEdges() []core.Edge {
	var edges []core.Edge
	seen := map[string]bool{}
	add := func(from, to string, edgeType core.EdgeType) {
		key := from + "\x00" + string(edgeType) + "\x00" + to
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{From: from, To: to, Type: edgeType,
			Confidence: 0.99, Source: core.EvidenceSourceNative, Reason: core.ReasonMethodSet})
	}
	for _, iface := range d.interfaces {
		if !goDispatchTypeValid(iface.typ, map[types.Type]bool{}) {
			continue
		}
		for i := 0; i < iface.typ.NumMethods(); i++ {
			add(iface.symbol.ID, iface.symbol.ID+"#"+iface.typ.Method(i).Name(), core.EdgeContains)
		}
		for _, candidate := range d.candidates {
			if !types.Implements(candidate.typ, iface.typ) {
				continue
			}
			add(candidate.symbol.ID, iface.symbol.ID, core.EdgeImplements)
			for i := 0; i < iface.typ.NumMethods(); i++ {
				method := iface.typ.Method(i)
				selection := candidate.methods.Lookup(method.Pkg(), method.Name())
				if selection == nil {
					continue
				}
				if symbol, ok := d.localMethodSymbol(selection.Obj()); ok {
					add(symbol.ID, iface.symbol.ID, core.EdgeOverrides)
				}
			}
		}
	}
	return edges
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
			symbol, ok := d.localMethodSymbol(selection.Obj())
			if !ok || seen[symbol.ID] {
				continue
			}
			seen[symbol.ID] = true
			result[method.Id()] = append(result[method.Id()], symbol)
		}
	}
	return result
}

func (d *goInterfaceDispatch) localMethodSymbol(obj types.Object) (core.SymbolRecord, bool) {
	fn, ok := obj.(*types.Func)
	if !ok || fn.Pkg() != d.pkg {
		return core.SymbolRecord{}, false
	}
	file, ok := relFile(d.root, d.fset.PositionFor(fn.Pos(), false).Filename)
	if !ok {
		return core.SymbolRecord{}, false
	}
	symbol, ok := d.index.byFileFunc[file+"\x00"+goCallableKey(d.dir, goFuncReceiverName(fn), fn.Name())]
	return symbol, ok
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
