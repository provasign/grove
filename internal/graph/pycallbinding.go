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

// pyDynamicMemberCandidates is the bounded fallback for Python member calls
// whose receiver crosses framework-managed attributes (connection.ops.call)
// and therefore cannot be pinned by imports or local annotations. These edges
// remain heuristic; the fanout cap prevents a common method name from turning
// into a repository-wide guess.
func pyDynamicMemberCandidates(idx *edgeIndex, caller *core.SymbolRecord, name, receiverHint string) ([]*core.SymbolRecord, bool) {
	var out []*core.SymbolRecord
	for _, candidate := range globalCallableCandidates(idx, caller, name) {
		if candidate.ParentSymbol != "" && pyReceiverFileMatches(candidate.FilePath, receiverHint) {
			out = append(out, candidate)
		}
	}
	if len(out) > maxCalleeFanout {
		return nil, true
	}
	return out, false
}

// pyOneHopMemberCandidates follows one additional import layer for dynamic
// receivers. A backend module commonly imports its concrete connection class,
// which in turn imports the operations class that owns the called method.
func pyOneHopMemberCandidates(idx *edgeIndex, caller *core.SymbolRecord, scope map[string]struct{}, name, receiverHint string) ([]*core.SymbolRecord, bool) {
	files := map[string]bool{}
	for file := range scope {
		for imported := range idx.importedFiles(file) {
			files[imported] = true
		}
	}
	var out []*core.SymbolRecord
	for _, candidate := range globalCallableCandidates(idx, caller, name) {
		if candidate.ParentSymbol != "" && files[candidate.FilePath] && pyReceiverFileMatches(candidate.FilePath, receiverHint) {
			out = append(out, candidate)
		}
	}
	if len(out) > maxCalleeFanout {
		return nil, true
	}
	return out, false
}

func pyReceiverFileMatches(file, receiverHint string) bool {
	if receiverHint == "" {
		return true
	}
	base := strings.ToLower(baseNameNoExt(file))
	hint := strings.ToLower(receiverHint)
	if strings.HasPrefix(base, hint) {
		return true
	}
	// Short receiver names are commonly contractions of their module
	// (`ops` → `operations`). Require every character in order so unrelated
	// modules such as schema/compiler do not enter the fallback.
	if len(hint) < 3 {
		return false
	}
	for i, j := 0, 0; i < len(base); i++ {
		if base[i] == hint[j] {
			j++
			if j == len(hint) {
				return true
			}
		}
	}
	return false
}

// pyCallableAliasTarget recognizes a same-name local callable alias established
// before the call, for example `quote_name = connection.ops.quote_name`. The
// latest assignment wins; renamed aliases, arbitrary values, and callable
// parameters remain unresolved because their repository-wide binding is much
// less precise.
func pyCallableAliasTarget(caller *core.SymbolRecord, alias string, callLine int) string {
	if caller == nil || alias == "" || caller.RawText == "" {
		return ""
	}
	lines := strings.Split(caller.RawText, "\n")
	limit := len(lines)
	if callLine > 0 && caller.Span.Start > 0 {
		if off := callLine - caller.Span.Start; off >= 0 && off < limit {
			limit = off
		}
	}
	target := ""
	for _, line := range lines[:limit] {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		eq := strings.IndexByte(line, '=')
		if eq < 0 || strings.Contains(line[:eq+1], "==") || strings.TrimSpace(line[:eq]) != alias {
			continue
		}
		target = "" // a later non-callable assignment shadows an earlier alias
		rhs := strings.TrimSpace(line[eq+1:])
		parts := strings.Split(rhs, ".")
		if len(parts) < 2 {
			continue
		}
		valid := true
		for _, part := range parts {
			if !pyIdentifier(strings.TrimSpace(part)) {
				valid = false
				break
			}
		}
		if valid {
			target = strings.TrimSpace(parts[len(parts)-1])
		}
	}
	if target != alias {
		return ""
	}
	return target
}

// pyReceiverAssignedFromSubscription reports whether the root receiver was
// obtained from a registry/container lookup before the call (`connection =
// connections[db]`). Such values are deliberately dynamic in Python; their
// downstream member calls need the bounded method-family fallback.
func pyReceiverAssignedFromSubscription(caller *core.SymbolRecord, receiver string, callLine int) bool {
	if caller == nil || receiver == "" || caller.RawText == "" {
		return false
	}
	lines := strings.Split(caller.RawText, "\n")
	limit := len(lines)
	if callLine > 0 && caller.Span.Start > 0 {
		if off := callLine - caller.Span.Start; off >= 0 && off < limit {
			limit = off
		}
	}
	matched := false
	for _, line := range lines[:limit] {
		line = strings.TrimSpace(strings.SplitN(line, "#", 2)[0])
		eq := strings.IndexByte(line, '=')
		if eq < 0 || strings.Contains(line[:eq+1], "==") || strings.TrimSpace(line[:eq]) != receiver {
			continue
		}
		rhs := strings.TrimSpace(line[eq+1:])
		matched = strings.Contains(rhs, "[") && strings.Contains(rhs, "]")
	}
	return matched
}

func pyIdentifier(s string) bool {
	if s == "" || !(s[0] == '_' || s[0] >= 'A' && s[0] <= 'Z' || s[0] >= 'a' && s[0] <= 'z') {
		return false
	}
	for i := 1; i < len(s); i++ {
		if c := s[i]; !(c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
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
