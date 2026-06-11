package index

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ignoreRule struct {
	// base is the repo-relative directory of the ignore file the rule came
	// from ("" for root). The rule only applies below base, and its pattern
	// is matched against the base-relative remainder of the path —
	// gitignore semantics for nested ignore files.
	base     string
	pattern  string
	negate   bool
	dirOnly  bool
	anchored bool // pattern contains a non-trailing slash or led with "/"
}

// ignoreMatcher accumulates ignore rules as the walk descends. Nested
// .gitignore/.groveignore files are loaded when their directory is visited;
// because parents are visited before children, deeper rules come later in
// the slice and override shallower ones (last match wins, as in git).
type ignoreMatcher struct {
	rules []ignoreRule
}

func newIgnoreMatcher(root string) *ignoreMatcher {
	m := &ignoreMatcher{}
	m.LoadDir(root, "")
	return m
}

// LoadDir parses ignore files in absDir (repo-relative relDir; "" = root).
func (m *ignoreMatcher) LoadDir(absDir, relDir string) {
	if relDir == "." {
		relDir = ""
	}
	for _, name := range []string{".groveignore", ".gitignore"} {
		data, err := os.ReadFile(filepath.Join(absDir, name))
		if err != nil {
			continue
		}
		m.rules = append(m.rules, parseIgnoreRulesAt(string(data), relDir)...)
	}
}

func (m *ignoreMatcher) Ignored(rel string, isDir bool) bool {
	return ignoredByRules(rel, isDir, m.rules)
}

func parseIgnoreRules(body string) []ignoreRule {
	return parseIgnoreRulesAt(body, "")
}

func parseIgnoreRulesAt(body, base string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{base: base}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			rule.anchored = true
			line = strings.TrimPrefix(line, "/")
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		line = filepath.ToSlash(line)
		if line == "" {
			continue
		}
		// A slash anywhere (after trimming the trailing one) anchors the
		// pattern to the ignore file's directory, per gitignore.
		if strings.Contains(line, "/") {
			rule.anchored = true
		}
		rule.pattern = line
		rules = append(rules, rule)
	}
	return rules
}

func ignoredByRules(rel string, isDir bool, rules []ignoreRule) bool {
	rel = filepath.ToSlash(rel)
	ignored := false
	for _, rule := range rules {
		if rule.dirOnly && !isDir {
			continue
		}
		if ignoreRuleMatches(rule, rel) {
			ignored = !rule.negate
		}
	}
	return ignored
}

func ignoreRuleMatches(rule ignoreRule, rel string) bool {
	if rule.pattern == "" {
		return false
	}
	// Scope to the rule's own directory and match against the remainder.
	if rule.base != "" {
		prefix := rule.base + "/"
		if !strings.HasPrefix(rel, prefix) {
			return false
		}
		rel = rel[len(prefix):]
	}

	if !rule.anchored {
		// Bare name: matches any path component at any depth.
		for _, segment := range strings.Split(rel, "/") {
			if globMatch(rule.pattern, segment) {
				return true
			}
		}
		return false
	}

	patSegs := strings.Split(rule.pattern, "/")
	relSegs := strings.Split(rel, "/")
	if matchSegments(patSegs, relSegs) {
		return true
	}
	// An anchored pattern naming a directory also ignores everything under
	// it ("build/sub" matches "build/sub/x/y.go").
	for i := 1; i < len(relSegs); i++ {
		if matchSegments(patSegs, relSegs[:i]) {
			return true
		}
	}
	return false
}

// matchSegments matches gitignore pattern segments against path segments.
// "**" crosses any number of segments (including zero); other segments use
// shell glob matching within the segment only.
func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			if len(pat) == 1 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegments(pat[1:], segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if !globMatch(pat[0], segs[0]) {
			return false
		}
		pat = pat[1:]
		segs = segs[1:]
	}
	return len(segs) == 0
}

func globMatch(pattern, value string) bool {
	ok, err := path.Match(pattern, value)
	return err == nil && ok
}

func shouldSkipDirName(name string) bool {
	switch name {
	case ".git", ".grove", ".cache", ".venv", ".tox", ".next", ".idea",
		"node_modules", "vendor", "dist", "bin", "__pycache__", "target",
		"coverage", ".pytest_cache", ".mypy_cache", ".ruff_cache":
		return true
	default:
		return false
	}
}

func isSensitivePath(rel string) bool {
	base := strings.ToLower(path.Base(filepath.ToSlash(rel)))
	ext := strings.ToLower(path.Ext(base))
	switch base {
	case ".gitignore", ".groveignore", ".env", ".envrc", ".npmrc", ".pypirc", ".netrc", "credentials",
		"credentials.json", "secrets.json", "secrets.yaml", "secrets.yml":
		return true
	}
	if strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".env") {
		return true
	}
	if strings.Contains(base, "secret") || strings.Contains(base, "credential") {
		return true
	}
	switch ext {
	case ".key", ".pem", ".crt", ".cer", ".p12", ".pfx", ".jks", ".keystore", ".pkcs12":
		return true
	default:
		return false
	}
}
