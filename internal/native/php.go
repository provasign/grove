package native

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type phpAnalyzer struct{}

func (phpAnalyzer) Name() string { return "php" }

func (phpAnalyzer) Languages() []string { return []string{"php"} }

func (phpAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "composer.json") {
		return Availability{Reason: "no composer.json"}
	}
	return Availability{Available: true}
}

type composerJSON struct {
	Autoload struct {
		PSR4 map[string]any `json:"psr-4"`
	} `json:"autoload"`
	AutoloadDev struct {
		PSR4 map[string]any `json:"psr-4"`
	} `json:"autoload-dev"`
}

func (phpAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	data, err := osReadFile(filepath.Join(req.Root, "composer.json"))
	if err != nil {
		return Result{Diagnostics: []string{"composer.json read failed: " + err.Error()}}
	}
	var composer composerJSON
	if err := unmarshalJSON(data, &composer); err != nil {
		return Result{Diagnostics: []string{"composer.json decode failed: " + err.Error()}}
	}
	psr4 := map[string][]string{}
	addPSR4(psr4, composer.Autoload.PSR4)
	addPSR4(psr4, composer.AutoloadDev.PSR4)

	fileScope := fileSet(req.Files)
	var edges []core.Edge
	for _, file := range req.Files {
		content, err := osReadFile(filepath.Join(req.Root, file))
		if err != nil {
			continue
		}
		for _, class := range phpReferencedClasses(string(content)) {
			if target, ok := resolvePHPClass(class, psr4, fileScope); ok && target != file {
				edges = append(edges, nativeImportEdge(file, target, 0.94))
			}
		}
	}
	autoloadCount := len(edges)
	semanticEdges := lexicalSemanticEdges(req.Symbols, map[string]bool{"php": true}, 0.91, 0.89)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"composer psr-4 prefixes loaded " + itoa(len(psr4)),
			"resolved " + itoa(autoloadCount) + " native autoload edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}

func addPSR4(out map[string][]string, in map[string]any) {
	for prefix, raw := range in {
		switch v := raw.(type) {
		case string:
			out[prefix] = append(out[prefix], filepath.ToSlash(v))
		case []any:
			for _, item := range v {
				if s, ok := item.(string); ok {
					out[prefix] = append(out[prefix], filepath.ToSlash(s))
				}
			}
		}
	}
}

var phpUsePattern = regexp.MustCompile(`(?m)^\s*use\s+([A-Za-z_\\][A-Za-z0-9_\\]*)\s*;`)
var phpNewPattern = regexp.MustCompile(`\bnew\s+\\?([A-Za-z_\\][A-Za-z0-9_\\]*)\s*\(`)

func phpReferencedClasses(content string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		name = strings.Trim(name, "\\")
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, match := range phpUsePattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			add(match[1])
		}
	}
	for _, match := range phpNewPattern.FindAllStringSubmatch(content, -1) {
		if len(match) == 2 {
			add(match[1])
		}
	}
	return out
}

func resolvePHPClass(class string, psr4 map[string][]string, fileScope map[string]bool) (string, bool) {
	class = strings.Trim(class, "\\")
	for prefix, dirs := range psr4 {
		prefix = strings.Trim(prefix, "\\")
		if class != prefix && !strings.HasPrefix(class, prefix+"\\") {
			continue
		}
		relative := strings.TrimPrefix(class, prefix)
		relative = strings.TrimPrefix(relative, "\\")
		relative = strings.ReplaceAll(relative, "\\", "/") + ".php"
		for _, dir := range dirs {
			candidate := filepath.ToSlash(filepath.Clean(filepath.Join(dir, relative)))
			if fileScope[candidate] {
				return candidate, true
			}
		}
	}
	return "", false
}
