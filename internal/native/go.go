package native

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type goAnalyzer struct{}

func (goAnalyzer) Name() string { return "go" }

func (goAnalyzer) Languages() []string { return []string{"go"} }

func (goAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "go.mod", "go.work") {
		return Availability{Reason: "no go.mod or go.work"}
	}
	if !commandExists("go") {
		return Availability{Reason: "go executable not found"}
	}
	return Availability{Available: true}
}

type goListPackage struct {
	Dir         string
	ImportPath  string
	GoFiles     []string
	TestGoFiles []string
	Imports     []string
}

func (goAnalyzer) Analyze(ctx context.Context, req Request) Result {
	cmd := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-json", "./...")
	cmd.Dir = req.Root
	cmd.Env = goAnalyzerEnv(req.Root)
	out, err := cmd.Output()
	if err != nil {
		return Result{Diagnostics: []string{"go list failed: " + err.Error()}}
	}
	dec := json.NewDecoder(bytesReader(out))
	var pkgs []goListPackage
	for {
		var pkg goListPackage
		if err := dec.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return Result{Diagnostics: []string{"go list JSON decode failed: " + err.Error()}}
		}
		pkgs = append(pkgs, pkg)
	}
	pkgByImport := map[string]goListPackage{}
	for _, pkg := range pkgs {
		pkgByImport[pkg.ImportPath] = pkg
	}

	var edges []core.Edge
	for _, pkg := range pkgs {
		fromFiles := packageFiles(req.Root, pkg)
		for _, imp := range pkg.Imports {
			target, ok := pkgByImport[imp]
			if !ok {
				continue
			}
			for _, from := range fromFiles {
				for _, to := range packageFiles(req.Root, target) {
					if from == to {
						continue
					}
					edges = append(edges, core.Edge{
						From:       "file:" + from,
						To:         "file:" + to,
						Type:       core.EdgeImports,
						Confidence: 0.98,
						Source:     core.EvidenceSourceNative,
					})
				}
			}
		}
	}
	semanticEdges, semanticDiagnostics := goSemanticEdges(req.Root, req.Files, req.Symbols, pkgs)
	edges = append(edges, semanticEdges...)
	callSiteEdges := goCallSiteEdges(req.Symbols)
	edges = append(edges, callSiteEdges...)
	typeUseEdges := goTypeUseEdges(req.Symbols)
	edges = append(edges, typeUseEdges...)

	return Result{
		Edges: edges,
		Diagnostics: append([]string{
			"go list resolved " + itoa(len(pkgs)) + " package(s)",
			"resolved " + itoa(countEdgesOfType(callSiteEdges, core.EdgeCalls)) + " native call-site edge(s)",
			"resolved " + itoa(countEdgesOfType(typeUseEdges, core.EdgeUsesType)) + " native lexical type-use edge(s)",
		}, semanticDiagnostics...),
	}
}

func goAnalyzerEnv(root string) []string {
	groveDir := filepath.Join(root, ".grove")
	goCache := filepath.Join(groveDir, "go-build")
	home := filepath.Join(groveDir, "home")
	_ = os.MkdirAll(goCache, 0o755)
	_ = os.MkdirAll(home, 0o755)
	return appendEnv("GOCACHE="+goCache, "HOME="+home)
}

func packageFiles(root string, pkg goListPackage) []string {
	names := append(append([]string{}, pkg.GoFiles...), pkg.TestGoFiles...)
	out := make([]string, 0, len(names))
	for _, name := range names {
		abs := filepath.Join(pkg.Dir, name)
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			continue
		}
		out = append(out, filepath.ToSlash(rel))
	}
	return out
}

type goSymbolIndex struct {
	byFileFunc map[string]core.SymbolRecord
	byFunc     map[string]core.SymbolRecord
	byType     map[string]core.SymbolRecord
}

func newGoSymbolIndex(symbols []core.SymbolRecord) goSymbolIndex {
	idx := goSymbolIndex{
		byFileFunc: map[string]core.SymbolRecord{},
		byFunc:     map[string]core.SymbolRecord{},
		byType:     map[string]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "go" {
			continue
		}
		dir := packageDir(symbol.FilePath)
		switch symbol.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
			key := goCallableKey(dir, symbol.ParentSymbol, symbol.Name)
			idx.byFunc[key] = symbol
			idx.byFileFunc[symbol.FilePath+"\x00"+key] = symbol
		case core.KindStruct, core.KindInterface, core.KindType:
			idx.byType[dir+"\x00"+symbol.Name] = symbol
		}
	}
	return idx
}

func goCallableKey(dir, recv, name string) string {
	if recv != "" {
		return dir + "\x00" + recv + "." + name
	}
	return dir + "\x00" + name
}

func goSemanticEdges(root string, files []string, symbols []core.SymbolRecord, pkgs []goListPackage) ([]core.Edge, []string) {
	symbolIdx := newGoSymbolIndex(symbols)
	pkgDirsByImport := map[string][]string{}
	for _, pkg := range pkgs {
		if rel, ok := relFile(root, pkg.Dir); ok {
			pkgDirsByImport[pkg.ImportPath] = append(pkgDirsByImport[pkg.ImportPath], rel)
		}
	}

	filesByDir := map[string][]string{}
	for _, file := range files {
		filesByDir[packageDir(file)] = append(filesByDir[packageDir(file)], file)
	}

	var edges []core.Edge
	var diagnostics []string
	for dir, dirFiles := range filesByDir {
		pkgEdges, err := goSemanticPackageEdges(root, dir, dirFiles, symbolIdx, pkgDirsByImport)
		if err != nil {
			diagnostics = append(diagnostics, "semantic package "+dir+" skipped: "+err.Error())
			continue
		}
		edges = append(edges, pkgEdges...)
	}
	diagnostics = append(diagnostics, "resolved "+itoa(countEdgesOfType(edges, core.EdgeCalls))+" native call edge(s)")
	diagnostics = append(diagnostics, "resolved "+itoa(countEdgesOfType(edges, core.EdgeUsesType))+" native type-use edge(s)")
	return edges, diagnostics
}

func goSemanticPackageEdges(root, dir string, files []string, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string) ([]core.Edge, error) {
	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	for _, file := range files {
		abs := filepath.Join(root, file)
		f, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		parsed = append(parsed, f)
	}
	info := &types.Info{
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{
		Importer: importer.Default(),
		Error:    func(error) {},
	}
	_, _ = conf.Check(dir, fset, parsed, info)

	var edges []core.Edge
	seen := map[string]bool{}
	add := func(edge core.Edge) {
		key := edge.From + "\x00" + string(edge.Type) + "\x00" + edge.To
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, edge)
	}

	for _, file := range parsed {
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			caller, ok := goCallerSymbol(fset, fn, symbolIdx)
			if !ok || fn.Body == nil {
				return false
			}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				switch n := inner.(type) {
				case *ast.CallExpr:
					if callee, ok := goResolveCall(dir, n.Fun, info, symbolIdx, pkgDirsByImport); ok && callee.ID != caller.ID {
						add(core.Edge{
							From:       caller.ID,
							To:         callee.ID,
							Type:       core.EdgeCalls,
							Confidence: 0.99,
							Source:     core.EvidenceSourceNative,
						})
					}
				case *ast.Ident:
					if typ, ok := goResolveType(dir, n, info, symbolIdx, pkgDirsByImport); ok && typ.ID != caller.ID {
						add(core.Edge{
							From:       caller.ID,
							To:         typ.ID,
							Type:       core.EdgeUsesType,
							Confidence: 0.97,
							Source:     core.EvidenceSourceNative,
						})
					}
				}
				return true
			})
			return false
		})
	}
	return edges, nil
}

func goCallerSymbol(fset *token.FileSet, fn *ast.FuncDecl, symbolIdx goSymbolIndex) (core.SymbolRecord, bool) {
	pos := fset.Position(fn.Pos())
	file := filepath.ToSlash(pos.Filename)
	if i := strings.LastIndex(file, "/"); i >= 0 {
		// fset stores absolute paths; SymbolRecord uses repo-relative paths.
		for key, symbol := range symbolIdx.byFileFunc {
			if strings.HasSuffix(file, symbol.FilePath) && strings.Contains(key, "\x00") {
				recv := goReceiverName(fn)
				wantKey := goCallableKey(packageDir(symbol.FilePath), recv, fn.Name.Name)
				if key == symbol.FilePath+"\x00"+wantKey {
					return symbol, true
				}
			}
		}
	}
	return core.SymbolRecord{}, false
}

func goResolveCall(currentDir string, expr ast.Expr, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string) (core.SymbolRecord, bool) {
	switch fun := expr.(type) {
	case *ast.Ident:
		obj, ok := info.Uses[fun].(*types.Func)
		if !ok {
			return core.SymbolRecord{}, false
		}
		return goSymbolForFunc(currentDir, obj, symbolIdx, pkgDirsByImport)
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return goSymbolForFunc(currentDir, fn, symbolIdx, pkgDirsByImport)
			}
		}
		if fn, ok := info.Uses[fun.Sel].(*types.Func); ok {
			return goSymbolForFunc(currentDir, fn, symbolIdx, pkgDirsByImport)
		}
	}
	return core.SymbolRecord{}, false
}

func goResolveType(currentDir string, ident *ast.Ident, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string) (core.SymbolRecord, bool) {
	obj, ok := info.Uses[ident].(*types.TypeName)
	if !ok {
		return core.SymbolRecord{}, false
	}
	dirs := goObjectDirs(currentDir, obj.Pkg(), pkgDirsByImport)
	for _, dir := range dirs {
		if symbol, ok := symbolIdx.byType[dir+"\x00"+obj.Name()]; ok {
			return symbol, true
		}
	}
	return core.SymbolRecord{}, false
}

func goSymbolForFunc(currentDir string, fn *types.Func, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string) (core.SymbolRecord, bool) {
	dirs := goObjectDirs(currentDir, fn.Pkg(), pkgDirsByImport)
	recv := goFuncReceiverName(fn)
	for _, dir := range dirs {
		if symbol, ok := symbolIdx.byFunc[goCallableKey(dir, recv, fn.Name())]; ok {
			return symbol, true
		}
		if recv != "" {
			if symbol, ok := symbolIdx.byFunc[goCallableKey(dir, "", fn.Name())]; ok {
				return symbol, true
			}
		}
	}
	return core.SymbolRecord{}, false
}

func goObjectDirs(currentDir string, pkg *types.Package, pkgDirsByImport map[string][]string) []string {
	if pkg == nil {
		return []string{currentDir}
	}
	if dirs := pkgDirsByImport[pkg.Path()]; len(dirs) > 0 {
		return dirs
	}
	return []string{currentDir}
}

func goReceiverName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	return goExprTypeName(fn.Recv.List[0].Type)
}

func goExprTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return goExprTypeName(t.X)
	case *ast.SelectorExpr:
		return t.Sel.Name
	case *ast.IndexExpr:
		return goExprTypeName(t.X)
	case *ast.IndexListExpr:
		return goExprTypeName(t.X)
	}
	return ""
}

func goFuncReceiverName(fn *types.Func) string {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return ""
	}
	return goTypeBaseName(sig.Recv().Type())
}

func goTypeBaseName(typ types.Type) string {
	switch t := typ.(type) {
	case *types.Named:
		return t.Obj().Name()
	case *types.Pointer:
		return goTypeBaseName(t.Elem())
	case *types.Alias:
		return t.Obj().Name()
	}
	return ""
}

func countEdgesOfType(edges []core.Edge, edgeType core.EdgeType) int {
	count := 0
	for _, edge := range edges {
		if edge.Type == edgeType {
			count++
		}
	}
	return count
}

func goCallSiteEdges(symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seen := map[string]bool{}
	for _, caller := range symbols {
		if caller.Language != "go" || !callableKind(caller.Kind) {
			continue
		}
		for _, callSite := range caller.CallSites {
			qualifier, name := splitGoCallSite(callSite.Callee)
			if name == "" {
				continue
			}
			var targets []core.SymbolRecord
			if qualifier == "" {
				for _, symbol := range symbols {
					if symbol.Language == "go" && symbol.Kind == core.KindFunction && symbol.Name == name && packageDir(symbol.FilePath) == packageDir(caller.FilePath) {
						targets = append(targets, symbol)
					}
				}
			} else {
				targetPkg, ok := goImportedPackageForQualifier(caller.Imports, qualifier)
				if !ok {
					continue
				}
				for _, symbol := range symbols {
					if symbol.Language != "go" || symbol.Kind != core.KindFunction || symbol.Name != name {
						continue
					}
					if packageDir(symbol.FilePath) != targetPkg {
						continue
					}
					targets = append(targets, symbol)
				}
			}
			if len(targets) != 1 {
				continue
			}
			target := targets[0]
			if target.ID == caller.ID {
				continue
			}
			key := caller.ID + "\x00" + target.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			edges = append(edges, symbolEdge(caller, target, core.EdgeCalls, 0.99))
		}
	}
	return edges
}

func splitGoCallSite(callee string) (string, string) {
	if i := strings.LastIndexByte(callee, '.'); i >= 0 {
		return callee[:i], callee[i+1:]
	}
	return "", callee
}

func goImportedPackageForQualifier(imports []string, qualifier string) (string, bool) {
	for _, imp := range imports {
		seg := imp
		if i := strings.LastIndexByte(seg, '/'); i >= 0 {
			seg = seg[i+1:]
		}
		if seg == qualifier {
			return seg, true
		}
	}
	return "", false
}

func goImportScope(caller core.SymbolRecord, symbols []core.SymbolRecord) map[string]bool {
	scope := map[string]bool{caller.FilePath: true}
	callerDir := packageDir(caller.FilePath)
	for _, symbol := range symbols {
		if symbol.Language == "go" && packageDir(symbol.FilePath) == callerDir {
			scope[symbol.FilePath] = true
		}
	}
	for _, imp := range caller.Imports {
		seg := imp
		if i := strings.LastIndexByte(seg, '/'); i >= 0 {
			seg = seg[i+1:]
		}
		for _, symbol := range symbols {
			if symbol.Language == "go" && (packageDir(symbol.FilePath) == seg || strings.HasSuffix(packageDir(symbol.FilePath), "/"+seg)) {
				scope[symbol.FilePath] = true
			}
		}
	}
	return scope
}

func goTypeUseEdges(symbols []core.SymbolRecord) []core.Edge {
	typesByName := map[string][]core.SymbolRecord{}
	for _, symbol := range symbols {
		if symbol.Language == "go" && typeKind(symbol.Kind) {
			typesByName[symbol.Name] = append(typesByName[symbol.Name], symbol)
		}
	}
	var edges []core.Edge
	seen := map[string]bool{}
	for _, caller := range symbols {
		if caller.Language != "go" || !callableKind(caller.Kind) || caller.RawText == "" {
			continue
		}
		scope := goImportScope(caller, symbols)
		for name, candidates := range typesByName {
			if !goContainsType(caller.RawText, name) {
				continue
			}
			for _, target := range candidates {
				if target.ID == caller.ID || !scope[target.FilePath] {
					continue
				}
				key := caller.ID + "\x00" + target.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				edges = append(edges, symbolEdge(caller, target, core.EdgeUsesType, 0.98))
			}
		}
	}
	return edges
}

func goContainsType(rawText, name string) bool {
	if containsTypeToken(rawText, name) {
		return true
	}
	pattern := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.` + regexp.QuoteMeta(name) + `\b`)
	return pattern.MatchString(stripQuotedText(rawText))
}
