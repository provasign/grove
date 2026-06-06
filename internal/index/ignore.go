package index

import (
	"os"
	"path"
	"path/filepath"
	"strings"
)

type ignoreRule struct {
	pattern string
	negate  bool
	dirOnly bool
	rooted  bool
}

func loadIgnoreRules(root string) []ignoreRule {
	var rules []ignoreRule
	for _, name := range []string{".groveignore", ".gitignore"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			continue
		}
		rules = append(rules, parseIgnoreRules(string(data))...)
	}
	return rules
}

func parseIgnoreRules(body string) []ignoreRule {
	var rules []ignoreRule
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule := ignoreRule{}
		if strings.HasPrefix(line, "!") {
			rule.negate = true
			line = strings.TrimSpace(strings.TrimPrefix(line, "!"))
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			rule.rooted = true
			line = strings.TrimPrefix(line, "/")
		}
		if strings.HasSuffix(line, "/") {
			rule.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		line = filepath.ToSlash(line)
		if line != "" {
			rule.pattern = line
			rules = append(rules, rule)
		}
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
	pattern := rule.pattern
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") && !rule.rooted {
		for _, segment := range strings.Split(rel, "/") {
			if globMatch(pattern, segment) {
				return true
			}
		}
		return false
	}
	if globMatch(pattern, rel) {
		return true
	}
	return strings.HasPrefix(rel, strings.TrimSuffix(pattern, "/")+"/")
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
