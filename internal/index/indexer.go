package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
	ignoreRules := loadIgnoreRules(root)

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
				ignoredByRules(relPath, true, ignoreRules)) {
				return filepath.SkipDir
			}
			return nil
		}
		if isSensitivePath(relPath) || ignoredByRules(relPath, false, ignoreRules) {
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

		symbols, err := i.parser.ExtractFile(path, root)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
			return nil
		}
		if err := i.store.UpsertFile(ctx, relPath, blobSHA, parser.DetectLanguage(path), symbols); err != nil {
			return err
		}
		result.FilesUpdated++
		return nil
	})
	if err != nil {
		return nil, result, err
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
