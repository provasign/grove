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

	"crypto/sha256"
	"encoding/hex"
	"github.com/provasign/grove/internal/core"
	"runtime"
	"sort"
	"sync"
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
	// Test-file imports are reported separately by go list; the scoped-delta
	// reverse-importer closure must include them or packages whose TESTS
	// import a changed package are never re-analyzed and their test edges
	// silently drop (blob-SHA endpoints stop resolving).
	TestImports  []string
	XTestImports []string
}

func (goAnalyzer) Analyze(ctx context.Context, req Request) Result {
	// go list output depends on module files, the go-file set, and import
	// blocks — never on function bodies. Cache it keyed by exactly those
	// inputs, so content-only edits skip the ~3s subprocess entirely.
	topoKey := goListTopologyKey(req.Root, req.Files, req.Symbols)
	cachePath := filepath.Join(req.Root, ".grove", "golist-cache.json")
	out, cacheHit := loadGoListCache(cachePath, topoKey)
	if !cacheHit {
		cmd := exec.CommandContext(ctx, "go", "list", "-mod=readonly", "-json", "./...")
		cmd.Dir = req.Root
		cmd.Env = goAnalyzerEnv(req.Root)
		var err error
		out, err = cmd.Output()
		if err != nil {
			return Result{Diagnostics: []string{"go list failed: " + err.Error()}}
		}
		saveGoListCache(cachePath, topoKey, out)
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
	// Scope the expensive passes (per-package type-check, symbol sweeps) to
	// the packages containing changed files plus their transitive reverse
	// importers — an exported-signature change propagates to everything that
	// type-checks against it. The cheap go-list import edges above stay
	// whole-repo. Partial reports the analyzed dirs so the indexer carries
	// the stored native edges of every other package.
	semFiles, semSymbols := req.Files, req.Symbols
	var partial map[string][]string
	var scopeDiag []string
	if len(req.ChangedFiles) > 0 {
		affected := goAffectedDirs(req.Root, req.ChangedFiles, pkgs)
		if len(affected) > 0 {
			semFiles = filterByDirs(req.Files, affected)
			semSymbols = filterSymbolsByDirs(req.Symbols, affected)
			dirs := make([]string, 0, len(affected))
			for d := range affected {
				dirs = append(dirs, d)
			}
			sort.Strings(dirs)
			partial = map[string][]string{"go": dirs}
			scopeDiag = []string{"scoped to " + itoa(len(dirs)) + " affected package dir(s); other packages' native edges carried forward"}
		}
	}
	semanticEdges, semanticDiagnostics := goSemanticEdges(ctx, req.Root, semFiles, req.Symbols, pkgs)
	edges = append(edges, semanticEdges...)
	callSiteEdges := goCallSiteEdges(semSymbols, req.Symbols)
	edges = append(edges, callSiteEdges...)
	typeUseEdges := goTypeUseEdges(ctx, semSymbols, req.Symbols)
	edges = append(edges, typeUseEdges...)

	head := []string{"go list resolved " + itoa(len(pkgs)) + " package(s)"}
	if cacheHit {
		head[0] += " (topology cache hit)"
	}
	head = append(head,
		"resolved "+itoa(countEdgesOfType(callSiteEdges, core.EdgeCalls))+" native call-site edge(s)",
		"resolved "+itoa(countEdgesOfType(typeUseEdges, core.EdgeUsesType))+" native lexical type-use edge(s)")
	head = append(head, scopeDiag...)
	return Result{
		Edges:       edges,
		Partial:     partial,
		Diagnostics: append(head, semanticDiagnostics...),
	}
}

// goAffectedDirs returns the package dirs of the changed go files plus every
// dir that transitively imports one of them (repo-relative, slash paths).
func goAffectedDirs(root string, changedFiles []string, pkgs []goListPackage) map[string]bool {
	changedDirs := map[string]bool{}
	for _, f := range changedFiles {
		if strings.HasSuffix(f, ".go") {
			changedDirs[packageDir(f)] = true
		}
	}
	if len(changedDirs) == 0 {
		return nil
	}
	// dir -> import path, and reverse import graph over repo packages
	dirToImport := map[string]string{}
	importers := map[string][]string{} // import path -> dirs importing it
	for _, pkg := range pkgs {
		rel, ok := relFile(root, pkg.Dir)
		if !ok {
			continue
		}
		dirToImport[rel] = pkg.ImportPath
		for _, imp := range pkg.Imports {
			importers[imp] = append(importers[imp], rel)
		}
		for _, imp := range pkg.TestImports {
			importers[imp] = append(importers[imp], rel)
		}
		for _, imp := range pkg.XTestImports {
			importers[imp] = append(importers[imp], rel)
		}
	}
	affected := map[string]bool{}
	queue := make([]string, 0, len(changedDirs))
	for d := range changedDirs {
		affected[d] = true
		queue = append(queue, d)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		dir := queue[0]
		queue = queue[1:]
		imp, ok := dirToImport[dir]
		if !ok {
			continue
		}
		deps := append([]string(nil), importers[imp]...)
		sort.Strings(deps)
		for _, d := range deps {
			if !affected[d] {
				affected[d] = true
				queue = append(queue, d)
			}
		}
	}
	// The lexical passes (goCallSiteEdges/goTypeUseEdges) resolve imports by
	// BASENAME, so a package importing any path whose last segment matches a
	// changed dir's basename may hold edges into it even without a go-list
	// import edge. One basename-level expansion (no transitive re-queue —
	// lexical edges are caller→target one-hop) keeps those callers fresh.
	changedBase := map[string]bool{}
	for d := range changedDirs {
		changedBase[lastPathSegment(d)] = true
	}
	for _, pkg := range pkgs {
		rel, ok := relFile(root, pkg.Dir)
		if !ok || affected[rel] {
			continue
		}
		for _, imp := range append(append(append([]string(nil), pkg.Imports...), pkg.TestImports...), pkg.XTestImports...) {
			if changedBase[lastPathSegment(imp)] {
				affected[rel] = true
				break
			}
		}
	}
	return affected
}

func filterByDirs(files []string, dirs map[string]bool) []string {
	var out []string
	for _, f := range files {
		if dirs[packageDir(f)] {
			out = append(out, f)
		}
	}
	return out
}

func filterSymbolsByDirs(symbols []core.SymbolRecord, dirs map[string]bool) []core.SymbolRecord {
	var out []core.SymbolRecord
	for _, s := range symbols {
		if dirs[packageDir(s.FilePath)] {
			out = append(out, s)
		}
	}
	return out
}

// goAnalyzerEnv returns a scrubbed, hardened environment for `go list` run
// against an untrusted repository. Two dangers it closes:
//   - GOTOOLCHAIN unset (default "auto") lets a `toolchain goX.Y.Z` directive
//     in a hostile go.mod make the go command DOWNLOAD AND RE-EXEC a different
//     toolchain over the network — arbitrary code from repo content. Pinned to
//     "local" so only the installed toolchain ever runs.
//   - GOPROXY reaching the network lets `go list ./...` fetch attacker-named
//     modules (forced egress / SSRF-ish). Set "off" so analysis is offline;
//     -mod=readonly already blocks go.mod edits.
// GOMODCACHE/GOCACHE/GOPATH are carried through the allowlist so the shared
// module cache and build cache still work; module downloads are simply
// disabled for this side-effect-free analysis pass.
func goAnalyzerEnv(_ string) []string {
	// GOTOOLCHAIN=local always: a `toolchain goX.Y.Z` directive in go.mod
	// otherwise makes `go list` download and re-exec a different toolchain over
	// the network — a surprise even for trusted repos, and arbitrary-code from
	// repo content for untrusted ones. -mod=readonly always: analysis never
	// edits go.mod. These do not degrade a normal `go list`.
	env := scrubbedEnv("GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOWORK=off")
	if untrustedMode() {
		// Cut network module fetches so a hostile go.mod cannot drive egress to
		// attacker-named modules. In trusted mode the operator's GOPROXY (from
		// the allowlist) is honored so dependency resolution works normally.
		env = append(env, "GOPROXY=off", "GONOSUMCHECK=1")
	}
	return env
}

// CleanupLegacyCaches removes the per-repo Go caches that earlier Grove
// versions created under .grove/ by redirecting HOME and GOCACHE. The module
// cache is written with read-only directory permissions, so directories are
// made writable before removal.
func CleanupLegacyCaches(root string) {
	for _, dir := range []string{
		filepath.Join(root, ".grove", "home"),
		filepath.Join(root, ".grove", "go-build"),
	} {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0o755)
			}
			return nil
		})
		_ = os.RemoveAll(dir)
	}
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

func goSemanticEdges(ctx context.Context, root string, files []string, symbols []core.SymbolRecord, pkgs []goListPackage) ([]core.Edge, []string) {
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
	dirs := make([]string, 0, len(filesByDir))
	for d := range filesByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	// Per-package type-checks are independent and symbolIdx/pkgDirsByImport
	// are read-only here; results land by index so output order (and thus
	// the built graph) is identical to the sequential loop.
	type dirOutcome struct {
		edges []core.Edge
		err   error
	}
	outcomes := make([]dirOutcome, len(dirs))
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	dirCh := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for di := range dirCh {
				// Honor cancellation: a timed-out index must stop burning
				// CPU here, not keep type-checking packages past deadline.
				if err := ctx.Err(); err != nil {
					outcomes[di] = dirOutcome{err: err}
					continue
				}
				e, err := goSemanticPackageEdges(root, dirs[di], filesByDir[dirs[di]], symbolIdx, pkgDirsByImport)
				outcomes[di] = dirOutcome{edges: e, err: err}
			}
		}()
	}
	for di := range dirs {
		dirCh <- di
	}
	close(dirCh)
	wg.Wait()
	for di, o := range outcomes {
		if o.err != nil {
			diagnostics = append(diagnostics, "semantic package "+dirs[di]+" skipped: "+o.err.Error())
			continue
		}
		edges = append(edges, o.edges...)
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

	for fi, file := range parsed {
		relPath := files[fi]
		ast.Inspect(file, func(node ast.Node) bool {
			fn, ok := node.(*ast.FuncDecl)
			if !ok {
				return true
			}
			caller, ok := goCallerSymbolAt(relPath, fn, symbolIdx)
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

// goCallerSymbolAt maps a FuncDecl to its symbol by direct key lookup. The
// caller knows the repo-relative path of the file being inspected, so no
// suffix scan over the whole index is needed (the previous implementation
// iterated every function in the repo per FuncDecl — quadratic on monorepos).
func goCallerSymbolAt(relPath string, fn *ast.FuncDecl, symbolIdx goSymbolIndex) (core.SymbolRecord, bool) {
	key := relPath + "\x00" + goCallableKey(packageDir(relPath), goReceiverName(fn), fn.Name.Name)
	symbol, ok := symbolIdx.byFileFunc[key]
	return symbol, ok
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

// goNativeIndex precomputes the lookups goCallSiteEdges and goTypeUseEdges
// need. The previous implementations scanned the full symbol slice per call
// site / per caller (O(callers × symbols)).
type goNativeIndex struct {
	funcsByName map[string][]core.SymbolRecord
	typesByName map[string][]core.SymbolRecord
	filesByDir  map[string][]string // package dir → files
	filesByBase map[string][]string // last dir segment → files
}

func newGoNativeIndex(symbols []core.SymbolRecord) *goNativeIndex {
	idx := &goNativeIndex{
		funcsByName: map[string][]core.SymbolRecord{},
		typesByName: map[string][]core.SymbolRecord{},
		filesByDir:  map[string][]string{},
		filesByBase: map[string][]string{},
	}
	seenFile := map[string]bool{}
	for _, symbol := range symbols {
		if symbol.Language != "go" {
			continue
		}
		if symbol.Kind == core.KindFunction {
			idx.funcsByName[symbol.Name] = append(idx.funcsByName[symbol.Name], symbol)
		}
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		if !seenFile[symbol.FilePath] {
			seenFile[symbol.FilePath] = true
			dir := packageDir(symbol.FilePath)
			idx.filesByDir[dir] = append(idx.filesByDir[dir], symbol.FilePath)
			idx.filesByBase[lastPathSegment(dir)] = append(idx.filesByBase[lastPathSegment(dir)], symbol.FilePath)
		}
	}
	return idx
}

func lastPathSegment(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// goCallSiteEdges resolves call sites for callers against the FULL symbol
// index — callers may be scoped to changed packages, targets live anywhere.
func goCallSiteEdges(callers, all []core.SymbolRecord) []core.Edge {
	idx := newGoNativeIndex(all)
	var edges []core.Edge
	seen := map[string]bool{}
	for _, caller := range callers {
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
				callerDir := packageDir(caller.FilePath)
				for _, symbol := range idx.funcsByName[name] {
					if packageDir(symbol.FilePath) == callerDir {
						targets = append(targets, symbol)
					}
				}
			} else {
				imp, ok := goImportedPackageForQualifier(caller.Imports, qualifier)
				if !ok {
					continue
				}
				for _, symbol := range idx.funcsByName[name] {
					if goDirMatchesImport(packageDir(symbol.FilePath), imp) {
						targets = append(targets, symbol)
					}
				}
				if len(targets) == 0 {
					// Layouts where file paths don't share the import's
					// module prefix: fall back to last-segment matching.
					for _, symbol := range idx.funcsByName[name] {
						if lastPathSegment(packageDir(symbol.FilePath)) == qualifier {
							targets = append(targets, symbol)
						}
					}
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

// goImportedPackageForQualifier returns the full import path whose last
// segment matches the qualifier. Returning only the segment (the previous
// behaviour) made the caller compare "store" against package dirs like
// "internal/store", silently dropping qualified cross-package call edges
// for every nested package.
func goImportedPackageForQualifier(imports []string, qualifier string) (string, bool) {
	for _, imp := range imports {
		if lastPathSegment(imp) == qualifier {
			return imp, true
		}
	}
	return "", false
}

// goDirMatchesImport reports whether a repo-relative package dir is the
// package an import path names (the import carries the module prefix that
// repo-relative dirs lack).
func goDirMatchesImport(dir, imp string) bool {
	if dir == "" || dir == "." {
		return false
	}
	return imp == dir || strings.HasSuffix(imp, "/"+dir)
}

func goImportScope(caller core.SymbolRecord, idx *goNativeIndex) map[string]bool {
	scope := map[string]bool{caller.FilePath: true}
	for _, file := range idx.filesByDir[packageDir(caller.FilePath)] {
		scope[file] = true
	}
	for _, imp := range caller.Imports {
		for _, file := range idx.filesByBase[lastPathSegment(imp)] {
			scope[file] = true
		}
	}
	return scope
}

// goIdentRe extracts identifier tokens from a stripped body in one pass.
var goIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// goTypeUseEdges emits lexical type-use edges. Each caller body is stripped
// and tokenized exactly once and tokens are resolved through the type index;
// the previous implementation compiled a fresh regex and re-stripped the
// body for every (caller, type-name) pair, unbounded by the analyzer
// completion (linear cost; the deadline governs subprocesses, not pure-Go passes).
// goTypeUseEdges resolves type references for callers against the FULL
// symbol index — callers may be scoped to changed packages, but their
// targets live anywhere in the repo.
func goTypeUseEdges(ctx context.Context, callers, all []core.SymbolRecord) []core.Edge {
	idx := newGoNativeIndex(all)
	if len(idx.typesByName) == 0 {
		return nil
	}
	var edges []core.Edge
	seen := map[string]bool{}
	for _, caller := range callers {
		if caller.Language != "go" || !callableKind(caller.Kind) || caller.RawText == "" {
			continue
		}
		scope := goImportScope(caller, idx)
		stripped := stripQuotedText(caller.RawText)
		seenToken := map[string]bool{}
		for _, token := range goIdentRe.FindAllString(stripped, -1) {
			if seenToken[token] {
				continue
			}
			seenToken[token] = true
			for _, target := range idx.typesByName[token] {
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

// goContainsType reports whether name appears as a bare or package-qualified
// type token in rawText. Retained for direct callers and tests; the hot path
// in goTypeUseEdges tokenizes instead.
func goContainsType(rawText, name string) bool {
	if containsTypeToken(rawText, name) {
		return true
	}
	pattern := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.` + regexp.QuoteMeta(name) + `\b`)
	return pattern.MatchString(stripQuotedText(rawText))
}

// goListTopologyKey hashes everything go list output depends on: module
// files, the sorted go-file set, and each file's import block.
func goListTopologyKey(root string, files []string, symbols []core.SymbolRecord) string {
	h := sha256.New()
	// Environment and toolchain change go list output (GOOS/GOARCH gate file
	// sets via build tags; GOFLAGS can add -mod=vendor). Known residual: an
	// edited //go:build line rotates file content but not this key — rare,
	// and recoverable with --force or any import-block change.
	for _, env := range []string{"GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED"} {
		h.Write([]byte(env + "=" + os.Getenv(env) + ";"))
	}
	h.Write([]byte(runtime.Version()))
	for _, mod := range []string{"go.mod", "go.sum", "go.work", "go.work.sum"} {
		if b, err := os.ReadFile(filepath.Join(root, mod)); err == nil {
			h.Write([]byte(mod))
			h.Write(b)
		}
	}
	importsByFile := map[string][]string{}
	for i := range symbols {
		sym := &symbols[i]
		if sym.Language == "go" && len(sym.Imports) > 0 {
			if _, ok := importsByFile[sym.FilePath]; !ok {
				importsByFile[sym.FilePath] = sym.Imports
			}
		}
	}
	sorted := append([]string(nil), files...)
	sort.Strings(sorted)
	for _, f := range sorted {
		h.Write([]byte(f))
		h.Write([]byte{0})
		for _, imp := range importsByFile[f] {
			h.Write([]byte(imp))
			h.Write([]byte{1})
		}
		// Build-constraint lines gate file-set membership per GOOS/GOARCH —
		// an edited //go:build line must rotate the key. Reading one leading
		// block per file (~1KB) costs far less than the go list subprocess
		// the cache avoids.
		h.Write(buildConstraintLines(filepath.Join(root, f)))
		h.Write([]byte{2})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// buildConstraintLines returns the //go:build and // +build lines from the
// file's leading comment block (before the package clause).
func buildConstraintLines(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	var out []byte
	for _, ln := range strings.Split(string(buf[:n]), "\n") {
		t := strings.TrimSpace(ln)
		if strings.HasPrefix(t, "package ") {
			break
		}
		if strings.HasPrefix(t, "//go:build") || strings.HasPrefix(t, "// +build") {
			out = append(out, t...)
			out = append(out, 0)
		}
	}
	return out
}

type goListCacheFile struct {
	Key    string `json:"key"`
	Output []byte `json:"output"`
}

func loadGoListCache(path, key string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	var c goListCacheFile
	if json.Unmarshal(b, &c) != nil || c.Key != key || len(c.Output) == 0 {
		return nil, false
	}
	return c.Output, true
}

func saveGoListCache(path, key string, output []byte) {
	b, err := json.Marshal(goListCacheFile{Key: key, Output: output})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	// temp+rename: a concurrent reader must never see torn JSON.
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
}
