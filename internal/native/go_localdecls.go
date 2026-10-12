package native

import (
	"go/ast"
	"go/token"
	"go/types"
	"slices"

	"github.com/provasign/grove/internal/core"
)

// goLocalDecls returns the declarations go/types attributed inside function
// bodies -- local types, their named fields and their interface methods --
// which the parser does not index (grafana: 499 local types, 1642 fields,
// 2026-10-12). Like javac's anonymous classes they become compiler-declared
// symbols, named after their function the way astkit names items inside a Rust
// function: `func (r *Repo) Clone() { type opts struct{ depth int } }` gives
// Repo.Clone.opts (parent Clone) and Repo.Clone.opts.depth (parent opts).
// Embedded fields stay out, as they do for parsed structs: named after their
// type, every one would be a second symbol under that type's name.
//
// localTypes maps each local type's object to its qualified name, so the uses
// inside the function land on the declaration instead of on a package-level
// type that happens to share the name.
func goLocalDecls(fset *token.FileSet, files []string, parsed []*ast.File, info *types.Info) ([]compilerDecl, map[types.Object]string) {
	var decls []compilerDecl
	localTypes := map[types.Object]string{}
	line := func(p token.Pos) int { return fset.Position(p).Line }
	for fi, file := range parsed {
		rel := files[fi]
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			scope := fn.Name.Name
			if recv := goReceiverName(fn); recv != "" {
				scope = recv + "." + scope
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				ds, ok := n.(*ast.DeclStmt)
				if !ok {
					return true
				}
				gen, ok := ds.Decl.(*ast.GenDecl)
				if !ok || gen.Tok != token.TYPE {
					return true
				}
				for _, spec := range gen.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || ts.Name.Name == "_" {
						continue
					}
					obj, ok := info.Defs[ts.Name].(*types.TypeName)
					if !ok {
						continue // not attributed: the compiler does not know it either
					}
					qualified := scope + "." + ts.Name.Name
					localTypes[obj] = qualified
					kind := "type"
					switch ts.Type.(type) {
					case *ast.StructType:
						kind = "struct"
					case *ast.InterfaceType:
						kind = "interface"
					}
					decls = append(decls, compilerDecl{
						File: rel, Kind: kind, Name: ts.Name.Name, Qualified: qualified,
						Parent: fn.Name.Name, Start: line(ts.Pos()), End: line(ts.End()),
						Modifiers: []string{"local"},
					})
					decls = append(decls, goLocalMembers(ts, qualified, rel, info, line)...)
				}
				return true
			})
		}
	}
	return decls, localTypes
}

// goLocalMembers returns a local struct's named fields and a local interface's
// methods, in the parser's conventions for package-level ones: a field's
// signature is "Name Type" without the tag, an interface method is a
// "declaration" (a contract, not a call target).
func goLocalMembers(ts *ast.TypeSpec, qualified, rel string, info *types.Info, line func(token.Pos) int) []compilerDecl {
	var out []compilerDecl
	switch t := ts.Type.(type) {
	case *ast.StructType:
		for _, field := range t.Fields.List {
			for _, name := range field.Names {
				if name.Name == "_" {
					continue
				}
				if _, ok := info.Defs[name].(*types.Var); !ok {
					continue
				}
				out = append(out, compilerDecl{
					File: rel, Kind: "field", Name: name.Name, Qualified: qualified + "." + name.Name,
					Parent: ts.Name.Name, Start: line(field.Pos()), End: line(field.End()),
					Signature: name.Name + " " + types.ExprString(field.Type),
					Modifiers: []string{"local"},
				})
			}
		}
	case *ast.InterfaceType:
		for _, method := range t.Methods.List {
			for _, name := range method.Names {
				if _, ok := info.Defs[name].(*types.Func); !ok {
					continue
				}
				out = append(out, compilerDecl{
					File: rel, Kind: "method", Name: name.Name, Qualified: qualified + "." + name.Name,
					Parent: ts.Name.Name, Start: line(method.Pos()), End: line(method.End()),
					Signature:   name.Name + types.ExprString(method.Type)[len("func"):],
					Modifiers:   []string{"local"},
					Annotations: []string{"declaration"},
				})
			}
		}
	}
	return out
}

// goLocalSymbol reports a compiler-declared function-local symbol. The
// name-keyed indexes (package dir + name) skip them: a local `config` is not
// the package's `config`, and a stored one comes back in req.Symbols on every
// incremental run.
func goLocalSymbol(s core.SymbolRecord) bool {
	return slices.Contains(s.Modifiers, "local") && slices.Contains(s.Modifiers, "compiler-declared")
}

// goLocalObject reports a type declared inside a function body.
func goLocalObject(obj types.Object) bool {
	return obj.Pkg() != nil && obj.Parent() != nil && obj.Parent() != obj.Pkg().Scope()
}
