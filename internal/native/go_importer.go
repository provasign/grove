package native

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
)

// goProjectImporter makes packages selected by `go list` available from
// source. importer.Default cannot load an unbuilt sibling package in the same
// module, which breaks otherwise valid cross-package interface information.
type goProjectImporter struct {
	packages   map[string]goListPackage
	loaded     map[string]*types.Package
	failed     map[string]error
	partial    map[string]error // loaded, but type-checked with errors
	loading    map[string]bool
	sealed     bool
	fallback   types.Importer
	fallbackMu sync.Mutex
	// root is the repository (for typing dependencies from source when the
	// export data is unreadable); exportUnreadable is the first such error.
	root             string
	exportUnreadable string
}

// newGoProjectImporter loads project packages from source. Everything else
// (standard library, module dependencies) comes from the compiler export data
// `go list -export` reported (exports: import path -> file). importer.Default
// alone looks packages up GOPATH-style and never finds a module dependency, so
// every project package importing one was type-checked partially even with
// the module cache fully populated.
func newGoProjectImporter(packages []goListPackage, deps goDeps) *goProjectImporter {
	var fallback types.Importer = importer.Default()
	exports := deps.exports
	if len(exports) > 0 {
		fallback = &exportDataImporter{exports: exports, fallback: fallback, root: deps.root, listed: map[string]bool{},
			gc: importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
				return os.Open(exports[path])
			})}
	} else if deps.source != nil {
		fallback = deps.source
	}
	p := &goProjectImporter{
		packages: map[string]goListPackage{}, loaded: map[string]*types.Package{},
		loading: map[string]bool{}, failed: map[string]error{}, partial: map[string]error{}, fallback: fallback,
	}
	for _, pkg := range packages {
		p.packages[pkg.ImportPath] = pkg
	}
	return p
}

// preloadProjectImports type-checks, from source, every project package an
// analyzed package imports, then seals the importer so the parallel
// per-package checks only read it.
//
// It used to load only packages that declare an interface (2026-09-06, for
// imported interface contracts). Every other project import then failed
// ("not selected for interface loading"), so a call on a value of another
// project package's type had no selection: the compiler pass resolved no
// cross-package method call, and name matching linked each one to every
// same-named method (prism: graphui and httpapi Server.Handler, 2026-11-22).
func (p *goProjectImporter) preloadProjectImports(importPaths []string) []string {
	defer func() { p.sealed = true }()
	paths := append([]string(nil), importPaths...)
	sort.Strings(paths)
	var diagnostics []string
	for _, path := range paths {
		pkg, ok := p.packages[path]
		if !ok {
			continue
		}
		imports := append(append([]string(nil), pkg.Imports...), pkg.TestImports...)
		sort.Strings(imports)
		for _, imported := range imports {
			if _, local := p.packages[imported]; !local {
				continue
			}
			if _, err := p.load(imported); err != nil {
				diagnostics = append(diagnostics, "project import "+imported+" skipped: "+err.Error())
			} else if perr := p.partial[imported]; perr != nil {
				diagnostics = append(diagnostics, "project import "+imported+" type-checked partially: "+perr.Error())
			}
		}
	}
	return diagnostics
}

func (p *goProjectImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := p.loaded[path]; ok {
		return pkg, nil
	}
	if err, failed := p.failed[path]; failed {
		return nil, err
	}
	if _, local := p.packages[path]; local {
		if p.sealed {
			return nil, fmt.Errorf("project source package %s was not selected for interface loading", path)
		}
		return p.load(path)
	}
	p.fallbackMu.Lock()
	defer p.fallbackMu.Unlock()
	pkg, err, panicked := importRecovering(p.fallback, path)
	if !panicked {
		return pkg, err
	}
	// The stdlib export-data reader panics on export data from a newer Go
	// than this binary was built with (a prism built with go1.26 reading a
	// go1.27 build cache: internal/pkgbits). Type the dependencies from
	// source for the rest of this pass instead of crashing the index.
	if p.exportUnreadable == "" {
		p.exportUnreadable = err.Error()
		if src := goSourceDeps(context.Background(), p.root); src != nil {
			p.fallback = src
		} else {
			p.fallback = failingImporter{err}
		}
	}
	pkg, err, panicked = importRecovering(p.fallback, path)
	if panicked {
		p.fallback = failingImporter{err}
	}
	return pkg, err
}

// importRecovering calls imp.Import and turns a panic into an error.
func importRecovering(imp types.Importer, path string) (pkg *types.Package, err error, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			pkg, panicked = nil, true
			err = fmt.Errorf("export data for %s unreadable by this build (%s): %v", path, runtime.Version(), r)
		}
	}()
	pkg, err = imp.Import(path)
	return pkg, err, false
}

type failingImporter struct{ err error }

func (f failingImporter) Import(string) (*types.Package, error) { return nil, f.err }

func (p *goProjectImporter) load(path string) (loaded *types.Package, err error) {
	if pkg, ok := p.loaded[path]; ok {
		return pkg, nil
	}
	if err, failed := p.failed[path]; failed {
		return nil, err
	}
	if p.loading[path] {
		return nil, fmt.Errorf("import cycle involving %s", path)
	}
	meta, ok := p.packages[path]
	if !ok {
		return p.Import(path)
	}
	p.loading[path] = true
	defer func() {
		delete(p.loading, path)
		if err != nil {
			p.failed[path] = err
		}
	}()
	for _, imported := range meta.Imports {
		if _, local := p.packages[imported]; local {
			if _, err := p.load(imported); err != nil {
				return nil, err
			}
		}
	}
	fset := token.NewFileSet()
	// cgo files join the ordinary Go files (the dependency source importer
	// below does the same); FakeImportC handles their `import "C"`.
	names := append(append([]string(nil), meta.GoFiles...), meta.CgoFiles...)
	files := make([]*ast.File, 0, len(names))
	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(meta.Dir, name), nil, parser.SkipObjectResolution)
		if file == nil {
			return nil, err
		}
		if err != nil {
			p.partial[path] = err // syntax error: declarations it could read still load
		}
		files = append(files, file)
	}
	// Under FakeImportC every C.x is untyped, so expressions using one
	// (int(t.n) for a C.int field) report type errors that only say cgo
	// was not run. Those are expected and do not make the package partial;
	// the first error elsewhere does.
	cgoFiles := map[string]bool{}
	for _, name := range meta.CgoFiles {
		cgoFiles[filepath.Join(meta.Dir, name)] = true
	}
	var firstErr error
	conf := types.Config{Importer: p, FakeImportC: true, Error: func(e error) {
		if te, ok := e.(types.Error); ok && cgoFiles[te.Fset.Position(te.Pos).Filename] {
			return
		}
		if firstErr == nil {
			firstErr = e
		}
	}}
	pkg, err := conf.Check(path, fset, files, nil)
	if pkg == nil {
		return nil, err
	}
	err = firstErr
	// A type error (typically one unresolvable third-party import: gin's
	// binding imports go.mongodb.org/mongo-driver, absent from a fresh
	// module cache) does not void the package. go/types checked everything
	// else; importers of this package still need its declared types, or
	// every package above it loses them too (gin: 6 packages skipped).
	if err != nil {
		p.partial[path] = err
	}
	p.loaded[path] = pkg
	return pkg, nil
}

// exportDataImporter imports from `go list -export` data when the package was
// listed, and from importer.Default otherwise. The listing covers the build
// graph of ./..., which leaves out packages only tests import: a module
// imported by nothing but an external test package (client_golang's
// api/prometheus/v1/example_test.go importing prometheus/common/config,
// 2026-11-13) left that test untyped. Such a path is listed on first use,
// with its own dependencies; that compiles only what the tests add.
// Not safe for concurrent use; the project importer calls it under a mutex.
type exportDataImporter struct {
	exports  map[string]string
	gc       types.Importer
	fallback types.Importer
	root     string
	listed   map[string]bool
}

func (e *exportDataImporter) Import(path string) (*types.Package, error) {
	if e.exports[path] == "" && e.root != "" && !e.listed[path] && strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
		e.listed[path] = true
		for p, file := range goListExportPaths(e.root, path) {
			if e.exports[p] == "" {
				e.exports[p] = file
			}
		}
	}
	if e.exports[path] != "" {
		if pkg, err := e.gc.Import(path); err == nil {
			return pkg, nil
		}
	}
	return e.fallback.Import(path)
}

// goListExportPaths lists one import path and its dependencies with
// compiler export data, bounded by goExportTimeout. Failure lists nothing.
func goListExportPaths(root, path string) map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), goExportTimeout())
	defer cancel()
	cmd := goCommand(ctx, root, "list", "-e", "-deps", "-export", "-json=ImportPath,Export", "--", path)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	found := map[string]string{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct{ ImportPath, Export string }
		if dec.Decode(&pkg) != nil {
			break
		}
		if pkg.Export != "" {
			found[pkg.ImportPath] = pkg.Export
		}
	}
	return found
}

// goDeps is how dependency packages (standard library, modules) get their
// types: compiler export data from `go list -export`, or, when that would
// first have to compile the whole build graph (a cold build cache: grafana
// ~29 min), type-checking their source with function bodies ignored.
type goDeps struct {
	exports map[string]string
	source  types.Importer
	root    string // where test-only imports missing from exports are listed
}

// goDepPackage is one dependency's source, as `go list -deps -json` lists it.
type goDepPackage struct {
	ImportPath string
	Dir        string
	GoFiles    []string
	CgoFiles   []string
	ImportMap  map[string]string
}

// goSourceImporter type-checks dependency packages from source, declarations
// only (IgnoreFuncBodies), the way gopls does for packages it has no export
// data for: no compilation, no codegen. cgo is faked (FakeImportC): C.x is
// untyped, everything else is checked. Imports go through ImportMap
// (vendored paths). Not safe for concurrent use; the project importer calls
// its fallback under a mutex.
type goSourceImporter struct {
	fset    *token.FileSet
	pkgs    map[string]goDepPackage
	loaded  map[string]*types.Package
	loading map[string]bool
}

func newGoSourceImporter(pkgs []goDepPackage) *goSourceImporter {
	g := &goSourceImporter{fset: token.NewFileSet(), pkgs: map[string]goDepPackage{},
		loaded: map[string]*types.Package{}, loading: map[string]bool{}}
	for _, p := range pkgs {
		g.pkgs[p.ImportPath] = p
	}
	return g
}

func (g *goSourceImporter) Import(path string) (*types.Package, error) {
	return g.importFrom(path, "")
}

func (g *goSourceImporter) importFrom(path, from string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if from != "" {
		if mapped := g.pkgs[from].ImportMap[path]; mapped != "" {
			path = mapped
		}
	}
	if pkg, ok := g.loaded[path]; ok {
		return pkg, nil
	}
	info, ok := g.pkgs[path]
	if !ok {
		return nil, fmt.Errorf("go source importer: %s not in the dependency listing", path)
	}
	if g.loading[path] {
		return nil, fmt.Errorf("go source importer: import cycle through %s", path)
	}
	g.loading[path] = true
	defer delete(g.loading, path)
	var files []*ast.File
	for _, name := range append(append([]string(nil), info.GoFiles...), info.CgoFiles...) {
		f, err := parser.ParseFile(g.fset, filepath.Join(info.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil && f == nil {
			continue
		}
		files = append(files, f)
	}
	conf := types.Config{
		Importer:         importerFunc(func(p string) (*types.Package, error) { return g.importFrom(p, path) }),
		IgnoreFuncBodies: true,
		FakeImportC:      true,
		Error:            func(error) {},
	}
	pkg, _ := conf.Check(path, g.fset, files, nil)
	if pkg == nil {
		return nil, fmt.Errorf("go source importer: %s did not type-check", path)
	}
	g.loaded[path] = pkg
	return pkg, nil
}

type importerFunc func(string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }
