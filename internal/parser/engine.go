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

	"github.com/provasign/astkit/textmask"
	"github.com/provasign/grove/internal/core"
)

type Engine struct{}

const MaxFileSizeBytes int64 = 10 * 1024 * 1024

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) ExtractFile(path string, root string) ([]core.SymbolRecord, error) {
	language := DetectLanguageFile(path)
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
	return e.ExtractContent(relPath, content)
}

// ExtractContent extracts symbols from in-memory content as if it lived at
// relPath (repo-relative, slash-separated). This is how callers preview the
// symbols a not-yet-written file would index — e.g. a merge driver whose
// result git only writes to the worktree after the driver exits.
func (e *Engine) ExtractContent(relPath string, content []byte) ([]core.SymbolRecord, error) {
	language := DetectLanguageContent(relPath, content)
	if language == "" {
		return nil, nil
	}
	if int64(len(content)) > MaxFileSizeBytes {
		return nil, fmt.Errorf("skip %s: content exceeds %d bytes", relPath, MaxFileSizeBytes)
	}
	relPath = filepath.ToSlash(relPath)
	blobSHA := sha1Hex(content)

	if language == PlaintextLanguage {
		return ExtractPlaintext(relPath, blobSHA, content), nil
	}

	src := string(content)
	imports := extractImports(language, src)
	symbols := extractSymbols(language, relPath, blobSHA, src, imports)
	if language == "cobol" && len(symbols) == 0 {
		// A COBOL file that yields no symbols must still be resolvable as a
		// COPY member and distinguishable from an unindexed file (spec
		// R-7.3): member resolution keys on file basenames drawn from
		// symbols, so a parse-empty copybook otherwise silently breaks the
		// include graph for every unit that copies it.
		base := relPath
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if i := strings.IndexByte(base, '.'); i >= 0 {
			base = base[:i]
		}
		name := strings.ToUpper(base)
		symbols = append(symbols, core.SymbolRecord{
			ID:            symID(relPath, name, blobSHA),
			FilePath:      relPath,
			BlobSHA:       blobSHA,
			Language:      language,
			Kind:          core.SymbolKind("copybook-member"),
			Name:          name,
			QualifiedName: name,
			Signature:     "member (no symbols extracted)",
			Span:          core.LineRange{Start: 1, End: 1},
			Imports:       append([]string(nil), imports...),
		})
	}
	ensureUniqueIDs(symbols)
	return symbols, nil
}

// ensureUniqueIDs disambiguates duplicate symbol IDs within one file. Parent
// qualification handles the common cases (methods on different receivers or
// classes), but extraction paths without parent context — and pathological
// inputs like two same-named nested classes — can still collide. Without this,
// the store's PRIMARY KEY dedup silently drops every colliding symbol after
// the first. The suffix is deterministic because extraction order is document
// order.
func ensureUniqueIDs(symbols []core.SymbolRecord) {
	if len(symbols) < 2 {
		return
	}
	seen := make(map[string]int, len(symbols))
	for i := range symbols {
		id := symbols[i].ID
		n := seen[id]
		seen[id] = n + 1
		if n > 0 {
			symbols[i].ID = fmt.Sprintf("%s#%d", id, n+1)
		}
	}
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
		if !SupportedFile(path) {
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
	// The tree walk hashes every candidate with FileBlobSHA BEFORE Parse gets
	// to apply its MaxFileSizeBytes guard, so this must guard itself. An
	// unbounded os.ReadFile here means a 2 GB file named x.go is slurped whole
	// into RAM (OOM the indexer) and a named pipe / device named x.go blocks
	// os.ReadFile forever (hang the index) — both trivially plantable in an
	// untrusted repo. Reject non-regular files and cap the read.
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular file: %s", path)
	}
	if info.Size() > MaxFileSizeBytes {
		return "", fmt.Errorf("file exceeds %d bytes: %s", MaxFileSizeBytes, path)
	}
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

	// Fallback (unsupported grammar, parse failure or timeout): the
	// line patterns read only code. Paths are string literals, so for the
	// languages that quote them only comments are blanked; the others
	// blank literals too (a docstring line "import os" is prose).
	switch language {
	case "go", "typescript", "tsx", "javascript", "c", "cpp", "php":
		content = textmask.MaskComments(language, content)
	default:
		content = textmask.Mask(language, content)
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
// starting from startIdx (0-indexed into lines). Callers pass the scanMask
// lines (comments and literals blanked) so a brace or dedent inside a
// comment, string, text block or docstring does not end the body, and cut
// the source body by the returned line.
func extractBody(lines []string, startIdx int, language string) (endLine int, body string) {
	switch language {
	case "go", "typescript", "tsx", "javascript", "java", "rust", "c", "cpp", "csharp":
		return extractBraceBody(lines, startIdx)
	case "python":
		return extractIndentBody(lines, startIdx)
	default:
		return startIdx + 1, lines[startIdx]
	}
}

// extractBraceBody scans forward from startIdx, tracking brace depth,
// and returns when the opening brace is balanced (depth returns to 0).
// lines are masked (scanMask), so every brace counted is code.
func extractBraceBody(lines []string, startIdx int) (endLine int, body string) {
	depth := 0
	opened := false

	const maxLines = 500
	limit := startIdx + maxLines
	if limit > len(lines) {
		limit = len(lines)
	}

	for i := startIdx; i < limit; i++ {
		line := lines[i]
		for j := 0; j < len(line); j++ {
			switch line[j] {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
		if opened && depth <= 0 {
			return i + 1, strings.Join(lines[startIdx:i+1], "\n")
		}
	}

	// No closing brace found (e.g., interface method, type declaration) — single-line
	if !opened {
		return startIdx + 1, lines[startIdx]
	}
	return limit, strings.Join(lines[startIdx:limit], "\n")
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
			// Global operator declaration in a header.
			{regexp.MustCompile(`^\s*(?:[\w*&:<>]+\s+)+(` + cppCallableNamePattern + `)\s*\([^;{}]*\)\s*;`), core.KindFunction, "", false},
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
	ast := extractASTForMerge(language, filePath, blobSHA, []byte(content), fileImports)
	astSyms, ok, hasErrors := ast.syms, ast.ok, ast.hasErrors
	if !ok {
		scan := scanMask(language, content)
		syms := extractSymbolsRegexScan(language, filePath, blobSHA, content, scan, fileImports)
		if language == "cpp" {
			syms = enrichCppNamespaces(syms, scan)
		}
		attachDocstrings(language, content, syms)
		return syms
	}
	if !hasErrors {
		if language == "c" || language == "cpp" {
			scan := scanMask(language, content)
			regexSyms := extractSymbolsRegexScan(language, filePath, blobSHA, content, scan, fileImports)
			if language == "cpp" {
				regexSyms = dropCppNamespaceTwins(astSyms, regexSyms)
				n := len(astSyms)
				combined := enrichCppNamespaces(append(append([]core.SymbolRecord(nil), astSyms...), regexSyms...), scan)
				astSyms, regexSyms = combined[:n], combined[n:]
			}
			astSyms = mergeSymbolsByShape(astSyms, regexSyms)
		}
		attachDocstrings(language, content, astSyms)
		return astSyms
	}
	// Syntax errors present — supplement AST results with regex to recover symbols
	// that fell inside ERROR subtrees (e.g. a function being actively typed).
	// JSX text is prose the textmask lexer reads as code; the partial tree
	// still knows it, so it is blanked by node range.
	scan := blankByteRanges(scanMask(language, content), ast.opaque)
	regexSyms := extractSymbolsRegexScan(language, filePath, blobSHA, content, scan, fileImports)
	for idx := range regexSyms {
		regexSyms[idx].Annotations = append(regexSyms[idx].Annotations, "syntax-recovery")
	}
	if language == "cpp" {
		regexSyms = dropCppNamespaceTwins(astSyms, regexSyms)
		n := len(astSyms)
		combined := enrichCppNamespaces(append(append([]core.SymbolRecord(nil), astSyms...), regexSyms...), scan)
		astSyms, regexSyms = combined[:n], combined[n:]
	}
	merged := mergeSymbols(astSyms, regexSyms)
	if language == "csharp" || language == "java" {
		enrichRecoveredClassParents(merged)
	}
	attachDocstrings(language, content, merged)
	return merged
}

// blankByteRanges returns s with the bytes of every [start, end) range
// replaced by spaces, newlines kept.
func blankByteRanges(s string, ranges [][2]int) string {
	if len(ranges) == 0 {
		return s
	}
	out := []byte(s)
	for _, r := range ranges {
		for k := r[0]; k < r[1] && k < len(out); k++ {
			if out[k] != '\n' && out[k] != '\r' {
				out[k] = ' '
			}
		}
	}
	return string(out)
}

// dropCppNamespaceTwins removes the line scanner's copy of every namespace
// the AST also reports (same name, same first line). The copy's span is
// wrong: with clang-format's `}  // namespace foo` its text runs past the
// AST's closing brace, so it "strictly contained" its twin and every name in
// the file became foo::foo::...; past 500 lines its brace scan stops early,
// so it was the narrowest scope around the file's first 500 lines and
// `namespace testing { namespace internal {` members landed in testing::.
func dropCppNamespaceTwins(astSyms, regexSyms []core.SymbolRecord) []core.SymbolRecord {
	astNamespaces := map[string]bool{}
	for _, s := range astSyms {
		if s.Kind == core.KindNamespace {
			astNamespaces[s.Name+"\x00"+fmt.Sprint(s.Span.Start)] = true
		}
	}
	if len(astNamespaces) == 0 {
		return regexSyms
	}
	kept := regexSyms[:0:0]
	for _, s := range regexSyms {
		if s.Kind == core.KindNamespace && astNamespaces[s.Name+"\x00"+fmt.Sprint(s.Span.Start)] {
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

func enrichRecoveredClassParents(symbols []core.SymbolRecord) {
	for idx := range symbols {
		symbol := &symbols[idx]
		if symbol.ParentSymbol != "" || (symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor) {
			continue
		}
		best := -1
		bestWidth := int(^uint(0) >> 1)
		for parentIdx := range symbols {
			parent := &symbols[parentIdx]
			switch parent.Kind {
			case core.KindClass, core.KindStruct, core.KindInterface:
			default:
				continue
			}
			if parent.FilePath != symbol.FilePath || parent.Span.Start > symbol.Span.Start || parent.Span.End < symbol.Span.End {
				continue
			}
			if width := parent.Span.End - parent.Span.Start; width < bestWidth {
				best, bestWidth = parentIdx, width
			}
		}
		if best < 0 {
			continue
		}
		parent := &symbols[best]
		symbol.ParentSymbol = parent.Name
		symbol.QualifiedName = parent.Name + "." + symbol.Name
		symbol.ID = symID(symbol.FilePath, symbol.QualifiedName, symbol.BlobSHA)
	}
}

const cppCallableNamePattern = `(?:operator\s*(?:\[\]|\(\)|new(?:\[\])?|delete(?:\[\])?|[+*/%<>=!&|^~-]+)|~?[A-Za-z_][A-Za-z0-9_]*)`

var cppQualifiedCallableRe = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_]*(?:<[^;{}()]+>)?(?:::[A-Za-z_][A-Za-z0-9_]*(?:<[^;{}()]+>)?)*)::(` + cppCallableNamePattern + `)\s*\(`)

func cppStripTemplateArgs(name string) string {
	var out strings.Builder
	depth := 0
	for _, r := range name {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				out.WriteRune(r)
			}
		}
	}
	return strings.TrimSpace(out.String())
}

// enrichCppNamespaces qualifies symbols by their enclosing namespaces and
// attaches the file's namespace bindings. scan is the file's scanMask.
func enrichCppNamespaces(symbols []core.SymbolRecord, scan string) []core.SymbolRecord {
	type scope struct {
		index  int
		parent int
		path   string
	}
	var scopes []scope
	for i := range symbols {
		if symbols[i].Kind == core.KindNamespace {
			scopes = append(scopes, scope{index: i, parent: -1})
		}
	}
	for i := range scopes {
		child := symbols[scopes[i].index]
		bestWidth := int(^uint(0) >> 1)
		bestRawWidth := int(^uint(0) >> 1)
		for j := range scopes {
			if i == j {
				continue
			}
			parent := symbols[scopes[j].index]
			if parent.Span.Start > child.Span.Start || parent.Span.End < child.Span.End {
				continue
			}
			width := parent.Span.End - parent.Span.Start
			rawWidth := len(parent.RawText)
			strictlyContains := width > child.Span.End-child.Span.Start ||
				(rawWidth > len(child.RawText) && strings.Contains(parent.RawText, child.RawText))
			if !strictlyContains {
				continue
			}
			if width < bestWidth || (width == bestWidth && rawWidth < bestRawWidth) {
				bestWidth = width
				bestRawWidth = rawWidth
				scopes[i].parent = j
			}
		}
	}
	state := make([]uint8, len(scopes))
	var scopePath func(int) string
	scopePath = func(i int) string {
		if scopes[i].path != "" {
			return scopes[i].path
		}
		if state[i] == 1 {
			// Span metadata from fallback extractors can be coarse. Never let a
			// malformed containment cycle turn one valid file into a process-wide
			// stack overflow and an empty repository index.
			scopes[i].parent = -1
			return symbols[scopes[i].index].Name
		}
		state[i] = 1
		name := symbols[scopes[i].index].Name
		if scopes[i].parent >= 0 {
			name = scopePath(scopes[i].parent) + "::" + name
		}
		scopes[i].path = name
		state[i] = 2
		return name
	}
	for i := range scopes {
		path := scopePath(i)
		symbols[scopes[i].index].QualifiedName = path
	}
	bindings := cppFileNamespaceBindings(scan)
	for i := range symbols {
		symbol := &symbols[i]
		symbol.Annotations = append(symbol.Annotations, bindings...)
		if symbol.Kind == core.KindNamespace {
			continue
		}
		namespace := ""
		bestWidth := int(^uint(0) >> 1)
		bestRawWidth := int(^uint(0) >> 1)
		for j := range scopes {
			ns := symbols[scopes[j].index]
			if ns.Span.Start <= symbol.Span.Start && ns.Span.End >= symbol.Span.End {
				width, rawWidth := ns.Span.End-ns.Span.Start, len(ns.RawText)
				if width < bestWidth || (width == bestWidth && rawWidth < bestRawWidth) {
					bestWidth = width
					bestRawWidth = rawWidth
					namespace = scopePath(j)
				}
			}
		}
		owner, name := symbol.ParentSymbol, symbol.Name
		// A line-scanned symbol's Signature is its raw source line, trailing
		// comment included: `void helper(int x) { // see Widget::draw(x)`
		// must not become Widget::draw.
		if match := cppQualifiedCallableRe.FindStringSubmatch(textmask.Mask("cpp", symbol.Signature)); len(match) == 3 {
			owner, name = cppStripTemplateArgs(match[1]), strings.ReplaceAll(match[2], " ", "")
		} else if split := strings.LastIndex(symbol.Name, "::"); split >= 0 {
			owner, name = symbol.Name[:split], symbol.Name[split+2:]
		}
		if owner != "" {
			if namespace != "" {
				owner = cppJoinScope(namespace, owner)
			}
			symbol.Name = name
			symbol.ParentSymbol = owner
			// C++ spells every scope `::`; a class outside any namespace
			// used to get `Global.g` while namespaced ones got `ns::A::g`.
			symbol.QualifiedName = owner + "::" + name
			if symbol.Kind == core.KindFunction {
				ownerName := owner
				if split := strings.LastIndex(owner, "::"); split >= 0 {
					ownerName = owner[split+2:]
				}
				if name == ownerName {
					symbol.Kind = core.KindConstructor
				} else {
					symbol.Kind = core.KindMethod
				}
			}
		} else if namespace != "" {
			symbol.QualifiedName = namespace + "::" + symbol.Name
		}
	}
	return symbols
}

// cppJoinScope qualifies an owner path written inside namespace ns. A bare
// owner (`Circle`) or a nested class path from the extractor (`Box::Node`)
// lives in ns; an owner that already spells some of ns (`detail::X` inside
// `nlohmann::detail`, `nlohmann::X` inside `nlohmann`) is not repeated.
func cppJoinScope(ns, owner string) string {
	if strings.HasPrefix(owner, "::") {
		return strings.TrimPrefix(owner, "::")
	}
	if !strings.Contains(owner, "::") {
		return ns + "::" + owner
	}
	if strings.HasPrefix(owner, ns+"::") {
		return owner
	}
	nsSegs := strings.Split(ns, "::")
	ownerSegs := strings.Split(owner, "::")
	for k := len(nsSegs); k > 0; k-- {
		if k >= len(ownerSegs) {
			continue
		}
		if strings.Join(nsSegs[len(nsSegs)-k:], "::") == strings.Join(ownerSegs[:k], "::") {
			return strings.Join(append(append([]string(nil), nsSegs[:len(nsSegs)-k]...), ownerSegs...), "::")
		}
	}
	return ns + "::" + owner
}

var (
	cppUsingNamespaceLineRe = regexp.MustCompile(`^using\s+namespace\s+([A-Za-z_][A-Za-z0-9_:]*)\s*;`)
	cppNamespaceAliasLineRe = regexp.MustCompile(`^namespace\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*([A-Za-z_][A-Za-z0-9_:]*)\s*;`)
	cppUsingTypeLineRe      = regexp.MustCompile(`^using\s+([A-Za-z_][A-Za-z0-9_:]*)::([A-Za-z_][A-Za-z0-9_]*)\s*;`)
)

// cppFileNamespaceBindings reads file-scope using-directives, namespace
// aliases and using-declarations from scan (scanMask: comments, literals and
// #if 0 branches blanked; a digit separator or `#error don't` no longer
// opens a quote that swallows the rest of the file).
func cppFileNamespaceBindings(scan string) []string {
	depth := 0
	var out []string
	for _, line := range strings.Split(scan, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth == 0 {
			switch {
			case cppUsingNamespaceLineRe.MatchString(trimmed):
				match := cppUsingNamespaceLineRe.FindStringSubmatch(trimmed)
				out = append(out, core.CppUsingNamespace(match[1]))
			case cppNamespaceAliasLineRe.MatchString(trimmed):
				match := cppNamespaceAliasLineRe.FindStringSubmatch(trimmed)
				out = append(out, core.CppNamespaceAlias(match[1], match[2]))
			case cppUsingTypeLineRe.MatchString(trimmed):
				match := cppUsingTypeLineRe.FindStringSubmatch(trimmed)
				out = append(out, core.CppUsingType(match[2], match[1]+"::"+match[2]))
			}
		}
		for i := 0; i < len(line); i++ {
			switch line[i] {
			case '{':
				depth++
			case '}':
				if depth > 0 {
					depth--
				}
			}
		}
	}
	return out
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
		if cFamilyControlKeywordPhantom(&s) {
			continue
		}
		if cFamilyStatementPhantom(&s, astSyms) {
			continue
		}
		if !seen[s.Name] {
			cFamilyMarkPrototype(&s)
			merged = append(merged, s)
		}
	}
	return merged
}

// cFamilyStatementPhantom reports a C/C++ regex-recovered callable that
// starts inside an AST callable's span. The line-scanning fallback reads
// `return json_null();` and `json_object_foreach(root, key, value) {` as
// old-style definitions; the enclosing AST function proves they are
// statements. mergeSymbolsByShape applies this on the clean path; the
// syntax-recovery path (jansson's macro-heavy files) merged by name alone
// and let these twins steal every call to the real function.
func cFamilyStatementPhantom(s *core.SymbolRecord, astSyms []core.SymbolRecord) bool {
	if (s.Language != "c" && s.Language != "cpp") || !isCallableKind(s.Kind) {
		return false
	}
	for i := range astSyms {
		a := &astSyms[i]
		if isCallableKind(a.Kind) && s.Span.Start > a.Span.Start && s.Span.Start <= a.Span.End {
			return true
		}
	}
	return false
}

// cFamilyMarkPrototype annotates a C/C++ regex-recovered callable whose
// declarator ends in `;` before any `{` as a "declaration": a prototype,
// never a call target while the definition exists. The brace scanner had
// also run past the prototype to the next `{` in the file, so the span is
// clamped to the prototype's own lines.
func cFamilyMarkPrototype(s *core.SymbolRecord) {
	if (s.Language != "c" && s.Language != "cpp") || !isCallableKind(s.Kind) {
		return
	}
	for _, a := range s.Annotations {
		if a == "declaration" {
			return
		}
	}
	body := textmask.Mask(s.Language, s.RawText)
	semi := strings.IndexByte(body, ';')
	brace := strings.IndexByte(body, '{')
	if semi < 0 || (brace >= 0 && brace < semi) {
		return
	}
	s.Annotations = append(s.Annotations, "declaration")
	lines := strings.Count(body[:semi], "\n")
	s.Span.End = s.Span.Start + lines
	if raw := strings.Split(s.RawText, "\n"); len(raw) > lines {
		s.RawText = strings.Join(raw[:lines+1], "\n")
	}
}

func mergeSymbolsByShape(astSyms, regexSyms []core.SymbolRecord) []core.SymbolRecord {
	if len(regexSyms) == 0 {
		return astSyms
	}
	seen := make(map[string]bool, len(astSyms))
	callable := make(map[string]bool, len(astSyms))
	callableAt := make(map[string]bool, len(astSyms))
	type locationKey struct {
		name, parent string
		kind         core.SymbolKind
		start, end   int
	}
	locations := make(map[locationKey]bool, len(astSyms))
	for _, s := range astSyms {
		seen[symbolShapeKey(s)] = true
		locations[locationKey{s.Name, s.ParentSymbol, s.Kind, s.Span.Start, s.Span.End}] = true
		if isCallableKind(s.Kind) {
			callable[s.Name+"\x00"+s.ParentSymbol] = true
			callableAt[s.Name+"\x00"+fmt.Sprint(s.Span.Start)+"\x00"+fmt.Sprint(s.Span.End)] = true
		}
	}
	merged := append([]core.SymbolRecord(nil), astSyms...)
	for _, s := range regexSyms {
		if cFamilyControlKeywordPhantom(&s) {
			continue
		}
		insideASTCallable := false
		for _, astSymbol := range astSyms {
			if isCallableKind(astSymbol.Kind) && s.Span.Start > astSymbol.Span.Start && s.Span.Start <= astSymbol.Span.End {
				insideASTCallable = true
				break
			}
			if cFamilyASTTwin(&s, &astSymbol) {
				insideASTCallable = true
				break
			}
		}
		if insideASTCallable {
			// The C-family fallback scans lines without syntax context.
			// Statements such as `return json_null();` and `if (work())`
			// resemble old-style declarations, but an AST callable already
			// enclosing the line proves they are not file-level symbols.
			continue
		}
		cppDeclaration := false
		for _, annotation := range s.Annotations {
			if annotation == "declaration" {
				cppDeclaration = true
				break
			}
		}
		// A regex-found callable whose name the AST already declared in
		// this file is the same function seen through a prototype, a
		// macro-wrapped line, or a call statement mis-read as a
		// declaration — never a second function. jansson's do_dump got
		// three 1-line twins this way, and span-based matching then
		// attributed the real body's 64 calls to none of them.
		if isCallableKind(s.Kind) && callable[s.Name+"\x00"+s.ParentSymbol] && !cppDeclaration {
			continue
		}
		// Tree-sitter knows an inline method's class owner; the fallback regex
		// can see the same line as a parentless free function. Matching callable
		// name and exact span is sufficient to suppress that phantom twin while
		// retaining real overload declarations on distinct lines.
		if isCallableKind(s.Kind) && callableAt[s.Name+"\x00"+fmt.Sprint(s.Span.Start)+"\x00"+fmt.Sprint(s.Span.End)] {
			continue
		}
		if locations[locationKey{s.Name, s.ParentSymbol, s.Kind, s.Span.Start, s.Span.End}] {
			continue
		}
		key := symbolShapeKey(s)
		if !seen[key] {
			seen[key] = true
			merged = append(merged, s)
		}
	}
	return merged
}

// cFamilyASTTwin reports a line-scanned C/C++ symbol that a clean AST
// parse already accounts for: a callable on the first line of an AST
// callable (`operator bool() const {` scanned as a method named bool), a
// same-named symbol on an AST symbol's first line (a class the scanner cut
// at its 500-line window), or anything inside an AST class/struct/enum body,
// whose members the AST extracted itself (the scanner credited a nested
// union's constructors to the outer class).
func cFamilyASTTwin(s, a *core.SymbolRecord) bool {
	if s.Language != "c" && s.Language != "cpp" {
		return false
	}
	if s.Span.Start == a.Span.Start && (s.Name == a.Name || (isCallableKind(s.Kind) && isCallableKind(a.Kind))) {
		return true
	}
	switch a.Kind {
	case core.KindClass, core.KindStruct, core.KindEnum:
		return s.Span.Start > a.Span.Start && s.Span.Start <= a.Span.End
	}
	return false
}

func cFamilyControlKeywordPhantom(symbol *core.SymbolRecord) bool {
	if symbol == nil || (symbol.Language != "c" && symbol.Language != "cpp") || !isCallableKind(symbol.Kind) {
		return false
	}
	switch symbol.Name {
	case "if", "while", "for", "switch", "catch", "return", "sizeof", "alignof", "decltype":
		return true
	}
	return false
}

func isCallableKind(k core.SymbolKind) bool {
	return k == core.KindFunction || k == core.KindMethod || k == core.KindConstructor
}

func symbolShapeKey(s core.SymbolRecord) string {
	return s.Name + "\x00" + string(s.Kind) + "\x00" + s.ParentSymbol + "\x00" + s.Signature
}

// attachDocstrings fills SymbolRecord.Docstring by looking at the source.
// Convention:
//
//   - Go / JS / TS / TSX / Java / Rust: preceding contiguous comment block
//     (`//`, `///`, `/** ... */`).
//   - Python: triple-quoted string as the first statement of the symbol body.
func attachDocstrings(language, content string, symbols []core.SymbolRecord) {
	var lines, code []string
	for i := range symbols {
		sym := &symbols[i]
		if sym.Docstring != "" || sym.Span.Start <= 0 {
			continue
		}
		if language == "python" {
			sym.Docstring = pythonDocstring(sym.RawText)
			continue
		}
		if lines == nil {
			lines = strings.Split(content, "\n")
			code = commentMaskedLines(language, content)
		}
		sym.Docstring = precedingCommentBlock(lines, code, sym.Span.Start)
	}
}

// precedingCommentBlock returns the doc comment directly above startLine:
// a `/** ... */` block or a run of `//` / `///` lines. code is lines with
// comments blanked (commentMaskedLines; nil when the language has no
// lexer), so a comment line is one that is blank in code but not in lines.
// The block is found by walking up through comment lines only: a code line
// (`int x; /* trailing */`, `const glob = "src/**";`) ends the walk and is
// never part of a docstring, and a plain `/* */` block is not a doc
// comment.
func precedingCommentBlock(lines, code []string, startLine int) string {
	idx := startLine - 2 // line just above the symbol (0-indexed)
	if idx < 0 || idx >= len(lines) {
		return ""
	}
	isComment := func(i int) bool {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			return false
		}
		if code == nil || i >= len(code) {
			return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "/*") || strings.HasPrefix(t, "*")
		}
		return strings.TrimSpace(code[i]) == ""
	}
	if !isComment(idx) {
		return ""
	}

	// /** ... */ block: walk upward through its lines to the opening "/*".
	if strings.HasSuffix(strings.TrimSpace(lines[idx]), "*/") {
		end := idx
		for ; idx >= 0 && isComment(idx); idx-- {
			line := strings.TrimSpace(lines[idx])
			if strings.HasPrefix(line, "/*") {
				if !strings.HasPrefix(line, "/**") {
					return ""
				}
				return cleanBlockComment(strings.Join(lines[idx:end+1], "\n"))
			}
		}
		return ""
	}

	// Contiguous //-style or ///-style comments (and Rust ///).
	var collected []string
	for ; idx >= 0 && isComment(idx); idx-- {
		line := strings.TrimSpace(lines[idx])
		if !strings.HasPrefix(line, "//") {
			break
		}
		collected = append([]string{stripLineComment(line)}, collected...)
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
	return extractSymbolsRegexScan(language, filePath, blobSHA, content, scanMask(language, content), fileImports)
}

// extractSymbolsRegexScan is extractSymbolsRegex over a precomputed
// scanMask(language, content). Patterns match, braces and indentation are
// counted, and export keywords are read on the masked copy (line numbers
// kept); signatures and bodies still come from the source. Syntax recovery
// merges every regex name the AST missed, and the Java patterns are
// unanchored: Javadoc prose such as " * Deserializer class that can ..."
// became a class named "that" spanning hundreds of lines, and a text block,
// raw string, template literal or heredoc holding code became symbols.
func extractSymbolsRegexScan(language, filePath, blobSHA, content, scan string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns(language)
	if len(patterns) == 0 {
		return nil
	}

	if language == "go" {
		return extractGoSymbols(filePath, blobSHA, content, scan, fileImports)
	}
	if language == "c" || language == "cpp" {
		return extractCFamilySymbols(language, filePath, blobSHA, content, scan, fileImports)
	}

	lines := strings.Split(content, "\n")
	clean := strings.Split(scan, "\n")
	var symbols []core.SymbolRecord

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(clean[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}

		for _, pattern := range patterns {
			matches := pattern.regex.FindStringSubmatch(clean[i])
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
			if parentSymbol != "" {
				qualifiedName = parentSymbol + "." + name
			}

			endLine, _ := extractBody(clean, i, language)
			body := strings.Join(lines[i:endLine], "\n")
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
				Exports:       isExported(language, name, clean[i]),
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
func extractCFamilySymbols(language, filePath, blobSHA, content, scan string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns(language)
	lines := strings.Split(content, "\n")
	// Patterns match, and braces are counted, on scan (scanMask: comments,
	// literals, inactive #if 0 branches and directive lines blanked, line
	// numbers kept): the scanner read ` * Copyright (c) 2009` in license
	// headers as a function named Copyright, prose in block comments as
	// functions spanning hundreds of lines, and a raw string or a multi-line
	// #define body as definitions. Signatures and bodies still come from
	// the source.
	clean := strings.Split(scan, "\n")
	var symbols []core.SymbolRecord

	for i := 0; i < len(lines); i++ {
		line := clean[i]
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
			if language == "cpp" && strings.HasPrefix(name, "operator") {
				name = strings.ReplaceAll(name, " ", "")
			}
			if name == "" {
				continue
			}
			if isCallableKind(pattern.kind) && (cFamilyReservedName[name] || strings.HasPrefix(trimmed, "typedef")) {
				// `typedef int (*compare_fn)(...)` matched as a function
				// named int; a type keyword is never a function name.
				continue
			}
			endLine, _ := extractBody(clean, i, language)
			if language == "cpp" && (pattern.kind == core.KindClass || pattern.kind == core.KindStruct) {
				endLine, _ = extractBraceBody(clean, i)
			}
			body := strings.Join(lines[i:endLine], "\n")
			line := lines[i]
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
				symbols = append(symbols, extractCPPClassMembers(filePath, blobSHA, name, lines, clean, i+1, endLine-1, fileImports)...)
			}
			if (pattern.kind == core.KindClass || pattern.kind == core.KindStruct) && endLine > i+1 {
				i = endLine - 1
			}
			break
		}
	}
	return symbols
}

var cppMemberPattern = regexp.MustCompile(`^\s*(?:(?:virtual|static|inline|constexpr|consteval|constinit|explicit|friend)\s+)*(?:(?:[\w:<>,~*&]+\s+)+)?(` + cppCallableNamePattern + `)\s*\([^;{}]*\)\s*(?:(?:const|volatile|override|final|noexcept(?:\s*\([^)]*\))?|&&?)\s*)*(?:->\s*[\w:<>,~*&\s]+\s*)?(?:=\s*(?:0|default|delete)\s*)?[;{]`)

func extractCPPClassMembers(filePath, blobSHA, className string, lines, clean []string, start, end int, fileImports []string) []core.SymbolRecord {
	var symbols []core.SymbolRecord
	for i := start; i < end && i < len(lines); i++ {
		trimmed := strings.TrimSpace(clean[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "//") || strings.HasSuffix(trimmed, ":") {
			continue
		}
		matches := cppMemberPattern.FindStringSubmatch(clean[i])
		if len(matches) != 2 {
			continue
		}
		line := lines[i]
		name := matches[1]
		name = strings.ReplaceAll(name, " ", "")
		if name == "" || cFamilyReservedName[name] {
			continue
		}
		kind := core.KindMethod
		if name == className {
			kind = core.KindConstructor
		}
		qualifiedName := className + "." + name
		bodyEnd, _ := extractBody(clean, i, "cpp")
		body := strings.Join(lines[i:bodyEnd], "\n")
		if bodyEnd > end || !strings.Contains(clean[i], "{") {
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
		if !strings.Contains(clean[i], "{") {
			symbols[len(symbols)-1].Annotations = []string{"declaration"}
		}
	}
	return symbols
}

// cFamilyReservedName lists C/C++ keywords and builtin type names the
// line-scanning fallback must never report as a callable's name.
var cFamilyReservedName = map[string]bool{
	"auto": true, "bool": true, "char": true, "const": true, "double": true, "enum": true,
	"extern": true, "float": true, "int": true, "long": true, "register": true, "short": true,
	"signed": true, "static": true, "struct": true, "typedef": true, "union": true,
	"unsigned": true, "void": true, "volatile": true, "inline": true, "else": true, "do": true,
	"case": true, "goto": true, "break": true, "continue": true, "default": true,
	"wchar_t": true, "char8_t": true, "char16_t": true, "char32_t": true, "namespace": true,
	"class": true, "template": true, "typename": true, "using": true, "new": true, "delete": true,
	"throw": true, "noexcept": true, "static_assert": true, "alignas": true,
}

// scanMask returns the copy of a whole file that the line scanners match
// on: comments, string literals (raw strings, text blocks, heredocs,
// template literals), PHP inline HTML and inactive `#if 0` / `#if false`
// branches are blanked by astkit textmask, keeping every byte offset and
// newline, so a line index or column into the result indexes content. In
// C, C++ and Objective-C the lines of every preprocessor directive,
// backslash continuations included, are blanked too: a multi-line
// `#define` body is macro text, not declarations, and its braces must not
// move a brace count. Without this the scanners read Javadoc prose, raw
// strings and macro bodies as classes and functions.
func scanMask(language, content string) string {
	if !textmask.Supported(language) {
		return content
	}
	masked := textmask.MaskFile(language, textmask.MaskInactivePreprocessor(language, content))
	switch language {
	case "c", "cpp", "objc":
		masked = blankDirectiveLines(masked)
	}
	return masked
}

// blankDirectiveLines blanks every preprocessor directive line of masked
// (comments already blanked), with its backslash-continued lines.
func blankDirectiveLines(masked string) string {
	var out []byte
	continued := false
	for ls := 0; ls < len(masked); {
		le := strings.IndexByte(masked[ls:], '\n')
		if le < 0 {
			le = len(masked)
		} else {
			le += ls
		}
		trimmed := strings.TrimSpace(masked[ls:le])
		directive := continued || strings.HasPrefix(trimmed, "#")
		continued = directive && strings.HasSuffix(trimmed, "\\")
		if directive && trimmed != "" {
			if out == nil {
				out = []byte(masked)
			}
			for k := ls; k < le; k++ {
				if out[k] != '\r' {
					out[k] = ' '
				}
			}
		}
		ls = le + 1
	}
	if out == nil {
		return masked
	}
	return string(out)
}

// commentMaskedLines returns content's lines with only comments blanked
// (string literals kept), for telling comment lines from code lines.
func commentMaskedLines(language, content string) []string {
	if !textmask.Supported(language) {
		return nil
	}
	if language == "php" {
		// MaskComments lexes from byte 0 as code; a PHP file starts in
		// inline HTML, where an apostrophe would open a string. textmask
		// has no comments-only file mode, so the HTML before the first
		// `<?` is blanked here (newlines kept); later `?>` HTML is handled.
		if open := strings.Index(content, "<?"); open > 0 {
			head := []byte(content[:open])
			for k, c := range head {
				if c != '\n' && c != '\r' {
					head[k] = ' '
				}
			}
			content = string(head) + content[open:]
		}
	}
	return strings.Split(textmask.MaskComments(language, content), "\n")
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
func extractGoSymbols(filePath, blobSHA, content, scan string, fileImports []string) []core.SymbolRecord {
	patterns := symbolPatterns("go")
	lines := strings.Split(content, "\n")
	// Declarations are matched, and braces counted, on scan (comments and
	// literals blanked, line numbers kept): a func or type inside a block
	// comment or a raw string is not a declaration, and `const (` inside
	// one would put every following line in block mode.
	clean := strings.Split(scan, "\n")
	var symbols []core.SymbolRecord

	// Pre-compile block-member regex once.
	blockMemberRe := regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\b`)

	inConstBlock := false
	inVarBlock := false

	for i := 0; i < len(lines); i++ {
		line := clean[i]
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
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
					Signature:     strings.TrimSpace(lines[i]),
					Span:          core.LineRange{Start: i + 1, End: i + 1},
					Exports:       isExported("go", name, line),
					RawText:       strings.TrimSpace(lines[i]),
					TokenEstimate: estimateTokens(lines[i]),
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
					Signature:     strings.TrimSpace(lines[i]),
					Span:          core.LineRange{Start: i + 1, End: i + 1},
					Exports:       isExported("go", name, line),
					RawText:       strings.TrimSpace(lines[i]),
					TokenEstimate: estimateTokens(lines[i]),
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

			// Methods are qualified by their receiver type so that same-named
			// methods on different receivers get distinct IDs.
			qualifiedName := name
			if parentSymbol != "" {
				qualifiedName = parentSymbol + "." + name
			}

			endLine, _ := extractBody(clean, i, "go")
			body := strings.Join(lines[i:endLine], "\n")

			symbols = append(symbols, core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@%s", filePath, qualifiedName, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      "go",
				Kind:          pattern.kind,
				Name:          name,
				QualifiedName: qualifiedName,
				Signature:     strings.TrimSpace(lines[i]),
				Span:          core.LineRange{Start: i + 1, End: endLine},
				Exports:       isExported("go", name, line),
				RawText:       body,
				ParentSymbol:  parentSymbol,
				TokenEstimate: estimateTokens(body),
			})
			// Skip past a balanced body so declarations nested inside it are not
			// re-extracted. During syntax recovery an unclosed function may swallow
			// every later top-level declaration; keep scanning so column-zero
			// declarations after the edit point remain visible.
			if bracesBalanced(clean[i:endLine]) {
				i = endLine - 1
			}
			break
		}
	}
	return symbols
}

// bracesBalanced reports whether the masked lines (comments and literals
// blanked) open no brace or close every brace they open.
func bracesBalanced(masked []string) bool {
	depth := 0
	opened := false
	for _, line := range masked {
		for k := 0; k < len(line); k++ {
			switch line[k] {
			case '{':
				depth++
				opened = true
			case '}':
				depth--
			}
		}
	}
	return !opened || depth <= 0
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
