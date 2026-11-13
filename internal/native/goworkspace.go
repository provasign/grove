package native

import (
	"crypto/sha256"
	"encoding/hex"
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
			// Only members this repository requires can change how it
			// type-checks; the rest are not fingerprinted.
			required := map[string]bool{}
			if mod, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
				if f, err := modfile.ParseLax("go.mod", mod, nil); err == nil {
					for _, r := range f.Require {
						required[r.Mod.Path] = true
					}
				}
			}
			for _, u := range w.Use {
				p := u.Path
				if !filepath.IsAbs(p) {
					p = filepath.Join(dir, filepath.FromSlash(p))
				}
				p = cleanResolved(p)
				if p == root {
					member = true
				} else if !isUnder(root, p) {
					if mod, err := os.ReadFile(filepath.Join(p, "go.mod")); err == nil && required[modfile.ModulePath(mod)] {
						ws.members = append(ws.members, p)
					}
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

// goWorkspaceInput is the name under which the workspace fingerprint is
// tracked with the other compiler inputs. It is not a file in the tree.
const goWorkspaceInput = "@go-workspace"

// javaLinkedInput is the pseudo-name for the linked repositories' Java
// sources a Java pass may read declarations from.
const javaLinkedInput = "@java-linked-sources"

// ExternalCompilerInputs returns fingerprints of compiler inputs that live
// outside the walked tree, by pseudo-name: the go.work above the repository
// with the sources of the workspace members it requires, and the Java
// sources of linked repositories. A change re-runs the analyzer exactly as a
// changed go.mod or pom.xml does.
func ExternalCompilerInputs(root string, linked []string) map[string]string {
	out := map[string]string{}
	if ws, ok := goWorkspaceFor(root); ok {
		sum := sha256.Sum256([]byte(ws.fingerprint()))
		out[goWorkspaceInput] = hex.EncodeToString(sum[:])
	}
	if roots := javaLinkedSourceRoots(root, linked); len(roots) > 0 {
		var b strings.Builder
		for _, r := range roots {
			count, size, newest := sourceStats(r, ".java")
			fmt.Fprintf(&b, "%s=%d/%d/%d;", r, count, size, newest)
		}
		sum := sha256.Sum256([]byte(b.String()))
		out[javaLinkedInput] = hex.EncodeToString(sum[:])
	}
	return out
}

// sourceStats is a cheap fingerprint of the files with one extension under
// dir: how many, their total size, and the newest modification time.
func sourceStats(dir, ext string) (count int, size, newest int64) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == ".grove" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ext) {
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
	return count, size, newest
}
