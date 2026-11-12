package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func find(reqs []Require, name string) *Require {
	for i := range reqs {
		if reqs[i].Name == name {
			return &reqs[i]
		}
	}
	return nil
}

func TestScanGoModuleRequiresAndLocalLinks(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	app := filepath.Join(parent, "app")
	write(t, filepath.Join(app, "go.mod"), `module example.com/app

go 1.22

require (
	example.com/lib v1.2.3
	example.com/pinned v0.4.0
	example.com/forked v1.0.0
	example.com/transitive v0.1.0 // indirect
)

replace example.com/forked => ../forked
`)
	write(t, filepath.Join(parent, "lib", "go.mod"), "module example.com/lib\n")
	write(t, filepath.Join(parent, "tool", "go.mod"), "module example.com/tool\n")
	write(t, filepath.Join(parent, "go.work"), "go 1.22\n\nuse (\n\t./app\n\t./lib\n\t./tool\n)\n")

	r := Scan(app)
	if len(r.Packages) != 1 || r.Packages[0].Name != "example.com/app" || r.Packages[0].Ecosystem != "go" {
		t.Fatalf("packages = %+v", r.Packages)
	}
	if find(r.Requires, "example.com/transitive") != nil {
		t.Fatal("an indirect requirement was reported as a dependency of this module's code")
	}
	lib := find(r.Requires, "example.com/lib")
	if lib == nil || lib.Version != "v1.2.3" || lib.LocalPath != filepath.Join(parent, "lib") || !strings.Contains(lib.Evidence, "use ./lib") {
		t.Fatalf("go.work link not merged onto the require: %+v", lib)
	}
	if p := find(r.Requires, "example.com/pinned"); p == nil || p.LocalPath != "" || p.Version != "v0.4.0" {
		t.Fatalf("a plain version pin must have no local path: %+v", p)
	}
	if f := find(r.Requires, "example.com/forked"); f == nil || f.LocalPath != filepath.Join(parent, "forked") || !strings.Contains(f.Evidence, "replace") {
		t.Fatalf("replace directive not reported as a local link: %+v", f)
	}
	if tool := find(r.Requires, "example.com/tool"); tool == nil || tool.LocalPath != filepath.Join(parent, "tool") || !tool.CoMember || lib.CoMember {
		t.Fatalf("a go.work module this one does not require is still a link: %+v", tool)
	}

	// A repository that is not in the go.work gets none of its links.
	write(t, filepath.Join(parent, "stranger", "go.mod"), "module example.com/stranger\n\nrequire example.com/lib v1.0.0\n")
	if l := find(Scan(filepath.Join(parent, "stranger")).Requires, "example.com/lib"); l == nil || l.LocalPath != "" {
		t.Fatalf("go.work applied to a module it does not use: %+v", l)
	}
}

func TestScanNPM(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(root, "package.json"), `{"name":"@acme/app","version":"2.0.0",
		"dependencies":{"@acme/core":"file:../core","left-pad":"^1.3.0"}}`)
	r := Scan(root)
	if len(r.Packages) != 1 || r.Packages[0].Name != "@acme/app" || r.Packages[0].Version != "2.0.0" {
		t.Fatalf("packages = %+v", r.Packages)
	}
	if c := find(r.Requires, "@acme/core"); c == nil || c.LocalPath != filepath.Join(filepath.Dir(root), "core") {
		t.Fatalf("file: dependency not reported as a local link: %+v", c)
	}
	if l := find(r.Requires, "left-pad"); l == nil || l.LocalPath != "" || l.Version != "^1.3.0" {
		t.Fatalf("registry dependency: %+v", l)
	}
}
