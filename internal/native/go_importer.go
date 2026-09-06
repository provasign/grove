package native

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
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
	loading    map[string]bool
	sealed     bool
	fallback   types.Importer
	fallbackMu sync.Mutex
}

func newGoProjectImporter(packages []goListPackage) *goProjectImporter {
	p := &goProjectImporter{
		packages: map[string]goListPackage{}, loaded: map[string]*types.Package{},
		loading: map[string]bool{}, failed: map[string]error{}, fallback: importer.Default(),
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
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	conf := types.Config{Importer: p, Error: func(error) {}}
	pkg, err := conf.Check(path, fset, files, nil)
	if err != nil {
		return nil, err
	}
	p.loaded[path] = pkg
	return pkg, nil
}
