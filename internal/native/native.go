package native

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/grove/internal/core"
)

const defaultTimeout = 5 * time.Second

// Analyzer enriches the tree-sitter/astkit symbol graph with project-native
// facts from compiler, package-manager, or language-server tooling.
type Analyzer interface {
	Name() string
	Languages() []string
	Available(context.Context, string) Availability
	Analyze(context.Context, Request) Result
}

type Availability struct {
	Available bool
	Reason    string
}

type Request struct {
	Root    string
	Symbols []core.SymbolRecord
	Files   []string
}

type Result struct {
	Edges       []core.Edge
	Diagnostics []string
}

func PriorityAnalyzers() []Analyzer {
	return []Analyzer{
		goAnalyzer{},
		jsTSAnalyzer{},
		pythonAnalyzer{},
		javaAnalyzer{},
		rustAnalyzer{},
		cFamilyAnalyzer{},
		csharpAnalyzer{},
		phpAnalyzer{},
	}
}

func Analyze(ctx context.Context, root string, symbols []core.SymbolRecord) Result {
	files := filesByLanguage(symbols)
	var combined Result
	for _, analyzer := range PriorityAnalyzers() {
		reqFiles := filterFiles(files, analyzer.Languages())
		if len(reqFiles) == 0 {
			continue
		}
		avail := analyzer.Available(ctx, root)
		if !avail.Available {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": skipped: "+avail.Reason)
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, defaultTimeout)
		result := analyzer.Analyze(runCtx, Request{Root: root, Symbols: symbols, Files: reqFiles})
		cancel()
		for _, diag := range result.Diagnostics {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": "+diag)
		}
		combined.Edges = append(combined.Edges, result.Edges...)
	}
	return combined
}

func filesByLanguage(symbols []core.SymbolRecord) map[string][]string {
	seen := map[string]bool{}
	out := map[string][]string{}
	for _, symbol := range symbols {
		if symbol.FilePath == "" || symbol.Language == "" {
			continue
		}
		key := symbol.Language + "\x00" + symbol.FilePath
		if seen[key] {
			continue
		}
		seen[key] = true
		out[symbol.Language] = append(out[symbol.Language], symbol.FilePath)
	}
	return out
}

func filterFiles(files map[string][]string, languages []string) []string {
	var out []string
	for _, lang := range languages {
		out = append(out, files[lang]...)
	}
	return out
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func anyFile(root string, names ...string) bool {
	for _, name := range names {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return true
		}
	}
	return false
}

func firstExistingExecutable(names ...string) string {
	for _, name := range names {
		if commandExists(name) {
			return name
		}
	}
	return ""
}

func packageDir(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
}

func fileSet(files []string) map[string]bool {
	out := make(map[string]bool, len(files))
	for _, file := range files {
		out[filepath.ToSlash(file)] = true
	}
	return out
}

func relFile(root, abs string) (string, bool) {
	rel, err := filepath.Rel(root, abs)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

func nativeImportEdge(from, to string, confidence float64) core.Edge {
	return core.Edge{
		From:       "file:" + filepath.ToSlash(from),
		To:         "file:" + filepath.ToSlash(to),
		Type:       core.EdgeImports,
		Confidence: confidence,
		Source:     core.EvidenceSourceNative,
	}
}

func symbolByFileAndName(symbols []core.SymbolRecord, languages map[string]bool) map[string]core.SymbolRecord {
	out := map[string]core.SymbolRecord{}
	for _, symbol := range symbols {
		if !languages[symbol.Language] {
			continue
		}
		out[symbol.FilePath+"\x00"+symbol.Name] = symbol
		if symbol.ParentSymbol != "" {
			out[symbol.FilePath+"\x00"+symbol.ParentSymbol+"."+symbol.Name] = symbol
		}
	}
	return out
}

func symbolEdge(from, to core.SymbolRecord, edgeType core.EdgeType, confidence float64) core.Edge {
	return core.Edge{
		From:       from.ID,
		To:         to.ID,
		Type:       edgeType,
		Confidence: confidence,
		Source:     core.EvidenceSourceNative,
	}
}

func decodeJSON[T any](data []byte) (T, error) {
	var out T
	err := json.Unmarshal(data, &out)
	return out, err
}
