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

func TestScanCargoPythonMavenComposer(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	write(t, filepath.Join(root, "Cargo.toml"), `[package]
name = "appcrate"
version = "0.2.0"

[dependencies]
serde = "1.0"
corelib = { path = "../corelib", version = "0.4" }
renamed = { package = "real-crate", version = "2" }
`)
	write(t, filepath.Join(root, "pyproject.toml"), `[project]
name = "My_App"
version = "1.0"
dependencies = [
  "Requests>=2.0",
  "ext-lib[fast] ; python_version < '3.12'",
]
`)
	write(t, filepath.Join(root, "pom.xml"), `<project><parent><groupId>org.acme</groupId><artifactId>parent</artifactId><version>3.1</version></parent>
<artifactId>app</artifactId><dependencies><dependency><groupId>org.acme</groupId><artifactId>core</artifactId><version>3.0</version></dependency></dependencies></project>`)
	write(t, filepath.Join(root, "composer.json"), `{"name":"acme/app","require":{"php":">=8.1","acme/core":"^2.0"},
		"repositories":[{"type":"path","url":"../core"}]}`)
	write(t, filepath.Join(filepath.Dir(root), "core", "composer.json"), `{"name":"acme/core"}`)

	r := Scan(root)
	declared := map[string]string{}
	for _, p := range r.Packages {
		declared[p.Ecosystem+":"+p.Name] = p.Version
	}
	for key, version := range map[string]string{"cargo:appcrate": "0.2.0", "pypi:my-app": "1.0", "maven:org.acme:app": "3.1", "composer:acme/app": ""} {
		if v, ok := declared[key]; !ok || v != version {
			t.Fatalf("package %s = %q, %v; all: %v", key, v, ok, declared)
		}
	}
	if c := find(r.Requires, "corelib"); c == nil || c.LocalPath != filepath.Join(filepath.Dir(root), "corelib") || c.Version != "0.4" {
		t.Fatalf("cargo path dependency: %+v", c)
	}
	if find(r.Requires, "real-crate") == nil || find(r.Requires, "renamed") != nil {
		t.Fatal("a renamed cargo dependency must be reported under its real crate name")
	}
	if q := find(r.Requires, "requests"); q == nil || q.Version != ">=2.0" || q.Ecosystem != "pypi" {
		t.Fatalf("python requirement: %+v", q)
	}
	if find(r.Requires, "ext-lib") == nil {
		t.Fatal("python requirement with extras and a marker not read")
	}
	if m := find(r.Requires, "org.acme:core"); m == nil || m.Version != "3.0" {
		t.Fatalf("maven dependency: %+v", m)
	}
	if c := find(r.Requires, "acme/core"); c == nil || c.LocalPath != filepath.Join(filepath.Dir(root), "core") {
		t.Fatalf("composer path repository not matched to its package: %+v", c)
	}
	if find(r.Requires, "php") != nil {
		t.Fatal("the PHP platform requirement was reported as a package")
	}
}

func TestScanNestedPackagesGradleAndRequirements(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(parent, "mono")
	// An npm workspace: two packages below the root, one requiring the other.
	write(t, filepath.Join(root, "package.json"), `{"name":"mono","private":true,"workspaces":["packages/*"]}`)
	write(t, filepath.Join(root, "packages", "core", "package.json"), `{"name":"@acme/core","version":"1.0.0"}`)
	write(t, filepath.Join(root, "packages", "web", "package.json"), `{"name":"@acme/web","dependencies":{"@acme/core":"workspace:*","react":"^18"}}`)
	write(t, filepath.Join(root, "node_modules", "junk", "package.json"), `{"name":"junk"}`)
	// A Gradle service in the same repository, in a composite build with a
	// sibling checkout.
	write(t, filepath.Join(root, "service", "build.gradle"), "group = 'com.acme'\nversion = '2.1'\n\ndependencies {\n    implementation 'com.acme:shared:1.4'\n    testImplementation(\"org.junit:junit:5\")\n}\n")
	write(t, filepath.Join(root, "service", "settings.gradle"), "rootProject.name = 'svc'\nincludeBuild('../../shared')\n")
	write(t, filepath.Join(parent, "shared", "build.gradle"), "group = 'com.acme'\n")
	write(t, filepath.Join(parent, "shared", "settings.gradle"), "rootProject.name = 'shared'\n")
	// A Python tool with an editable sibling.
	write(t, filepath.Join(root, "tool", "requirements.txt"), "requests==2.31\n-e ../../pylib\n# comment\n")
	write(t, filepath.Join(parent, "pylib", "pyproject.toml"), "[project]\nname = \"py_lib\"\n")

	r := Scan(root)
	declared := map[string]string{}
	for _, p := range r.Packages {
		declared[p.Ecosystem+":"+p.Name] = p.Manifest
	}
	for id, manifest := range map[string]string{
		"npm:mono": "package.json", "npm:@acme/core": "packages/core/package.json", "npm:@acme/web": "packages/web/package.json",
		"maven:com.acme:svc": "service/build.gradle",
	} {
		if declared[id] != manifest {
			t.Fatalf("package %s declared by %q, want %q; all: %v", id, declared[id], manifest, declared)
		}
	}
	if declared["npm:junk"] != "" {
		t.Fatal("a package under node_modules was reported as declared by the repository")
	}
	if find(r.Requires, "@acme/core") != nil {
		t.Fatal("a requirement on a package the repository itself declares was reported as external")
	}
	if find(r.Requires, "react") == nil {
		t.Fatal("a nested package's external requirement was lost")
	}
	if s := find(r.Requires, "com.acme:shared"); s == nil || s.LocalPath != filepath.Join(parent, "shared") || s.Version != "1.4" || !strings.Contains(s.Evidence, "includeBuild") {
		t.Fatalf("Gradle composite build not reported as a local link: %+v", s)
	}
	if j := find(r.Requires, "org.junit:junit"); j == nil || j.Version != "5" {
		t.Fatalf("Gradle dependency in call notation: %+v", j)
	}
	if p := find(r.Requires, "py-lib"); p == nil || p.LocalPath != filepath.Join(parent, "pylib") {
		t.Fatalf("editable requirement not reported as a local link: %+v", p)
	}
	if q := find(r.Requires, "requests"); q == nil || q.Version != "==2.31" {
		t.Fatalf("requirements.txt pin: %+v", q)
	}
}
