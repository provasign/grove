package native

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// Budget: see javaAnalyzer.Budget.
func (goAnalyzer) Budget(files int) time.Duration { return compilerBudget(files) }

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

// ensureGOROOT repairs the build-time-baked GOROOT when it does not exist on
// the running machine. Release binaries bake the CI runner's toolchain path
// (e.g. /Users/runner/hostedtoolcache/...), which silently breaks
// importer.Default()'s type resolution in the semantic pass — released
// binaries produced slightly different native edges than locally-built ones
// (measured: 190 of 6.3M rows on kubernetes). runtime.GOROOT() honors the
// GOROOT env var, so setting it from `go env GOROOT` once restores identical
// behavior across build environments.
var ensureGOROOTOnce sync.Once

func ensureGOROOT() {
	ensureGOROOTOnce.Do(func() {
		if root := os.Getenv("GOROOT"); root != "" {
			return // operator override wins
		}
		if _, err := os.Stat(runtime.GOROOT()); err == nil {
			return // baked path is valid on this machine
		}
		out, err := exec.Command("go", "env", "GOROOT").Output()
		if err != nil {
			return
		}
		if root := strings.TrimSpace(string(out)); root != "" {
			os.Setenv("GOROOT", root)
			// go/build captured runtime.GOROOT() into build.Default at
			// package init, before the env var existed; both importers
			// (gc export data and source) resolve stdlib through it. Without
			// this the release binary still searched the CI runner's
			// /Users/runner/hostedtoolcache/... and skipped every package
			// type-checked from source (gin: 11 packages, incl. binding).
			build.Default.GOROOT = root
		}
	})
}

func (goAnalyzer) Analyze(ctx context.Context, req Request) Result {
	ensureGOROOT()
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
		var stderr strings.Builder
		cmd.Stderr = &stderr
		var err error
		out, err = cmd.Output()
		if err != nil {
			diagnostic := "go list failed: " + err.Error()
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				diagnostic += ": " + detail
			}
			return Result{Diagnostics: []string{diagnostic}}
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
	pkgDirsByImport := map[string][]string{}
	for _, pkg := range pkgs {
		pkgByImport[pkg.ImportPath] = pkg
		if rel, ok := relFile(req.Root, pkg.Dir); ok {
			pkgDirsByImport[pkg.ImportPath] = append(pkgDirsByImport[pkg.ImportPath], rel)
		}
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
	deps, exportDiag := goExportData(ctx, req.Root, topoKey)
	semanticEdges, decls, semanticDiagnostics := goSemanticEdges(ctx, req.Root, semFiles, req.Symbols, pkgs, deps)
	// go/types attributed every declaration in the checked packages; the ones
	// the parser does not index (types declared inside function bodies and
	// their members) become compiler-declared symbols, as javac's do.
	declared := compilerDeclaredSymbolsFor(req, "go", decls)
	if len(declared) > 0 {
		files := map[string]bool{}
		for _, d := range declared {
			files[d.FilePath] = true
		}
		semanticDiagnostics = append(semanticDiagnostics, "go/types declared "+itoa(len(declared))+" symbol(s) in "+itoa(len(files))+" file(s) the parser did not index; added as compiler-declared symbols")
	}
	if exportDiag != "" {
		semanticDiagnostics = append(semanticDiagnostics, exportDiag)
	}
	edges = append(edges, semanticEdges...)
	callSiteEdges := goCallSiteEdges(semSymbols, req.Symbols, pkgDirsByImport)
	edges = append(edges, callSiteEdges...)
	typeUseEdges := goTypeUseEdges(ctx, semSymbols, req.Symbols, pkgDirsByImport)
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
		Symbols:     declared,
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
//
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
		rel, ok := relFile(root, filepath.Join(pkg.Dir, name))
		if !ok {
			continue // outside this checkout
		}
		out = append(out, rel)
	}
	return out
}

type goSymbolIndex struct {
	byFileFunc map[string]core.SymbolRecord
	byFunc     map[string]core.SymbolRecord
	byType     map[string]core.SymbolRecord
	// byBaseFunc keys callables by their directory's last element instead
	// of the whole path, to recognize a project package go list did not map.
	byBaseFunc map[string]bool
}

func newGoSymbolIndex(symbols []core.SymbolRecord) goSymbolIndex {
	idx := goSymbolIndex{
		byFileFunc: map[string]core.SymbolRecord{},
		byFunc:     map[string]core.SymbolRecord{},
		byType:     map[string]core.SymbolRecord{},
		byBaseFunc: map[string]bool{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "go" || goLocalSymbol(symbol) {
			continue
		}
		dir := packageDir(symbol.FilePath)
		switch symbol.Kind {
		case core.KindFunction, core.KindMethod, core.KindConstructor:
			key := goCallableKey(dir, symbol.ParentSymbol, symbol.Name)
			idx.byFunc[key] = symbol
			idx.byFileFunc[symbol.FilePath+"\x00"+key] = symbol
			idx.byBaseFunc[goCallableKey(path.Base(dir), symbol.ParentSymbol, symbol.Name)] = true
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

func goSemanticEdges(ctx context.Context, root string, files []string, symbols []core.SymbolRecord, pkgs []goListPackage, deps goDeps) ([]core.Edge, []compilerDecl, []string) {
	symbolIdx := newGoSymbolIndex(symbols)
	pkgDirsByImport := map[string][]string{}
	importPathByDir := map[string]string{}
	for _, pkg := range pkgs {
		if rel, ok := relFile(root, pkg.Dir); ok {
			pkgDirsByImport[pkg.ImportPath] = append(pkgDirsByImport[pkg.ImportPath], rel)
			importPathByDir[rel] = pkg.ImportPath
		}
	}

	filesByDir := map[string][]string{}
	for _, file := range files {
		filesByDir[packageDir(file)] = append(filesByDir[packageDir(file)], file)
	}

	var edges []core.Edge
	var decls []compilerDecl
	var diagnostics []string
	dirs := make([]string, 0, len(filesByDir))
	for d := range filesByDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	projectImporter := newGoProjectImporter(pkgs, deps)
	var analyzedImports []string
	interfacePackages := map[string]bool{}
	for _, dir := range dirs {
		if path := importPathByDir[dir]; path != "" {
			analyzedImports = append(analyzedImports, path)
		}
	}
	for _, symbol := range symbols {
		if symbol.Language == "go" && symbol.Kind == core.KindInterface {
			if path := importPathByDir[packageDir(symbol.FilePath)]; path != "" {
				interfacePackages[path] = true
			}
		}
	}
	// One line per distinct problem: the same partially-checked import is
	// reached from every package that imports it (grafana: 14 copies).
	seenDiag := map[string]bool{}
	for _, d := range projectImporter.preloadInterfaceImports(analyzedImports, interfacePackages) {
		if !seenDiag[d] {
			seenDiag[d] = true
			diagnostics = append(diagnostics, d)
		}
	}
	// Per-package type-checks are independent and symbolIdx/pkgDirsByImport
	// are read-only here; results land by index so output order (and thus
	// the built graph) is identical to the sequential loop.
	type dirOutcome struct {
		edges []core.Edge
		decls []compilerDecl
		pkg   *types.Package
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
				pkgPath := importPathByDir[dirs[di]]
				if pkgPath == "" {
					pkgPath = dirs[di]
				}
				e, d, pkg, err := goSemanticPackageEdges(root, pkgPath, dirs[di], filesByDir[dirs[di]], symbolIdx, pkgDirsByImport, projectImporter)
				outcomes[di] = dirOutcome{edges: e, decls: d, pkg: pkg, err: err}
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
		decls = append(decls, o.decls...)
		edges = append(edges, goExternalContracts(o.pkg, dirs[di], symbolIdx, pkgDirsByImport)...)
	}
	diagnostics = append(diagnostics, "resolved "+itoa(countEdgesOfType(edges, core.EdgeCalls))+" native call edge(s)")
	diagnostics = append(diagnostics, "resolved "+itoa(countEdgesOfType(edges, core.EdgeUsesType))+" native type-use edge(s)")
	return edges, decls, diagnostics
}

// The checked package is returned for the contracts pass
// (goExternalContracts), which runs once every type-check goroutine is
// done: imported packages are shared and their scopes fill in lazily while
// other goroutines type-check.
func goSemanticPackageEdges(root, pkgPath, dir string, files []string, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, projectImporter types.Importer) ([]core.Edge, []compilerDecl, *types.Package, error) {
	fset := token.NewFileSet()
	parsed := make([]*ast.File, 0, len(files))
	for _, file := range files {
		abs := filepath.Join(root, file)
		// Mid-edit syntax errors: the parser still returns every declaration
		// it could read, and go/types checks those. Dropping the file (or,
		// as before, the whole package) left every call in the package
		// name-matched until the file parsed again.
		f, err := parser.ParseFile(fset, abs, nil, parser.SkipObjectResolution)
		if f == nil {
			if err == nil {
				err = fmt.Errorf("%s: no syntax tree", file)
			}
			return nil, nil, nil, err
		}
		parsed = append(parsed, f)
	}
	info := &types.Info{
		Uses:       map[*ast.Ident]types.Object{},
		Defs:       map[*ast.Ident]types.Object{},
		Selections: map[*ast.SelectorExpr]*types.Selection{},
	}
	conf := types.Config{
		Importer: projectImporter,
		Error:    func(error) {},
	}
	pkg, _ := conf.Check(pkgPath, fset, parsed, info)
	dispatch := newGoInterfaceDispatch(pkg, root, dir, fset, symbolIdx, pkgDirsByImport)
	decls, localTypes := goLocalDecls(fset, files, parsed, info)

	pe := newPositionedEdges()
	marks := resolvedMarks{}
	fieldCalls := resolvedMarks{} // caller -> "dir:Type.Field@line:col" (core.EdgeFuncFieldCall)
	ifaceCalls := resolvedMarks{} // caller -> "pkg.Iface.Method@line:col" (core.EdgeExternalIfaceCall)
	var fieldStores []core.Edge
	add := func(edge core.Edge) { pe.add(edge, 0, 0) }
	lines := goLineReader(fset)
	for _, edge := range dispatch.contractEdges() {
		add(edge)
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
			fnObj, _ := info.Defs[fn.Name].(*types.Func)
			// Function values flowing through this function's parameters,
			// its locals and other functions' results: calls through them
			// join the stores below (see goFuncValueFlow).
			flow := goFuncValueFlow(dir, fn, fnObj, info, pkgDirsByImport, pkg)
			fieldStores = append(fieldStores, goReturnedFuncStores(dir, caller, fn, fnObj, info, symbolIdx, pkgDirsByImport, pkg)...)
			// called holds the identifiers a call names; any other use of
			// a function or method is a value reference (`f := pkg.Do`,
			// `http.HandlerFunc(h)`, `s.Do` passed as a func), which a
			// signature change breaks just like a call. Inspect visits the
			// CallExpr before its Fun, so the set is filled in time.
			called := map[token.Pos]bool{}
			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				switch n := inner.(type) {
				case *ast.CallExpr:
					called[goCalledName(n.Fun)] = true
					// The called name's position: an edit list names that
					// token (javac/tsc do the same).
					line, col := lines(goCalledName(n.Fun))
					call := func(e core.Edge) { pe.add(e, line, col) }
					if anchor, ok := dispatch.interfaceAnchor(n.Fun, info); ok {
						call(core.Edge{From: caller.ID, To: anchor, Type: core.EdgeCalls,
							Confidence: 0.99, Source: core.EvidenceSourceNative, Reason: core.ReasonMethodSet})
					}
					for _, callee := range dispatch.targets(n.Fun, info) {
						if callee.ID != caller.ID {
							call(core.Edge{From: caller.ID, To: callee.ID, Type: core.EdgeCalls,
								Confidence: 0.99, Source: core.EvidenceSourceNative, Reason: core.ReasonMethodSet})
						}
					}
					if key, ok := goFuncFieldKey(dir, n.Fun, info, pkgDirsByImport, pkg); ok {
						fieldCalls.add(caller.ID, key, line, col)
					}
					if key, ok := goExternalIfaceCallKey(n.Fun, info, pkgDirsByImport); ok {
						ifaceCalls.add(caller.ID, key, line, col)
					}
					for _, key := range flow.callKeys(n.Fun, info, dir, pkgDirsByImport, pkg) {
						fieldCalls.add(caller.ID, key, line, col)
					}
					if calleeFn := goCalledFunc(n.Fun, info); calleeFn != nil && !strings.HasSuffix(relPath, "_test.go") {
						// F(..., G, ...): G is stored into F's parameter slot.
						// A test passing G (MatchAll(NoArgs)) makes F call G
						// only under that test; the test's own reference to
						// G already carries the impact.
						for i, arg := range n.Args {
							target, ok := goFuncValueSymbol(dir, arg, info, symbolIdx, pkgDirsByImport, pkg)
							if !ok {
								continue
							}
							if key, ok := goFuncParamKey(dir, calleeFn, i, pkgDirsByImport, pkg); ok {
								fieldStores = append(fieldStores, core.Edge{From: caller.ID, To: target.ID, Type: core.EdgeFuncFieldStore,
									Confidence: 0.9, Source: core.EvidenceSourceNative, Lines: key})
							}
						}
					}
					callee, resolvedOK := goResolveCall(dir, n.Fun, info, symbolIdx, pkgDirsByImport, pkg)
					switch {
					case resolvedOK && callee.ID == caller.ID:
						marks.add(caller.ID, callee.Name, line, col)
					case !resolvedOK:
						if fnObj := goCalledFunc(n.Fun, info); fnObj != nil && goExternalConcrete(dir, fnObj, symbolIdx, pkgDirsByImport, pkg) {
							marks.add(caller.ID, fnObj.Name(), line, col)
						}
					}
					if resolvedOK && callee.ID != caller.ID {
						call(core.Edge{
							From:       caller.ID,
							To:         callee.ID,
							Type:       core.EdgeCalls,
							Confidence: 0.99,
							Source:     core.EvidenceSourceNative,
						})
					}
				case *ast.CompositeLit:
					// &Command{Args: NoArgs}: a function stored into a
					// function-valued field.
					litType := goCompositeLitType(n, info)
					for _, elt := range n.Elts {
						kv, ok := elt.(*ast.KeyValueExpr)
						if !ok {
							continue
						}
						keyIdent, ok := kv.Key.(*ast.Ident)
						if !ok {
							continue
						}
						fieldObj := info.Uses[keyIdent]
						if fieldObj == nil && litType != nil {
							if st, ok := litType.Underlying().(*types.Struct); ok {
								for fi := 0; fi < st.NumFields(); fi++ {
									if st.Field(fi).Name() == keyIdent.Name {
										fieldObj = st.Field(fi)
									}
								}
							}
						}
						if litType == nil {
							continue
						}
						if key, ok := goFieldVarKey(dir, fieldObj, litType, pkgDirsByImport, pkg); ok {
							if target, ok := goFuncValueSymbol(dir, kv.Value, info, symbolIdx, pkgDirsByImport, pkg); ok {
								fieldStores = append(fieldStores, core.Edge{From: caller.ID, To: target.ID, Type: core.EdgeFuncFieldStore,
									Confidence: 0.9, Source: core.EvidenceSourceNative, Lines: key})
							}
						}
					}
				case *ast.AssignStmt:
					// c.Args = NoArgs
					for i, lhs := range n.Lhs {
						if i >= len(n.Rhs) {
							break
						}
						sel, ok := lhs.(*ast.SelectorExpr)
						if !ok {
							continue
						}
						if key, ok := goFuncFieldKey(dir, sel, info, pkgDirsByImport, pkg); ok {
							if target, ok := goFuncValueSymbol(dir, n.Rhs[i], info, symbolIdx, pkgDirsByImport, pkg); ok {
								fieldStores = append(fieldStores, core.Edge{From: caller.ID, To: target.ID, Type: core.EdgeFuncFieldStore,
									Confidence: 0.9, Source: core.EvidenceSourceNative, Lines: key})
							}
						}
					}
				case *ast.Ident:
					if fnObj, ok := info.Uses[n].(*types.Func); ok && !called[n.Pos()] {
						if target, ok := goSymbolForFunc(dir, fnObj, symbolIdx, pkgDirsByImport, pkg); ok && target.ID != caller.ID {
							line, col := lines(n.Pos())
							pe.add(core.Edge{From: caller.ID, To: target.ID, Type: core.EdgeReads,
								Confidence: 0.99, Source: core.EvidenceSourceNative}, line, col)
						}
						return true
					}
					if qualified, ok := localTypes[info.Uses[n]]; ok {
						// A function-local type is declared in this function's
						// own file: its compiler-declared record shares the
						// caller's file and blob.
						add(core.Edge{
							From:       caller.ID,
							To:         caller.FilePath + "::" + qualified + "@" + caller.BlobSHA,
							Type:       core.EdgeUsesType,
							Confidence: 0.97,
							Source:     core.EvidenceSourceNative,
						})
						return true
					}
					if typ, ok := goResolveType(dir, n, info, symbolIdx, pkgDirsByImport, pkg); ok && typ.ID != caller.ID {
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
	out := append(pe.finish(), marks.edges()...)
	for _, e := range fieldCalls.edges() {
		e.Type = core.EdgeFuncFieldCall
		out = append(out, e)
	}
	out = append(out, ifaceCalls.edgesOf(core.EdgeExternalIfaceCall)...)
	return append(out, dedupeEdges(fieldStores)...), decls, pkg, nil
}

// goExternalIfaceCallKey names a call through a library interface's method
// ("net/http.ResponseWriter.Write" for render's w.Write): the graph joins
// it with the project's methods whose external contract lists that
// interface. The predeclared error interface is left out: every
// err.Error() would reach every error type.
func goExternalIfaceCallKey(expr ast.Expr, info *types.Info, pkgDirsByImport map[string][]string) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.MethodVal {
		return "", false
	}
	named, ok := selection.Recv().(*types.Named)
	if !ok {
		return "", false
	}
	if _, isIface := named.Underlying().(*types.Interface); !isIface {
		return "", false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || len(pkgDirsByImport[obj.Pkg().Path()]) > 0 {
		return "", false // error, or a project interface (resolved by dispatch)
	}
	return obj.Pkg().Path() + "." + obj.Name() + "." + selection.Obj().Name(), true
}

// goCompositeLitType is the named type a composite literal spells
// (Cmd{...}, pkg.Cmd{...}), from the type name's object: the pass records
// no expression types.
func goCompositeLitType(lit *ast.CompositeLit, info *types.Info) types.Type {
	var id *ast.Ident
	switch t := lit.Type.(type) {
	case *ast.Ident:
		id = t
	case *ast.SelectorExpr:
		id = t.Sel
	}
	if id == nil {
		return nil
	}
	if tn, ok := info.Uses[id].(*types.TypeName); ok {
		return tn.Type()
	}
	return nil
}

// goFuncFieldKey names the function-valued struct field a selector selects
// ("dir:Type.Field"), when the struct is the project's.
func goFuncFieldKey(dir string, expr ast.Expr, info *types.Info, pkgDirsByImport map[string][]string, selfPkg *types.Package) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	selection := info.Selections[sel]
	if selection == nil || selection.Kind() != types.FieldVal {
		return "", false
	}
	return goFieldVarKey(dir, selection.Obj(), selection.Recv(), pkgDirsByImport, selfPkg)
}

// goFieldVarKey keys a func-typed field object of a project struct type.
func goFieldVarKey(dir string, obj types.Object, owner types.Type, pkgDirsByImport map[string][]string, selfPkg *types.Package) (string, bool) {
	field, ok := obj.(*types.Var)
	if !ok || !field.IsField() {
		return "", false
	}
	if _, isFunc := field.Type().Underlying().(*types.Signature); !isFunc {
		return "", false
	}
	for {
		if ptr, ok := owner.(*types.Pointer); ok {
			owner = ptr.Elem()
			continue
		}
		break
	}
	named, ok := owner.(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", false
	}
	dirs := goObjectDirs(dir, named.Obj().Pkg(), pkgDirsByImport, selfPkg)
	if len(dirs) == 0 {
		return "", false
	}
	return dirs[0] + ":" + named.Obj().Name() + "." + field.Name(), true
}

// goFuncValueSymbol resolves a function-value expression (NoArgs,
// pkg.NoArgs, obj.Method) to the project function it names.
func goFuncValueSymbol(dir string, expr ast.Expr, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) (core.SymbolRecord, bool) {
	var fn *types.Func
	switch v := expr.(type) {
	case *ast.Ident:
		fn, _ = info.Uses[v].(*types.Func)
	case *ast.SelectorExpr:
		if sel := info.Selections[v]; sel != nil {
			fn, _ = sel.Obj().(*types.Func)
		} else {
			fn, _ = info.Uses[v.Sel].(*types.Func)
		}
	}
	if fn == nil {
		return core.SymbolRecord{}, false
	}
	return goSymbolForFunc(dir, fn, symbolIdx, pkgDirsByImport, selfPkg)
}

func dedupeEdges(edges []core.Edge) []core.Edge {
	seen := map[string]bool{}
	out := edges[:0]
	for _, e := range edges {
		k := e.From + "\x00" + e.To + "\x00" + e.Lines
		if !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// goExternalContracts records, on each method of the package's own types,
// the library interfaces (reachable through the package's imports, and
// error) whose contract that method fulfils. Go satisfies interfaces
// implicitly: without this record a change set for gin's
// responseWriter.Write claimed every caller while render's w.Write on an
// http.ResponseWriter reaches it.
func goExternalContracts(pkg *types.Package, dir string, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string) []core.Edge {
	if pkg == nil {
		return nil
	}
	type iface struct {
		name string
		typ  *types.Interface
	}
	var ifaces []iface
	if e, ok := types.Universe.Lookup("error").Type().Underlying().(*types.Interface); ok {
		ifaces = append(ifaces, iface{"error", e})
	}
	// The package's own imports only: their scopes are complete for it,
	// and the set depends on nothing else analyzed in the same run, so an
	// incremental and a full index record the same contracts.
	for _, p := range pkg.Imports() {
		if p != nil && p.Complete() && len(pkgDirsByImport[p.Path()]) == 0 {
			for _, name := range p.Scope().Names() {
				obj, ok := p.Scope().Lookup(name).(*types.TypeName)
				if !ok || !obj.Exported() {
					continue
				}
				it, ok := obj.Type().Underlying().(*types.Interface)
				if !ok || it.NumMethods() == 0 {
					continue
				}
				exported := true
				for i := 0; i < it.NumMethods(); i++ {
					if !it.Method(i).Exported() {
						exported = false
						break
					}
				}
				if exported {
					ifaces = append(ifaces, iface{p.Path() + "." + name, it})
				}
			}
		}
	}
	byMethod := map[string]map[string]bool{} // method symbol ID -> interface names
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types.TypeName)
		if !ok || obj.IsAlias() {
			continue
		}
		named, ok := obj.Type().(*types.Named)
		if !ok {
			continue
		}
		if _, isIface := named.Underlying().(*types.Interface); isIface {
			continue
		}
		ptr := types.NewPointer(named)
		for _, it := range ifaces {
			if !types.Implements(ptr, it.typ) {
				continue
			}
			for i := 0; i < it.typ.NumMethods(); i++ {
				m := it.typ.Method(i).Name()
				sym, ok := symbolIdx.byFunc[goCallableKey(dir, name, m)]
				if !ok {
					continue // promoted from an embedded type
				}
				if byMethod[sym.ID] == nil {
					byMethod[sym.ID] = map[string]bool{}
				}
				byMethod[sym.ID][it.name] = true
			}
		}
	}
	ids := make([]string, 0, len(byMethod))
	for id := range byMethod {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]core.Edge, 0, len(ids))
	for _, id := range ids {
		names := make([]string, 0, len(byMethod[id]))
		for n := range byMethod[id] {
			names = append(names, n)
		}
		sort.Strings(names)
		out = append(out, core.Edge{From: id, To: id, Type: core.EdgeExternalContract, Confidence: 0.99,
			Source: core.EvidenceSourceNative, Lines: strings.Join(names, ",")})
	}
	return out
}

// goCalledFunc is the function or method a call names, if go/types
// resolved one.
func goCalledFunc(expr ast.Expr, info *types.Info) *types.Func {
	switch fun := expr.(type) {
	case *ast.IndexExpr:
		return goCalledFunc(fun.X, info)
	case *ast.IndexListExpr:
		return goCalledFunc(fun.X, info)
	case *ast.ParenExpr:
		return goCalledFunc(fun.X, info)
	case *ast.Ident:
		fn, _ := info.Uses[fun].(*types.Func)
		return fn
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			fn, _ := sel.Obj().(*types.Func)
			return fn
		}
		fn, _ := info.Uses[fun.Sel].(*types.Func)
		return fn
	}
	return nil
}

// goExternalConcrete: fn is a concrete function or method of a package
// outside the project (stdlib or a dependency). An interface method is not
// one: its dynamic target may be a project type. A package go list did not
// map but whose last path element names a project directory declaring the
// same callable is treated as possibly the project's own.
func goExternalConcrete(currentDir string, fn *types.Func, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) bool {
	if fn.Pkg() == nil || goObjectDirs(currentDir, fn.Pkg(), pkgDirsByImport, selfPkg) != nil {
		return false
	}
	sig, ok := fn.Type().(*types.Signature)
	if !ok {
		return false
	}
	if recv := sig.Recv(); recv != nil && types.IsInterface(recv.Type()) {
		return false
	}
	return !symbolIdx.byBaseFunc[goCallableKey(path.Base(fn.Pkg().Path()), goFuncReceiverName(fn), fn.Name())]
}

// goCalledName is the identifier a call names: f(), x.M(), pkg.F[T]().
func goCalledName(fun ast.Expr) token.Pos {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Pos()
	case *ast.SelectorExpr:
		return f.Sel.Pos()
	case *ast.IndexExpr:
		return goCalledName(f.X)
	case *ast.IndexListExpr:
		return goCalledName(f.X)
	case *ast.ParenExpr:
		return goCalledName(f.X)
	}
	return token.NoPos
}

// goLineReader turns a position into its 1-based line and character
// column (UTF-16 units, as javac and tsc count), reading each file once.
func goLineReader(fset *token.FileSet) func(token.Pos) (int, int) {
	files := map[string][]string{}
	return func(pos token.Pos) (int, int) {
		if !pos.IsValid() {
			return 0, 0
		}
		at := fset.Position(pos)
		src, ok := files[at.Filename]
		if !ok {
			if data, err := osReadFile(at.Filename); err == nil {
				src = strings.Split(string(data), "\n")
			}
			files[at.Filename] = src
		}
		if at.Line < 1 || at.Line > len(src) {
			return at.Line, 0
		}
		return at.Line, charColumn(src[at.Line-1], at.Column-1)
	}
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

func goResolveCall(currentDir string, expr ast.Expr, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) (core.SymbolRecord, bool) {
	switch fun := expr.(type) {
	case *ast.IndexExpr:
		return goResolveCall(currentDir, fun.X, info, symbolIdx, pkgDirsByImport, selfPkg)
	case *ast.IndexListExpr:
		return goResolveCall(currentDir, fun.X, info, symbolIdx, pkgDirsByImport, selfPkg)
	case *ast.Ident:
		obj, ok := info.Uses[fun].(*types.Func)
		if !ok {
			return core.SymbolRecord{}, false
		}
		return goSymbolForFunc(currentDir, obj, symbolIdx, pkgDirsByImport, selfPkg)
	case *ast.SelectorExpr:
		if sel := info.Selections[fun]; sel != nil {
			if fn, ok := sel.Obj().(*types.Func); ok {
				return goSymbolForFunc(currentDir, fn, symbolIdx, pkgDirsByImport, selfPkg)
			}
		}
		if fn, ok := info.Uses[fun.Sel].(*types.Func); ok {
			return goSymbolForFunc(currentDir, fn, symbolIdx, pkgDirsByImport, selfPkg)
		}
	}
	return core.SymbolRecord{}, false
}

func goResolveType(currentDir string, ident *ast.Ident, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) (core.SymbolRecord, bool) {
	obj, ok := info.Uses[ident].(*types.TypeName)
	if !ok || goLocalObject(obj) {
		// A function-local type is not the package-level type of the same
		// name; goSemanticPackageEdges links it to its own declaration.
		return core.SymbolRecord{}, false
	}
	dirs := goObjectDirs(currentDir, obj.Pkg(), pkgDirsByImport, selfPkg)
	for _, dir := range dirs {
		if symbol, ok := symbolIdx.byType[dir+"\x00"+obj.Name()]; ok {
			return symbol, true
		}
	}
	return core.SymbolRecord{}, false
}

func goSymbolForFunc(currentDir string, fn *types.Func, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) (core.SymbolRecord, bool) {
	dirs := goObjectDirs(currentDir, fn.Pkg(), pkgDirsByImport, selfPkg)
	recv := goFuncReceiverName(fn)
	for _, dir := range dirs {
		if symbol, ok := symbolIdx.byFunc[goCallableKey(dir, recv, fn.Name())]; ok {
			return symbol, true
		}
	}
	return core.SymbolRecord{}, false
}

// goObjectDirs maps a resolved object's package to the repo-relative
// directories it lives in. selfPkg is the package currently being
// analyzed (from this pass's own types.Config.Check) — its path can
// legitimately be missing from pkgDirsByImport (a synthetic/no-go.mod
// analysis, or a types.Package path that doesn't match go list's module-
// qualified naming for the SAME package), so a same-package reference
// still resolves via currentDir. Any OTHER unmapped, non-nil package is
// external (stdlib or a third-party module): go/types resolved it
// exactly, so guessing "maybe it's the caller's own package" would
// override a precise negative with a wrong positive — html/template.New
// defaulting to currentDir matched gin's own same-directory New(), the
// single biggest gin false positive this pass produced.
func goObjectDirs(currentDir string, pkg *types.Package, pkgDirsByImport map[string][]string, selfPkg *types.Package) []string {
	if pkg == nil {
		// A true universe-scope builtin (len, append, new, ...) has no
		// package at all; these never collide with an indexed symbol name
		// in practice, and the caller's own directory is the only
		// plausible guess left.
		return []string{currentDir}
	}
	if dirs := pkgDirsByImport[pkg.Path()]; len(dirs) > 0 {
		return dirs
	}
	if selfPkg != nil && pkg == selfPkg {
		return []string{currentDir}
	}
	return nil
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
}

func newGoNativeIndex(symbols []core.SymbolRecord) *goNativeIndex {
	idx := &goNativeIndex{
		funcsByName: map[string][]core.SymbolRecord{},
		typesByName: map[string][]core.SymbolRecord{},
		filesByDir:  map[string][]string{},
	}
	seenFile := map[string]bool{}
	for _, symbol := range symbols {
		if symbol.Language != "go" || goLocalSymbol(symbol) {
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
func goCallSiteEdges(callers, all []core.SymbolRecord, pkgDirsByImport map[string][]string) []core.Edge {
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
					if goDirMatchesImport(packageDir(symbol.FilePath), imp, pkgDirsByImport) {
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
	aliased := map[string]bool{}
	for _, imp := range imports {
		if _, path, ok := core.ParseGoImportAlias(imp); ok {
			aliased[path] = true
		}
	}
	for _, imp := range imports {
		if alias, path, ok := core.ParseGoImportAlias(imp); ok {
			if alias == qualifier {
				return path, true
			}
			continue
		}
		if !aliased[imp] && lastPathSegment(imp) == qualifier {
			return imp, true
		}
	}
	return "", false
}

// goDirMatchesImport reports whether a repo-relative package dir is the
// package an import path names (the import carries the module prefix that
// repo-relative dirs lack).
func goDirMatchesImport(dir, imp string, pkgDirsByImport map[string][]string) bool {
	for _, localDir := range pkgDirsByImport[imp] {
		if dir == localDir {
			return true
		}
	}
	return false
}

func goImportScope(caller core.SymbolRecord, idx *goNativeIndex, pkgDirsByImport map[string][]string) map[string]bool {
	scope := map[string]bool{caller.FilePath: true}
	for _, file := range idx.filesByDir[packageDir(caller.FilePath)] {
		scope[file] = true
	}
	for _, imp := range caller.Imports {
		if _, path, ok := core.ParseGoImportAlias(imp); ok {
			imp = path
		}
		for dir, files := range idx.filesByDir {
			if !goDirMatchesImport(dir, imp, pkgDirsByImport) {
				continue
			}
			for _, file := range files {
				scope[file] = true
			}
		}
	}
	return scope
}

// goIdentRe extracts identifier tokens from a masked body in one pass.
var goIdentRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
var goQualifiedIdentRe = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\b`)

// goTypeUseEdges emits lexical type-use edges. Each caller body is masked
// (comments and literals blanked) and tokenized exactly once and tokens are resolved through the type index;
// the previous implementation compiled a fresh regex and re-stripped the
// body for every (caller, type-name) pair, unbounded by the analyzer
// completion (linear cost; the deadline governs subprocesses, not pure-Go passes).
// goTypeUseEdges resolves type references for callers against the FULL
// symbol index — callers may be scoped to changed packages, but their
// targets live anywhere in the repo.
func goTypeUseEdges(ctx context.Context, callers, all []core.SymbolRecord, pkgDirsByImport map[string][]string) []core.Edge {
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
		scope := goImportScope(caller, idx, pkgDirsByImport)
		stripped := maskCode("go", caller.RawText)
		emit := func(target core.SymbolRecord) {
			if target.ID == caller.ID {
				return
			}
			key := caller.ID + "\x00" + target.ID
			if seen[key] {
				return
			}
			seen[key] = true
			edges = append(edges, symbolEdge(caller, target, core.EdgeUsesType, 0.98))
		}
		for _, match := range goQualifiedIdentRe.FindAllStringSubmatch(stripped, -1) {
			imp, ok := goImportedPackageForQualifier(caller.Imports, match[1])
			if !ok {
				continue
			}
			for _, target := range idx.typesByName[match[2]] {
				if goDirMatchesImport(packageDir(target.FilePath), imp, pkgDirsByImport) {
					emit(target)
				}
			}
		}
		// Qualified names were resolved with their import identity above. Remove
		// them before bare-token matching so alias.Type cannot fan out to every
		// imported package that declares Type.
		stripped = goQualifiedIdentRe.ReplaceAllString(stripped, " ")
		seenToken := map[string]bool{}
		for _, token := range goIdentRe.FindAllString(stripped, -1) {
			if seenToken[token] {
				continue
			}
			seenToken[token] = true
			for _, target := range idx.typesByName[token] {
				if !scope[target.FilePath] {
					continue
				}
				emit(target)
			}
		}
	}
	return edges
}

// goContainsType reports whether name appears as a bare or package-qualified
// type token in rawText's code. Retained for direct callers and tests; the
// hot path in goTypeUseEdges tokenizes instead.
func goContainsType(rawText, name string) bool {
	masked := maskCode("go", rawText)
	if containsTypeToken(masked, name) {
		return true
	}
	pattern := regexp.MustCompile(`\b[A-Za-z_][A-Za-z0-9_]*\.` + regexp.QuoteMeta(name) + `\b`)
	return pattern.MatchString(masked)
}

// goListTopologyKey hashes everything go list output depends on: module
// files, the sorted go-file set, and each file's import block.
func goListTopologyKey(root string, files []string, symbols []core.SymbolRecord) string {
	h := sha256.New()
	// Environment and toolchain change go list output (GOOS/GOARCH gate file
	// sets via build tags; GOFLAGS can add -mod=vendor). Known residual: an
	// edited //go:build line rotates file content but not this key — rare,
	// and recoverable with --force or any import-block change.
	// go list reports absolute package directories: a cache written for
	// another checkout (a copied .grove) names that checkout's files.
	h.Write([]byte("root=" + filepath.Clean(root) + ";"))
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
	// temp+rename keeps READERS safe from torn JSON; a unique temp name keeps
	// concurrent WRITERS from truncating each other's file mid-rename (the
	// fixed path+".tmp" name was a writer/writer race).
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if _, werr := tmp.Write(b); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(name)
		return
	}
	if os.Rename(name, path) != nil {
		_ = os.Remove(name)
	}
}

// goExportData maps every package in the build graph of ./... (standard
// library and module dependencies) to its compiler export data, the way
// golang.org/x/tools/go/packages loads dependencies. `go list -export`
// compiles what the build cache lacks: one full compilation, reused through
// GOCACHE afterwards. -e keeps going past packages that do not build (a
// dependency missing from the module cache, a project package mid-edit);
// those simply have no export data and are reported by the type checker.
func goExportData(ctx context.Context, root, topoKey string) (goDeps, string) {
	cachePath := filepath.Join(root, ".grove", "goexport-cache.json")
	for attempt := 0; attempt < 2; attempt++ {
		out, hit := []byte(nil), false
		if attempt == 0 {
			out, hit = loadGoListCache(cachePath, topoKey)
		}
		if !hit {
			var err error
			var timedOut bool
			out, timedOut, err = goListExportBounded(ctx, root, topoKey, cachePath)
			if timedOut {
				// A cold build cache: compiling the whole build graph can
				// take many minutes. Type the dependencies from source this
				// time; the listing keeps compiling in the background and is
				// cached for the next index.
				if src := goSourceDeps(ctx, root); src != nil {
					return goDeps{source: src}, "go list -export still compiling dependencies (cold build cache); this index typed them from source"
				}
				return goDeps{}, "go list -export still compiling dependencies (cold build cache) and the dependency listing failed"
			}
			if err != nil {
				return goDeps{}, err.Error()
			}
		}
		exports := map[string]string{}
		stale := false
		dec := json.NewDecoder(bytesReader(out))
		for {
			var pkg struct{ ImportPath, Export string }
			if err := dec.Decode(&pkg); err != nil {
				if err == io.EOF {
					break
				}
				return goDeps{}, "go list -export JSON decode failed: " + err.Error()
			}
			if pkg.Export == "" {
				continue
			}
			if _, err := os.Stat(pkg.Export); err != nil {
				stale = true // build cache trimmed since the listing was cached
				break
			}
			exports[pkg.ImportPath] = pkg.Export
		}
		if stale && hit {
			continue
		}
		if !hit {
			saveGoListCache(cachePath, topoKey, out)
		}
		return goDeps{exports: exports}, ""
	}
	return goDeps{}, ""
}

// goExportTimeout bounds the wait for `go list -export`: with a warm build
// cache it returns in about a minute on grafana, cold it compiles everything
// (~29 min). GROVE_GO_EXPORT_TIMEOUT (a Go duration) overrides it.
func goExportTimeout() time.Duration {
	if v := os.Getenv("GROVE_GO_EXPORT_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 2 * time.Minute
}

// goListExportBounded runs `go list -export`, waiting at most
// goExportTimeout. Past it, the command keeps running in the background and
// saves its listing to the cache when it finishes (until ctx ends).
func goListExportBounded(ctx context.Context, root, topoKey, cachePath string) ([]byte, bool, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-export", "-json=ImportPath,Export", "./...")
	cmd.Dir = root
	cmd.Env = goAnalyzerEnv(root)
	var stdout bytes.Buffer
	var stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return nil, false, fmt.Errorf("go list -export failed: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			diag := "go list -export failed: " + err.Error()
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				diag += ": " + clip(detail, 300)
			}
			return nil, false, errors.New(diag)
		}
		return stdout.Bytes(), false, nil
	case <-time.After(goExportTimeout()):
		go func() {
			if err := <-done; err == nil {
				saveGoListCache(cachePath, topoKey, stdout.Bytes())
			}
		}()
		return nil, true, nil
	}
}

// goSourceDeps lists the dependency packages' source (`go list -deps`
// without -export compiles nothing) for goSourceImporter.
func goSourceDeps(ctx context.Context, root string) types.Importer {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-deps", "-json=ImportPath,Dir,GoFiles,CgoFiles,ImportMap", "./...")
	cmd.Dir = root
	cmd.Env = goAnalyzerEnv(root)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var pkgs []goDepPackage
	dec := json.NewDecoder(bytesReader(out))
	for {
		var p goDepPackage
		if err := dec.Decode(&p); err != nil {
			break
		}
		pkgs = append(pkgs, p)
	}
	if len(pkgs) == 0 {
		return nil
	}
	return newGoSourceImporter(pkgs)
}

// goFuncSlotKey keys a project function's parameter or result slot for the
// function-value join ("dir:Command.UsageFunc#ret", "dir:MatchAll#p0"),
// stable across edits like the function-field keys.
func goFuncSlotKey(currentDir string, fn *types.Func, slot string, pkgDirsByImport map[string][]string, selfPkg *types.Package) (string, bool) {
	if fn == nil || fn.Pkg() == nil {
		return "", false
	}
	dirs := goObjectDirs(currentDir, fn.Pkg(), pkgDirsByImport, selfPkg)
	if len(dirs) == 0 {
		return "", false
	}
	name := fn.Name()
	if recv := goFuncReceiverName(fn); recv != "" {
		name = recv + "." + name
	}
	return dirs[0] + ":" + name + "#" + slot, true
}

// goFuncParamKey is the slot key of argument i of a call to fn: variadic
// arguments share the last parameter's slot. Only func-typed parameters
// (or a variadic of them) have one.
func goFuncParamKey(currentDir string, fn *types.Func, i int, pkgDirsByImport map[string][]string, selfPkg *types.Package) (string, bool) {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Params().Len() == 0 {
		return "", false
	}
	n := sig.Params().Len()
	if i >= n {
		if !sig.Variadic() {
			return "", false
		}
		i = n - 1
	}
	t := sig.Params().At(i).Type()
	if sig.Variadic() && i == n-1 {
		if sl, ok := t.(*types.Slice); ok {
			t = sl.Elem()
		}
	}
	if _, isFunc := t.Underlying().(*types.Signature); !isFunc {
		return "", false
	}
	return goFuncSlotKey(currentDir, fn, "p"+strconv.Itoa(i), pkgDirsByImport, selfPkg)
}

// goFlow maps a function's func-valued variables to the slots their values
// come from: its own parameters (MatchAll's pargs), a project function's
// result (fn := c.getUsageTemplateFunc()), or a function-valued field
// (completionFn := cmd.ValidArgsFunction).
type goFlow map[*types.Var][]string

// goFuncValueFlow builds the variable sources of one function body. Two
// sweeps let a copy of a copy (`f := g; g := pargs[0]`) resolve.
func goFuncValueFlow(dir string, fn *ast.FuncDecl, fnObj *types.Func, info *types.Info, pkgDirsByImport map[string][]string, selfPkg *types.Package) goFlow {
	flow := goFlow{}
	if fnObj != nil {
		if sig, ok := fnObj.Type().(*types.Signature); ok {
			for i := 0; i < sig.Params().Len(); i++ {
				if key, ok := goFuncParamKey(dir, fnObj, i, pkgDirsByImport, selfPkg); ok {
					flow[sig.Params().At(i)] = []string{key}
				}
			}
		}
	}
	assign := func(lhs ast.Expr, rhs ast.Expr) {
		id, ok := lhs.(*ast.Ident)
		if !ok {
			return
		}
		v, _ := info.Defs[id].(*types.Var)
		if v == nil {
			v, _ = info.Uses[id].(*types.Var)
		}
		if v == nil || v.IsField() {
			return
		}
		for _, k := range flow.exprKeys(rhs, info, dir, pkgDirsByImport, selfPkg) {
			if !containsString(flow[v], k) {
				flow[v] = append(flow[v], k)
			}
		}
	}
	for sweep := 0; sweep < 2; sweep++ {
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.AssignStmt:
				if len(n.Lhs) == len(n.Rhs) {
					for i := range n.Lhs {
						assign(n.Lhs[i], n.Rhs[i])
					}
				}
			case *ast.ValueSpec:
				if len(n.Names) == len(n.Values) {
					for i := range n.Names {
						assign(n.Names[i], n.Values[i])
					}
				}
			case *ast.RangeStmt:
				// for _, parg := range pargs: each element of a variadic
				// func parameter.
				if n.Value != nil {
					if x, ok := n.X.(*ast.Ident); ok {
						if src, _ := info.Uses[x].(*types.Var); src != nil && len(flow[src]) > 0 {
							assign(n.Value, n.X)
						}
					}
				}
			}
			return true
		})
	}
	return flow
}

// exprKeys returns the slots a func-valued expression's value comes from.
func (f goFlow) exprKeys(e ast.Expr, info *types.Info, dir string, pkgDirsByImport map[string][]string, selfPkg *types.Package) []string {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return f.exprKeys(x.X, info, dir, pkgDirsByImport, selfPkg)
	case *ast.Ident:
		if v, _ := info.Uses[x].(*types.Var); v != nil {
			return f[v]
		}
	case *ast.IndexExpr:
		return f.exprKeys(x.X, info, dir, pkgDirsByImport, selfPkg)
	case *ast.SelectorExpr:
		if key, ok := goFuncFieldKey(dir, x, info, pkgDirsByImport, selfPkg); ok {
			return []string{key}
		}
	case *ast.CallExpr:
		if fn := goCalledFunc(x.Fun, info); fn != nil {
			if sig, ok := fn.Type().(*types.Signature); ok && sig.Results().Len() == 1 {
				if _, isFunc := sig.Results().At(0).Type().Underlying().(*types.Signature); isFunc {
					if key, ok := goFuncSlotKey(dir, fn, "ret", pkgDirsByImport, selfPkg); ok {
						return []string{key}
					}
				}
			}
		}
	}
	return nil
}

// callKeys returns the slots a call's function expression draws from: a
// func-valued local or parameter (parg(cmd, args)), an element of one
// (pargs[0](...)), or a function's result called directly (H()(x)).
// Field calls (c.Args(...)) are keyed by goFuncFieldKey already.
func (f goFlow) callKeys(fun ast.Expr, info *types.Info, dir string, pkgDirsByImport map[string][]string, selfPkg *types.Package) []string {
	switch x := fun.(type) {
	case *ast.Ident, *ast.IndexExpr, *ast.ParenExpr, *ast.CallExpr:
		return f.exprKeys(x, info, dir, pkgDirsByImport, selfPkg)
	}
	return nil
}

// goReturnedFuncStores records the project functions fn returns as values
// (`return defaultVersionFunc`) into fn's result slot. Returns inside a
// function literal belong to the literal and are skipped.
func goReturnedFuncStores(dir string, caller core.SymbolRecord, fn *ast.FuncDecl, fnObj *types.Func, info *types.Info, symbolIdx goSymbolIndex, pkgDirsByImport map[string][]string, selfPkg *types.Package) []core.Edge {
	if fnObj == nil {
		return nil
	}
	sig, ok := fnObj.Type().(*types.Signature)
	if !ok || sig.Results().Len() != 1 {
		return nil
	}
	if _, isFunc := sig.Results().At(0).Type().Underlying().(*types.Signature); !isFunc {
		return nil
	}
	key, ok := goFuncSlotKey(dir, fnObj, "ret", pkgDirsByImport, selfPkg)
	if !ok {
		return nil
	}
	var out []core.Edge
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncLit:
			return false
		case *ast.ReturnStmt:
			if len(n.Results) == 1 {
				if target, ok := goFuncValueSymbol(dir, n.Results[0], info, symbolIdx, pkgDirsByImport, selfPkg); ok {
					out = append(out, core.Edge{From: caller.ID, To: target.ID, Type: core.EdgeFuncFieldStore,
						Confidence: 0.9, Source: core.EvidenceSourceNative, Lines: key})
				}
			}
		}
		return true
	})
	return out
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
