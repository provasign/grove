package native

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/mod/modfile"
)

// goWorkspace describes the go.work above a repository that lists it as a
// module. The go command builds such a repository against the other listed
// checkouts, not against the versions its go.mod pins; analysis that ignores
// the workspace type-checks against code the build never uses, and misses
// calls to anything that exists only in the checkout.
type goWorkspace struct {
	file    string   // absolute path of go.work
	members []string // absolute directories of the other modules it uses
}

// goWorkDisabled records roots whose workspace `go list` rejected, so the
// commands of one pass agree on running without it.
var goWorkDisabled sync.Map

// goWorkspaceFor finds a go.work in a directory strictly above root that
// uses root. A go.work in root itself is left alone: it is part of the
// repository and was never honored here. Untrusted mode never follows a
// file outside the repository.
func goWorkspaceFor(root string) (goWorkspace, bool) {
	if untrustedMode() {
		return goWorkspace{}, false
	}
	root = cleanResolved(root)
	if _, off := goWorkDisabled.Load(root); off {
		return goWorkspace{}, false
	}
	for dir := filepath.Dir(root); ; {
		file := filepath.Join(dir, "go.work")
		if data, err := os.ReadFile(file); err == nil {
			w, err := modfile.ParseWork(file, data, nil)
			if err != nil {
				return goWorkspace{}, false
			}
			ws := goWorkspace{file: file}
			member := false
			for _, u := range w.Use {
				p := u.Path
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, filepath.FromSlash(p))
				}
				p = cleanResolved(p)
				if p == root {
					member = true
				} else if !isUnder(root, p) {
					ws.members = append(ws.members, p)
				}
			}
			return ws, member // the nearest go.work decides, as for the go command
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return goWorkspace{}, false
		}
		dir = parent
	}
}

// fingerprint changes when the workspace file or any Go source of another
// member changes: their code is what this repository is type-checked
// against, so cached listings and export data must not outlive it.
func (w goWorkspace) fingerprint() string {
	var b strings.Builder
	if data, err := os.ReadFile(w.file); err == nil {
		b.Write(data)
	}
	for _, m := range w.members {
		var count int
		var newest, size int64
		_ = filepath.WalkDir(m, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				switch d.Name() {
				case ".git", ".grove", "node_modules", "vendor", "testdata":
					return filepath.SkipDir
				}
				return nil
			}
			if name := d.Name(); strings.HasSuffix(name, ".go") || name == "go.mod" {
				if info, err := d.Info(); err == nil {
					count++
					size += info.Size()
					if t := info.ModTime().UnixNano(); t > newest {
						newest = t
					}
				}
			}
			return nil
		})
		fmt.Fprintf(&b, ";%s=%d/%d/%d", m, count, size, newest)
	}
	return b.String()
}

func cleanResolved(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Clean(p)
}

func isUnder(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
