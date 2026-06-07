package native

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/provasign/grove/internal/core"
)

const defaultTimeout = 5 * time.Second

type Config struct {
	Enabled           bool
	Languages         map[string]bool
	DisabledLanguages map[string]bool
	Timeout           time.Duration
}

func DefaultConfig() Config {
	return Config{Enabled: true, Timeout: defaultTimeout}
}

func ConfigFromEnv() Config {
	cfg := DefaultConfig()
	if value := strings.TrimSpace(os.Getenv("GROVE_NATIVE")); value != "" {
		cfg.Enabled = !isFalse(value)
	}
	if value := strings.TrimSpace(os.Getenv("GROVE_NATIVE_LANGUAGES")); value != "" {
		cfg.Languages = languageSet(value)
	}
	if value := strings.TrimSpace(os.Getenv("GROVE_NATIVE_DISABLED_LANGUAGES")); value != "" {
		cfg.DisabledLanguages = languageSet(value)
	}
	if value := strings.TrimSpace(os.Getenv("GROVE_NATIVE_TIMEOUT")); value != "" {
		if d, err := time.ParseDuration(value); err == nil && d > 0 {
			cfg.Timeout = d
		}
	}
	if value := strings.TrimSpace(os.Getenv("GROVE_NATIVE_TIMEOUT_MS")); value != "" {
		if ms, err := strconv.Atoi(value); err == nil && ms > 0 {
			cfg.Timeout = time.Duration(ms) * time.Millisecond
		}
	}
	return cfg
}

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
	return AnalyzeWithConfig(ctx, root, symbols, ConfigFromEnv())
}

func AnalyzeWithConfig(ctx context.Context, root string, symbols []core.SymbolRecord, cfg Config) Result {
	if !cfg.Enabled {
		return Result{Diagnostics: []string{"native analyzers disabled"}}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	files := filesByLanguage(symbols)
	var combined Result
	for _, analyzer := range PriorityAnalyzers() {
		if !analyzerEnabled(analyzer, cfg) {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": skipped: disabled by config")
			continue
		}
		reqFiles := filterFiles(files, analyzer.Languages())
		if len(reqFiles) == 0 {
			continue
		}
		avail := analyzer.Available(ctx, root)
		if !avail.Available {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": skipped: "+avail.Reason)
			continue
		}
		runCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		result := analyzer.Analyze(runCtx, Request{Root: root, Symbols: symbols, Files: reqFiles})
		cancel()
		for _, diag := range result.Diagnostics {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": "+diag)
		}
		combined.Edges = append(combined.Edges, result.Edges...)
	}
	return combined
}

func analyzerEnabled(analyzer Analyzer, cfg Config) bool {
	for _, lang := range analyzer.Languages() {
		if cfg.DisabledLanguages[lang] || cfg.DisabledLanguages[analyzer.Name()] {
			return false
		}
	}
	if len(cfg.Languages) == 0 {
		return true
	}
	if cfg.Languages[analyzer.Name()] {
		return true
	}
	for _, lang := range analyzer.Languages() {
		if cfg.Languages[lang] {
			return true
		}
	}
	return false
}

func isFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off", "disabled":
		return true
	default:
		return false
	}
}

func languageSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.ToLower(strings.TrimSpace(part))
		if part != "" {
			out[part] = true
		}
	}
	return out
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
