package index

import (
	"path/filepath"

	"github.com/provasign/grove/internal/parser"
)

// DirFilter is the indexer's rule for which directories and files a walk of
// the repository covers: the built-in skip list, .gitignore and .groveignore
// files at every level, and Python environments. A watcher that uses it
// reacts to exactly the files the index reads.
type DirFilter struct {
	root   string
	ignore *ignoreMatcher
	skip   *parser.DirSkipper
}

// NewDirFilter loads the root's ignore files and its tracked-directory list.
func NewDirFilter(root string) *DirFilter {
	return &DirFilter{root: root, ignore: newIgnoreMatcher(root), skip: parser.NewDirSkipper(root)}
}

// Enter reports whether a walk descends into the directory absDir. When it
// does, the directory's own ignore files are loaded, so a walk must call
// Enter on a parent before its children.
func (f *DirFilter) Enter(absDir string) bool {
	rel, ok := f.rel(absDir)
	if !ok {
		return false
	}
	if rel == "" {
		return true
	}
	if f.skip.Skip(rel, filepath.Base(absDir)) || f.ignore.Ignored(rel, true) || isPythonEnvDir(absDir, filepath.Base(absDir)) {
		return false
	}
	f.ignore.LoadDir(absDir, rel)
	return true
}

// FileIgnored reports whether the file absPath is excluded by the ignore
// rules loaded so far or is a sensitive path the index never reads.
func (f *DirFilter) FileIgnored(absPath string) bool {
	rel, ok := f.rel(absPath)
	if !ok || rel == "" {
		return true
	}
	return isSensitivePath(rel) || f.ignore.Ignored(rel, false)
}

func (f *DirFilter) rel(abs string) (string, bool) {
	rel, err := filepath.Rel(f.root, abs)
	if err != nil || rel == ".." || len(rel) > 2 && rel[:3] == ".."+string(filepath.Separator) {
		return "", false
	}
	if rel == "." {
		return "", true
	}
	return filepath.ToSlash(rel), true
}
