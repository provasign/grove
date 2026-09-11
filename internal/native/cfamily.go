package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type cFamilyAnalyzer struct{}

func (cFamilyAnalyzer) Name() string { return "c-cpp" }

func (cFamilyAnalyzer) Languages() []string { return []string{"c", "cpp"} }

func (cFamilyAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "compile_commands.json") {
		return Availability{Reason: "no compile_commands.json"}
	}
	return Availability{Available: true}
}

type compileCommand struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments"`
	Command   string   `json:"command"`
}

func (cFamilyAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	data, err := osReadFile(filepath.Join(req.Root, "compile_commands.json"))
	if err != nil {
		return Result{Diagnostics: []string{"compile_commands.json read failed: " + err.Error()}}
	}
	var commands []compileCommand
	if err := json.Unmarshal(data, &commands); err != nil {
		return Result{Diagnostics: []string{"compile_commands.json decode failed: " + err.Error()}}
	}
	includeDirs := cIncludeDirs(req.Root, commands)
	fileScope := fileSet(req.Files)
	var edges []core.Edge
	includeTargets := map[string][]string{}
	for _, file := range req.Files {
		content, err := osReadFile(filepath.Join(req.Root, file))
		if err != nil {
			continue
		}
		for _, inc := range cIncludes(string(content)) {
			if target, ok := resolveCInclude(req.Root, file, inc, includeDirs, fileScope); ok {
				edges = append(edges, nativeImportEdge(file, target, 0.95))
				includeTargets[file] = append(includeTargets[file], target)
			}
		}
	}
	includeCount := len(edges)
	semanticEdges := cFamilySemanticEdges(req.Symbols, includeTargets)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"compile_commands loaded " + itoa(len(commands)) + " command(s)",
			"resolved " + itoa(includeCount) + " native include edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}

func cIncludeDirs(root string, commands []compileCommand) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		rel, ok := relFile(root, dir)
		if !ok {
			return
		}
		if !seen[rel] {
			seen[rel] = true
			dirs = append(dirs, rel)
		}
	}
	for _, cmd := range commands {
		args := cmd.Arguments
		if len(args) == 0 && cmd.Command != "" {
			args = strings.Fields(cmd.Command)
		}
		base := cmd.Directory
		if base == "" {
			base = root
		}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "-I" || arg == "-isystem" || arg == "/I":
				if i+1 < len(args) {
					add(resolveAgainst(base, args[i+1]))
					i++
				}
			case strings.HasPrefix(arg, "-I") && len(arg) > 2:
				add(resolveAgainst(base, strings.TrimPrefix(arg, "-I")))
			case strings.HasPrefix(arg, "/I") && len(arg) > 2:
				add(resolveAgainst(base, strings.TrimPrefix(arg, "/I")))
			}
		}
	}
	return dirs
}

var cIncludePattern = regexp.MustCompile(`(?m)^\s*#\s*include\s+["<]([^">]+)[">]`)

func cIncludes(content string) []string {
	matches := cIncludePattern.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 && match[1] != "" {
			out = append(out, match[1])
		}
	}
	return out
}

func resolveCInclude(root, fromFile, inc string, includeDirs []string, fileScope map[string]bool) (string, bool) {
	candidates := []string{
		filepath.ToSlash(filepath.Join(packageDir(fromFile), inc)),
		filepath.ToSlash(inc),
	}
	for _, dir := range includeDirs {
		candidates = append(candidates, filepath.ToSlash(filepath.Join(dir, inc)))
	}
	for _, cand := range candidates {
		cand = filepath.ToSlash(filepath.Clean(cand))
		if fileScope[cand] {
			return cand, true
		}
		if rel, ok := relFile(root, filepath.Join(root, cand)); ok && fileScope[rel] {
			return rel, true
		}
	}
	return "", false
}

func resolveAgainst(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

type cFamilyIndex struct {
	byFile       map[string][]core.SymbolRecord
	typesByName  map[string][]core.SymbolRecord
	methods      map[string][]core.SymbolRecord
	functions    map[string][]core.SymbolRecord
	ctors        map[string][]core.SymbolRecord
	methodsByCls map[string][]core.SymbolRecord
}

func newCFamilyIndex(symbols []core.SymbolRecord) cFamilyIndex {
	idx := cFamilyIndex{
		byFile:       map[string][]core.SymbolRecord{},
		typesByName:  map[string][]core.SymbolRecord{},
		methods:      map[string][]core.SymbolRecord{},
		functions:    map[string][]core.SymbolRecord{},
		ctors:        map[string][]core.SymbolRecord{},
		methodsByCls: map[string][]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "c" && symbol.Language != "cpp" {
			continue
		}
		idx.byFile[symbol.FilePath] = append(idx.byFile[symbol.FilePath], symbol)
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		switch symbol.Kind {
		case core.KindFunction:
			idx.functions[symbol.Name] = append(idx.functions[symbol.Name], symbol)
		case core.KindMethod:
			key := symbol.ParentSymbol + "::" + symbol.Name
			idx.methods[key] = append(idx.methods[key], symbol)
			idx.methodsByCls[symbol.ParentSymbol] = append(idx.methodsByCls[symbol.ParentSymbol], symbol)
		case core.KindConstructor:
			idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
		}
	}
	return idx
}

func cFamilySemanticEdges(symbols []core.SymbolRecord, includeTargets map[string][]string) []core.Edge {
	idx := newCFamilyIndex(symbols)
	var edges []core.Edge
	seen := map[string]bool{}
	add := func(edge core.Edge) {
		key := edge.From + "\x00" + string(edge.Type) + "\x00" + edge.To
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, edge)
	}
	for _, caller := range symbols {
		if (caller.Language != "c" && caller.Language != "cpp") || caller.RawText == "" || !callableKind(caller.Kind) {
			continue
		}
		// Call edges intentionally NOT emitted here (same lesson as the Java,
		// Rust, C#, and PHP native passes): text matching edged every
		// same-named callable it saw. The graph layer's call-site resolution
		// owns calls; this pass keeps only the type-usage evidence text
		// matching is still reliable for.
		scopeFiles := cFamilyScopeFiles(caller.FilePath, includeTargets)
		for _, file := range scopeFiles {
			for _, target := range idx.byFile[file] {
				if target.ID == caller.ID {
					continue
				}
				if typeKind(target.Kind) && cFamilyContainsType(caller, target, file != caller.FilePath) {
					add(symbolEdge(caller, target, core.EdgeUsesType, 0.91))
				}
			}
		}
		for _, typeName := range cFamilyConstructedTypes(caller.RawText) {
			if target, ok := cFamilyBestType(idx, typeName, caller); ok && target.ID != caller.ID {
				add(symbolEdge(caller, target, core.EdgeUsesType, 0.93))
			}
		}
	}
	return edges
}

func cFamilyScopeFiles(fromFile string, includeTargets map[string][]string) []string {
	seen := map[string]bool{fromFile: true}
	out := []string{fromFile}
	for _, target := range includeTargets[fromFile] {
		if !seen[target] {
			seen[target] = true
			out = append(out, target)
		}
	}
	return out
}

func cFamilyContainsCallable(rawText string, target core.SymbolRecord, crossFile bool) bool {
	if !crossFile {
		return containsCall(rawText, target.Name)
	}
	if target.ParentSymbol != "" {
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(target.ParentSymbol) + `::` + regexp.QuoteMeta(target.Name) + `\s*\(`)
		if pattern.MatchString(stripQuotedText(rawText)) {
			return true
		}
	}
	return containsCall(rawText, target.Name)
}

func cFamilyContainsType(caller, target core.SymbolRecord, crossFile bool) bool {
	clean := stripCStyleComments(stripQuotedText(caller.RawText))
	if strings.Contains(target.QualifiedName, "::") {
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(target.QualifiedName) + `\b`)
		if pattern.MatchString(clean) {
			return true
		}
		callerScope := caller.QualifiedName
		if strings.Contains(caller.ParentSymbol, "::") {
			callerScope = caller.ParentSymbol
		}
		if split := strings.LastIndex(callerScope, "::"); split >= 0 &&
			target.QualifiedName == callerScope[:split]+"::"+target.Name {
			return containsTypeToken(clean, target.Name)
		}
		for _, annotation := range caller.Annotations {
			if namespace, ok := core.ParseCppUsingNamespace(annotation); ok && target.QualifiedName == namespace+"::"+target.Name {
				return containsTypeToken(clean, target.Name)
			}
			if local, qualified, ok := core.ParseCppUsingType(annotation); ok && qualified == target.QualifiedName {
				return containsTypeToken(clean, local)
			}
			if local, namespace, ok := core.ParseCppNamespaceAlias(annotation); ok && strings.HasPrefix(target.QualifiedName, namespace+"::") {
				aliased := local + strings.TrimPrefix(target.QualifiedName, namespace)
				return containsTypeToken(clean, aliased)
			}
		}
		return false
	}
	if !crossFile {
		return containsTypeToken(clean, target.Name)
	}
	if target.ParentSymbol != "" {
		pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(target.ParentSymbol) + `::` + regexp.QuoteMeta(target.Name) + `\b`)
		if pattern.MatchString(clean) {
			return true
		}
	}
	return containsTypeToken(clean, target.Name)
}

func stripCStyleComments(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	for i := 0; i < len(text); {
		if i+1 < len(text) && text[i] == '/' && text[i+1] == '/' {
			for i < len(text) && text[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(text) && text[i] == '/' && text[i+1] == '*' {
			i += 2
			for i+1 < len(text) && !(text[i] == '*' && text[i+1] == '/') {
				if text[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			if i+1 < len(text) {
				i += 2
			}
			continue
		}
		out.WriteByte(text[i])
		i++
	}
	return out.String()
}

type cFamilyQualifiedCall struct {
	Qualifier string
	Method    string
}

var cFamilyQualifiedCallPattern = regexp.MustCompile(`\b([A-Z_][A-Za-z0-9_]*)::([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func cFamilyQualifiedCalls(rawText string) []cFamilyQualifiedCall {
	matches := cFamilyQualifiedCallPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	out := make([]cFamilyQualifiedCall, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			out = append(out, cFamilyQualifiedCall{Qualifier: match[1], Method: match[2]})
		}
	}
	return out
}

var cFamilyConstructorPattern = regexp.MustCompile(`\b(?:new\s+)?([A-Z_][A-Za-z0-9_]*(?:::[A-Za-z_][A-Za-z0-9_]*)*)\s*\(`)

func cFamilyConstructedTypes(rawText string) []string {
	matches := cFamilyConstructorPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		name := match[1]
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func cFamilyBestType(idx cFamilyIndex, name string, caller core.SymbolRecord) (core.SymbolRecord, bool) {
	for _, annotation := range caller.Annotations {
		if local, target, ok := core.ParseCppUsingType(annotation); ok && local == name {
			name = target
			break
		}
		if local, target, ok := core.ParseCppNamespaceAlias(annotation); ok && strings.HasPrefix(name, local+"::") {
			name = target + strings.TrimPrefix(name, local)
			break
		}
	}
	simple := name
	if i := strings.LastIndex(name, "::"); i >= 0 {
		simple = name[i+2:]
	}
	candidates := idx.typesByName[simple]
	if len(candidates) == 0 {
		return core.SymbolRecord{}, false
	}
	if simple != name {
		for _, candidate := range candidates {
			if candidate.QualifiedName == name {
				return candidate, true
			}
		}
		return core.SymbolRecord{}, false
	}
	callerScope := caller.QualifiedName
	if strings.Contains(caller.ParentSymbol, "::") {
		callerScope = caller.ParentSymbol
	}
	if split := strings.LastIndex(callerScope, "::"); split >= 0 {
		qualified := callerScope[:split] + "::" + simple
		for _, candidate := range candidates {
			if candidate.QualifiedName == qualified {
				return candidate, true
			}
		}
	}
	var imported *core.SymbolRecord
	for _, annotation := range caller.Annotations {
		namespace, ok := core.ParseCppUsingNamespace(annotation)
		if !ok {
			continue
		}
		qualified := namespace + "::" + simple
		for i := range candidates {
			if candidates[i].QualifiedName != qualified {
				continue
			}
			if imported != nil && imported.ID != candidates[i].ID {
				return core.SymbolRecord{}, false
			}
			candidate := candidates[i]
			imported = &candidate
		}
	}
	if imported != nil {
		return *imported, true
	}
	for _, candidate := range candidates {
		if candidate.FilePath == caller.FilePath {
			return candidate, true
		}
	}
	fromDir := packageDir(caller.FilePath)
	for _, candidate := range candidates {
		if packageDir(candidate.FilePath) == fromDir {
			return candidate, true
		}
	}
	return candidates[0], true
}
