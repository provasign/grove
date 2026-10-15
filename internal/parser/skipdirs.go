package parser

import (
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// DirSkipper decides which directories a repository walk skips. Some names
// are always tool state or third-party trees. Others (bin, dist, target)
// usually hold build output, but not always: Cargo's src/bin/*.rs, a C
// project's tests/bin/*.c and a checked-in dist/ are source. Those are
// skipped only when git tracks nothing in them; skipping by name alone left
// tracked source out of the index with no trace (jansson test/bin,
// 2026-10-14).
type DirSkipper struct {
	tracked map[string]bool // repo-relative dirs holding a git-tracked file
}

// NewDirSkipper reads root's tracked files once. Outside a git repository
// (or without git) the name rule alone applies.
func NewDirSkipper(root string) *DirSkipper {
	s := &DirSkipper{}
	out, err := exec.Command("git", "-C", root, "ls-files", "-z").Output()
	if err != nil {
		return s
	}
	s.tracked = map[string]bool{}
	for _, f := range strings.Split(string(out), "\x00") {
		for dir := path.Dir(f); dir != "." && dir != "/" && !s.tracked[dir]; dir = path.Dir(dir) {
			s.tracked[dir] = true
		}
	}
	return s
}

// Skip reports whether the directory rel (repo-relative) named name is
// skipped.
func (s *DirSkipper) Skip(rel, name string) bool {
	switch name {
	case ".git", ".grove", ".cache", ".venv", ".tox", ".next", ".idea",
		"node_modules", "vendor", "__pycache__",
		"coverage", ".pytest_cache", ".mypy_cache", ".ruff_cache":
		return true
	case "dist", "bin", "target":
		return s == nil || !s.tracked[filepath.ToSlash(rel)]
	}
	return false
}

// relOf is p relative to root with forward slashes, or p when it is not
// under root.
func relOf(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}
