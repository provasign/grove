package native

import (
	"context"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type javaAnalyzer struct{}

func (javaAnalyzer) Name() string { return "java" }

func (javaAnalyzer) Languages() []string { return []string{"java"} }

func (javaAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") {
		return Availability{Reason: "no Maven or Gradle project config"}
	}
	if firstExistingExecutable("jdtls", "mvn", "gradle", "javac") == "" {
		return Availability{Reason: "jdtls, mvn, gradle, or javac executable not found"}
	}
	return Availability{Available: true}
}

func (javaAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	edges := javaSemanticEdges(req.Symbols)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"project tooling detected",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeUsesType)) + " native type-use edge(s)",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeExtends)) + " native extends edge(s)",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeImplements)) + " native implements edge(s)",
		},
	}
}

type javaIndex struct {
	typesByName  map[string][]core.SymbolRecord
	methods      map[string][]core.SymbolRecord
	ctors        map[string][]core.SymbolRecord
	methodsByCls map[string][]core.SymbolRecord
}

func newJavaIndex(symbols []core.SymbolRecord) javaIndex {
	idx := javaIndex{
		typesByName:  map[string][]core.SymbolRecord{},
		methods:      map[string][]core.SymbolRecord{},
		ctors:        map[string][]core.SymbolRecord{},
		methodsByCls: map[string][]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "java" {
			continue
		}
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		switch symbol.Kind {
		case core.KindMethod:
			key := symbol.ParentSymbol + "." + symbol.Name
			idx.methods[key] = append(idx.methods[key], symbol)
			idx.methodsByCls[symbol.ParentSymbol] = append(idx.methodsByCls[symbol.ParentSymbol], symbol)
		case core.KindConstructor:
			idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
		}
	}
	return idx
}

func javaSemanticEdges(symbols []core.SymbolRecord) []core.Edge {
	idx := newJavaIndex(symbols)
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
		if symbol.Language != "java" {
			continue
		}
		if typeKind(symbol.Kind) {
			for _, ref := range javaInheritanceRefs(symbol.Signature + "\n" + firstLine(symbol.RawText)) {
				if target, ok := javaBestType(idx, ref.Name, symbol.FilePath); ok && target.ID != symbol.ID {
					add(symbolEdge(symbol, target, ref.EdgeType, 0.97))
				}
			}
		}
		if !callableKind(symbol.Kind) || symbol.RawText == "" {
			continue
		}
		for _, method := range idx.methodsByCls[symbol.ParentSymbol] {
			if method.ID != symbol.ID && containsCall(symbol.RawText, method.Name) {
				add(symbolEdge(symbol, method, core.EdgeCalls, 0.96))
			}
		}
		for _, call := range javaQualifiedCalls(symbol.RawText) {
			for _, method := range idx.methods[call.Qualifier+"."+call.Method] {
				if method.ID != symbol.ID {
					add(symbolEdge(symbol, method, core.EdgeCalls, 0.97))
				}
			}
		}
		for _, className := range javaConstructedTypes(symbol.RawText) {
			if target, ok := javaBestType(idx, className, symbol.FilePath); ok && target.ID != symbol.ID {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.96))
			}
			for _, ctor := range idx.ctors[className] {
				if ctor.ID != symbol.ID {
					add(symbolEdge(symbol, ctor, core.EdgeCalls, 0.97))
				}
			}
		}
		for name := range idx.typesByName {
			if target, ok := javaBestType(idx, name, symbol.FilePath); ok && target.ID != symbol.ID && containsTypeToken(symbol.RawText, name) {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.94))
			}
		}
	}
	return edges
}

type javaInheritanceRef struct {
	Name     string
	EdgeType core.EdgeType
}

func javaInheritanceRefs(text string) []javaInheritanceRef {
	var refs []javaInheritanceRef
	for _, name := range javaMatchNameList(`\bextends\s+([A-Za-z_][A-Za-z0-9_.]*)`, text) {
		refs = append(refs, javaInheritanceRef{Name: lastDottedName(name), EdgeType: core.EdgeExtends})
	}
	for _, name := range javaMatchNameList(`\bimplements\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`, text) {
		refs = append(refs, javaInheritanceRef{Name: lastDottedName(name), EdgeType: core.EdgeImplements})
	}
	return refs
}

func javaMatchNameList(pattern, text string) []string {
	re := regexp.MustCompile(pattern)
	matches := re.FindAllStringSubmatch(text, -1)
	var out []string
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		for _, part := range strings.Split(match[1], ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

type javaQualifiedCall struct {
	Qualifier string
	Method    string
}

var javaQualifiedCallPattern = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func javaQualifiedCalls(rawText string) []javaQualifiedCall {
	matches := javaQualifiedCallPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	out := make([]javaQualifiedCall, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			out = append(out, javaQualifiedCall{Qualifier: match[1], Method: match[2]})
		}
	}
	return out
}

var javaNewPattern = regexp.MustCompile(`\bnew\s+([A-Za-z_][A-Za-z0-9_.]*)\s*\(`)

func javaConstructedTypes(rawText string) []string {
	matches := javaNewPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		name := lastDottedName(match[1])
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	return out
}

func javaBestType(idx javaIndex, name, fromFile string) (core.SymbolRecord, bool) {
	candidates := idx.typesByName[lastDottedName(name)]
	if len(candidates) == 0 {
		return core.SymbolRecord{}, false
	}
	for _, candidate := range candidates {
		if candidate.FilePath == fromFile {
			return candidate, true
		}
	}
	fromDir := packageDir(fromFile)
	for _, candidate := range candidates {
		if packageDir(candidate.FilePath) == fromDir {
			return candidate, true
		}
	}
	return candidates[0], true
}

func lastDottedName(name string) string {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		name = name[i+1:]
	}
	return name
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i]
	}
	return text
}
