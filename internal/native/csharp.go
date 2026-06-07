package native

import (
	"context"
	"encoding/xml"
	"path/filepath"
	"strings"

	"github.com/provasign/grove/internal/core"
)

type csharpAnalyzer struct{}

func (csharpAnalyzer) Name() string { return "csharp" }

func (csharpAnalyzer) Languages() []string { return []string{"csharp"} }

func (csharpAnalyzer) Available(_ context.Context, root string) Availability {
	if len(filesWithExt(root, ".csproj")) == 0 {
		return Availability{Reason: "no .csproj file"}
	}
	return Availability{Available: true}
}

type csProject struct {
	Items []struct {
		Compile []struct {
			Include string `xml:"Include,attr"`
			Remove  string `xml:"Remove,attr"`
		} `xml:"Compile"`
		ProjectReference []struct {
			Include string `xml:"Include,attr"`
		} `xml:"ProjectReference"`
	} `xml:"ItemGroup"`
}

func (csharpAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	projects := filesWithExt(req.Root, ".csproj")
	projectFiles := map[string][]string{}
	projectRefs := map[string][]string{}
	fileScope := fileSet(req.Files)
	for _, project := range projects {
		data, err := osReadFile(filepath.Join(req.Root, project))
		if err != nil {
			continue
		}
		var parsed csProject
		if err := xml.Unmarshal(data, &parsed); err != nil {
			continue
		}
		projectDir := packageDir(project)
		files := explicitCSharpFiles(projectDir, parsed, fileScope)
		if len(files) == 0 {
			files = filesUnderDir(projectDir, req.Files, ".cs")
		}
		projectFiles[project] = files
		for _, group := range parsed.Items {
			for _, ref := range group.ProjectReference {
				refPath := filepath.ToSlash(filepath.Clean(filepath.Join(projectDir, ref.Include)))
				projectRefs[project] = append(projectRefs[project], refPath)
			}
		}
	}

	var edges []core.Edge
	for project, refs := range projectRefs {
		for _, ref := range refs {
			for _, from := range projectFiles[project] {
				for _, to := range projectFiles[ref] {
					if from != to {
						edges = append(edges, nativeImportEdge(from, to, 0.94))
					}
				}
			}
		}
	}
	projectRefCount := len(edges)
	semanticEdges := lexicalSemanticEdges(req.Symbols, map[string]bool{"csharp": true}, 0.93, 0.9)
	edges = append(edges, semanticEdges...)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"loaded " + itoa(len(projects)) + " csproj file(s)",
			"resolved " + itoa(projectRefCount) + " native project-reference edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(semanticEdges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}

func explicitCSharpFiles(projectDir string, project csProject, fileScope map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range project.Items {
		for _, compile := range group.Compile {
			if compile.Include == "" || strings.ContainsAny(compile.Include, "*?") {
				continue
			}
			path := filepath.ToSlash(filepath.Clean(filepath.Join(projectDir, compile.Include)))
			if fileScope[path] && !seen[path] {
				seen[path] = true
				out = append(out, path)
			}
		}
	}
	return out
}

func filesUnderDir(dir string, files []string, ext string) []string {
	if dir == "." {
		dir = ""
	}
	prefix := dir
	if prefix != "" && !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	var out []string
	for _, file := range files {
		if strings.HasPrefix(file, prefix) && strings.EqualFold(filepath.Ext(file), ext) {
			out = append(out, file)
		}
	}
	return out
}
