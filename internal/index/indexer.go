package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

type Indexer struct {
	parser       *parser.Engine
	store        *store.Store
	nativeConfig native.Config
}

func New(parser *parser.Engine, store *store.Store) *Indexer {
	return &Indexer{parser: parser, store: store, nativeConfig: native.ConfigFromEnv()}
}

func NewWithNativeConfig(parser *parser.Engine, store *store.Store, cfg native.Config) *Indexer {
	return &Indexer{parser: parser, store: store, nativeConfig: cfg}
}

func (i *Indexer) SetNativeConfig(cfg native.Config) {
	i.nativeConfig = cfg
}

// Options controls a single Index run.
type Options struct {
	// Force re-runs native analyzers and rebuilds edges even when no files
	// changed — needed after toolchain availability changes (e.g. installing
	// go/node makes richer native edges possible without any file edits).
	Force bool
}

// phaseTimer prints per-phase durations to stderr when GROVE_TIMING=1 —
// the profiling surface for index/delta performance work.
func phaseTimer() func(string) {
	if os.Getenv("GROVE_TIMING") == "" {
		return func(string) {}
	}
	last := time.Now()
	return func(phase string) {
		now := time.Now()
		fmt.Fprintf(os.Stderr, "[timing] %-28s %8.2fs\n", phase, now.Sub(last).Seconds())
		last = now
	}
}

func (i *Indexer) Index(ctx context.Context, root string) (*graph.CodeGraph, core.IndexResult, error) {
	return i.IndexWithOptions(ctx, root, Options{})
}

func (i *Indexer) IndexWithOptions(ctx context.Context, root string, opts Options) (*graph.CodeGraph, core.IndexResult, error) {
	var result core.IndexResult
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, result, err
	}
	// Resolve symlinks so that external tools (compile_commands.json, go list,
	// cargo metadata) which emit real paths stay consistent with our root.
	// On macOS /tmp is a symlink to /private/tmp; without this relFile() fails
	// to relativize those absolute paths.
	if resolved, err2 := filepath.EvalSymlinks(absRoot); err2 == nil {
		absRoot = resolved
	}
	root = absRoot
	result.Root = root
	tick := phaseTimer()
	// Remove per-repo Go caches left behind by earlier Grove versions
	// before walking, so they are neither indexed nor left to grow.
	native.CleanupLegacyCaches(root)
	currentFiles := map[string]bool{}
	ignore := newIgnoreMatcher(root)

	// Phase 1 (serial): walk the tree, apply ignore/sensitivity rules, and
	// collect the files whose content hash changed since the last index.
	type parseTask struct {
		absPath string
		relPath string
		blobSHA string
	}
	var tasks []parseTask
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Errors = append(result.Errors, walkErr.Error())
			return nil
		}
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)

		if entry.IsDir() {
			if path != root && (shouldSkipDirName(entry.Name()) ||
				ignore.Ignored(relPath, true)) {
				return filepath.SkipDir
			}
			// Nested ignore files apply from this directory downward;
			// WalkDir visits parents first, so deeper rules land later and
			// override (gitignore last-match-wins).
			if path != root {
				ignore.LoadDir(path, relPath)
			}
			return nil
		}
		if isSensitivePath(relPath) || ignore.Ignored(relPath, false) {
			return nil
		}
		if !parser.Supported(path) {
			return nil
		}

		result.FilesSeen++
		currentFiles[relPath] = true

		blobSHA, err := parser.FileBlobSHA(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath, err))
			return nil
		}
		previousSHA, found, err := i.store.FileBlobSHA(ctx, relPath)
		if err != nil {
			return err
		}
		if found && previousSHA == blobSHA {
			result.FilesSkipped++
			return nil
		}
		tasks = append(tasks, parseTask{absPath: path, relPath: relPath, blobSHA: blobSHA})
		return nil
	})
	if err != nil {
		return nil, result, err
	}
	tick("walk+sha-scan")

	// Phase 2 (parallel): parse changed files. Tree-sitter parsing is the
	// dominant cold-index cost and astkit engines are safe for concurrent
	// use (a fresh parser per Parse call). Outcomes land by index so the
	// serial write phase below stays in walk order — deterministic errors
	// and store contents regardless of worker scheduling.
	type parseOutcome struct {
		symbols []core.SymbolRecord
		err     error
	}
	outcomes := make([]parseOutcome, len(tasks))
	if len(tasks) > 0 {
		workers := runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8
		}
		if workers > len(tasks) {
			workers = len(tasks)
		}
		taskCh := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for idx := range taskCh {
					symbols, parseErr := i.parser.ExtractFile(tasks[idx].absPath, root)
					outcomes[idx] = parseOutcome{symbols: symbols, err: parseErr}
				}
			}()
		}
		for idx := range tasks {
			taskCh <- idx
		}
		close(taskCh)
		wg.Wait()
	}
	tick("parse-changed")

	// Phase 3 (serial): persist. SQLite has one writer; ordered writes keep
	// the run reproducible.
	changedLanguages := map[string]bool{}
	for idx, task := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, result, err
		}
		if outcomes[idx].err != nil {
			result.Errors = append(result.Errors, outcomes[idx].err.Error())
			continue
		}
		language := parser.DetectLanguage(task.absPath)
		if err := i.store.UpsertFile(ctx, task.relPath, task.blobSHA, language, outcomes[idx].symbols); err != nil {
			return nil, result, err
		}
		changedLanguages[language] = true
		result.FilesUpdated++
	}
	prunedFiles, err := i.store.FilesNotIn(ctx, currentFiles)
	if err != nil {
		return nil, result, err
	}
	for _, pruned := range prunedFiles {
		changedLanguages[parser.DetectLanguage(pruned)] = true
	}
	filesPruned, err := i.store.DeleteFilesNotIn(ctx, currentFiles)
	if err != nil {
		return nil, result, err
	}
	result.FilesPruned = filesPruned

	tick("persist-changed")
	symbols, err := i.store.AllSymbols(ctx)
	if err != nil {
		return nil, result, err
	}
	tick("load-all-symbols")

	// No file changed: the persisted edges are still exactly what a rebuild
	// would produce, so reuse them instead of re-running native analyzers
	// and edge construction (which previously made a no-change reindex take
	// seconds instead of milliseconds). Force opts back into the full path.
	if !opts.Force && result.FilesUpdated == 0 && result.FilesPruned == 0 {
		edges, err := i.store.AllEdges(ctx)
		if err != nil {
			return nil, result, err
		}
		if len(edges) > 0 || len(symbols) == 0 {
			codeGraph := graph.New()
			codeGraph.ReplaceWithStoredEdges(symbols, edges, result.FilesSeen)
			result.SymbolCount = len(symbols)
			result.EdgeCount = len(edges)
			result.Native = append(result.Native, "skipped: no file changes since last index (use --force to re-run analyzers)")
			return codeGraph, result, nil
		}
	}

	// Scope native analysis to the languages that changed. A cold index
	// (store was empty before this run) or Force analyzes everything.
	scope := changedLanguages
	if opts.Force || result.FilesUpdated == result.FilesSeen {
		scope = nil
	}
	var changedRel []string
	if scope != nil {
		for _, task := range tasks {
			changedRel = append(changedRel, task.relPath)
		}
		changedRel = append(changedRel, prunedFiles...)
	}
	nativeResult := native.AnalyzeChangedFiles(ctx, root, symbols, i.nativeConfig, scope, changedRel)
	result.Native = append(result.Native, nativeResult.Diagnostics...)
	tick("native-analyzers")

	// Carry forward stored native edges for the skipped analyzers' languages:
	// their facts didn't change, and rebuilding edges from scratch below
	// would otherwise drop them.
	if len(nativeResult.SkippedLanguages) > 0 {
		carried, err := i.carriedNativeEdges(ctx, symbols, nativeResult.SkippedLanguages)
		if err != nil {
			return nil, result, err
		}
		nativeResult.Edges = append(nativeResult.Edges, carried...)
	}
	// Partially-analyzed languages: carry the stored native edges whose
	// source file lives outside the analyzed package dirs — those packages'
	// facts did not change and were not recomputed.
	if len(nativeResult.Partial) > 0 {
		carried, err := i.carriedPartialEdges(ctx, symbols, nativeResult.Partial)
		if err != nil {
			return nil, result, err
		}
		nativeResult.Edges = append(nativeResult.Edges, carried...)
	}

	codeGraph := graph.New()
	codeGraph.ReplaceWithEdges(symbols, nativeResult.Edges, result.FilesSeen)
	tick("edge-construction")
	_, edges := codeGraph.Snapshot()
	if err := i.store.ReplaceEdges(ctx, edges); err != nil {
		return nil, result, err
	}
	tick("edge-store-write")

	result.SymbolCount = len(symbols)
	result.EdgeCount = len(edges)
	return codeGraph, result, nil
}

// carriedNativeEdges returns the stored native-analyzer edges whose source
// endpoint belongs to one of the skipped languages and whose endpoints still
// resolve against the current symbol set. Skipping an analyzer must not
// erase the facts it produced last run.
func (i *Indexer) carriedNativeEdges(ctx context.Context, symbols []core.SymbolRecord, skippedLanguages []string) ([]core.Edge, error) {
	stored, err := i.store.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	skipped := make(map[string]bool, len(skippedLanguages))
	for _, l := range skippedLanguages {
		skipped[l] = true
	}
	symLang := make(map[string]string, len(symbols))
	fileLang := map[string]string{}
	for idx := range symbols {
		s := &symbols[idx]
		symLang[s.ID] = s.Language
		if _, ok := fileLang[s.FilePath]; !ok {
			fileLang[s.FilePath] = s.Language
		}
	}
	nodeLang := func(node string) (string, bool) {
		if l, ok := symLang[node]; ok {
			return l, true
		}
		if rest, ok := strings.CutPrefix(node, "file:"); ok {
			if l, ok2 := fileLang[rest]; ok2 {
				return l, true
			}
		}
		return "", false
	}
	var out []core.Edge
	for _, e := range stored {
		if e.Source != core.EvidenceSourceNative {
			continue
		}
		fromLang, ok := nodeLang(e.From)
		if !ok || !skipped[fromLang] {
			continue
		}
		if _, ok := nodeLang(e.To); !ok {
			continue // endpoint no longer exists; drop the stale edge
		}
		out = append(out, e)
	}
	return out, nil
}

// carriedPartialEdges returns stored native edges of partially-analyzed
// languages whose SOURCE file is outside the analyzed package dirs (fresh
// facts exist for files inside them). Endpoints must still resolve — edge
// IDs embed blob SHAs, so edges into changed files drop out naturally.
func (i *Indexer) carriedPartialEdges(ctx context.Context, symbols []core.SymbolRecord, partial map[string][]string) ([]core.Edge, error) {
	stored, err := i.store.AllEdges(ctx)
	if err != nil {
		return nil, err
	}
	analyzed := map[string]map[string]bool{} // lang -> dir set
	for lang, dirs := range partial {
		set := map[string]bool{}
		for _, d := range dirs {
			set[d] = true
		}
		analyzed[lang] = set
	}
	symInfo := make(map[string]*core.SymbolRecord, len(symbols))
	fileLang := map[string]string{}
	for idx := range symbols {
		s := &symbols[idx]
		symInfo[s.ID] = s
		if _, ok := fileLang[s.FilePath]; !ok {
			fileLang[s.FilePath] = s.Language
		}
	}
	nodeInfo := func(node string) (lang, file string, ok bool) {
		if s, found := symInfo[node]; found {
			return s.Language, s.FilePath, true
		}
		if rest, found := strings.CutPrefix(node, "file:"); found {
			if l, ok2 := fileLang[rest]; ok2 {
				return l, rest, true
			}
		}
		return "", "", false
	}
	dirOf := func(f string) string {
		if i := strings.LastIndexByte(f, '/'); i >= 0 {
			return f[:i]
		}
		return "."
	}
	var out []core.Edge
	for _, e := range stored {
		if e.Source != core.EvidenceSourceNative {
			continue
		}
		lang, file, ok := nodeInfo(e.From)
		if !ok {
			continue
		}
		dirs, isPartial := analyzed[lang]
		if !isPartial || dirs[dirOf(file)] {
			continue // fully analyzed language, or a freshly analyzed dir
		}
		if _, _, ok := nodeInfo(e.To); !ok {
			continue // endpoint no longer exists; drop the stale edge
		}
		out = append(out, e)
	}
	return out, nil
}
