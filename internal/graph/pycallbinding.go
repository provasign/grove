package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Separate lexical free-function lookup from member lookup. A class method
// is not a binding in its enclosing module; obj.method cannot target an
// unrelated module-level function called method.
func pyCallCandidates(idx *edgeIndex, caller *core.SymbolRecord, qualifier, fullQualifier, callName, name string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	if qualifier != "" {
		if files, bound := idx.pyFullModuleBinding(caller, fullQualifier); bound {
			for _, candidate := range cands {
				if files[candidate.FilePath] {
					out = append(out, candidate)
				}
			}
			return out
		}
		_, module := idx.importFilesForQualifierForSymbol(caller, qualifier)
		for _, c := range cands {
			if c.ParentSymbol != "" || module {
				out = append(out, c)
			}
		}
		return out
	}
	// From-imports are recorded as module#member. Respect that binding
	// before same-file preference (a local import can shadow a module def).
	bound := false
	files := map[string]bool{}
	_, _, visible, known := idx.pyImportBinding(caller, callName)
	if mod, member, ok, _ := idx.pyImportBinding(caller, callName); ok && member == name {
		bound = true
		for _, f := range idx.pyModuleFiles(caller.FilePath, mod) {
			files[f] = true
		}
	}
	for _, imp := range caller.Imports {
		if len(idx.pyImportBindings[caller.FilePath]) > 0 {
			break
		}
		mod, member, ok := strings.Cut(imp, "#")
		if !ok || member != name {
			continue
		}
		bound = true
		for _, f := range idx.pyModuleFiles(caller.FilePath, mod) {
			files[f] = true
		}
	}
	var sameFile []*core.SymbolRecord
	for _, c := range cands {
		if c.ParentSymbol != "" || (bound && !files[c.FilePath]) || (known && !visible && c.FilePath != caller.FilePath) {
			continue
		}
		out = append(out, c)
		if c.FilePath == caller.FilePath {
			sameFile = append(sameFile, c)
		}
	}
	if !bound && len(sameFile) > 0 {
		return sameFile
	}
	return out
}

// pyFullModuleBinding resolves the real binding created by `import a.b` while
// preserving Python semantics: the local name is `a`, never the fabricated
// leaf `b`. Astkit retains the full receiver chain, so `a.b.call()` can be
// pinned directly to module a.b without inventing an invalid local name.
func (idx *edgeIndex) pyFullModuleBinding(caller *core.SymbolRecord, qualifier string) (map[string]bool, bool) {
	root := qualifier
	if dot := strings.IndexByte(root, '.'); dot >= 0 {
		root = root[:dot]
	}
	module, member, visible, known := idx.pyImportBinding(caller, root)
	if !known || member != "" {
		return nil, false
	}
	if !visible {
		return map[string]bool{}, true
	}
	// An alias (`import a.b as mod`) is used directly; an unaliased dotted
	// import is spelled by its full module path (`a.b`).
	if qualifier != root && qualifier != module {
		return nil, false
	}
	files := map[string]bool{}
	for _, file := range idx.pyModuleFiles(caller.FilePath, module) {
		files[file] = true
	}
	return files, true
}
