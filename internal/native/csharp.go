package native

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type csharpAnalyzer struct{}

func (csharpAnalyzer) Name() string { return "csharp" }

func (csharpAnalyzer) Languages() []string { return []string{"csharp"} }

func (csharpAnalyzer) Available(_ context.Context, root string) Availability {
	if len(filesWithExt(root, ".csproj")) == 0 {
		return Availability{Reason: "no .csproj file"}
	}
	return Availability{Available: true}
}

type csProject struct {
	Items []struct {
		Compile []struct {
			Include string `xml:"Include,attr"`
			Remove  string `xml:"Remove,attr"`
		} `xml:"Compile"`
		ProjectReference []struct {
			Include string `xml:"Include,attr"`
		} `xml:"ProjectReference"`
	} `xml:"ItemGroup"`
}

func (csharpAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	projects := filesWithExt(req.Root, ".csproj")
	projectFiles := map[string][]string{}
	projectRefs := map[string][]string{}
	fileScope := fileSet(req.Files)
	for _, project := range projects {
		data, err := osReadFile(filepath.Join(req.Root, project))
		if err != nil {
			continue
		}
		var parsed csProject
		if err := xml.Unmarshal(data, &parsed); err != nil {
			continue
		}
		projectDir := packageDir(project)
		files := explicitCSharpFiles(projectDir, parsed, fileScope)
		if len(files) == 0 {
			files = filesUnderDir(projectDir, req.Files, ".cs")
		}
		projectFiles[project] = files
		for _, group := range parsed.Items {
			for _, ref := range group.ProjectReference {
				refPath := filepath.ToSlash(filepath.Clean(filepath.Join(projectDir, ref.Include)))
				projectRefs[project] = append(projectRefs[project], refPath)
			}
		}
	}

	var edges []core.Edge
	for project, refs := range projectRefs {
		for _, ref := range refs {
			for _, from := range projectFiles[project] {
				for _, to := range projectFiles[ref] {
					if from != to {
						edges = append(edges, nativeImportEdge(from, to, 0.94))
					}
				}
			}
		}
	}
	projectRefCount := len(edges)
	semanticEdges := csharpSemanticEdges(req.Symbols)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"loaded " + itoa(len(projects)) + " csproj file(s)",
			"resolved " + itoa(projectRefCount) + " native project-reference edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeExtends)) + " native extends edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeImplements)) + " native implements edge(s)",
		},
	}
}

func explicitCSharpFiles(projectDir string, project csProject, fileScope map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range project.Items {
		for _, compile := range group.Compile {
			if compile.Include == "" || strings.ContainsAny(compile.Include, "*?") {
				continue
			}
			path := filepath.ToSlash(filepath.Clean(filepath.Join(projectDir, compile.Include)))
			if fileScope[path] && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	return out
}

type csharpIndex struct {
	typesByName  map[string][]core.SymbolRecord
	methods      map[string][]core.SymbolRecord
	ctors        map[string][]core.SymbolRecord
	methodsByCls map[string][]core.SymbolRecord
}

func newCSharpIndex(symbols []core.SymbolRecord) csharpIndex {
	idx := csharpIndex{
		typesByName:  map[string][]core.SymbolRecord{},
		methods:      map[string][]core.SymbolRecord{},
		ctors:        map[string][]core.SymbolRecord{},
		methodsByCls: map[string][]core.SymbolRecord{},
	}
	for _, symbol := range symbols {
		if symbol.Language != "csharp" {
			continue
		}
		if typeKind(symbol.Kind) {
			idx.typesByName[symbol.Name] = append(idx.typesByName[symbol.Name], symbol)
		}
		switch symbol.Kind {
		case core.KindMethod, core.KindFunction:
			key := symbol.ParentSymbol + "." + symbol.Name
			idx.methods[key] = append(idx.methods[key], symbol)
			idx.methodsByCls[symbol.ParentSymbol] = append(idx.methodsByCls[symbol.ParentSymbol], symbol)
		case core.KindConstructor:
			idx.ctors[symbol.ParentSymbol] = append(idx.ctors[symbol.ParentSymbol], symbol)
		}
	}
	return idx
}

func csharpSemanticEdges(symbols []core.SymbolRecord) []core.Edge {
	idx := newCSharpIndex(symbols)
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
		if symbol.Language != "csharp" {
			continue
		}
		if typeKind(symbol.Kind) {
			for _, ref := range csharpInheritanceRefs(symbol.Signature+"\n"+firstLine(symbol.RawText), symbol.Kind) {
				if target, ok := csharpBestType(idx, ref.Name, symbol.FilePath); ok && target.ID != symbol.ID {
					add(symbolEdge(symbol, target, ref.EdgeType, 0.96))
				}
			}
		}
		if !callableKind(symbol.Kind) || symbol.RawText == "" {
			continue
		}
		for _, method := range idx.methodsByCls[symbol.ParentSymbol] {
			if method.ID != symbol.ID && containsCall(symbol.RawText, method.Name) {
				add(symbolEdge(symbol, method, core.EdgeCalls, 0.95))
			}
		}
		for _, call := range csharpQualifiedCalls(symbol.RawText) {
			for _, method := range idx.methods[call.Qualifier+"."+call.Method] {
				if method.ID != symbol.ID {
					add(symbolEdge(symbol, method, core.EdgeCalls, 0.96))
				}
			}
		}
		for _, className := range csharpConstructedTypes(symbol.RawText) {
			if target, ok := csharpBestType(idx, className, symbol.FilePath); ok && target.ID != symbol.ID {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.95))
			}
			for _, ctor := range idx.ctors[className] {
				if ctor.ID != symbol.ID {
					add(symbolEdge(symbol, ctor, core.EdgeCalls, 0.96))
				}
			}
		}
		for name := range idx.typesByName {
			if target, ok := csharpBestType(idx, name, symbol.FilePath); ok && target.ID != symbol.ID && containsTypeToken(symbol.RawText, name) {
				add(symbolEdge(symbol, target, core.EdgeUsesType, 0.93))
			}
		}
	}
	return edges
}

type csharpInheritanceRef struct {
	Name     string
	EdgeType core.EdgeType
}

var csharpDeclInheritancePattern = regexp.MustCompile(`\b(?:class|record|struct|interface)\s+[A-Za-z_][A-Za-z0-9_]*(?:<[^>{}]+>)?\s*:\s*([A-Za-z_][A-Za-z0-9_.,\s<>]*)`)

func csharpInheritanceRefs(text string, kind core.SymbolKind) []csharpInheritanceRef {
	match := csharpDeclInheritancePattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return nil
	}
	names := splitCSharpTypeList(match[1])
	refs := make([]csharpInheritanceRef, 0, len(names))
	for i, name := range names {
		edgeType := core.EdgeImplements
		if kind == core.KindClass && i == 0 && !strings.HasPrefix(name, "I") {
			edgeType = core.EdgeExtends
		}
		if kind == core.KindInterface {
			edgeType = core.EdgeExtends
		}
		refs = append(refs, csharpInheritanceRef{Name: lastDottedName(name), EdgeType: edgeType})
	}
	return refs
}

func splitCSharpTypeList(text string) []string {
	parts := strings.Split(text, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if i := strings.IndexByte(part, '<'); i >= 0 {
			part = strings.TrimSpace(part[:i])
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

type csharpQualifiedCall struct {
	Qualifier string
	Method    string
}

var csharpQualifiedCallPattern = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

func csharpQualifiedCalls(rawText string) []csharpQualifiedCall {
	matches := csharpQualifiedCallPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
	out := make([]csharpQualifiedCall, 0, len(matches))
	for _, match := range matches {
		if len(match) == 3 {
			out = append(out, csharpQualifiedCall{Qualifier: match[1], Method: match[2]})
		}
	}
	return out
}

var csharpNewPattern = regexp.MustCompile(`\bnew\s+([A-Za-z_][A-Za-z0-9_.]*)\s*\(`)

func csharpConstructedTypes(rawText string) []string {
	matches := csharpNewPattern.FindAllStringSubmatch(stripQuotedText(rawText), -1)
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

func csharpBestType(idx csharpIndex, name, fromFile string) (core.SymbolRecord, bool) {
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

func filesUnderDir(dir string, files []string, ext string) []string {
	if dir == "." {
		dir = ""
	}
	prefix := dir
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var out []string
	for _, file := range files {
		if strings.HasPrefix(file, prefix) && strings.EqualFold(filepath.Ext(file), ext) {
			out = append(out, file)
		}
	}
	return out
}
