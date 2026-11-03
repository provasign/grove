package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

type pyImportBinding struct {
	line   int
	target string
	owner  string
}

func (idx *edgeIndex) assignPyImportOwners() {
	for file, locals := range idx.pyImportBindings {
		for local, bindings := range locals {
			for i := range bindings {
				bestSize := int(^uint(0) >> 1)
				for _, symbol := range idx.byFile[file] {
					// The synthetic module body spans the whole file, but it is
					// not a lexical function scope. Imports owned by it are module
					// bindings and must remain visible to every real declaration.
					if symbol.Name == "<top-level>" {
						continue
					}
					if bindings[i].line < symbol.Span.Start || bindings[i].line > symbol.Span.End {
						continue
					}
					switch symbol.Kind {
					case core.KindFunction, core.KindMethod, core.KindConstructor, core.KindClass:
					default:
						continue
					}
					if size := symbol.Span.End - symbol.Span.Start; size < bestSize {
						bestSize = size
						bindings[i].owner = symbol.ID
					}
				}
			}
			locals[local] = bindings
		}
	}
}

// pyImportBinding returns the binding visible in symbol. A function/class-local
// binding wins over a module binding. known distinguishes an out-of-scope local
// import from a name that is not imported anywhere in the file.
func (idx *edgeIndex) pyImportBinding(symbol *core.SymbolRecord, local string) (module, member string, visible, known bool) {
	bindings := idx.pyImportBindings[symbol.FilePath][local]
	if len(bindings) == 0 {
		return "", "", false, false
	}
	selectTarget := func(owner string) (string, bool) {
		target := ""
		for _, binding := range bindings {
			if binding.owner != owner {
				continue
			}
			if target != "" && target != binding.target {
				return "", false
			}
			target = binding.target
		}
		return target, target != ""
	}
	target, ok := selectTarget(symbol.ID)
	if !ok {
		target, ok = selectTarget("")
	}
	if !ok {
		return "", "", false, true
	}
	module, member, _ = strings.Cut(target, "#")
	return module, member, true, true
}

// pyReexportFiles follows a module-level re-export of member in file (an
// import of member at module level, `from ._base import X as X` or plain
// `from ._base import X`) to the files of the module it comes from, up to
// hops levels.
//
// A package import names every file of the package's directory, so each hop
// can fan out to a whole directory: unmemoized, the walk grew as the cube of
// the directory size, with every file repeated many times over (a
// 10,000-file Python project ran for minutes and 1.6 GB without finishing).
// Results are memoized per (file, member, hops), on the index like
// importedFilesCache, and each list holds a file once; the set of files is
// the one the plain walk returns.
func (idx *edgeIndex) pyReexportFiles(file, member string, hops int) []string {
	if member == "" || member == "*" || hops <= 0 {
		return nil
	}
	key := pyReexportKey{file, member, hops}
	if cached, ok := idx.pyReexportCache[key]; ok {
		return cached
	}
	seen := map[string]bool{}
	var out []string
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, b := range idx.pyImportBindings[file][member] {
		if b.owner != "" {
			continue // a function-local import re-exports nothing
		}
		module, imported, _ := strings.Cut(b.target, "#")
		if imported == "" {
			continue
		}
		for _, g := range idx.pyModuleFiles(file, module) {
			if g == file {
				continue
			}
			add(g)
			for _, h := range idx.pyReexportFiles(g, imported, hops-1) {
				add(h)
			}
		}
	}
	if idx.pyReexportCache == nil {
		idx.pyReexportCache = map[pyReexportKey][]string{}
	}
	idx.pyReexportCache[key] = out
	return out
}

// pyReexportKey is one step of a re-export walk.
type pyReexportKey struct {
	file, member string
	hops         int
}
