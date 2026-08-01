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
	// SkipNoopGraph makes a no-change index return a nil graph instead of
	// rehydrating the stored one from AllSymbols/AllEdges (a multi-second
	// load on monorepos). Callers that already hold a resident graph — or
	// none at all, like one-shot CLI runs — opt in; result counts still come
	// from the store. The stored-edge invariant (stored == what a rebuild
	// would produce) is untouched.
	SkipNoopGraph bool

	// PrevEdges/PrevSymbols enable incremental edge construction on delta
	// runs (gated additionally by GROVE_INCREMENTAL=1): the caller's
	// resident graph state from the previous index. PrevEdges may be the
	// MERGED (base+native) set — the delta path re-merges fresh native
	// results, and the affected set is widened to every natively
	// re-analyzed package so a vanished native edge cannot strand a stale
	// kept edge. Nil disables the incremental path (full rebuild).
	PrevEdges   []core.Edge
	PrevSymbols []core.SymbolRecord
}

// spliceEdgeWrite persists an incremental delta's write-set: rows touching
// changed files were already deleted by the persist phase; here the
// recomputed owners' rows are deleted and every row the delta could have
// altered is upserted with merge semantics. A COUNT(*) invariant guards the
// splice — any divergence from the in-memory edge set self-heals through the
// full ReplaceEdges diff in the same run.
func (i *Indexer) spliceEdgeWrite(ctx context.Context, symbols []core.SymbolRecord, edges []core.Edge, meta *graph.DeltaMeta, result *core.IndexResult) error {
	owners := make([]string, 0, len(meta.AffectedOwners))
	for id := range meta.AffectedOwners {
		owners = append(owners, id)
	}

	var nativeFiles []string
	if len(meta.NativeAnalyzedDirs) > 0 {
		seen := map[string]bool{}
		for si := range symbols {
			f := symbols[si].FilePath
			if !seen[f] && meta.NativeAnalyzedDirs[fileDir(f)] {
				seen[f] = true
				nativeFiles = append(nativeFiles, f)
			}
		}
	}

	nodePath := func(n string) string {
		if rest, ok := strings.CutPrefix(n, "file:"); ok {
			return rest
		}
		if idx := strings.Index(n, "::"); idx >= 0 {
			return n[:idx]
		}
		return ""
	}
	ownerSet := make(map[string]bool, len(owners))
	for _, id := range owners {
		ownerSet[id] = true
	}
	var inserts []core.Edge
	for _, e := range edges {
		fp, tp := nodePath(e.From), nodePath(e.To)
		switch {
		case meta.ChangedFiles[fp], meta.ChangedFiles[tp]:
			inserts = append(inserts, e)
		case ownerSet[e.From]:
			inserts = append(inserts, e)
		case e.Source == core.EvidenceSourceNative && (meta.NativeAnalyzedDirs[fileDir(fp)] || meta.NativeAnalyzedDirs[fileDir(tp)]):
			inserts = append(inserts, e)
		}
	}

	if err := i.store.SpliceEdges(ctx, owners, nativeFiles, inserts); err != nil {
		return err
	}
	count, err := i.store.EdgeCount(ctx)
	if err != nil {
		return err
	}
	if count != len(edges) {
		// Write-set miss: self-heal with the full diff and record it —
		// a persistent mismatch is a bug in the splice enumeration.
		result.Native = append(result.Native,
			fmt.Sprintf("edge splice mismatch (stored %d != memory %d): healed via full diff", count, len(edges)))
		return i.store.ReplaceEdges(ctx, edges)
	}
	result.Native = append(result.Native,
		fmt.Sprintf("edge splice: %d owners, %d native files, %d upserts", len(owners), len(nativeFiles), len(inserts)))
	return nil
}

// incrementalEnabled gates incremental edge construction — ON by default,
// GROVE_INCREMENTAL=0 opts out. The path is guarded end to end: canonical
// install order makes its in-memory state identical to a full rebuild's,
// degenerate deltas fall back to a full rebuild automatically, and the store
// splice self-heals through the full diff on any COUNT invariant miss.
func incrementalEnabled() bool {
	return os.Getenv("GROVE_INCREMENTAL") != "0"
}

// fileDir returns the slash-path directory of a repo-relative file path.
func fileDir(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return "."
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
		size    int64
		mtime   int64
	}
	var tasks []parseTask
	fileMeta, err := i.store.AllFileMeta(ctx)
	if err != nil {
		return nil, result, err
	}
	statRefresh := map[string][2]int64{} // touched but content-identical
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
		// A symlinked FILE entry inside root can point anywhere; WalkDir
		// already refuses to descend into a symlinked DIRECTORY (its
		// DirEntry.IsDir() is false for a symlink even when the target is a
		// dir, so it falls through to this file branch and typically fails
		// parser.Supported below) — but a symlinked file whose target has a
		// recognized extension would otherwise have its content read,
		// parsed, and exposed under the in-repo symlink name even though
		// the real bytes live outside the indexed tree. Reject any entry
		// whose resolved target escapes root before it is ever parsed.
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, e := filepath.EvalSymlinks(path)
			if e != nil {
				return nil
			}
			if r, e := filepath.Rel(root, resolved); e != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
				return nil
			}
		}
		if !parser.Supported(path) {
			return nil
		}

		result.FilesSeen++
		currentFiles[relPath] = true

		// Stat cache: size+mtime match means the content is unchanged —
		// skip reading and hashing 1.4GB of sources on every rescan.
		// os.Stat (not entry.Info) so symlinked sources stat the TARGET;
		// the link's own stat never changes when the target is edited.
		// --force bypasses the shortcut: it is the recovery path for
		// mtime-preserving writers (rsync -t, tar, build caches).
		info, statErr := os.Stat(path)
		meta, found := fileMeta[relPath]
		if !opts.Force && statErr == nil && found &&
			meta.Size == info.Size() && meta.Mtime == info.ModTime().UnixNano() {
			result.FilesSkipped++
			return nil
		}
		blobSHA, err := parser.FileBlobSHA(path)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", relPath, err))
			return nil
		}
		var size, mtime int64 = -1, -1
		if statErr == nil {
			size, mtime = info.Size(), info.ModTime().UnixNano()
		}
		if found && meta.BlobSHA == blobSHA {
			// Touched (or first run after the stat-cache migration) but
			// content-identical: refresh the stat so the next walk skips it.
			result.FilesSkipped++
			statRefresh[relPath] = [2]int64{size, mtime}
			return nil
		}
		tasks = append(tasks, parseTask{absPath: path, relPath: relPath, blobSHA: blobSHA, size: size, mtime: mtime})
		return nil
	})
	if err != nil {
		return nil, result, err
	}
	if err := i.store.UpdateFileStats(ctx, statRefresh); err != nil {
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
					// Honor cancellation so a timed-out index stops parsing
					// instead of running the full task list past deadline.
					if err := ctx.Err(); err != nil {
						outcomes[idx] = parseOutcome{err: err}
						continue
					}
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
		if err := i.store.UpsertFile(ctx, task.relPath, task.blobSHA, language, task.size, task.mtime, outcomes[idx].symbols); err != nil {
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

	// No file changed: the persisted edges are still exactly what a rebuild
	// would produce, so reuse them instead of re-running native analyzers
	// and edge construction (which previously made a no-change reindex take
	// seconds instead of milliseconds). Force opts back into the full path.
	// The check runs BEFORE AllSymbols so a no-op never pays the full symbol
	// load; counts come from COUNT(*) queries instead of loaded slices.
	if !opts.Force && result.FilesUpdated == 0 && result.FilesPruned == 0 {
		status, err := i.store.Status(ctx)
		if err != nil {
			return nil, result, err
		}
		// Same guard as the previous len(edges) > 0 || len(symbols) == 0:
		// a legacy store with symbols but no edges falls through to a full
		// rebuild.
		if status.EdgeCount > 0 || status.SymbolCount == 0 {
			result.SymbolCount = status.SymbolCount
			result.EdgeCount = status.EdgeCount
			result.Native = append(result.Native, "skipped: no file changes since last index (use --force to re-run analyzers)")
			if opts.SkipNoopGraph {
				return nil, result, nil
			}
			symbols, err := i.store.AllSymbols(ctx)
			if err != nil {
				return nil, result, err
			}
			edges, err := i.store.AllEdges(ctx)
			if err != nil {
				return nil, result, err
			}
			codeGraph := graph.New()
			codeGraph.ReplaceWithStoredEdges(symbols, edges, result.FilesSeen)
			return codeGraph, result, nil
		}
	}

	symbols, err := i.store.AllSymbols(ctx)
	if err != nil {
		return nil, result, err
	}
	tick("load-all-symbols")

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
	if len(nativeResult.SkippedLanguages) > 0 || len(nativeResult.Partial) > 0 {
		// Both carry paths keep only Source==native edges, so load exactly
		// those rows. Loading the full table here used to hold millions of
		// discarded rows live through the allocation-heavy edge build below,
		// and the GC pressure alone made delta edge construction ~5x slower
		// than cold on monorepos.
		stored, err := i.store.EdgesBySource(ctx, string(core.EvidenceSourceNative))
		if err != nil {
			return nil, result, err
		}
		if len(nativeResult.SkippedLanguages) > 0 {
			nativeResult.Edges = append(nativeResult.Edges,
				carriedNativeEdges(stored, symbols, nativeResult.SkippedLanguages)...)
		}
		// Partially-analyzed languages: carry the stored native edges whose
		// source file lives outside the analyzed package dirs — those
		// packages' facts did not change and were not recomputed.
		if len(nativeResult.Partial) > 0 {
			nativeResult.Edges = append(nativeResult.Edges,
				carriedPartialEdges(stored, symbols, nativeResult.Partial)...)
		}
	}

	codeGraph := graph.New()
	var deltaMeta *graph.DeltaMeta
	if incrementalEnabled() && opts.PrevEdges != nil && scope != nil {
		// Incremental edge construction: recompute only affected owners'
		// call/test edges (graph.BuildEdgesDelta). Natively re-analyzed
		// package dirs are passed separately — the delta path drops previous
		// native-source edges they own (fresh native results replace them)
		// instead of re-resolving every symbol in those packages.
		changedSet := make(map[string]bool, len(changedRel))
		for _, f := range changedRel {
			changedSet[f] = true
		}
		analyzedDirs := map[string]bool{}
		for _, dirs := range nativeResult.Partial {
			for _, d := range dirs {
				analyzedDirs[d] = true
			}
		}
		base, meta := graph.BuildEdgesDeltaMeta(opts.PrevEdges, opts.PrevSymbols, symbols, changedSet, analyzedDirs)
		codeGraph.ReplaceWithBaseEdges(symbols, base, nativeResult.Edges, result.FilesSeen)
		result.Native = append(result.Native, "edge construction: incremental (GROVE_INCREMENTAL=1)")
		deltaMeta = meta
	} else {
		codeGraph.ReplaceWithEdges(symbols, nativeResult.Edges, result.FilesSeen)
	}
	tick("edge-construction")
	edges := codeGraph.EdgesSnapshot()
	if deltaMeta != nil {
		// Splice write: apply the known write-set instead of diffing the
		// whole table, then verify with a COUNT(*) invariant. Any mismatch
		// self-heals through the full diff path in the same run.
		if err := i.spliceEdgeWrite(ctx, symbols, edges, deltaMeta, &result); err != nil {
			return nil, result, err
		}
	} else if err := i.store.ReplaceEdges(ctx, edges); err != nil {
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
func carriedNativeEdges(stored []core.Edge, symbols []core.SymbolRecord, skippedLanguages []string) []core.Edge {
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
	return out
}

// carriedPartialEdges returns stored native edges of partially-analyzed
// languages whose SOURCE file is outside the analyzed package dirs (fresh
// facts exist for files inside them). Endpoints must still resolve — edge
// IDs embed blob SHAs, so edges into changed files drop out naturally.
func carriedPartialEdges(stored []core.Edge, symbols []core.SymbolRecord, partial map[string][]string) []core.Edge {
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
	return out
}
