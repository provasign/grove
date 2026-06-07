package parser

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type Engine struct{}

const MaxFileSizeBytes int64 = 10 * 1024 * 1024

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) ExtractFile(path string, root string) ([]core.SymbolRecord, error) {
	language := DetectLanguage(path)
	if language == "" {
		return nil, nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > MaxFileSizeBytes {
		return nil, fmt.Errorf("skip %s: file exceeds %d bytes", path, MaxFileSizeBytes)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	relPath, err := filepath.Rel(root, path)
	if err != nil {
		relPath = path
	}
	relPath = filepath.ToSlash(relPath)

	blobSHA := sha1Hex(content)

	if language == PlaintextLanguage {
		return ExtractPlaintext(relPath, blobSHA, content), nil
	}

	src := string(content)
	imports := extractImports(language, src)
	symbols := extractSymbols(language, relPath, blobSHA, src, imports)
	return symbols, nil
}

func (e *Engine) Walk(root string) ([]core.SymbolRecord, int, error) {
	var symbols []core.SymbolRecord
	filesIndexed := 0

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == ".grove" || name == "node_modules" || name == "vendor" || name == "dist" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !Supported(path) {
			return nil
		}
		extracted, err := e.ExtractFile(path, root)
		if err != nil {
			return err
		}
		filesIndexed++
		symbols = append(symbols, extracted...)
		return nil
	})

	return symbols, filesIndexed, err
}

func sha1Hex(content []byte) string {
	sum := sha1.Sum(content)
	return hex.EncodeToString(sum[:])
}

func FileBlobSHA(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sha1Hex(content), nil
}

// extractImports returns the list of import paths declared in the file.
// For Go, scoped strictly to the import block. For other languages, regex-based.
func extractImports(language string, content string) []string {
	if imports, ok := extractImportsFromAST(language, []byte(content)); ok {
		return imports
	}

	if language == "go" {
		return extractGoImports(content)
	}

	patterns := map[string][]*regexp.Regexp{
		"typescript": {
			regexp.MustCompile(`from\s+['"]([^'"]+)['"]`),
			regexp.MustCompile(`require\(['"]([^'"]+)['"]\)`),
		},
		"tsx": {
			regexp.MustCompile(`from\s+['"]([^'"]+)['"]`),
			regexp.MustCompile(`require\(['"]([^'"]+)['"]\)`),
		},
		"javascript": {
			regexp.MustCompile(`from\s+['"]([^'"]+)['"]`),
			regexp.MustCompile(`require\(['"]([^'"]+)['"]\)`),
		},
		"python": {
			regexp.MustCompile(`^\s*import\s+([A-Za-z0-9_\.]+)`),
			regexp.MustCompile(`^\s*from\s+([A-Za-z0-9_\.]+)\s+import\s+`),
		},
		"java": {
			regexp.MustCompile(`^\s*import\s+([A-Za-z0-9_\.]+);`),
		},
		"rust": {
			regexp.MustCompile(`^\s*use\s+([^;]+);`),
		},
		"c": {
			regexp.MustCompile(`^\s*#\s*include\s+[<"]([^>"]+)[>"]`),
		},
		"cpp": {
			regexp.MustCompile(`^\s*#\s*include\s+[<"]([^>"]+)[>"]`),
		},
		"csharp": {
			regexp.MustCompile(`^\s*using\s+([A-Za-z0-9_\.]+)\s*;`),
		},
		"php": {
			regexp.MustCompile(`^\s*(?:use|require_once|require|include_once|include)\s+['"']?([A-Za-z0-9_\\/\.]+)['"']?\s*;?`),
			regexp.MustCompile(`^\s*namespace\s+([A-Za-z0-9_\\]+)\s*;`),
		},
	}

	seen := map[string]bool{}
	var imports []string
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		for _, pattern := range patterns[language] {
			matches := pattern.FindStringSubmatch(line)
			if len(matches) == 2 && !seen[matches[1]] {
				seen[matches[1]] = true
				imports = append(imports, matches[1])
			}
		}
	}
	return imports
}

// extractGoImports strictly parses import blocks and single-line imports.
func extractGoImports(content string) []string {
	var imports []string
	seen := map[string]bool{}
	inBlock := false

	// Single-line import: import "pkg"
	singleRe := regexp.MustCompile(`^import\s+"([^"]+)"`)
	// Import path inside a block: "pkg" or alias "pkg"
	blockRe := regexp.MustCompile(`"([^"]+)"`)

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())

		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "/*") {
			continue
		}
		if strings.HasPrefix(line, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if line == ")" {
				inBlock = false
				continue
			}
			if m := blockRe.FindStringSubmatch(line); len(m) == 2 && !seen[m[1]] {
				seen[m[1]] = true
				imports = append(imports, m[1])
			}
			continue
		}
		if m := singleRe.FindStringSubmatch(line); len(m) == 2 && !seen[m[1]] {
			seen[m[1]] = true
			imports = append(imports, m[1])
		}
	}
	return imports
}

// extractBody returns the end line number (1-indexed, inclusive) and full body text
// starting from startIdx (0-indexed into lines).
func extractBody(lines []string, startIdx int, language string) (endLine int, body string) {
	switch language {
	case "go", "typescript", "tsx", "javascript", "java", "rust":
		return extractBraceBody(lines, startIdx)
	case "python":
		return extractIndentBody(lines, startIdx)
	default:
		return startIdx + 1, lines[startIdx]
	}
}

// extractBraceBody scans forward from startIdx, tracking brace depth,
// and returns when the opening brace is balanced (depth returns to 0).
func extractBraceBody(lines []string, startIdx int) (endLine int, body string) {
	var bodyLines []string
	depth := 0
	opened := false

	const maxLines = 500
	limit := startIdx + maxLines
	if limit > len(lines) {
		limit = len(lines)
	}

	for i := startIdx; i < limit; i++ {
		line := lines[i]
		bodyLines = append(bodyLines, line)

		// Count braces, naively ignoring strings/comments (good enough for typical Go/TS/Java)
		inString := false
		var stringChar byte
		for j := 0; j < len(line); j++ {
			ch := line[j]
			if inString {
				if ch == '\\' {
					j++ // skip escaped char
					continue
				}
				if ch == stringChar {
					inString = false
				}
				continue
			}
			if ch == '"' || ch == '\'' || ch == '`' {
				inString = true
				stringChar = ch
				continue
			}
			// Inline comment
			if ch == '/' && j+1 < len(line) && line[j+1] == '/' {
				break
			}
			if ch == '{' {
				depth++
				opened = true
			} else if ch == '}' {
				depth--
			}
		}

		if opened && depth <= 0 {
			return i + 1, strings.Join(bodyLines, "\n")
		}
	}

	// No closing brace found (e.g., interface method, type declaration) — single-line
	if !opened {
		return startIdx + 1, lines[startIdx]
	}
	return startIdx + len(bodyLines), strings.Join(bodyLines, "\n")
}

// extractIndentBody collects Python body lines based on indentation.
func extractIndentBody(lines []string, startIdx int) (endLine int, body string) {
	if startIdx >= len(lines) {
		return startIdx + 1, ""
	}
	startLine := lines[startIdx]
	baseIndent := len(startLine) - len(strings.TrimLeft(startLine, " \t"))

	var bodyLines []string
	bodyLines = append(bodyLines, startLine)

	const maxLines = 200
	for i := startIdx + 1; i < len(lines) && i < startIdx+maxLines; i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			bodyLines = append(bodyLines, line)
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent <= baseIndent {
			return i, strings.Join(bodyLines, "\n")
		}
		bodyLines = append(bodyLines, line)
	}
	return startIdx + len(bodyLines), strings.Join(bodyLines, "\n")
}

// symbolPattern describes how to extract a single symbol kind from a line.
type symbolPattern struct {
	regex         *regexp.Regexp
	kind          core.SymbolKind
	qualifier     string
	captureParent bool // when true: match[1]=parent, match[2]=name; else match[last]=name
}

func symbolPatterns(language string) []symbolPattern {
	switch language {
	case "go":
		return []symbolPattern{
			// Method with receiver: func (recv *ReceiverType) MethodName(
			{
				regexp.MustCompile(`^\s*func\s+\([A-Za-z_][A-Za-z0-9_]*\s+\*?([A-Za-z_][A-Za-z0-9_]*)\)\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`),
				core.KindMethod, "", true,
			},
			// Standalone function: func FuncName(
			{regexp.MustCompile(`^\s*func\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+struct\b`), core.KindStruct, "", false},
			{regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s+interface\b`), core.KindInterface, "", false},
			{regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindType, "", false},
			{regexp.MustCompile(`^\s*const\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindConst, "", false},
			{regexp.MustCompile(`^\s*var\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindVariable, "", false},
		}
	case "typescript", "tsx", "javascript":
		return []symbolPattern{
			{regexp.MustCompile(`^\s*(?:export\s+)?(?:async\s+)?function\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*(?:export\s+)?class\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`), core.KindClass, "", false},
			{regexp.MustCompile(`^\s*(?:export\s+)?interface\s+([A-Za-z_$][A-Za-z0-9_$]*)\b`), core.KindInterface, "", false},
			{regexp.MustCompile(`^\s*(?:export\s+)?type\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=`), core.KindType, "", false},
			// Arrow function or const function: must have => or function keyword to be a function
			{regexp.MustCompile(`^\s*(?:export\s+)?(?:const|let)\s+([A-Za-z_$][A-Za-z0-9_$]*)\s*=\s*(?:async\s*)?\([^)]*\)\s*=>`), core.KindFunction, "", false},
		}
	case "python":
		return []symbolPattern{
			{regexp.MustCompile(`^\s*(?:async\s+)?def\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindClass, "", false},
		}
	case "java":
		return []symbolPattern{
			{regexp.MustCompile(`\bclass\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindClass, "", false},
			{regexp.MustCompile(`\binterface\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindInterface, "", false},
			{regexp.MustCompile(`\benum\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindEnum, "", false},
			{regexp.MustCompile(`\b(?:public|protected|private|static|final|abstract|synchronized|void|int|long|String|boolean|double|float|[A-Z][A-Za-z0-9_<>,\[\]]*)\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`), core.KindMethod, "", false},
		}
	case "rust":
		return []symbolPattern{
			{regexp.MustCompile(`^\s*(?:pub\s+)?(?:async\s+)?fn\s+([A-Za-z_][A-Za-z0-9_]*)\s*[(<]`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*(?:pub\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindStruct, "", false},
			{regexp.MustCompile(`^\s*(?:pub\s+)?enum\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindEnum, "", false},
			{regexp.MustCompile(`^\s*(?:pub\s+)?trait\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindTrait, "", false},
			{regexp.MustCompile(`^\s*type\s+([A-Za-z_][A-Za-z0-9_]*)\s*=`), core.KindType, "", false},
		}
	case "c", "cpp":
		return []symbolPattern{
			// Free function: return-type name(  — anchored to avoid matching variable decls
			{regexp.MustCompile(`^(?:[\w*&:<>\s]+\s+)+\*?([A-Za-z_][A-Za-z0-9_:]*)\s*\([^;]*$`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*(?:typedef\s+)?struct\s+([A-Za-z_][A-Za-z0-9_]*)\s*[{;]`), core.KindStruct, "", false},
			{regexp.MustCompile(`^\s*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindClass, "", false},
			{regexp.MustCompile(`^\s*enum\s+(?:class\s+)?([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindEnum, "", false},
			{regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][A-Za-z0-9_]*)\s*\{`), core.KindNamespace, "", false},
		}
	case "csharp":
		return []symbolPattern{
			{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|abstract|virtual|override|sealed|async)\s+)*(?:[\w<>\[\],?]+\s+)+([A-Za-z_][A-Za-z0-9_]*)\s*\(`), core.KindMethod, "", false},
			{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal|static|abstract|sealed)\s+)*class\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindClass, "", false},
			{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal)\s+)*interface\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindInterface, "", false},
			{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal)\s+)*(?:struct|record)\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindStruct, "", false},
			{regexp.MustCompile(`^\s*(?:(?:public|private|protected|internal)\s+)*enum\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindEnum, "", false},
			{regexp.MustCompile(`^\s*namespace\s+([A-Za-z_][A-Za-z0-9_.]*)\s*[{;]`), core.KindNamespace, "", false},
		}
	case "php":
		return []symbolPattern{
			{regexp.MustCompile(`^\s*(?:public|protected|private|static|abstract|final|\s)*function\s+([A-Za-z_][A-Za-z0-9_]*)\s*\(`), core.KindFunction, "", false},
			{regexp.MustCompile(`^\s*(?:abstract\s+|final\s+)?class\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindClass, "", false},
			{regexp.MustCompile(`^\s*interface\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindInterface, "", false},
			{regexp.MustCompile(`^\s*trait\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindTrait, "", false},
			{regexp.MustCompile(`^\s*enum\s+([A-Za-z_][A-Za-z0-9_]*)\b`), core.KindEnum, "", false},
		}
	default:
		return nil
	}
}

// extractSymbols returns symbols for the given source file.
// Tree-sitter AST extraction is tried first; the regex extractor is used as
// a fallback for languages not yet covered or for parse timeouts.
//
// When tree-sitter reports syntax errors (file actively being edited), both
// extractors are run and their results are merged: AST symbols take precedence
// (more accurate), but any symbol name the regex extractor found that the AST
// missed is added. This prevents a partially-typed function from disappearing
// from the index entirely while the developer is writing it.
func extractSymbols(language, filePath, blobSHA, content string, fileImports []string) []core.SymbolRecord {
	astSyms, ok, hasErrors := extractSymbolsFromAST(language, filePath, blobSHA, []byte(content), fileImports)
	if !ok {
		syms := extractSymbolsRegex(language, filePath, blobSHA, content, fileImports)
		attachDocstrings(language, content, syms)
		return syms
	}
	if !hasErrors {
		if language == "c" || language == "cpp" {
			astSyms = mergeSymbolsByShape(astSyms, extractSymbolsRegex(language, filePath, blobSHA, content, fileImports))
		}
		attachDocstrings(language, content, astSyms)
		return astSyms
	}
	// Syntax errors present — supplement AST results with regex to recover symbols
	// that fell inside ERROR subtrees (e.g. a function being actively typed).
	regexSyms := extractSymbolsRegex(language, filePath, blobSHA, content, fileImports)
	merged := mergeSymbols(astSyms, regexSyms)
	attachDocstrings(language, content, merged)
	return merged
}

// mergeSymbols returns the union of astSyms and regexSyms, preferring AST
// results for any symbol name that appears in both. Regex-only symbols are
// appended at the end — they cover declarations inside ERROR subtrees.
func mergeSymbols(astSyms, regexSyms []core.SymbolRecord) []core.SymbolRecord {
	if len(regexSyms) == 0 {
		return astSyms
	}
	seen := make(map[string]bool, len(astSyms))
	for _, s := range astSyms {
		seen[s.Name] = true
	}
	merged := append([]core.SymbolRecord(nil), astSyms...)
	for _, s := range regexSyms {
		if !seen[s.Name] {
			merged = append(merged, s)
		}
	}
	return merged
}

func mergeSymbolsByShape(astSyms, regexSyms []core.SymbolRecord) []core.SymbolRecord {
	if len(regexSyms) == 0 {
		return astSyms
	}
	seen := make(map[string]bool, len(astSyms))
	for _, s := range astSyms {
		seen[symbolShapeKey(s)] = true
	}
	merged := append([]core.SymbolRecord(nil), astSyms...)
	for _, s := range regexSyms {
		key := symbolShapeKey(s)
		if !seen[key] {
			seen[key] = true
			merged = append(merged, s)
		}
	}
	return merged
}

func symbolShapeKey(s core.SymbolRecord) string {
	return s.Name + "\x00" + string(s.Kind) + "\x00" + s.ParentSymbol
}

// attachDocstrings fills SymbolRecord.Docstring by looking at the source.
// Convention:
//
//   - Go / JS / TS / TSX / Java / Rust: preceding contiguous comment block
//     (`//`, `///`, `/** ... */`).
//   - Python: triple-quoted string as the first statement of the symbol body.
func attachDocstrings(language, content string, symbols []core.SymbolRecord) {
	lines := strings.Split(content, "\n")
	for i := range symbols {
		sym := &symbols[i]
		if sym.Docstring != "" || sym.Span.Start <= 0 {
			continue
		}
		if language == "python" {
			sym.Docstring = pythonDocstring(sym.RawText)
			continue
		}
		sym.Docstring = precedingCommentBlock(lines, sym.Span.Start)
	}
}

func precedingCommentBlock(lines []string, startLine int) string {
	idx := startLine - 2 // line just above the symbol (0-indexed)
	if idx < 0 || idx >= len(lines) {
		return ""
	}

	// /** ... */ block: walk upward to find opening "/**".
	if strings.HasSuffix(strings.TrimSpace(lines[idx]), "*/") {
		end := idx
		for idx >= 0 && !strings.Contains(lines[idx], "/**") {
			idx--
		}
		if idx < 0 {
			return ""
		}
		return cleanBlockComment(strings.Join(lines[idx:end+1], "\n"))
	}

	// Contiguous //-style or ///-style comments (and Rust ///).
	var collected []string
	for idx >= 0 {
		line := strings.TrimSpace(lines[idx])
		if !(strings.HasPrefix(line, "//") || strings.HasPrefix(line, "///")) {
			break
		}
		collected = append([]string{stripLineComment(line)}, collected...)
		idx--
	}
	if len(collected) == 0 {
		return ""
	}
	return strings.TrimSpace(strings.Join(collected, "\n"))
}

func stripLineComment(line string) string {
	line = strings.TrimPrefix(line, "///")
	line = strings.TrimPrefix(line, "//")
	return strings.TrimSpace(line)
}

func cleanBlockComment(block string) string {
	block = strings.TrimSpace(block)
	block = strings.TrimPrefix(block, "/**")
	block = strings.TrimPrefix(block, "/*")
	block = strings.TrimSuffix(block, "*/")
	var cleaned []string
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}

func pythonDocstring(body string) string {
	// First non-empty line after the `def ...:` / `class ...:` header that
	// starts with """ or '''. Capture until the matching closing triple-quote.
	lines := strings.Split(body, "\n")
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		quote := ""
		switch {
		case strings.HasPrefix(line, `"""`):
			quote = `"""`
		case strings.HasPrefix(line, `'''`):
			quote = `'''`
		default:
			return ""
		}
		// Single-line docstring.
		body := strings.TrimPrefix(line, quote)
		if strings.HasSuffix(body, quote) && body != "" {
			return strings.TrimSpace(strings.TrimSuffix(body, quote))
		}
		// Multi-line docstring.
		var collected []string
		if body != "" {
			collected = append(collected, body)
		}
		for j := i + 1; j < len(lines); j++ {
			text := lines[j]
			if strings.Contains(text, quote) {
				collected = append(collected, strings.TrimSuffix(strings.TrimSpace(text), quote))
				break
			}
			collected = append(collected, strings.TrimSpace(text))
		}
		return strings.TrimSpace(strings.Join(collected, "\n"))
	}
	return ""
}

// extractSymbolsRegex is the regex-based fallback extractor.
func extractSymbolsRegex(language, filePath, blobSHA, content string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns(language)
	if len(patterns) == 0 {
		return nil
	}

	if language == "go" {
		return extractGoSymbols(filePath, blobSHA, content, fileImports)
	}
	if language == "c" || language == "cpp" {
		return extractCFamilySymbols(language, filePath, blobSHA, content, fileImports)
	}

	lines := strings.Split(content, "\n")
	var symbols []core.SymbolRecord

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, pattern := range patterns {
			matches := pattern.regex.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}

			name, parentSymbol := extractNameAndParent(matches, pattern)
			if name == "" {
				continue
			}

			qualifiedName := name
			if pattern.qualifier != "" {
				qualifiedName = pattern.qualifier + "." + name
			}

			endLine, body := extractBody(lines, i, language)
			symbol := core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@%s", filePath, qualifiedName, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      language,
				Kind:          pattern.kind,
				Name:          name,
				QualifiedName: qualifiedName,
				Signature:     strings.TrimSpace(line),
				Span:          core.LineRange{Start: i + 1, End: endLine},
				Exports:       isExported(language, name, line),
				RawText:       body,
				ParentSymbol:  parentSymbol,
				Imports:       fileImports,
				TokenEstimate: estimateTokens(body),
			}
			symbols = append(symbols, symbol)
			break
		}
	}
	return symbols
}

// extractCFamilySymbols handles top-level C/C++ declarations plus simple
// in-class C++ method and constructor declarations.
func extractCFamilySymbols(language, filePath, blobSHA, content string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns(language)
	lines := strings.Split(content, "\n")
	var symbols []core.SymbolRecord

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, pattern := range patterns {
			matches := pattern.regex.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}

			name, parentSymbol := extractNameAndParent(matches, pattern)
			if name == "" {
				continue
			}
			endLine, body := extractBody(lines, i, language)
			if language == "cpp" && (pattern.kind == core.KindClass || pattern.kind == core.KindStruct) {
				endLine, body = extractBraceBody(lines, i)
			}
			symbols = append(symbols, core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@%s", filePath, name, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      language,
				Kind:          pattern.kind,
				Name:          name,
				QualifiedName: name,
				Signature:     strings.TrimSpace(line),
				Span:          core.LineRange{Start: i + 1, End: endLine},
				Exports:       isExported(language, name, line),
				RawText:       body,
				ParentSymbol:  parentSymbol,
				Imports:       fileImports,
				TokenEstimate: estimateTokens(body),
			})
			if language == "cpp" && pattern.kind == core.KindClass {
				symbols = append(symbols, extractCPPClassMembers(filePath, blobSHA, name, lines, i+1, endLine-1, fileImports)...)
			}
			break
		}
	}
	return symbols
}

var cppMemberPattern = regexp.MustCompile(`^\s*(?:(?:virtual|static|inline|constexpr|explicit|friend)\s+)*(?:(?:[\w:<>,~*&]+\s+)+)?(~?[A-Za-z_][A-Za-z0-9_]*)\s*\([^;{}]*\)\s*(?:const\s*)?(?:=\s*0\s*)?[;{]`)

func extractCPPClassMembers(filePath, blobSHA, className string, lines []string, start, end int, fileImports []string) []core.SymbolRecord {
	var symbols []core.SymbolRecord
	for i := start; i < end && i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasSuffix(trimmed, ":") {
			continue
		}
		matches := cppMemberPattern.FindStringSubmatch(line)
		if len(matches) != 2 {
			continue
		}
		name := matches[1]
		if name == "" || strings.HasPrefix(name, "~") {
			continue
		}
		kind := core.KindMethod
		if name == className {
			kind = core.KindConstructor
		}
		qualifiedName := className + "." + name
		bodyEnd, body := extractBody(lines, i, "cpp")
		if bodyEnd > end || !strings.Contains(line, "{") {
			bodyEnd = i + 1
			body = strings.TrimSpace(line)
		}
		symbols = append(symbols, core.SymbolRecord{
			ID:            fmt.Sprintf("%s::%s@%s", filePath, qualifiedName, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "cpp",
			Kind:          kind,
			Name:          name,
			QualifiedName: qualifiedName,
			Signature:     strings.TrimSpace(line),
			Span:          core.LineRange{Start: i + 1, End: bodyEnd},
			Exports:       true,
			RawText:       body,
			ParentSymbol:  className,
			Imports:       fileImports,
			TokenEstimate: estimateTokens(body),
		})
	}
	return symbols
}

// extractNameAndParent returns (name, parentSymbol) from regex matches.
func extractNameAndParent(matches []string, pattern symbolPattern) (string, string) {
	if pattern.captureParent && len(matches) >= 3 {
		return matches[2], matches[1]
	}
	return matches[len(matches)-1], ""
}

// extractGoSymbols handles Go-specific extraction including const/var blocks
// and receiver methods.
//
// IMPORTANT: uses an index-based loop (not range) so that after extracting a
// function/method body we can skip i past the end of that body, preventing
// local variables inside the body (e.g. "var req struct{...}") from being
// mistakenly extracted as package-level symbols.
func extractGoSymbols(filePath, blobSHA, content string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns("go")
	lines := strings.Split(content, "\n")
	var symbols []core.SymbolRecord

	// Pre-compile block-member regex once.
	blockMemberRe := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\b`)

	inConstBlock := false
	inVarBlock := false

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}

		// Handle const ( ... ) blocks
		if strings.HasPrefix(trimmed, "const (") {
			inConstBlock = true
			continue
		}
		if strings.HasPrefix(trimmed, "var (") {
			inVarBlock = true
			continue
		}
		if (inConstBlock || inVarBlock) && trimmed == ")" {
			inConstBlock = false
			inVarBlock = false
			continue
		}
		if inConstBlock {
			// Extract constant names from block: Name = value or Name Type = value
			if m := blockMemberRe.FindStringSubmatch(line); len(m) == 2 {
				name := m[1]
				symbols = append(symbols, core.SymbolRecord{
					ID:            fmt.Sprintf("%s::%s@%s", filePath, name, blobSHA),
					FilePath:      filePath,
					BlobSHA:       blobSHA,
					Language:      "go",
					Kind:          core.KindConst,
					Name:          name,
					QualifiedName: name,
					Signature:     strings.TrimSpace(line),
					Span:          core.LineRange{Start: i + 1, End: i + 1},
					Exports:       isExported("go", name, line),
					RawText:       strings.TrimSpace(line),
					TokenEstimate: estimateTokens(line),
				})
			}
			continue
		}
		if inVarBlock {
			if m := blockMemberRe.FindStringSubmatch(line); len(m) == 2 {
				name := m[1]
				symbols = append(symbols, core.SymbolRecord{
					ID:            fmt.Sprintf("%s::%s@%s", filePath, name, blobSHA),
					FilePath:      filePath,
					BlobSHA:       blobSHA,
					Language:      "go",
					Kind:          core.KindVariable,
					Name:          name,
					QualifiedName: name,
					Signature:     strings.TrimSpace(line),
					Span:          core.LineRange{Start: i + 1, End: i + 1},
					Exports:       isExported("go", name, line),
					RawText:       strings.TrimSpace(line),
					TokenEstimate: estimateTokens(line),
				})
			}
			continue
		}

		// Regular top-level declaration extraction.
		// After extracting a body we advance i to endLine-1 so the next
		// iteration starts on the first line AFTER the closing brace.
		for _, pattern := range patterns {
			matches := pattern.regex.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}

			name, parentSymbol := extractNameAndParent(matches, pattern)
			if name == "" {
				continue
			}

			endLine, body := extractBody(lines, i, "go")

			symbols = append(symbols, core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@%s", filePath, name, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      "go",
				Kind:          pattern.kind,
				Name:          name,
				QualifiedName: name,
				Signature:     strings.TrimSpace(line),
				Span:          core.LineRange{Start: i + 1, End: endLine},
				Exports:       isExported("go", name, line),
				RawText:       body,
				ParentSymbol:  parentSymbol,
				TokenEstimate: estimateTokens(body),
			})
			// Skip past the body so inner declarations are not re-extracted.
			i = endLine - 1
			break
		}
	}
	return symbols
}

func isExported(language, name, line string) bool {
	switch language {
	case "go":
		return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
	case "typescript", "tsx", "javascript":
		return strings.Contains(line, "export ") || strings.Contains(line, "module.exports")
	case "python":
		return !strings.HasPrefix(name, "_")
	case "java", "rust":
		return strings.Contains(line, "public ") || strings.Contains(line, "pub ")
	default:
		return false
	}
}

func estimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) / 4) + 1
}
