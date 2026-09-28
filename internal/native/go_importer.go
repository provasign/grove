package native

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"sort"
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
}

// newGoProjectImporter loads project packages from source. Everything else
// (standard library, module dependencies) comes from the compiler export data
// `go list -export` reported (exports: import path -> file). importer.Default
// alone looks packages up GOPATH-style and never finds a module dependency, so
// every project package importing one was type-checked partially even with
// the module cache fully populated.
func newGoProjectImporter(packages []goListPackage, exports map[string]string) *goProjectImporter {
	var fallback types.Importer = importer.Default()
	if len(exports) > 0 {
		fallback = exportDataImporter{exports: exports, fallback: fallback,
			gc: importer.ForCompiler(token.NewFileSet(), "gc", func(path string) (io.ReadCloser, error) {
				return os.Open(exports[path])
			})}
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

func (p *goProjectImporter) preloadInterfaceImports(importPaths []string, interfacePackages map[string]bool) []string {
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
			if _, local := p.packages[imported]; !local || !interfacePackages[imported] {
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
	return p.fallback.Import(path)
}

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
	files := make([]*ast.File, 0, len(meta.GoFiles))
	for _, name := range meta.GoFiles {
		file, err := parser.ParseFile(fset, filepath.Join(meta.Dir, name), nil, parser.SkipObjectResolution)
		if file == nil {
			return nil, err
		}
		if err != nil {
			p.partial[path] = err // syntax error: declarations it could read still load
		}
		files = append(files, file)
	}
	conf := types.Config{Importer: p, Error: func(error) {}}
	pkg, err := conf.Check(path, fset, files, nil)
	if pkg == nil {
		return nil, err
	}
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
// listed, and from importer.Default otherwise.
type exportDataImporter struct {
	exports  map[string]string
	gc       types.Importer
	fallback types.Importer
}

func (e exportDataImporter) Import(path string) (*types.Package, error) {
	if e.exports[path] != "" {
		if pkg, err := e.gc.Import(path); err == nil {
			return pkg, nil
		}
	}
	return e.fallback.Import(path)
}
