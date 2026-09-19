package eval

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Swift call-edge ground truth from SourceKit, via the `sourcekitten index`
// command (a thin CLI over the same source.request.index SourceKit request
// that drives Xcode's own indexing). Unlike rust-analyzer/scip-clang's SCIP
// output, this comes back as one JSON entity tree per file: declarations and
// the references inside their bodies are already properly nested, so no
// separate "enclosing span" search is needed — a reference's caller is
// whichever declaration entity encloses it in the tree.
//
// The whole repo is indexed as one flat compiler invocation (every .swift
// file passed as a compiler argument to every other file's index request):
// real multi-target Swift packages are a coarser approximation of this, but
// resolving cross-file/cross-type calls at all requires the whole module's
// files to be visible together, and a flat module is the simplest way to
// get that.

// swiftCallableKinds are the SourceKit declaration/reference kinds that
// denote something actually callable and directly represented by astkit's
// Swift strategy: free functions, methods (instance/static/class),
// constructors ("init"), destructors ("deinit"), and subscripts. Property
// accessors (getter/setter, always synthesized even for a stored property)
// are deliberately excluded — astkit's Swift extraction has no separate
// symbol for them, so treating a "get"/"set" body as a caller/callee would
// score against a Grove decl that structurally cannot exist.
var swiftCallableKinds = map[string]bool{
	"source.lang.swift.decl.function.free":            true,
	"source.lang.swift.decl.function.method.instance": true,
	"source.lang.swift.decl.function.method.static":   true,
	"source.lang.swift.decl.function.method.class":    true,
	"source.lang.swift.decl.function.constructor":     true,
	"source.lang.swift.decl.function.destructor":      true,
	"source.lang.swift.decl.function.subscript":       true,
	"source.lang.swift.ref.function.free":             true,
	"source.lang.swift.ref.function.method.instance":  true,
	"source.lang.swift.ref.function.method.static":    true,
	"source.lang.swift.ref.function.method.class":     true,
	"source.lang.swift.ref.function.constructor":      true,
	"source.lang.swift.ref.function.destructor":       true,
	"source.lang.swift.ref.function.subscript":        true,
}

// swiftTypeKinds are the declaration kinds that open a new qualifying scope
// for display names ("Type.method") — class/struct/enum/protocol/extension.
var swiftTypeKinds = map[string]bool{
	"source.lang.swift.decl.class":            true,
	"source.lang.swift.decl.struct":           true,
	"source.lang.swift.decl.enum":             true,
	"source.lang.swift.decl.protocol":         true,
	"source.lang.swift.decl.extension.class":  true,
	"source.lang.swift.decl.extension.struct": true,
	"source.lang.swift.decl.extension.enum":   true,
}

type swiftEntity struct {
	Kind        string        `json:"key.kind"`
	Name        string        `json:"key.name"`
	USR         string        `json:"key.usr"`
	ReceiverUSR string        `json:"key.receiver_usr"`
	Line        int           `json:"key.line"`
	Entities    []swiftEntity `json:"key.entities"`
}

// SwiftCallTruth indexes every .swift file under repoRoot with SourceKit and
// derives caller→callee edges between in-repo declarations.
func SwiftCallTruth(repoRoot string) (TruthFile, []TruthEdge, error) {
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return TruthFile{}, nil, err
	}
	files, err := swiftDiscoverFiles(root)
	if err != nil {
		return TruthFile{}, nil, err
	}
	if len(files) == 0 {
		return TruthFile{}, nil, fmt.Errorf("no .swift sources under %s", root)
	}
	sdk, err := swiftSDKPath()
	if err != nil {
		return TruthFile{}, nil, err
	}

	trees := make(map[string]swiftEntity, len(files)) // repo-relative file -> root entity
	for _, f := range files {
		rel, err := filepath.Rel(root, f)
		if err != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		out, err := runSourceKittenIndex(f, files, sdk)
		if err != nil {
			return TruthFile{}, nil, fmt.Errorf("sourcekitten index %s: %w", rel, err)
		}
		var top swiftEntity
		if err := json.Unmarshal(out, &top); err != nil {
			return TruthFile{}, nil, fmt.Errorf("parse sourcekitten output for %s: %w", rel, err)
		}
		trees[rel] = top
	}

	decls := map[string]FuncRef{}
	for file, top := range trees {
		for _, e := range top.Entities {
			swiftCollectDecls(e, file, "", decls)
		}
	}

	seen := map[[2]string]bool{}
	var edges []TruthEdge
	for _, top := range trees {
		for _, e := range top.Entities {
			swiftCollectEdges(e, "", decls, seen, &edges)
		}
	}

	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.Caller != b.Caller {
			return a.Caller.funcKey() < b.Caller.funcKey()
		}
		return a.Callee.funcKey() < b.Callee.funcKey()
	})
	header := TruthFile{
		Schema:    "grove-eval/calls/v1",
		Repo:      filepath.Base(root),
		Generator: "sourcekitten-index",
		Functions: len(decls),
		Edges:     len(edges),
	}
	return header, edges, nil
}

// swiftCollectDecls walks one file's entity tree collecting callable
// declarations, tracking the enclosing type name for "Type.method" display.
func swiftCollectDecls(e swiftEntity, file, typeName string, decls map[string]FuncRef) {
	nextType := typeName
	if swiftTypeKinds[e.Kind] && e.Name != "" {
		nextType = e.Name
	}
	if swiftCallableDeclKind(e.Kind) && e.Name != "" && e.USR != "" {
		if _, exists := decls[e.USR]; !exists {
			decls[e.USR] = FuncRef{File: file, Line: e.Line, Name: swiftDisplayName(nextType, e.Name)}
		}
	}
	for _, c := range e.Entities {
		swiftCollectDecls(c, file, nextType, decls)
	}
}

// swiftCollectEdges walks one file's entity tree a second time, attributing
// every callable reference to the nearest enclosing callable declaration —
// exact by construction, since SourceKit already nests body references
// under their declaring entity.
func swiftCollectEdges(e swiftEntity, currentCaller string, decls map[string]FuncRef, seen map[[2]string]bool, edges *[]TruthEdge) {
	nextCaller := currentCaller
	if swiftCallableDeclKind(e.Kind) && e.Name != "" {
		if _, ok := decls[e.USR]; ok {
			nextCaller = e.USR
		}
	}
	if swiftCallableRefKind(e.Kind) && currentCaller != "" && e.USR != currentCaller {
		if callee, ok := decls[e.USR]; ok {
			key := [2]string{currentCaller, e.USR}
			if !seen[key] {
				seen[key] = true
				*edges = append(*edges, TruthEdge{Caller: decls[currentCaller], Callee: callee})
			}
		}
	}
	for _, c := range e.Entities {
		swiftCollectEdges(c, nextCaller, decls, seen, edges)
	}
}

func swiftCallableDeclKind(kind string) bool {
	return swiftCallableKinds[kind] && strings.HasPrefix(kind, "source.lang.swift.decl.")
}

func swiftCallableRefKind(kind string) bool {
	return swiftCallableKinds[kind] && strings.HasPrefix(kind, "source.lang.swift.ref.")
}

// swiftDisplayName strips parameter labels ("greet()" -> "greet",
// "init(x:y:)" -> "init") and prefixes the enclosing type, if any.
func swiftDisplayName(typeName, raw string) string {
	name := raw
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	if name == "init" && typeName != "" {
		// astkit names every Swift constructor after its enclosing type
		// (Person(...), never init(...), is what a caller actually
		// writes) — matched here so matchDecls's base-name comparison
		// agrees with what Grove records.
		name = typeName
	}
	if typeName == "" {
		return name
	}
	return typeName + "." + name
}

// swiftDiscoverFiles walks repoRoot for .swift files, skipping build output
// and dependency checkouts a plain checkout never contains.
func swiftDiscoverFiles(root string) ([]string, error) {
	skip := map[string]bool{
		".build": true, ".swiftpm": true, "Pods": true, "Carthage": true,
		"DerivedData": true, ".git": true,
	}
	var files []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".swift") {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

// swiftSDKPath returns the active Xcode/Command Line Tools SDK path.
func swiftSDKPath() (string, error) {
	if env := os.Getenv("GROVE_EVAL_SWIFT_SDK"); env != "" {
		return env, nil
	}
	out, err := exec.Command("xcrun", "--show-sdk-path").Output()
	if err != nil {
		return "", fmt.Errorf("xcrun --show-sdk-path: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// runSourceKittenIndex indexes one file with every file in the module
// visible as a compiler argument, so cross-file references resolve.
// sourcekitten's default framework search misses a Command Line
// Tools–only install's sourcekitdInProc.framework (it only checks
// Xcode.app's own toolchain paths and a couple of standard locations) — the
// first attempt runs unmodified so it works unchanged on a full Xcode
// install, and only on that specific load failure do we retry with
// DYLD_FRAMEWORK_PATH pointed at the active developer directory.
func runSourceKittenIndex(file string, allFiles []string, sdk string) ([]byte, error) {
	args := []string{"index", "--file", file, "--"}
	args = append(args, allFiles...)
	args = append(args, "-sdk", sdk)

	run := func(extraEnv []string) ([]byte, string, error) {
		cmd := exec.Command("sourcekitten", args...)
		if len(extraEnv) > 0 {
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Run()
		return []byte(stdout.String()), stderr.String(), err
	}

	out, errOut, err := run(nil)
	if err != nil && runtime.GOOS == "darwin" && strings.Contains(errOut, "sourcekitdInProc.framework") {
		if devDir, derr := exec.Command("xcode-select", "-p").Output(); derr == nil {
			fwPath := filepath.Join(strings.TrimSpace(string(devDir)), "usr", "lib")
			if st, serr := os.Stat(fwPath); serr == nil && st.IsDir() {
				out, errOut, err = run([]string{"DYLD_FRAMEWORK_PATH=" + fwPath})
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("%w\n%s", err, tail([]byte(errOut)))
	}
	return out, nil
}
