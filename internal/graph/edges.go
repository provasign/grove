package graph

import (
	"regexp"
	"strings"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

// edgeIndex holds per-build symbol indexes used by the edge constructors.
type edgeIndex struct {
	byName map[string][]*core.SymbolRecord // lowercase name → symbols
	byFile map[string][]*core.SymbolRecord
	byID   map[string]*core.SymbolRecord

	// fileImports maps filePath → set of import strings declared in that file.
	// We pick the union over all symbols in the file since the parser sets
	// Imports per-symbol from the same file-level import list.
	fileImports map[string]map[string]struct{}

	// importedFilesCache memoizes the result of importedFiles() per file.
	importedFilesCache map[string]map[string]struct{}
}

func newEdgeIndex(symbols []core.SymbolRecord) *edgeIndex {
	idx := &edgeIndex{
		byName:             make(map[string][]*core.SymbolRecord),
		byFile:             make(map[string][]*core.SymbolRecord),
		byID:               make(map[string]*core.SymbolRecord),
		fileImports:        make(map[string]map[string]struct{}),
		importedFilesCache: make(map[string]map[string]struct{}),
	}
	for i := range symbols {
		s := &symbols[i]
		idx.byID[s.ID] = s
		idx.byName[strings.ToLower(s.Name)] = append(idx.byName[strings.ToLower(s.Name)], s)
		idx.byFile[s.FilePath] = append(idx.byFile[s.FilePath], s)
		if _, ok := idx.fileImports[s.FilePath]; !ok {
			idx.fileImports[s.FilePath] = make(map[string]struct{})
		}
		for _, imp := range s.Imports {
			idx.fileImports[s.FilePath][imp] = struct{}{}
		}
	}
	return idx
}

// importedFiles returns the set of file paths that are reachable through
// the import declarations of fromFile. Resolution is heuristic: an import
// string matches a candidate file when the candidate's path or basename
// shares the import's last path segment. Always includes fromFile itself.
func (idx *edgeIndex) importedFiles(fromFile string) map[string]struct{} {
	if cached, ok := idx.importedFilesCache[fromFile]; ok {
		return cached
	}
	out := map[string]struct{}{fromFile: {}}
	imports, ok := idx.fileImports[fromFile]
	if !ok {
		idx.importedFilesCache[fromFile] = out
		return out
	}
	// Build candidate set: every other indexed file.
	candidates := make([]string, 0, len(idx.byFile))
	for f := range idx.byFile {
		if f == fromFile {
			continue
		}
		candidates = append(candidates, f)
	}
	for imp := range imports {
		// Last path segment of the import (e.g., "lodash/fp" → "fp",
		// "./auth" → "auth", "fmt" → "fmt", "com.example.Auth" → "Auth").
		seg := lastImportSegment(imp)
		if seg == "" {
			continue
		}
		segLower := strings.ToLower(seg)
		for _, c := range candidates {
			lower := strings.ToLower(c)
			base := strings.ToLower(baseNameNoExt(c))
			if base == segLower || strings.HasSuffix(lower, "/"+segLower) ||
				strings.HasSuffix(lower, "/"+segLower+".go") ||
				strings.HasSuffix(lower, "/"+segLower+".py") ||
				strings.HasSuffix(lower, "/"+segLower+".ts") ||
				strings.HasSuffix(lower, "/"+segLower+".tsx") ||
				strings.HasSuffix(lower, "/"+segLower+".js") ||
				strings.HasSuffix(lower, "/"+segLower+".jsx") ||
				strings.HasSuffix(lower, "/"+segLower+".java") ||
				strings.HasSuffix(lower, "/"+segLower+".rs") {
				out[c] = struct{}{}
			}
		}
	}
	idx.importedFilesCache[fromFile] = out
	return out
}

func lastImportSegment(imp string) string {
	imp = strings.Trim(imp, "\"' ;")
	// Java: dot-separated; everything else: slash-separated.
	if strings.Contains(imp, "/") {
		parts := strings.Split(imp, "/")
		return parts[len(parts)-1]
	}
	if strings.Contains(imp, ".") {
		parts := strings.Split(imp, ".")
		return parts[len(parts)-1]
	}
	return imp
}

func baseNameNoExt(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		path = path[:i]
	}
	return path
}

// stripCommentsAndStrings removes // line comments, /* */ block comments,
// # python comments, and string literals from a source body so that
// regex-based call matching does not produce false positives.
func stripCommentsAndStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		ch := src[i]
		// Block comment
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
			continue
		}
		// Line comment // and #
		if (ch == '/' && i+1 < n && src[i+1] == '/') || ch == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// String literal — preserve newlines so call matching keeps line layout.
		if ch == '"' || ch == '\'' || ch == '`' {
			quote := ch
			out.WriteByte(' ')
			i++
			for i < n && src[i] != quote {
				if src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			if i < n {
				i++ // closing quote
			}
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// buildDefinesAndImports emits "file → symbol" defines edges and
// deduplicated "file → import:path" imports edges.
func buildDefinesAndImports(symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seenImports := make(map[string]bool)
	seenFiles := make(map[string]bool)
	for _, symbol := range symbols {
		fileNode := "file:" + symbol.FilePath
		edges = append(edges, core.Edge{
			From:       fileNode,
			To:         symbol.ID,
			Type:       core.EdgeDefines,
			Confidence: 1.0,
		})
		if seenFiles[symbol.FilePath] {
			continue
		}
		seenFiles[symbol.FilePath] = true
		for _, imp := range symbol.Imports {
			key := fileNode + "::import:" + imp
			if seenImports[key] {
				continue
			}
			seenImports[key] = true
			edges = append(edges, core.Edge{
				From:       fileNode,
				To:         "import:" + imp,
				Type:       core.EdgeImports,
				Confidence: 0.9,
			})
		}
	}
	return edges
}

// buildContains emits parent-symbol → child-symbol edges.
func buildContains(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		if symbol.ParentSymbol == "" {
			continue
		}
		for _, parent := range idx.byName[strings.ToLower(symbol.ParentSymbol)] {
			if parent.FilePath != symbol.FilePath {
				continue
			}
			if parent.Kind != core.KindStruct && parent.Kind != core.KindClass &&
				parent.Kind != core.KindInterface && parent.Kind != core.KindTrait {
				continue
			}
			edges = append(edges, core.Edge{
				From:       parent.ID,
				To:         symbol.ID,
				Type:       core.EdgeContains,
				Confidence: 1.0,
			})
		}
	}
	return edges
}

// extendsRe / implementsRe match the inheritance clauses of class/interface
// declarations across JS/TS/Java. Python uses parenthesized base classes.
var (
	extendsRe       = regexp.MustCompile(`\bextends\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	implementsRe    = regexp.MustCompile(`\bimplements\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	pythonClassBase = regexp.MustCompile(`^\s*class\s+[A-Za-z_][A-Za-z0-9_]*\s*\(([^)]+)\)`)
	rustImplForRe   = regexp.MustCompile(`\bimpl\s+(?:<[^>]+>\s+)?([A-Za-z_][A-Za-z0-9_:]*)\s+for\s+([A-Za-z_][A-Za-z0-9_:]*)`)
	usesTypeIdent   = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]+)\b`)
)

// buildExtendsImplements emits inheritance edges. It reads the symbol's
// Signature (and RawText for Python/Rust where the signature is sparse) to
// detect parent classes, implemented interfaces, and trait impls.
func buildExtendsImplements(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		switch symbol.Language {
		case "typescript", "tsx", "javascript", "java":
			if symbol.Kind != core.KindClass && symbol.Kind != core.KindInterface {
				continue
			}
			text := symbol.Signature
			if text == "" {
				text = firstLine(symbol.RawText)
			}
			for _, name := range matchNameList(extendsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.85)...)
			}
			for _, name := range matchNameList(implementsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeImplements, 0.85)...)
			}
		case "python":
			if symbol.Kind != core.KindClass {
				continue
			}
			line := firstLine(symbol.RawText)
			matches := pythonClassBase.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}
			for _, base := range splitTrim(matches[1], ',') {
				base = stripPythonBase(base)
				if base == "" {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, base, core.EdgeExtends, 0.85)...)
			}
		case "rust":
			// Rust uses `impl Trait for Type` to implement traits; we attach
			// the implements edge to the *type* symbol.
			if symbol.Kind != core.KindStruct && symbol.Kind != core.KindEnum {
				continue
			}
			body := symbol.RawText
			matches := rustImplForRe.FindAllStringSubmatch(body, -1)
			for _, m := range matches {
				traitName, typeName := m[1], m[2]
				if typeName != symbol.Name {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, traitName, core.EdgeImplements, 0.85)...)
			}
		case "go":
			// Go has structural interface satisfaction; emitting implements
			// edges accurately requires interface-method matching across the
			// graph. We skip for v0.1; struct embedding (extends) is detected
			// from RawText below.
			if symbol.Kind != core.KindStruct {
				continue
			}
			for _, name := range goEmbeddedTypes(symbol.RawText) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.7)...)
			}
		}
	}
	return edges
}

// buildUsesType emits uses-type edges from a symbol's signature, scoped to
// same-file and imported-file symbols (per Implementation Plan). The "to"
// side of each edge is a concrete symbol ID when resolvable.
func buildUsesType(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seen := make(map[string]bool)
	for _, symbol := range symbols {
		if symbol.Signature == "" {
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)
		matches := usesTypeIdent.FindAllStringSubmatch(symbol.Signature, -1)
		for _, m := range matches {
			candidateName := m[1]
			if candidateName == symbol.Name {
				continue
			}
			for _, target := range idx.byName[strings.ToLower(candidateName)] {
				if _, inScope := scope[target.FilePath]; !inScope {
					continue
				}
				key := symbol.ID + "::uses-type::" + target.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				edges = append(edges, core.Edge{
					From:       symbol.ID,
					To:         target.ID,
					Type:       core.EdgeUsesType,
					Confidence: 0.5,
				})
			}
		}
	}
	return edges
}

// buildTests emits TestX → X edges across files in the project.
// Matches Go (TestFoo), Python (test_foo), Java (FooTest) test conventions.
func buildTests(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		if !isTestFile(symbol.FilePath) {
			continue
		}
		for _, target := range testTargets(symbol, idx) {
			if target.FilePath == symbol.FilePath {
				continue
			}
			edges = append(edges, core.Edge{
				From:       symbol.ID,
				To:         target.ID,
				Type:       core.EdgeTests,
				Confidence: 0.8,
			})
		}
	}
	return edges
}

func testTargets(symbol core.SymbolRecord, idx *edgeIndex) []*core.SymbolRecord {
	name := symbol.Name
	var candidates []string
	switch {
	case strings.HasPrefix(name, "Test"):
		candidates = append(candidates, strings.TrimPrefix(name, "Test"))
	case strings.HasPrefix(name, "test_"):
		candidates = append(candidates, strings.TrimPrefix(name, "test_"))
	case strings.HasSuffix(name, "Test"):
		candidates = append(candidates, strings.TrimSuffix(name, "Test"))
	case strings.HasSuffix(name, "Spec"):
		candidates = append(candidates, strings.TrimSuffix(name, "Spec"))
	}
	var out []*core.SymbolRecord
	for _, c := range candidates {
		if c == "" {
			continue
		}
		out = append(out, idx.byName[strings.ToLower(c)]...)
	}
	return out
}

// buildCalls emits same-file + imported-file call edges with strings/comments
// stripped from the body before matching.
func buildCalls(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	type matcher struct {
		symbol  *core.SymbolRecord
		pattern *regexp.Regexp
	}
	// Pre-compile per-symbol regex patterns for the fallback path.
	all := make([]matcher, 0, len(symbols))
	for i := range symbols {
		s := &symbols[i]
		if s.Kind != core.KindFunction && s.Kind != core.KindMethod && s.Kind != core.KindConstructor {
			continue
		}
		pattern, err := regexp.Compile(`\b` + regexp.QuoteMeta(s.Name) + `\s*\(`)
		if err != nil {
			continue
		}
		all = append(all, matcher{s, pattern})
	}

	var edges []core.Edge
	seen := make(map[string]bool)

	addEdge := func(fromID, toID string, confidence float64) {
		key := fromID + "::calls::" + toID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: fromID, To: toID,
			Type: core.EdgeCalls, Confidence: confidence,
		})
	}

	for _, symbol := range symbols {
		if symbol.Kind != core.KindFunction && symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor {
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)

		// ── High-confidence path: AST-extracted CallSites ───────────────────
		if len(symbol.CallSites) > 0 {
			for _, cs := range symbol.CallSites {
				calleeName := cs.Callee
				// Strip receiver prefix (e.g. "user.save" → "save", "self.do" → "do").
				if idx := strings.LastIndexByte(calleeName, '.'); idx >= 0 {
					calleeName = calleeName[idx+1:]
				}
				if calleeName == "" {
					continue
				}
				candidates := idx.byName[strings.ToLower(calleeName)]
				for _, cand := range candidates {
					if cand.ID == symbol.ID {
						continue
					}
					if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod && cand.Kind != core.KindConstructor {
						continue
					}
					if _, ok := scope[cand.FilePath]; !ok {
						continue
					}
					addEdge(symbol.ID, cand.ID, 0.95)
				}
			}
			continue // CallSites authoritative; skip regex fallback for this symbol
		}

		// ── Fallback: regex over comment/string-stripped body ───────────────
		if symbol.RawText == "" {
			continue
		}
		stripped := stripCommentsAndStrings(symbol.RawText)
		for _, m := range all {
			if m.symbol.ID == symbol.ID {
				continue
			}
			if _, ok := scope[m.symbol.FilePath]; !ok {
				continue
			}
			if !m.pattern.MatchString(stripped) {
				continue
			}
			confidence := 0.6
			if m.symbol.FilePath == symbol.FilePath {
				confidence = 0.85
			}
			addEdge(symbol.ID, m.symbol.ID, confidence)
		}
	}
	return edges
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func matchNameList(re *regexp.Regexp, text string) []string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	var out []string
	for _, name := range splitTrim(m[1], ',') {
		name = strings.TrimSpace(name)
		if i := strings.Index(name, "<"); i >= 0 {
			name = name[:i]
		}
		// Drop dotted prefixes (java.util.List → List).
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func splitTrim(s string, sep byte) []string {
	parts := strings.Split(s, string(sep))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripPythonBase(b string) string {
	b = strings.TrimSpace(b)
	// Strip default ABC bases that add noise (object, Generic[T], etc.)
	if strings.HasPrefix(b, "metaclass=") || b == "object" {
		return ""
	}
	if i := strings.Index(b, "["); i >= 0 {
		b = b[:i]
	}
	if i := strings.LastIndexByte(b, '.'); i >= 0 {
		b = b[i+1:]
	}
	return b
}

func goEmbeddedTypes(body string) []string {
	// Look at lines between the first `{` and the matching `}` of the struct
	// declaration. We consider each non-empty line consisting of a single
	// identifier (or *Identifier) to be an embedded type.
	open := strings.IndexByte(body, '{')
	if open < 0 {
		return nil
	}
	body = body[open+1:]
	close := strings.LastIndexByte(body, '}')
	if close >= 0 {
		body = body[:close]
	}
	var out []string
	embeddedRe := regexp.MustCompile(`^\s*\*?([A-Z][A-Za-z0-9_]*)\s*(?://.*)?$`)
	for _, line := range strings.Split(body, "\n") {
		if m := embeddedRe.FindStringSubmatch(line); len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

// resolveTypeEdges returns 0 or more edges from `symbol` to a target type
// resolved by name. If no concrete symbol is found, no edge is emitted.
func resolveTypeEdges(idx *edgeIndex, symbol core.SymbolRecord, targetName string, edgeType core.EdgeType, confidence float64) []core.Edge {
	var out []core.Edge
	for _, target := range idx.byName[strings.ToLower(targetName)] {
		if target.ID == symbol.ID {
			continue
		}
		out = append(out, core.Edge{
			From:       symbol.ID,
			To:         target.ID,
			Type:       edgeType,
			Confidence: confidence,
		})
	}
	return out
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return strings.TrimSpace(text[:i])
	}
	return strings.TrimSpace(text)
}
