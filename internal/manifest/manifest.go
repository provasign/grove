// Package manifest reads what a repository declares about itself and what
// it requires from others, from its build manifests. It is static: no
// package manager runs and nothing is downloaded.
//
// This is what lets two separately indexed repositories be linked: one
// declares package P, the other requires P.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
)

// Package is something the repository declares.
type Package struct {
	Dir       string `json:"dir"`       // repo-relative directory of the manifest; "" is the root
	Ecosystem string `json:"ecosystem"` // go | npm | cargo | pypi | maven | composer
	Name      string `json:"name"`      // module path, package name
	Version   string `json:"version,omitempty"`
	Manifest  string `json:"manifest"` // repo-relative manifest path
}

// Require is something the repository depends on.
type Require struct {
	Manifest  string `json:"manifest"`
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"` // as written
	// LocalPath is set when the manifest itself points at a directory on
	// disk for this dependency (go.work use, replace => ../x, file:../x).
	// Absolute and cleaned.
	LocalPath string `json:"localPath,omitempty"`
	// Evidence is the manifest line that says so, for display.
	Evidence string `json:"evidence"`
	// CoMember is true for a go.work module this repository does not itself
	// require: the two are built in one workspace, which says nothing about
	// either depending on the other.
	CoMember bool `json:"coMember,omitempty"`
}

// Result is everything Scan found.
type Result struct {
	Packages []Package `json:"packages"`
	Requires []Require `json:"requires"`
}

// Scan reads the manifests at the repository root: go.mod and go.work,
// package.json, Cargo.toml, pyproject.toml, pom.xml and composer.json. A
// manifest that does not parse is skipped: a broken go.mod must not hide a
// readable package.json.
func Scan(root string) Result {
	var r Result
	scanDir(root, &r)
	// A repository often declares more than one package: npm and pnpm
	// workspaces, Cargo workspace members, Maven modules, nested Go modules.
	// Their manifests sit below the root, a few levels down. Each is read
	// the same way; what it declares belongs to this repository, and a
	// requirement on a package the repository itself declares is internal.
	for _, dir := range nestedManifestDirs(root) {
		var sub Result
		scanDir(dir, &sub)
		rel := filepath.ToSlash(relOr(root, dir))
		for _, p := range sub.Packages {
			p.Dir, p.Manifest = rel, rel+"/"+p.Manifest
			r.Packages = append(r.Packages, p)
		}
		for _, q := range sub.Requires {
			if q.CoMember {
				continue // the go.work above was already read from the root
			}
			q.Manifest = rel + "/" + q.Manifest
			q.Evidence = rel + "/" + q.Evidence
			r.Requires = append(r.Requires, q)
		}
	}
	own := map[string]bool{}
	for _, p := range r.Packages {
		own[p.Ecosystem+":"+p.Name] = true
	}
	kept := r.Requires[:0]
	seen := map[string]bool{}
	for _, q := range r.Requires {
		id := q.Ecosystem + ":" + q.Name
		if own[id] || seen[id+"\x00"+q.Version] {
			continue
		}
		seen[id+"\x00"+q.Version] = true
		kept = append(kept, q)
	}
	r.Requires = kept
	sort.SliceStable(r.Requires, func(i, j int) bool {
		a, b := r.Requires[i], r.Requires[j]
		if a.Ecosystem != b.Ecosystem {
			return a.Ecosystem < b.Ecosystem
		}
		return a.Name < b.Name
	})
	return r
}

func scanGo(root string, r *Result) {
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		if f, err := modfile.Parse("go.mod", data, nil); err == nil && f.Module != nil {
			r.Packages = append(r.Packages, Package{Ecosystem: "go", Name: f.Module.Mod.Path, Manifest: "go.mod"})
			local := map[string]Require{}
			for _, rep := range f.Replace {
				if rep.New.Version != "" { // module replacement, not a directory
					continue
				}
				p := rep.New.Path
				if !filepath.IsAbs(p) {
					p = filepath.Join(root, filepath.FromSlash(p))
				}
				local[rep.Old.Path] = Require{
					Manifest: "go.mod", Ecosystem: "go", Name: rep.Old.Path, LocalPath: filepath.Clean(p),
					Evidence: fmt.Sprintf("go.mod:%d replace %s => %s", rep.Syntax.Start.Line, rep.Old.Path, rep.New.Path),
				}
			}
			for _, req := range f.Require {
				if req.Indirect {
					continue // not imported by this module's own code
				}
				out := Require{
					Manifest: "go.mod", Ecosystem: "go", Name: req.Mod.Path, Version: req.Mod.Version,
					Evidence: fmt.Sprintf("go.mod:%d require %s %s", req.Syntax.Start.Line, req.Mod.Path, req.Mod.Version),
				}
				if l, ok := local[req.Mod.Path]; ok {
					out.LocalPath, out.Evidence = l.LocalPath, l.Evidence+" (required "+req.Mod.Version+")"
					delete(local, req.Mod.Path)
				}
				r.Requires = append(r.Requires, out)
			}
		}
	}
	// go.work, here or in an ancestor, the way the go command finds it. When
	// this repository is one of its modules, the others are built from their
	// directories: explicit local links.
	workDir, data := findUp(root, "go.work")
	if data == nil {
		return
	}
	w, err := modfile.ParseWork("go.work", data, nil)
	if err != nil {
		return
	}
	member := false
	var others []Require
	for _, u := range w.Use {
		dir := u.Path
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(workDir, filepath.FromSlash(dir))
		}
		dir = filepath.Clean(dir)
		if dir == filepath.Clean(root) {
			member = true
			continue
		}
		mod := goModulePath(filepath.Join(dir, "go.mod"))
		if mod == "" {
			continue
		}
		others = append(others, Require{
			Manifest: filepath.ToSlash(relOr(root, filepath.Join(workDir, "go.work"))), Ecosystem: "go",
			Name: mod, LocalPath: dir,
			Evidence: fmt.Sprintf("%s:%d use %s", filepath.ToSlash(relOr(root, filepath.Join(workDir, "go.work"))), u.Syntax.Start.Line, u.Path),
		})
	}
	if !member {
		return
	}
	// A go.work link overrides the version pin of a module this repository
	// requires; a workspace module it does not require is still a link.
	for _, o := range others {
		merged := false
		for i := range r.Requires {
			if r.Requires[i].Ecosystem == "go" && r.Requires[i].Name == o.Name && r.Requires[i].LocalPath == "" {
				r.Requires[i].LocalPath = o.LocalPath
				r.Requires[i].Evidence = o.Evidence + " (required " + r.Requires[i].Version + ")"
				merged = true
			}
		}
		if !merged {
			o.CoMember = true
			r.Requires = append(r.Requires, o)
		}
	}
}

func scanNPM(root string, r *Result) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Name            string            `json:"name"`
		Version         string            `json:"version"`
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		PeerDeps        map[string]string `json:"peerDependencies"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return
	}
	if pkg.Name != "" {
		r.Packages = append(r.Packages, Package{Ecosystem: "npm", Name: pkg.Name, Version: pkg.Version, Manifest: "package.json"})
	}
	for _, group := range []struct {
		field string
		deps  map[string]string
	}{{"dependencies", pkg.Dependencies}, {"devDependencies", pkg.DevDependencies}, {"peerDependencies", pkg.PeerDeps}} {
		for name, spec := range group.deps {
			req := Require{
				Manifest: "package.json", Ecosystem: "npm", Name: name, Version: spec,
				Evidence: fmt.Sprintf("package.json %s: %q: %q", group.field, name, spec),
			}
			for _, scheme := range []string{"file:", "link:"} {
				if p, ok := strings.CutPrefix(spec, scheme); ok {
					if !filepath.IsAbs(p) {
						p = filepath.Join(root, filepath.FromSlash(p))
					}
					req.LocalPath = filepath.Clean(p)
				}
			}
			r.Requires = append(r.Requires, req)
		}
	}
}

func goModulePath(gomod string) string {
	data, err := os.ReadFile(gomod)
	if err != nil {
		return ""
	}
	return modfile.ModulePath(data)
}

// findUp returns the nearest directory at or above start holding name, and
// that file's contents.
func findUp(start, name string) (string, []byte) {
	for d := filepath.Clean(start); ; {
		if data, err := os.ReadFile(filepath.Join(d, name)); err == nil {
			return d, data
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", nil
		}
		d = parent
	}
}

func relOr(base, target string) string {
	if rel, err := filepath.Rel(base, target); err == nil {
		return rel
	}
	return target
}

func scanDir(dir string, r *Result) {
	scanGo(dir, r)
	scanNPM(dir, r)
	scanCargo(dir, r)
	scanPython(dir, r)
	scanMaven(dir, r)
	scanGradle(dir, r)
	scanComposer(dir, r)
}

var manifestNames = []string{"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "requirements.txt", "pom.xml",
	"build.gradle", "build.gradle.kts", "composer.json"}

// nestedManifestDirs finds directories below root that hold a manifest, to
// a bounded depth and count, skipping dependency and build output trees.
func nestedManifestDirs(root string) []string {
	const maxDepth, maxDirs, maxVisited = 4, 200, 20000
	var out []string
	visited := 0
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if depth > maxDepth || len(out) >= maxDirs || visited >= maxVisited {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		visited += len(entries)
		if depth > 0 {
			for _, name := range manifestNames {
				if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
					out = append(out, dir)
					break
				}
			}
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			switch name := e.Name(); {
			case strings.HasPrefix(name, "."), name == "node_modules", name == "vendor", name == "target",
				name == "build", name == "dist", name == "out", name == "testdata", name == "__pycache__":
				continue
			}
			walk(filepath.Join(dir, e.Name()), depth+1)
		}
	}
	walk(root, 0)
	sort.Strings(out)
	return out
}
