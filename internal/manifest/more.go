package manifest

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Cargo, Python, Maven and Composer manifests. These are read for package
// identity and declared requirements only, with small line-level readers
// rather than full format parsers: an unusual layout is skipped, never
// guessed at.

func scanCargo(root string, r *Result) {
	data, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return
	}
	section := ""
	var name, version string
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section {
		case "package":
			switch key {
			case "name":
				name = tomlString(value)
			case "version":
				version = tomlString(value)
			}
		case "dependencies", "dev-dependencies", "build-dependencies":
			req := Require{Manifest: "Cargo.toml", Ecosystem: "cargo", Name: strings.Trim(key, `"`),
				Evidence: fmt.Sprintf("Cargo.toml:%d [%s] %s", n+1, section, line)}
			if strings.HasPrefix(value, "{") {
				req.Version = tomlField(value, "version")
				if p := tomlField(value, "path"); p != "" {
					if !filepath.IsAbs(p) {
						p = filepath.Join(root, filepath.FromSlash(p))
					}
					req.LocalPath = filepath.Clean(p)
				}
				if pkg := tomlField(value, "package"); pkg != "" {
					req.Name = pkg // renamed dependency: the real crate
				}
			} else {
				req.Version = tomlString(value)
			}
			r.Requires = append(r.Requires, req)
		}
	}
	if name != "" {
		r.Packages = append(r.Packages, Package{Ecosystem: "cargo", Name: name, Version: version, Manifest: "Cargo.toml"})
	}
}

func tomlString(v string) string {
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.Trim(strings.TrimSpace(v), `"'`)
}

var tomlFieldRe = regexp.MustCompile(`(\w[\w-]*)\s*=\s*"([^"]*)"`)

func tomlField(inline, field string) string {
	for _, m := range tomlFieldRe.FindAllStringSubmatch(inline, -1) {
		if m[1] == field {
			return m[2]
		}
	}
	return ""
}

// pyDistName is the distribution name at the start of a requirement
// string ("requests>=2", "pkg[extra] ; python_version<'3.9'").
var pyDistName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*`)

// scanRequirements reads requirements.txt: one requirement per line, and
// "-e <path>" for a local project installed in development mode.
func scanRequirements(root string, r *Result) {
	data, err := os.ReadFile(filepath.Join(root, "requirements.txt"))
	if err != nil {
		return
	}
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "-e "); ok || strings.HasPrefix(line, "--editable ") {
			if !ok {
				rest = strings.TrimPrefix(line, "--editable ")
			}
			p := strings.TrimSpace(rest)
			if strings.Contains(p, "://") || strings.HasPrefix(p, "git+") {
				continue
			}
			if !filepath.IsAbs(p) {
				p = filepath.Join(root, filepath.FromSlash(p))
			}
			p = filepath.Clean(p)
			// The distribution's name is in that directory's own manifest.
			var other Result
			scanPython(p, &other)
			name := filepath.Base(p)
			if len(other.Packages) > 0 {
				name = other.Packages[0].Name
			}
			r.Requires = append(r.Requires, Require{Manifest: "requirements.txt", Ecosystem: "pypi", Name: normalizePyName(name),
				LocalPath: p, Evidence: fmt.Sprintf("requirements.txt:%d %s", n+1, line)})
			continue
		}
		if strings.HasPrefix(line, "-") {
			continue
		}
		dist := pyDistName.FindString(line)
		if dist == "" {
			continue
		}
		r.Requires = append(r.Requires, Require{Manifest: "requirements.txt", Ecosystem: "pypi", Name: normalizePyName(dist),
			Version: strings.TrimSpace(strings.TrimPrefix(line, dist)), Evidence: fmt.Sprintf("requirements.txt:%d %s", n+1, line)})
	}
}

var (
	gradleDep     = regexp.MustCompile(`(?m)^\s*(?:api|implementation|compileOnly|runtimeOnly|testImplementation|compile|testCompile)\s*\(?\s*["']([\w.-]+):([\w.-]+)(?::([^"']+))?["']`)
	gradleGroup   = regexp.MustCompile(`(?m)^\s*group\s*=?\s*["']([\w.-]+)["']`)
	gradleVersion = regexp.MustCompile(`(?m)^\s*version\s*=?\s*["']([^"']+)["']`)
	gradleName    = regexp.MustCompile(`(?m)^\s*rootProject\.name\s*=\s*["']([^"']+)["']`)
	gradleInclude = regexp.MustCompile(`(?m)^\s*includeBuild\s*\(?\s*["']([^"']+)["']`)
)

// scanGradle reads a Gradle build by pattern: the group and version
// assignments, rootProject.name, string-notation dependencies, and
// includeBuild (a composite build: another build's directory used in place
// of its published artifact). Gradle files are programs; anything computed
// is not seen.
func scanGradle(root string, r *Result) {
	var build, manifest string
	for _, name := range []string{"build.gradle.kts", "build.gradle"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			build, manifest = string(data), name
			break
		}
	}
	var settings, settingsName string
	for _, name := range []string{"settings.gradle.kts", "settings.gradle"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			settings, settingsName = string(data), name
			break
		}
	}
	if build == "" && settings == "" {
		return
	}
	if _, err := os.Stat(filepath.Join(root, "pom.xml")); err != nil && build != "" {
		if g := gradleGroup.FindStringSubmatch(build); g != nil {
			name := filepath.Base(root)
			if m := gradleName.FindStringSubmatch(settings); m != nil {
				name = m[1]
			}
			version := ""
			if v := gradleVersion.FindStringSubmatch(build); v != nil {
				version = v[1]
			}
			r.Packages = append(r.Packages, Package{Ecosystem: "maven", Name: g[1] + ":" + name, Version: version, Manifest: manifest})
		}
	}
	for _, m := range gradleDep.FindAllStringSubmatch(build, -1) {
		r.Requires = append(r.Requires, Require{Manifest: manifest, Ecosystem: "maven", Name: m[1] + ":" + m[2], Version: m[3],
			Evidence: strings.TrimSpace(manifest + " dependency " + m[1] + ":" + m[2] + " " + m[3])})
	}
	// A composite build names a directory; what it provides is declared there.
	for _, m := range gradleInclude.FindAllStringSubmatch(settings, -1) {
		dir := m[1]
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, filepath.FromSlash(dir))
		}
		dir = filepath.Clean(dir)
		var other Result
		scanGradle(dir, &other)
		scanMaven(dir, &other)
		for _, p := range other.Packages {
			found := false
			for i := range r.Requires {
				if r.Requires[i].Ecosystem == p.Ecosystem && r.Requires[i].Name == p.Name {
					r.Requires[i].LocalPath = dir
					r.Requires[i].Evidence = settingsName + " includeBuild " + m[1] + " (required " + r.Requires[i].Version + ")"
					found = true
				}
			}
			if !found {
				r.Requires = append(r.Requires, Require{Manifest: settingsName, Ecosystem: p.Ecosystem, Name: p.Name, LocalPath: dir,
					Evidence: settingsName + " includeBuild " + m[1]})
			}
		}
	}
}

func scanPython(root string, r *Result) {
	scanRequirements(root, r)
	data, err := os.ReadFile(filepath.Join(root, "pyproject.toml"))
	if err != nil {
		return
	}
	section := ""
	inDeps := false
	var name, version string
	addReq := func(spec string, line int) {
		spec = strings.Trim(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(spec), ",")), `"'`)
		dist := pyDistName.FindString(spec)
		if dist == "" {
			return
		}
		r.Requires = append(r.Requires, Require{Manifest: "pyproject.toml", Ecosystem: "pypi", Name: normalizePyName(dist),
			Version:  strings.TrimSpace(strings.TrimPrefix(spec, dist)),
			Evidence: fmt.Sprintf("pyproject.toml:%d %s", line, spec)})
	}
	for n, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if inDeps {
			if strings.HasPrefix(line, "]") {
				inDeps = false
				continue
			}
			addReq(line, n+1)
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch section {
		case "project", "tool.poetry":
			switch key {
			case "name":
				name = normalizePyName(tomlString(value))
			case "version":
				version = tomlString(value)
			case "dependencies":
				if strings.HasPrefix(value, "[") {
					body := strings.TrimPrefix(value, "[")
					if end := strings.Index(body, "]"); end >= 0 {
						for _, spec := range strings.Split(body[:end], ",") {
							addReq(spec, n+1)
						}
					} else {
						inDeps = true
						if strings.TrimSpace(body) != "" {
							addReq(body, n+1)
						}
					}
				}
			}
		case "tool.poetry.dependencies", "tool.poetry.group.dev.dependencies":
			if key == "python" {
				continue
			}
			req := Require{Manifest: "pyproject.toml", Ecosystem: "pypi", Name: normalizePyName(strings.Trim(key, `"`)),
				Evidence: fmt.Sprintf("pyproject.toml:%d [%s] %s", n+1, section, line)}
			if strings.HasPrefix(value, "{") {
				req.Version = tomlField(value, "version")
				if p := tomlField(value, "path"); p != "" {
					if !filepath.IsAbs(p) {
						p = filepath.Join(root, filepath.FromSlash(p))
					}
					req.LocalPath = filepath.Clean(p)
				}
			} else {
				req.Version = tomlString(value)
			}
			r.Requires = append(r.Requires, req)
		}
	}
	if name != "" {
		r.Packages = append(r.Packages, Package{Ecosystem: "pypi", Name: name, Version: version, Manifest: "pyproject.toml"})
	}
}

// normalizePyName applies the distribution-name normalization (PEP 503):
// case-insensitive, with runs of -, _ and . equal.
func normalizePyName(s string) string {
	return regexp.MustCompile(`[-_.]+`).ReplaceAllString(strings.ToLower(s), "-")
}

func scanMaven(root string, r *Result) {
	data, err := os.ReadFile(filepath.Join(root, "pom.xml"))
	if err != nil {
		return
	}
	type dep struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
	}
	var pom struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
		Parent     dep    `xml:"parent"`
		Deps       []dep  `xml:"dependencies>dependency"`
	}
	if xml.Unmarshal(data, &pom) != nil || pom.ArtifactID == "" {
		return
	}
	group, version := pom.GroupID, pom.Version
	if group == "" {
		group = pom.Parent.GroupID
	}
	if version == "" {
		version = pom.Parent.Version
	}
	r.Packages = append(r.Packages, Package{Ecosystem: "maven", Name: group + ":" + pom.ArtifactID, Version: version, Manifest: "pom.xml"})
	for _, d := range pom.Deps {
		if d.GroupID == "" || d.ArtifactID == "" {
			continue
		}
		r.Requires = append(r.Requires, Require{Manifest: "pom.xml", Ecosystem: "maven", Name: d.GroupID + ":" + d.ArtifactID,
			Version: d.Version, Evidence: strings.TrimSpace("pom.xml dependency " + d.GroupID + ":" + d.ArtifactID + " " + d.Version)})
	}
}

func scanComposer(root string, r *Result) {
	data, err := os.ReadFile(filepath.Join(root, "composer.json"))
	if err != nil {
		return
	}
	var c struct {
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Require      map[string]string `json:"require"`
		RequireDev   map[string]string `json:"require-dev"`
		Repositories []struct {
			Type string `json:"type"`
			URL  string `json:"url"`
		} `json:"repositories"`
	}
	if json.Unmarshal(data, &c) != nil {
		return
	}
	if c.Name != "" {
		r.Packages = append(r.Packages, Package{Ecosystem: "composer", Name: c.Name, Version: c.Version, Manifest: "composer.json"})
	}
	// A "path" repository names a directory; which package it provides is
	// in that directory's own composer.json.
	local := map[string]string{}
	for _, repo := range c.Repositories {
		if repo.Type != "path" || strings.ContainsAny(repo.URL, "*?[") {
			continue
		}
		dir := repo.URL
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, filepath.FromSlash(dir))
		}
		var other struct {
			Name string `json:"name"`
		}
		if d, err := os.ReadFile(filepath.Join(dir, "composer.json")); err == nil && json.Unmarshal(d, &other) == nil && other.Name != "" {
			local[other.Name] = filepath.Clean(dir)
		}
	}
	for field, deps := range map[string]map[string]string{"require": c.Require, "require-dev": c.RequireDev} {
		for name, spec := range deps {
			if name == "php" || strings.HasPrefix(name, "ext-") {
				continue
			}
			r.Requires = append(r.Requires, Require{Manifest: "composer.json", Ecosystem: "composer", Name: name, Version: spec,
				LocalPath: local[name], Evidence: fmt.Sprintf("composer.json %s: %q: %q", field, name, spec)})
		}
	}
}
