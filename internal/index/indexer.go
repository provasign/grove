package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"

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

	// Phase 3 (serial): persist. SQLite has one writer; ordered writes keep
	// the run reproducible.
	for idx, task := range tasks {
		if err := ctx.Err(); err != nil {
			return nil, result, err
		}
		if outcomes[idx].err != nil {
			result.Errors = append(result.Errors, outcomes[idx].err.Error())
			continue
		}
		if err := i.store.UpsertFile(ctx, task.relPath, task.blobSHA, parser.DetectLanguage(task.absPath), outcomes[idx].symbols); err != nil {
			return nil, result, err
		}
		result.FilesUpdated++
	}
	filesPruned, err := i.store.DeleteFilesNotIn(ctx, currentFiles)
	if err != nil {
		return nil, result, err
	}
	result.FilesPruned = filesPruned

	symbols, err := i.store.AllSymbols(ctx)
	if err != nil {
		return nil, result, err
	}

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

	nativeResult := native.AnalyzeWithConfig(ctx, root, symbols, i.nativeConfig)
	result.Native = append(result.Native, nativeResult.Diagnostics...)

	codeGraph := graph.New()
	codeGraph.ReplaceWithEdges(symbols, nativeResult.Edges, result.FilesSeen)
	_, edges := codeGraph.Snapshot()
	if err := i.store.ReplaceEdges(ctx, edges); err != nil {
		return nil, result, err
	}

	result.SymbolCount = len(symbols)
	result.EdgeCount = len(edges)
	return codeGraph, result, nil
}
