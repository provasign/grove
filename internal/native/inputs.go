package native

import (
	"path"
	"strings"
)

// Compiler inputs: build-configuration files an analyzer's result depends on
// besides the source files themselves. Grove indexes most of them as
// plaintext (tsconfig.json, pom.xml) or not at all (go.mod, go.sum), so a
// change to one never touched the analyzer's languages and the analyzer was
// skipped with its previous edges carried forward — after a tsconfig "paths"
// or module-resolution edit the graph kept the old import resolution (stack
// audit 2026-10-20 #2). The indexer fingerprints these files and treats a
// change as invalidating the analyzer's completed pass.
//
// Matching is by base name, anywhere in the walked tree (nested modules and
// workspace packages have their own). Each list names what the analyzer or
// its toolchain reads: module/workspace files for go list and go/types,
// tsconfig/jsconfig and package.json ("types", "exports", "imports",
// workspaces) for the TypeScript program, the build files javac's classpath
// is resolved from (the same list as the Java classpath cache key), Cargo
// manifests, compile_commands.json, project files, and the files whose
// presence decides availability (pyproject.toml, composer.json).

// CompilerInputAnalyzers returns the analyzers (by Analyzer.Name) whose
// compiler inputs include the slash-separated relPath, or nil.
func CompilerInputAnalyzers(relPath string) []string {
	base := path.Base(relPath)
	switch base {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return []string{"go"}
	case "package.json":
		return []string{"js-ts"}
	case "pyproject.toml", "setup.cfg", "requirements.txt":
		return []string{"python"}
	case "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle", "settings.gradle.kts",
		"gradle.properties", "libs.versions.toml":
		return []string{"java"}
	case "Cargo.toml", "Cargo.lock":
		return []string{"rust"}
	case "compile_commands.json":
		return []string{"cfamily"}
	case "Directory.Build.props":
		return []string{"csharp"}
	case "composer.json":
		return []string{"php"}
	}
	if strings.HasSuffix(base, ".json") && (strings.HasPrefix(base, "tsconfig") || strings.HasPrefix(base, "jsconfig")) {
		return []string{"js-ts"} // tsconfig.base.json, tsconfig.build.json, ...
	}
	if strings.HasSuffix(base, ".csproj") {
		return []string{"csharp"}
	}
	return nil
}

// InvalidateResident stops the resident worker processes that hold compiler
// state (parsed programs, a loaded workspace) of the named analyzers for
// root, so the next run starts from the changed configuration instead of
// reusing state built under the old one. Analyzers without resident state
// are ignored.
func InvalidateResident(root string, analyzers []string) {
	for _, name := range analyzers {
		switch name {
		case "js-ts":
			stopResidentWorkers(root, "ts\x00")
		case "java":
			stopResidentWorkers(root, "java\x00")
		case "rust":
			stopRustLSP(root)
		}
	}
}
