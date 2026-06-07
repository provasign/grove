package native

import (
	"context"

	"github.com/provasign/grove/internal/core"
)

type javaAnalyzer struct{}

func (javaAnalyzer) Name() string { return "java" }

func (javaAnalyzer) Languages() []string { return []string{"java"} }

func (javaAnalyzer) Available(_ context.Context, root string) Availability {
	if !anyFile(root, "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts") {
		return Availability{Reason: "no Maven or Gradle project config"}
	}
	if firstExistingExecutable("jdtls", "mvn", "gradle") == "" {
		return Availability{Reason: "jdtls, mvn, or gradle executable not found"}
	}
	return Availability{Available: true}
}

func (javaAnalyzer) Analyze(ctx context.Context, req Request) Result {
	_ = ctx
	edges := lexicalSemanticEdges(req.Symbols, map[string]bool{"java": true}, 0.93, 0.9)
	return Result{
		Edges: edges,
		Diagnostics: []string{
			"project tooling detected",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeCalls)) + " native call edge(s)",
			"resolved " + itoa(countNativeEdges(edges, core.EdgeUsesType)) + " native type-use edge(s)",
		},
	}
}
