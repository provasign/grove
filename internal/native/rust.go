package native

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type rustAnalyzer struct{}

func (rustAnalyzer) Name() string { return "rust" }

func (rustAnalyzer) Languages() []string { return []string{"rust"} }

func (rustAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "Cargo.toml") {
		return Availability{Reason: "no Cargo.toml"}
	}
	if !commandExists("cargo") {
		return Availability{Reason: "cargo executable not found"}
	}
	return Availability{Available: true}
}

type cargoMetadata struct {
	Packages []struct {
		ID      string `json:"id"`
		Name    string `json:"name"`
		Targets []struct {
			SrcPath string `json:"src_path"`
		} `json:"targets"`
		Dependencies []struct {
			Name string `json:"name"`
		} `json:"dependencies"`
	} `json:"packages"`
	Resolve *struct {
		Nodes []struct {
			ID   string `json:"id"`
			Deps []struct {
				Pkg string `json:"pkg"`
			} `json:"deps"`
		} `json:"nodes"`
	} `json:"resolve"`
}

func (rustAnalyzer) Analyze(ctx context.Context, req Request) Result {
	cmd := exec.CommandContext(ctx, "cargo", "metadata", "--format-version=1", "--no-deps", "--locked")
	cmd.Dir = req.Root
	out, err := cmd.Output()
	if err != nil {
		return Result{Diagnostics: []string{"cargo metadata failed: " + err.Error()}}
	}
	var meta cargoMetadata
	if err := json.Unmarshal(out, &meta); err != nil {
		return Result{Diagnostics: []string{"cargo metadata JSON decode failed: " + err.Error()}}
	}
	pkgTargets := map[string][]string{}
	pkgByName := map[string]string{}
	for _, pkg := range meta.Packages {
		pkgByName[pkg.Name] = pkg.ID
		for _, target := range pkg.Targets {
			rel, ok := relFile(req.Root, target.SrcPath)
			if ok {
				pkgTargets[pkg.ID] = append(pkgTargets[pkg.ID], rel)
			}
		}
	}
	var edges []core.Edge
	for _, pkg := range meta.Packages {
		fromTargets := pkgTargets[pkg.ID]
		for _, dep := range pkg.Dependencies {
			depID := pkgByName[dep.Name]
			if depID == "" {
				continue
			}
			for _, from := range fromTargets {
				for _, to := range pkgTargets[depID] {
					if from == to {
						continue
					}
					edges = append(edges, nativeImportEdge(from, to, 0.96))
				}
			}
		}
	}
	edges = append(edges, rustModuleEdges(req.Root, req.Files)...)
	importCount := len(edges)
	semanticEdges := rustSemanticEdges(req.Symbols, req.Files)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"cargo metadata resolved " + itoa(len(meta.Packages)) + " package(s)",
			"resolved " + itoa(importCount) + " native import edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}

func rustModuleEdges(root string, files []string) []core.Edge {
	fileScope := fileSet(files)
	var edges []core.Edge
	for _, file := range files {
		content, err := osReadFile(filepath.Join(root, file))
		if err != nil {
			continue
		}
		for _, mod := range rustModuleNames(string(content)) {
			for _, target := range rustModuleCandidates(file, mod) {
				if fileScope[target] {
					edges = append(edges, nativeImportEdge(file, target, 0.93))
					break
				}
			}
		}
	}
	return edges
}

func rustModuleNames(content string) []string {
	lines := strings.Split(content, "\n")
	var mods []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "pub ")
		if !strings.HasPrefix(line, "mod ") || !strings.HasSuffix(line, ";") {
			continue
		}
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "mod "), ";"))
		if name != "" && !strings.ContainsAny(name, " \t:{") {
			mods = append(mods, name)
		}
	}
	return mods
}

func rustModuleCandidates(from, mod string) []string {
	dir := packageDir(from)
	return []string{
		filepath.ToSlash(filepath.Join(dir, mod+".rs")),
		filepath.ToSlash(filepath.Join(dir, mod, "mod.rs")),
	}
}

func rustSemanticEdges(symbols []core.SymbolRecord, files []string) []core.Edge {
	byFile := map[string][]core.SymbolRecord{}
	moduleFiles := rustModuleFileIndex(files)
	for _, symbol := range symbols {
		if symbol.Language == "rust" {
			byFile[symbol.FilePath] = append(byFile[symbol.FilePath], symbol)
		}
	}
	var edges []core.Edge
	seen := map[string]bool{}
	add := func(edge core.Edge) {
		key := edge.From + "\x00" + string(edge.Type) + "\x00" + edge.To
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, edge)
	}
	for _, caller := range symbols {
		if caller.Language != "rust" || caller.RawText == "" || !callableKind(caller.Kind) {
			continue
		}
		scopeFiles := append([]string{caller.FilePath}, rustModuleScopeFiles(caller.FilePath, caller.RawText, moduleFiles)...)
		for _, file := range scopeFiles {
			for _, target := range byFile[file] {
				if target.ID == caller.ID {
					continue
				}
				if callableKind(target.Kind) && rustContainsCall(caller.RawText, target, file != caller.FilePath) {
					add(symbolEdge(caller, target, core.EdgeCalls, 0.95))
				}
				if typeKind(target.Kind) && rustContainsType(caller.RawText, target, file != caller.FilePath) {
					add(symbolEdge(caller, target, core.EdgeUsesType, 0.93))
				}
			}
		}
	}
	return edges
}

func rustModuleFileIndex(files []string) map[string]string {
	out := map[string]string{}
	for _, file := range files {
		if !strings.HasSuffix(file, ".rs") {
			continue
		}
		base := filepath.Base(file)
		name := strings.TrimSuffix(base, ".rs")
		if name == "mod" {
			name = filepath.Base(packageDir(file))
		}
		if name != "" {
			out[name] = file
		}
	}
	return out
}

func rustModuleScopeFiles(fromFile, rawText string, moduleFiles map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, mod := range rustPathPrefixes(rawText) {
		if file := moduleFiles[mod]; file != "" && file != fromFile && !seen[file] {
			seen[file] = true
			out = append(out, file)
		}
	}
	return out
}

var rustPathPrefixPattern = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]*)::[A-Za-z_][A-Za-z0-9_]*`)

func rustPathPrefixes(rawText string) []string {
	matches := rustPathPrefixPattern.FindAllStringSubmatch(rawText, -1)
	seen := map[string]bool{}
	var out []string
	for _, match := range matches {
		if len(match) == 2 && !seen[match[1]] {
			seen[match[1]] = true
			out = append(out, match[1])
		}
	}
	return out
}

func rustContainsCall(rawText string, target core.SymbolRecord, qualified bool) bool {
	if !qualified {
		return containsCall(rawText, target.Name)
	}
	module := rustModuleName(target.FilePath)
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(module) + `::` + regexp.QuoteMeta(target.Name) + `\s*\(`)
	return pattern.MatchString(stripQuotedText(rawText))
}

func rustContainsType(rawText string, target core.SymbolRecord, qualified bool) bool {
	if !qualified {
		return containsTypeToken(rawText, target.Name)
	}
	module := rustModuleName(target.FilePath)
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(module) + `::` + regexp.QuoteMeta(target.Name) + `\b`)
	return pattern.MatchString(stripQuotedText(rawText))
}

func rustModuleName(file string) string {
	name := strings.TrimSuffix(filepath.Base(file), ".rs")
	if name == "mod" {
		name = filepath.Base(packageDir(file))
	}
	return name
}
