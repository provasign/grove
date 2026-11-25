package grove

import "github.com/provasign/grove/internal/index"

// WalkFilter applies the indexer's rules for which directories and files it
// reads: the built-in skip list, .gitignore and .groveignore at every level,
// Python environments and sensitive paths. A file watcher uses it to react to
// exactly the files Index reads.
type WalkFilter struct{ f *index.DirFilter }

// NewWalkFilter loads repoRoot's ignore rules.
func NewWalkFilter(repoRoot string) *WalkFilter {
	return &WalkFilter{f: index.NewDirFilter(repoRoot)}
}

// Enter reports whether the indexer descends into the directory absDir, and
// loads that directory's ignore files when it does. Call it on a parent
// before its children.
func (w *WalkFilter) Enter(absDir string) bool { return w.f.Enter(absDir) }

// FileIgnored reports whether the indexer skips the file absPath under the
// rules loaded so far. Paths outside the root are ignored.
func (w *WalkFilter) FileIgnored(absPath string) bool { return w.f.FileIgnored(absPath) }
