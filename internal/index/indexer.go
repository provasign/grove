package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tabladrum/grove-suite/grove/internal/core"
	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

type Indexer struct {
	parser *parser.Engine
	store  *store.Store
}

func New(parser *parser.Engine, store *store.Store) *Indexer {
	return &Indexer{parser: parser, store: store}
}

func (i *Indexer) Index(ctx context.Context, root string) (*graph.CodeGraph, core.IndexResult, error) {
	var result core.IndexResult
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, result, err
	}
	root = absRoot
	result.Root = root
	currentFiles := map[string]bool{}

	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			result.Errors = append(result.Errors, walkErr.Error())
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == ".grove" || name == "node_modules" || name == "vendor" || name == "dist" || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !parser.Supported(path) {
			return nil
		}

		result.FilesSeen++
		relPath, err := filepath.Rel(root, path)
		if err != nil {
			relPath = path
		}
		relPath = filepath.ToSlash(relPath)
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
	codeGraph := graph.New()
	codeGraph.Replace(symbols, result.FilesSeen)
	_, edges := codeGraph.Snapshot()
	if err := i.store.ReplaceEdges(ctx, edges); err != nil {
		return nil, result, err
	}

	result.SymbolCount = len(symbols)
	result.EdgeCount = len(edges)
	return codeGraph, result, nil
}
