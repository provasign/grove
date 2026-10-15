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
func (idx *edgeIndex) pyReexportFiles(file, member string, hops int) []string {
	if member == "" || member == "*" || hops <= 0 {
		return nil
	}
	var out []string
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
			out = append(out, g)
			out = append(out, idx.pyReexportFiles(g, imported, hops-1)...)
		}
	}
	return out
}
