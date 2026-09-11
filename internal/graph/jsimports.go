package graph

import (
	"strings"

	"github.com/provasign/grove/internal/core"
)

func (idx *edgeIndex) jsImportAlias(file, local string) (module, member string, ok bool) {
	target, ok := idx.jsImportAliases[file][local]
	if !ok {
		return "", "", false
	}
	module, member, _ = strings.Cut(target, "#")
	return module, member, true
}

func (idx *edgeIndex) jsImportTargetName(symbol *core.SymbolRecord, local string) (string, bool) {
	module, member, ok := idx.jsImportAlias(symbol.FilePath, local)
	if !ok || member == "" {
		return member, ok
	}
	if member != "default" {
		return member, true
	}
	var found string
	for _, file := range idx.resolveRelativeImport(symbol.FilePath, module) {
		for _, candidate := range idx.byFile[file] {
			isDefault := false
			for _, modifier := range candidate.Modifiers {
				if modifier == "default-export" {
					isDefault = true
					break
				}
			}
			if !isDefault {
				continue
			}
			if found != "" && found != candidate.Name {
				return "", false
			}
			found = candidate.Name
		}
	}
	return found, found != ""
}
