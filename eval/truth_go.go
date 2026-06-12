package eval

import (
	"fmt"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/callgraph/cha"
	"golang.org/x/tools/go/callgraph/vta"
	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/ssa/ssautil"
)

// GoTruthOptions controls ground-truth generation for a Go repo.
type GoTruthOptions struct {
	RepoRoot     string
	IncludeTests bool
	Env          []string // extra environment entries, e.g. GOWORK=off
}

// GoCallTruth builds the caller→callee ground truth for a Go repository
// using the typed SSA callgraph (CHA refined by VTA). Only edges whose both
// endpoints are named, non-synthetic declarations inside the repo are kept.
func GoCallTruth(opts GoTruthOptions) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(opts.RepoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	cfg := &packages.Config{
		Mode:  packages.LoadAllSyntax,
		Dir:   root,
		Tests: opts.IncludeTests,
		Env:   opts.Env,
	}
	pkgs, err := packages.Load(cfg, "./...")
	if err != nil {
		return TruthFile{}, nil, fmt.Errorf("load packages: %w", err)
	}
	var loadErrs int
	packages.Visit(pkgs, nil, func(p *packages.Package) { loadErrs += len(p.Errors) })
	if loadErrs > 0 {
		// Type errors poison SSA construction; the oracle must be clean.
		var first string
		packages.Visit(pkgs, nil, func(p *packages.Package) {
			if first == "" && len(p.Errors) > 0 {
				first = p.Errors[0].Error()
			}
		})
		return TruthFile{}, nil, fmt.Errorf("%d package load errors (first: %s)", loadErrs, first)
	}

	prog, _ := ssautil.AllPackages(pkgs, ssa.InstantiateGenerics)
	prog.Build()

	fns := ssautil.AllFunctions(prog)
	graph := vta.CallGraph(fns, cha.CallGraph(prog))

	refs := map[*ssa.Function]FuncRef{}
	resolve := func(fn *ssa.Function) (FuncRef, bool) {
		if ref, ok := refs[fn]; ok {
			return ref, ref.File != ""
		}
		ref, ok := goFuncRef(prog.Fset, fn, root)
		if !ok {
			refs[fn] = FuncRef{}
			return FuncRef{}, false
		}
		refs[fn] = ref
		return ref, true
	}
	// Grove attributes calls made inside closures to the enclosing named
	// declaration — the right model for blast radius. Mirror that here by
	// resolving anonymous callers to their nearest named ancestor.
	resolveCaller := func(fn *ssa.Function) (FuncRef, bool) {
		for fn != nil && fn.Parent() != nil {
			fn = fn.Parent()
		}
		return resolve(fn)
	}

	seen := map[string]bool{}
	var edges []TruthEdge
	for fn, node := range graph.Nodes {
		caller, ok := resolveCaller(fn)
		if !ok {
			continue
		}
		for _, out := range node.Out {
			callee, ok := resolve(out.Callee.Func)
			if !ok {
				continue
			}
			key := caller.File + "\x00" + caller.Name + "\x00" + callee.File + "\x00" + fmt.Sprint(callee.Line) + "\x00" + callee.Name
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, TruthEdge{Caller: caller, Callee: callee})
		}
	}
	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller.File != b.Caller.File {
			return a.Caller.File < b.Caller.File
		}
		if a.Caller.Line != b.Caller.Line {
			return a.Caller.Line < b.Caller.Line
		}
		if a.Callee.File != b.Callee.File {
			return a.Callee.File < b.Callee.File
		}
		return a.Callee.Line < b.Callee.Line
	})

	funcs := map[string]bool{}
	for _, ref := range refs {
		if ref.File != "" {
			funcs[ref.File+"\x00"+fmt.Sprint(ref.Line)+"\x00"+ref.Name] = true
		}
	}
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "go-ssa-vta",
		Functions: len(funcs),
		Edges:     len(edges),
	}
	return header, edges, nil
}

// goFuncRef maps an SSA function to a repo-relative FuncRef, rejecting
// synthetic wrappers, anonymous functions, and declarations outside the repo.
func goFuncRef(fset *token.FileSet, fn *ssa.Function, root string) (FuncRef, bool) {
	if fn == nil || fn.Synthetic != "" || fn.Parent() != nil {
		return FuncRef{}, false
	}
	if strings.Contains(fn.Name(), "$") || fn.Name() == "init" {
		return FuncRef{}, false
	}
	pos := fn.Pos()
	if !pos.IsValid() {
		return FuncRef{}, false
	}
	position := fset.Position(pos)
	rel, err := filepath.Rel(root, position.Filename)
	if err != nil || strings.HasPrefix(rel, "..") {
		return FuncRef{}, false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "vendor/") || strings.Contains(rel, "/vendor/") {
		return FuncRef{}, false
	}
	name := fn.Name()
	if recv := fn.Signature.Recv(); recv != nil {
		name = recvTypeName(recv.Type()) + "." + name
	}
	return FuncRef{File: rel, Line: position.Line, Name: name}, true
}

func recvTypeName(t types.Type) string {
	for {
		switch v := t.(type) {
		case *types.Pointer:
			t = v.Elem()
		case *types.Named:
			return v.Obj().Name()
		default:
			s := t.String()
			if i := strings.LastIndex(s, "."); i >= 0 {
				s = s[i+1:]
			}
			return strings.TrimLeft(s, "*")
		}
	}
}
