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

// maxScaledTimeout caps the size-scaled analyzer budget.
const maxScaledTimeout = 60 * time.Second

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
	// ChangedFiles, when non-empty, allows the analyzer to scope expensive
	// per-package work to the packages containing these files (plus their
	// reverse importers). An analyzer that scopes MUST report the analyzed
	// package dirs in Result.Partial so the indexer carries the stored
	// native edges of every other package forward.
	ChangedFiles []string
}

type Result struct {
	Edges       []core.Edge
	Diagnostics []string
	// SkippedLanguages lists languages whose analyzers were skipped because
	// no files of theirs changed; the indexer carries their stored native
	// edges forward instead of dropping them.
	SkippedLanguages []string
	// Partial maps a language to the package dirs its analyzer actually
	// re-analyzed this run. The indexer carries stored native edges of that
	// language whose source file lives OUTSIDE those dirs.
	Partial map[string][]string
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
	return AnalyzeChanged(ctx, root, symbols, cfg, nil)
}

// AnalyzeChanged is AnalyzeWithConfig scoped to the languages that actually
// changed. changedLanguages == nil means "analyze everything" (cold index,
// --force). An analyzer whose languages saw no changed files is skipped and
// reported as such — its previous edges are carried forward by the indexer.
// On a polyglot monorepo this is the difference between a one-file Go edit
// re-running the whole TypeScript program check and not.
func AnalyzeChanged(ctx context.Context, root string, symbols []core.SymbolRecord, cfg Config, changedLanguages map[string]bool) Result {
	return AnalyzeChangedFiles(ctx, root, symbols, cfg, changedLanguages, nil)
}

// AnalyzeChangedFiles additionally passes the changed file list so analyzers
// can scope per-package work (see Request.ChangedFiles).
func AnalyzeChangedFiles(ctx context.Context, root string, symbols []core.SymbolRecord, cfg Config, changedLanguages map[string]bool, changedFiles []string) Result {
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
		if changedLanguages != nil && !touchesLanguages(analyzer, changedLanguages) {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": skipped: no changed files in its languages (previous edges carried forward)")
			combined.SkippedLanguages = append(combined.SkippedLanguages, analyzer.Languages()...)
			continue
		}
		reqFiles := filterFiles(files, analyzer.Languages())
		if len(reqFiles) == 0 {
			continue
		}
		avail := analyzer.Available(ctx, root)
		if !avail.Available {
			// Availability flaps with PATH/toolchain env; carrying the
			// stored edges keeps the graph stable across such flaps.
			combined.Diagnostics = append(combined.Diagnostics,
				analyzer.Name()+": skipped: "+avail.Reason+" (previous native edges carried forward)")
			combined.SkippedLanguages = append(combined.SkippedLanguages, analyzer.Languages()...)
			continue
		}
		// Budget scales with input size unless the user pinned a timeout:
		// the 5s default starves real monorepos (go list alone needs ~3s on
		// 19k files) and a starved analyzer used to land PARTIAL results —
		// half a million edges flapping run to run.
		timeout := cfg.Timeout
		if cfg.Timeout == defaultTimeout {
			if scaled := time.Duration(len(reqFiles)) * 4 * time.Millisecond; scaled > timeout {
				timeout = scaled
			}
			if timeout > maxScaledTimeout {
				timeout = maxScaledTimeout
			}
		}
		runCtx, cancel := context.WithTimeout(ctx, timeout)
		result := analyzer.Analyze(runCtx, Request{Root: root, Symbols: symbols, Files: reqFiles, ChangedFiles: changedFiles})
		timedOut := runCtx.Err() != nil
		cancel()
		for _, diag := range result.Diagnostics {
			combined.Diagnostics = append(combined.Diagnostics, analyzer.Name()+": "+diag)
		}
		// All-or-nothing: analyzers produce their full edge set or nothing
		// (one subprocess, decoded at the end), so an empty result means the
		// analyzer failed or was killed. Report its languages as skipped and
		// the indexer carries the previously stored native edges forward —
		// stale-proof, because edge endpoints embed blob SHAs and carried
		// edges whose endpoint files changed no longer resolve and drop.
		// A result WITH edges is complete even if the deadline expired at
		// the boundary — never discard computed facts.
		if len(result.Edges) == 0 {
			reason := "no edges produced"
			if timedOut {
				reason = "timed out after " + timeout.String()
			}
			combined.Diagnostics = append(combined.Diagnostics,
				analyzer.Name()+": "+reason+" — previous native edges carried forward")
			combined.SkippedLanguages = append(combined.SkippedLanguages, analyzer.Languages()...)
			continue
		}
		combined.Edges = append(combined.Edges, result.Edges...)
		for lang, dirs := range result.Partial {
			if combined.Partial == nil {
				combined.Partial = map[string][]string{}
			}
			combined.Partial[lang] = append(combined.Partial[lang], dirs...)
		}
	}
	return combined
}

func touchesLanguages(analyzer Analyzer, changed map[string]bool) bool {
	for _, lang := range analyzer.Languages() {
		if changed[lang] {
			return true
		}
	}
	return false
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
