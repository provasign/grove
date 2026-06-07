package native

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type cFamilyAnalyzer struct{}

func (cFamilyAnalyzer) Name() string { return "c-cpp" }

func (cFamilyAnalyzer) Languages() []string { return []string{"c", "cpp"} }

func (cFamilyAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "compile_commands.json") {
		return Availability{Reason: "no compile_commands.json"}
	}
	return Availability{Available: true}
}

type compileCommand struct {
	Directory string   `json:"directory"`
	File      string   `json:"file"`
	Arguments []string `json:"arguments"`
	Command   string   `json:"command"`
}

func (cFamilyAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	data, err := osReadFile(filepath.Join(req.Root, "compile_commands.json"))
	if err != nil {
		return Result{Diagnostics: []string{"compile_commands.json read failed: " + err.Error()}}
	}
	var commands []compileCommand
	if err := json.Unmarshal(data, &commands); err != nil {
		return Result{Diagnostics: []string{"compile_commands.json decode failed: " + err.Error()}}
	}
	includeDirs := cIncludeDirs(req.Root, commands)
	fileScope := fileSet(req.Files)
	var edges []core.Edge
	for _, file := range req.Files {
		content, err := osReadFile(filepath.Join(req.Root, file))
		if err != nil {
			continue
		}
		for _, inc := range cIncludes(string(content)) {
			if target, ok := resolveCInclude(req.Root, file, inc, includeDirs, fileScope); ok {
				edges = append(edges, nativeImportEdge(file, target, 0.95))
			}
		}
	}
	includeCount := len(edges)
	semanticEdges := lexicalSemanticEdges(req.Symbols, map[string]bool{"c": true, "cpp": true}, 0.9, 0.88)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"compile_commands loaded " + itoa(len(commands)) + " command(s)",
			"resolved " + itoa(includeCount) + " native include edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}

func cIncludeDirs(root string, commands []compileCommand) []string {
	seen := map[string]bool{}
	var dirs []string
	add := func(dir string) {
		if dir == "" {
			return
		}
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		rel, ok := relFile(root, dir)
		if !ok {
			return
		}
		if !seen[rel] {
			seen[rel] = true
			dirs = append(dirs, rel)
		}
	}
	for _, cmd := range commands {
		args := cmd.Arguments
		if len(args) == 0 && cmd.Command != "" {
			args = strings.Fields(cmd.Command)
		}
		base := cmd.Directory
		if base == "" {
			base = root
		}
		for i := 0; i < len(args); i++ {
			arg := args[i]
			switch {
			case arg == "-I" || arg == "-isystem" || arg == "/I":
				if i+1 < len(args) {
					add(resolveAgainst(base, args[i+1]))
					i++
				}
			case strings.HasPrefix(arg, "-I") && len(arg) > 2:
				add(resolveAgainst(base, strings.TrimPrefix(arg, "-I")))
			case strings.HasPrefix(arg, "/I") && len(arg) > 2:
				add(resolveAgainst(base, strings.TrimPrefix(arg, "/I")))
			}
		}
	}
	return dirs
}

var cIncludePattern = regexp.MustCompile(`(?m)^\s*#\s*include\s+["<]([^">]+)[">]`)

func cIncludes(content string) []string {
	matches := cIncludePattern.FindAllStringSubmatch(content, -1)
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) == 2 && match[1] != "" {
			out = append(out, match[1])
		}
	}
	return out
}

func resolveCInclude(root, fromFile, inc string, includeDirs []string, fileScope map[string]bool) (string, bool) {
	candidates := []string{
		filepath.ToSlash(filepath.Join(packageDir(fromFile), inc)),
		filepath.ToSlash(inc),
	}
	for _, dir := range includeDirs {
		candidates = append(candidates, filepath.ToSlash(filepath.Join(dir, inc)))
	}
	for _, cand := range candidates {
		cand = filepath.ToSlash(filepath.Clean(cand))
		if fileScope[cand] {
			return cand, true
		}
		if rel, ok := relFile(root, filepath.Join(root, cand)); ok && fileScope[rel] {
			return rel, true
		}
	}
	return "", false
}

func resolveAgainst(base, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}
