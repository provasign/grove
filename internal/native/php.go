package native

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type phpAnalyzer struct{}

func (phpAnalyzer) Name() string { return "php" }

func (phpAnalyzer) Languages() []string { return []string{"php"} }

func (phpAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "composer.json") {
		return Availability{Reason: "no composer.json"}
	}
	return Availability{Available: true}
}

type composerJSON struct {
	Autoload struct {
		PSR4 map[string]any `json:"psr-4"`
	} `json:"autoload"`
	AutoloadDev struct {
		PSR4 map[string]any `json:"psr-4"`
	} `json:"autoload-dev"`
}

func (phpAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	data, err := osReadFile(filepath.Join(req.Root, "composer.json"))
	if err != nil {
		return Result{Diagnostics: []string{"composer.json read failed: " + err.Error()}}
	}
	var composer composerJSON
	if err := unmarshalJSON(data, &composer); err != nil {
		return Result{Diagnostics: []string{"composer.json decode failed: " + err.Error()}}
	}
	psr4 := map[string][]string{}
	addPSR4(psr4, composer.Autoload.PSR4)
	addPSR4(psr4, composer.AutoloadDev.PSR4)

	fileScope := fileSet(req.Files)
	var edges []core.Edge
	for _, file := range req.Files {
		content, err := osReadFile(filepath.Join(req.Root, file))
		if err != nil {
			continue
		}
		for _, class := range phpReferencedClasses(string(content)) {
			if target, ok := resolvePHPClass(class, psr4, fileScope); ok && target != file {
				edges = append(edges, nativeImportEdge(file, target, 0.94))
			}
		}
	}
	autoloadCount := len(edges)
	semanticEdges := phpSemanticEdges(req.Symbols, psr4, fileScope)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"composer psr-4 prefixes loaded " + itoa(len(psr4)),
			"resolved " + itoa(autoloadCount) + " native autoload edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeExtends)) + " native extends edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeImplements)) + " native implements edge(s)",
		},
	}
}

func addPSR4(out map[string][]string, in map[string]any) {
	for prefix, raw := range in {
		switch v := raw.(type) {
		case string:
			out[prefix] = append(out[prefix], filepath.ToSlash(v))
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out[prefix] = append(out[prefix], filepath.ToSlash(s))
				}
			}
		}
	}
}

var phpUsePattern = regexp.MustCompile(`(?m)^\s*use\s+([A-Za-z_\\][A-Za-z0-9_\\]*)\s*;`)
var phpNewPattern = regexp.MustCompile(`\bnew\s+\\?([A-Za-z_\\][A-Za-z0-9_\\]*)\s*\(`)

func phpReferencedClasses(content string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.Trim(name, "\\")
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, match := range phpUsePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			add(match[1])
		}
	}
	for _, match := range phpNewPattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			add(match[1])
		}
	}
	return out
}

func resolvePHPClass(class string, psr4 map[string][]string, fileScope map[string]bool) (string, bool) {
	class = strings.Trim(class, "\\")
	for prefix, dirs := range psr4 {
		prefix = strings.Trim(prefix, "\\")
		if class != prefix && !strings.HasPrefix(class, prefix+"\\") {
			continue
		}
		relative := strings.TrimPrefix(class, prefix)
		relative = strings.TrimPrefix(relative, "\\")
		relative = strings.ReplaceAll(relative, "\\", "/") + ".php"
		for _, dir := range dirs {
			candidate := filepath.ToSlash(filepath.Clean(filepath.Join(dir, relative)))
			if fileScope[candidate] {
				return candidate, true
			}
		}
	}
	return "", false
}

type phpIndex struct {
	typesByName  map[string][]core.SymbolRecord
	methods      map[string][]core.SymbolRecord
	ctors        map[string][]core.SymbolRecord
	methodsByCls map[string][]core.SymbolRecord
	functions    map[string][]core.SymbolRecord
}

func newPHPIndex(symbols []core.SymbolRecord) phpIndex {
	idx := phpIndex{
		typesByName:  map[string][]core.SymbolRecord{},
		methods:      map[string][]core.SymbolRecord{},
		ctors:        map[string][]core.SymbolRecord{},
		methodsByCls: map[string][]core.SymbolRecord{},
		functions:    map[string][]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "php" {
			continue
		}
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		switch symbol.Kind {
		case core.KindFunction:
			idx.functions[symbol.Name] = append(idx.functions[symbol.Name], symbol)
		case core.KindMethod:
			if symbol.Name == "__construct" {
				idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
			}
			key := symbol.ParentSymbol + "::" + symbol.Name
			idx.methods[key] = append(idx.methods[key], symbol)
			idx.methodsByCls[symbol.ParentSymbol] = append(idx.methodsByCls[symbol.ParentSymbol], symbol)
		case core.KindConstructor:
			idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
		}
	}
	return idx
}

func phpSemanticEdges(symbols []core.SymbolRecord, psr4 map[string][]string, fileScope map[string]bool) []core.Edge {
	idx := newPHPIndex(symbols)
	aliases := phpUseAliases(symbols)
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
	for _, symbol := range symbols {
		if symbol.Language != "php" {
			continue
		}
		if typeKind(symbol.Kind) {
			for _, ref := range phpInheritanceRefs(symbol.RawText) {
				name := phpResolveAlias(ref.Name, aliases[symbol.FilePath])
				if target, ok := phpBestType(idx, name, symbol.FilePath, psr4, fileScope); ok && target.ID != symbol.ID {
					add(symbolEdge(symbol, target, ref.EdgeType, 0.95))
				}
			}
		}
		if !callableKind(symbol.Kind) || symbol.RawText == "" {
			continue
		}
		// Call edges intentionally NOT emitted here (same lesson as the Java,
		// Rust, and C# native passes): text matching edged every same-named
		// method it saw. The graph layer's call-site resolution owns calls;
		// this pass keeps only the type-usage evidence text matching is still
		// reliable for.
		for _, className := range phpConstructedTypes(symbol.RawText) {
			className = phpResolveAlias(className, aliases[symbol.FilePath])
			if target, ok := phpBestType(idx, className, symbol.FilePath, psr4, fileScope); ok && target.ID != symbol.ID {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.94))
			}
		}
		for name := range idx.typesByName {
			if target, ok := phpBestType(idx, name, symbol.FilePath, psr4, fileScope); ok && target.ID != symbol.ID && containsTypeToken(symbol.RawText, name) {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.9))
			}
		}
	}
	return edges
}

func phpUseAliases(symbols []core.SymbolRecord) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, symbol := range symbols {
		if symbol.Language != "php" || symbol.RawText == "" {
			continue
		}
		if _, ok := out[symbol.FilePath]; !ok {
			out[symbol.FilePath] = map[string]string{}
		}
		for _, class := range phpReferencedClasses(symbol.RawText) {
			out[symbol.FilePath][lastDottedName(strings.ReplaceAll(class, "\\", "."))] = class
		}
	}
	return out
}

type phpInheritanceRef struct {
	Name     string
	EdgeType core.EdgeType
}

var phpExtendsPattern = regexp.MustCompile(`\bextends\s+\\?([A-Za-z_\\][A-Za-z0-9_\\]*)`)
var phpImplementsPattern = regexp.MustCompile(`\bimplements\s+([A-Za-z_\\][A-Za-z0-9_\\]*(?:\s*,\s*\\?[A-Za-z_\\][A-Za-z0-9_\\]*)*)`)
var phpTraitUsePattern = regexp.MustCompile(`\buse\s+\\?([A-Za-z_\\][A-Za-z0-9_\\]*)\s*;`)

func phpInheritanceRefs(rawText string) []phpInheritanceRef {
	var refs []phpInheritanceRef
	for _, match := range phpExtendsPattern.FindAllStringSubmatch(rawText, -1) {
		if len(match) == 2 {
			refs = append(refs, phpInheritanceRef{Name: match[1], EdgeType: core.EdgeExtends})
		}
	}
	for _, match := range phpImplementsPattern.FindAllStringSubmatch(rawText, -1) {
		if len(match) != 2 {
			continue
		}
		for _, part := range strings.Split(match[1], ",") {
			if part = strings.TrimSpace(part); part != "" {
				refs = append(refs, phpInheritanceRef{Name: strings.Trim(part, "\\"), EdgeType: core.EdgeImplements})
			}
		}
	}
	for _, match := range phpTraitUsePattern.FindAllStringSubmatch(rawText, -1) {
		if len(match) == 2 {
			refs = append(refs, phpInheritanceRef{Name: match[1], EdgeType: core.EdgeImplements})
		}
	}
	return refs
}

type phpStaticCall struct {
	Class  string
	Method string
}

var phpStaticCallPattern = regexp.MustCompile(`\b\\?([A-Za-z_\\][A-Za-z0-9_\\]*)::([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func phpStaticCalls(rawText string) []phpStaticCall {
	matches := phpStaticCallPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	out := make([]phpStaticCall, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 && match[2] != "class" {
			out = append(out, phpStaticCall{Class: strings.Trim(match[1], "\\"), Method: match[2]})
		}
	}
	return out
}

func phpConstructedTypes(rawText string) []string {
	matches := phpNewPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		name := strings.Trim(match[1], "\\")
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func phpBestType(idx phpIndex, name, fromFile string, psr4 map[string][]string, fileScope map[string]bool) (core.SymbolRecord, bool) {
	short := lastDottedName(strings.ReplaceAll(strings.Trim(name, "\\"), "\\", "."))
	candidates := idx.typesByName[short]
	if len(candidates) == 0 {
		if targetFile, ok := resolvePHPClass(name, psr4, fileScope); ok {
			for _, candidate := range idx.typesByName[short] {
				if candidate.FilePath == targetFile {
					return candidate, true
				}
			}
		}
		return core.SymbolRecord{}, false
	}
	for _, candidate := range candidates {
		if candidate.FilePath == fromFile {
			return candidate, true
		}
	}
	if targetFile, ok := resolvePHPClass(name, psr4, fileScope); ok {
		for _, candidate := range candidates {
			if candidate.FilePath == targetFile {
				return candidate, true
			}
		}
	}
	return candidates[0], true
}

func phpResolveAlias(name string, aliases map[string]string) string {
	name = strings.Trim(name, "\\")
	short := lastDottedName(strings.ReplaceAll(name, "\\", "."))
	if full := aliases[short]; full != "" {
		return full
	}
	return name
}
