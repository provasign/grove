package native

import (
	"go/ast"
	"go/token"
	"go/types"

	"github.com/provasign/grove/internal/core"
)

// goClaimedImplements records where a project type is used as a project
// interface it does not satisfy: returned from a function whose result is the
// interface, assigned or declared as it, passed as it, converted to it, or
// stored in a field, element or map value of it.
//
// Go has no implements clause. The implements edges contractEdges builds come
// from method sets, so the moment an interface gains a method every type that
// lacks it leaves the family, and missing-implementations had nobody to
// report: prism's assist.Provider gained Close() and three providers that
// go vet rejected were reported as zero missing (2026-11-22). These uses are
// the compiler's own evidence of which types are meant to satisfy the
// interface. go/types records the operand's type even when the assignment
// fails, so the claim survives the very error it explains.
func goClaimedImplements(files []*ast.File, info *types.Info, currentDir string, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) []core.Edge {
	var edges []core.Edge
	seen := map[string]bool{}
	claim := func(expr ast.Expr, target types.Type) {
		if expr == nil || target == nil {
			return
		}
		named, ok := types.Unalias(target).(*types.Named)
		if !ok {
			return
		}
		iface, ok := named.Underlying().(*types.Interface)
		if !ok || iface.NumMethods() == 0 {
			return
		}
		operand := info.TypeOf(expr)
		if operand == nil || types.IsInterface(operand) {
			return
		}
		if types.Implements(operand, iface) {
			return // satisfied: contractEdges already records it
		}
		concrete := types.Unalias(operand)
		if ptr, ok := concrete.(*types.Pointer); ok {
			concrete = types.Unalias(ptr.Elem())
		}
		cnamed, ok := concrete.(*types.Named)
		if !ok {
			return
		}
		from, ok := goNamedTypeSymbol(currentDir, cnamed.Obj(), symbolIdx, pkgDirsByImport, selfPkg)
		if !ok {
			return
		}
		to, ok := goNamedTypeSymbol(currentDir, named.Obj(), symbolIdx, pkgDirsByImport, selfPkg)
		if !ok || from.ID == to.ID {
			return
		}
		key := from.ID + "\x00" + to.ID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{From: from.ID, To: to.ID, Type: core.EdgeImplements,
			Confidence: 0.9, Source: core.EvidenceSourceNative, Reason: core.ReasonUsedAs})
	}

	var walk func(node ast.Node, results *types.Tuple)
	walk = func(node ast.Node, results *types.Tuple) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.FuncDecl:
				if x.Body == nil {
					return false
				}
				var res *types.Tuple
				if fn, ok := info.Defs[x.Name].(*types.Func); ok {
					res = fn.Type().(*types.Signature).Results()
				}
				walk(x.Body, res)
				return false
			case *ast.FuncLit:
				var res *types.Tuple
				if sig, ok := info.TypeOf(x).(*types.Signature); ok {
					res = sig.Results()
				}
				walk(x.Body, res)
				return false
			case *ast.ReturnStmt:
				if results != nil && len(x.Results) == results.Len() {
					for i, r := range x.Results {
						claim(r, results.At(i).Type())
					}
				}
			case *ast.ValueSpec:
				if x.Type != nil {
					target := info.TypeOf(x.Type)
					for _, v := range x.Values {
						claim(v, target)
					}
				}
			case *ast.AssignStmt:
				if x.Tok == token.ASSIGN && len(x.Lhs) == len(x.Rhs) {
					for i := range x.Lhs {
						claim(x.Rhs[i], info.TypeOf(x.Lhs[i]))
					}
				}
			case *ast.CallExpr:
				if tv, ok := info.Types[x.Fun]; ok && tv.IsType() {
					if len(x.Args) == 1 {
						claim(x.Args[0], tv.Type) // I(v)
					}
					return true
				}
				sig, ok := types.Unalias(info.TypeOf(x.Fun)).(*types.Signature)
				if !ok {
					return true
				}
				params := sig.Params()
				for i, arg := range x.Args {
					switch {
					case i < params.Len()-1 || (i < params.Len() && !sig.Variadic()):
						claim(arg, params.At(i).Type())
					case sig.Variadic() && params.Len() > 0 && !x.Ellipsis.IsValid():
						if s, ok := params.At(params.Len() - 1).Type().(*types.Slice); ok {
							claim(arg, s.Elem())
						}
					}
				}
			case *ast.CompositeLit:
				lit := info.TypeOf(x)
				if lit == nil {
					return true
				}
				if p, ok := lit.Underlying().(*types.Pointer); ok {
					lit = p.Elem()
				}
				switch u := lit.Underlying().(type) {
				case *types.Struct:
					for i, elt := range x.Elts {
						if kv, ok := elt.(*ast.KeyValueExpr); ok {
							if key, ok := kv.Key.(*ast.Ident); ok {
								for f := 0; f < u.NumFields(); f++ {
									if u.Field(f).Name() == key.Name {
										claim(kv.Value, u.Field(f).Type())
									}
								}
							}
						} else if i < u.NumFields() {
							claim(elt, u.Field(i).Type())
						}
					}
				case *types.Slice:
					for _, elt := range x.Elts {
						claim(compositeValue(elt), u.Elem())
					}
				case *types.Array:
					for _, elt := range x.Elts {
						claim(compositeValue(elt), u.Elem())
					}
				case *types.Map:
					for _, elt := range x.Elts {
						claim(compositeValue(elt), u.Elem())
					}
				}
			}
			return true
		})
	}
	for _, f := range files {
		walk(f, nil)
	}
	return edges
}

// compositeValue is the value of a keyed element, or the element itself.
func compositeValue(elt ast.Expr) ast.Expr {
	if kv, ok := elt.(*ast.KeyValueExpr); ok {
		return kv.Value
	}
	return elt
}

// goNamedTypeSymbol maps a package-level type to its indexed symbol.
func goNamedTypeSymbol(currentDir string, obj *types.TypeName, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) (core.SymbolRecord, bool) {
	if obj == nil || goLocalObject(obj) {
		return core.SymbolRecord{}, false
	}
	for _, dir := range goObjectDirs(currentDir, obj.Pkg(), pkgDirsByImport, selfPkg) {
		if symbol, ok := symbolIdx.byType[dir+"\x00"+obj.Name()]; ok {
			return symbol, true
		}
	}
	return core.SymbolRecord{}, false
}
