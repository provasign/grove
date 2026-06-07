package native

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
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
	semanticEdges := lexicalSemanticEdges(req.Symbols, map[string]bool{"rust": true}, 0.93, 0.9)
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
