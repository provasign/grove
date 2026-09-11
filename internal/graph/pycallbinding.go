package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Separate lexical free-function lookup from member lookup. A class method
// is not a binding in its enclosing module; obj.method cannot target an
// unrelated module-level function called method.
func pyCallCandidates(idx *edgeIndex, caller *core.SymbolRecord, qualifier, callName, name string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	if qualifier != "" {
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
