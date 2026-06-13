package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// edgeIndex holds per-build symbol indexes used by the edge constructors.
type edgeIndex struct {
	byName map[string][]*core.SymbolRecord // lowercase name → symbols
	byFile map[string][]*core.SymbolRecord
	byID   map[string]*core.SymbolRecord

	// fileImports maps filePath → set of import strings declared in that file.
	// We pick the union over all symbols in the file since the parser sets
	// Imports per-symbol from the same file-level import list.
	fileImports map[string]map[string]struct{}

	// dirToFiles maps directory → []filePath for O(1) Go same-package lookup.
	// Without this index, importedFiles would scan byFile (O(n)) for every
	// file in the same directory, yielding O(n²) total for a repo with many
	// same-directory files (e.g. a 50-file package).
	dirToFiles map[string][]string

	// dirFilesLower / dirFilesByBase support O(import-depth) resolution of
	// package/directory imports: a dir matches an import when the import
	// path equals it or ends with "/"+dir (looked up via the import's
	// slash-suffixes), or when the dir's last segment equals the import's
	// last segment. The previous implementation scanned every directory per
	// import — ~0.5 billion string ops on a 19k-file monorepo.
	dirFilesLower  map[string][]string
	dirFilesByBase map[string][]string

	// importPathToFiles maps a slash-separated import path without extension to
	// files whose path matches that import exactly or by package directory.
	importPathToFiles map[string][]string

	// baseToFiles maps lowercase basename without extension to files.
	baseToFiles map[string][]string

	// importedFilesCache memoizes the result of importedFiles() per file.
	importedFilesCache map[string]map[string]struct{}

	// Rust crate topology: visibility in Rust is crate-wide (any item is
	// reachable through crate:: paths without a per-file use), so scope is
	// the enclosing crate plus used workspace crates. A crate root is a
	// directory holding lib.rs or main.rs; files attach to the nearest
	// root above them.
	rustCrateOfFile map[string]string   // .rs file → crate root dir
	rustCrateFiles  map[string][]string // crate root dir → files under it
	rustCrateByName map[string]string   // normalized crate name → root dir
}

func newEdgeIndex(symbols []core.SymbolRecord) *edgeIndex {
	idx := &edgeIndex{
		byName:             make(map[string][]*core.SymbolRecord),
		byFile:             make(map[string][]*core.SymbolRecord),
		byID:               make(map[string]*core.SymbolRecord),
		fileImports:        make(map[string]map[string]struct{}),
		dirToFiles:         make(map[string][]string),
		dirFilesLower:      make(map[string][]string),
		dirFilesByBase:     make(map[string][]string),
		importPathToFiles:  make(map[string][]string),
		baseToFiles:        make(map[string][]string),
		importedFilesCache: make(map[string]map[string]struct{}),
	}
	for i := range symbols {
		s := &symbols[i]
		idx.byID[s.ID] = s
		idx.byName[strings.ToLower(s.Name)] = append(idx.byName[strings.ToLower(s.Name)], s)
		idx.byFile[s.FilePath] = append(idx.byFile[s.FilePath], s)
		if _, ok := idx.fileImports[s.FilePath]; !ok {
			idx.fileImports[s.FilePath] = make(map[string]struct{})
		}
		for _, imp := range s.Imports {
			idx.fileImports[s.FilePath][imp] = struct{}{}
		}
	}
	// Build dirToFiles after byFile is populated so each directory maps to
	// all its files in one pass (O(n) total, vs O(n) per-file scan later).
	for f := range idx.byFile {
		d := dirOf(f)
		idx.dirToFiles[d] = append(idx.dirToFiles[d], f)
		if dLower := strings.ToLower(d); dLower != "" && dLower != "." {
			idx.dirFilesLower[dLower] = append(idx.dirFilesLower[dLower], f)
			idx.dirFilesByBase[baseOf(dLower)] = append(idx.dirFilesByBase[baseOf(dLower)], f)
		}
		idx.importPathToFiles[strings.ToLower(trimExt(f))] = append(idx.importPathToFiles[strings.ToLower(trimExt(f))], f)
		idx.baseToFiles[strings.ToLower(baseNameNoExt(f))] = append(idx.baseToFiles[strings.ToLower(baseNameNoExt(f))], f)
	}
	idx.buildRustCrates()
	return idx
}

// buildRustCrates derives the crate topology from file layout alone: every
// directory containing lib.rs or main.rs roots a crate; each .rs file
// belongs to the nearest root above it (its own directory when no root
// exists, e.g. integration-test targets). The crate's referenceable name is
// the root's directory name — or its parent's for src/ layouts — registered
// with hyphens normalized to underscores plus its last underscore token,
// because package names commonly prefix the directory name (grep-searcher
// lives in crates/searcher).
func (idx *edgeIndex) buildRustCrates() {
	roots := map[string]bool{}
	for f := range idx.byFile {
		if !strings.HasSuffix(f, ".rs") {
			continue
		}
		if base := baseNameNoExt(f); base == "lib" || base == "main" {
			roots[dirOf(f)] = true
		}
	}
	if len(roots) == 0 {
		return
	}
	idx.rustCrateOfFile = map[string]string{}
	idx.rustCrateFiles = map[string][]string{}
	idx.rustCrateByName = map[string]string{}
	for f := range idx.byFile {
		if !strings.HasSuffix(f, ".rs") {
			continue
		}
		root := dirOf(f)
		for d := root; ; {
			if roots[d] {
				root = d
				break
			}
			parent := dirOf(d)
			if parent == d || parent == "." || parent == "" {
				break
			}
			d = parent
		}
		idx.rustCrateOfFile[f] = root
		idx.rustCrateFiles[root] = append(idx.rustCrateFiles[root], f)
	}
	crateName := func(root string) string {
		name := baseOf(root)
		if name == "src" {
			name = baseOf(dirOf(root))
		}
		return strings.ToLower(strings.ReplaceAll(name, "-", "_"))
	}
	for root := range roots {
		if name := crateName(root); name != "" && name != "." {
			idx.rustCrateByName[name] = root
		}
	}
	// Token aliases never displace an exact crate name.
	for root := range roots {
		name := crateName(root)
		if i := strings.LastIndexByte(name, '_'); i >= 0 && i+1 < len(name) {
			if tok := name[i+1:]; idx.rustCrateByName[tok] == "" {
				idx.rustCrateByName[tok] = root
			}
		}
	}
}

// importedFiles returns the set of file paths that are reachable through
// the import declarations of fromFile. Resolution is heuristic: an import
// string matches a candidate file when the candidate's path or basename
// shares the import's last path segment. Always includes fromFile itself.
func (idx *edgeIndex) importedFiles(fromFile string) map[string]struct{} {
	if cached, ok := idx.importedFilesCache[fromFile]; ok {
		return cached
	}
	out := map[string]struct{}{fromFile: {}}

	// C#: `using` imports a namespace, not a file, and namespaces don't map
	// to directories — so file-level import resolution can't see the target.
	// Within one assembly every type is mutually visible, so scope is the
	// whole repo; precision is held by type narrowing (qualified calls must
	// resolve to a known type or an inferable local — see the csharp static
	// block in buildCalls), not by scope.
	if lang := fileLanguage(idx, fromFile); lang == "csharp" || lang == "php" || lang == "c" || lang == "cpp" {
		// C#/PHP resolve types through namespace imports (`using`/`use`),
		// which don't map to directories; within one library every type is
		// mutually visible, so scope is the whole repo and precision is held
		// by type narrowing (the static block in buildCalls), not by scope.
		for f := range idx.byFile {
			out[f] = struct{}{}
		}
		idx.importedFilesCache[fromFile] = out
		return out
	}

	// Same-package scope (Go only): a Go file does not import its own package,
	// yet calls between files in the same directory are extremely common
	// (compressor.go ↔ compressor_test.go, split implementation files). In Go a
	// directory IS a package, so every file sharing fromFile's directory is in
	// scope. This is NOT true for TS/JS/Java/Python, where imports are always
	// explicit per file regardless of directory — so we gate on language to
	// avoid linking unrelated same-folder modules there.
	lang := fileLanguage(idx, fromFile)
	if lang == "go" || lang == "java" {
		// Go: a directory IS a package. Java: a directory is a package too —
		// same-package classes need no import.
		fromDir := dirOf(fromFile)
		for _, f := range idx.dirToFiles[fromDir] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}
	}
	if lang == "rust" && idx.rustCrateOfFile != nil {
		// Rust visibility is crate-wide: crate::/super:: paths reach any
		// sibling module with no per-file use declaration. Scope is the
		// whole enclosing crate plus used workspace crates — transitively,
		// because facade crates re-export their dependencies (ripgrep's
		// core uses grep::printer::..., where crates/grep is a shim over
		// grep-printer) and paths through a re-export reach the underlying
		// crate's items directly.
		ownRoot := idx.rustCrateOfFile[fromFile]
		visited := map[string]bool{}
		queue := []string{ownRoot}
		for len(queue) > 0 {
			root := queue[0]
			queue = queue[1:]
			if visited[root] {
				continue
			}
			visited[root] = true
			for _, f := range idx.rustCrateFiles[root] {
				if f != fromFile {
					out[f] = struct{}{}
				}
				for imp := range idx.fileImports[f] {
					if root != ownRoot && !strings.HasPrefix(imp, "pub ") {
						// Any own-crate import is dependency evidence (the
						// extern prelude names deps crate-wide), but other
						// crates extend reachability only through pub use
						// re-export chains.
						continue
					}
					seg := strings.TrimPrefix(imp, "pub ")
					seg = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(seg), "use "))
					if i := strings.Index(seg, "::"); i >= 0 {
						seg = seg[:i]
					}
					// "pub use grep_printer as printer" — the crate name
					// ends at the first space.
					if i := strings.IndexByte(seg, ' '); i >= 0 {
						seg = seg[:i]
					}
					seg = strings.ToLower(strings.TrimSpace(seg))
					switch seg {
					case "", "crate", "super", "self", "std", "core", "alloc":
						continue
					}
					crateRoot, ok := idx.rustCrateByName[seg]
					if !ok {
						// Package names commonly prefix the directory name
						// (use grep_searcher → crates/searcher): retry on
						// the path's last underscore token.
						if i := strings.LastIndexByte(seg, '_'); i >= 0 && i+1 < len(seg) {
							crateRoot, ok = idx.rustCrateByName[seg[i+1:]]
						}
					}
					if ok && !visited[crateRoot] {
						queue = append(queue, crateRoot)
					}
				}
			}
		}
		idx.importedFilesCache[fromFile] = out
		return out
	}

	imports, ok := idx.fileImports[fromFile]
	if !ok {
		idx.importedFilesCache[fromFile] = out
		return out
	}
	for imp := range imports {
		raw := strings.Trim(imp, "\"' ;")
		// Relative imports name one specific file or directory: resolve them
		// against the importing file's location and skip fuzzy matching —
		// basename fallback would pull every same-named file in a monorepo
		// ("./socket" matching all socket.ts files) into scope.
		if strings.HasPrefix(raw, "./") || strings.HasPrefix(raw, "../") {
			if resolved := idx.resolveRelativeImport(fromFile, raw); len(resolved) > 0 {
				for _, f := range resolved {
					if f != fromFile {
						out[f] = struct{}{}
					}
				}
				continue
			}
		}
		impNorm := strings.ToLower(strings.Trim(imp, "\"' ;"))
		impNorm = strings.TrimPrefix(impNorm, "./")
		impNorm = strings.TrimSuffix(impNorm, ".go")
		impNorm = strings.TrimSuffix(impNorm, ".py")
		impNorm = strings.TrimSuffix(impNorm, ".ts")
		impNorm = strings.TrimSuffix(impNorm, ".tsx")
		impNorm = strings.TrimSuffix(impNorm, ".js")
		impNorm = strings.TrimSuffix(impNorm, ".jsx")
		impNorm = strings.TrimSuffix(impNorm, ".java")
		impNorm = strings.TrimSuffix(impNorm, ".rs")

		// Last path segment of the import (e.g., "lodash/fp" → "fp",
		// "./auth" → "auth", "fmt" → "fmt", "com.example.Auth" → "Auth").
		seg := lastImportSegment(imp)
		if seg == "" {
			continue
		}
		segLower := strings.ToLower(seg)

		// Fast path: direct file-path match (e.g. relative imports, or same-depth imports
		// where the import string matches the file path exactly after extension strip).
		for _, f := range idx.importPathToFiles[impNorm] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}

		// (1) Package / directory imports — the common case for Go, Rust,
		// and Python, where one import names a DIRECTORY and pulls in every
		// file under it. A directory matches when the (module-prefixed)
		// import path equals it or ends with "/"+dir — i.e. when one of the
		// import's slash-suffixes equals the dir — or when the dir's last
		// segment equals the import's last segment. Suffix lookups make this
		// O(import-depth) instead of a scan over every directory, which on a
		// 19k-file monorepo was ~0.5 billion string comparisons per index.
		parts := strings.Split(impNorm, "/")
		for i := range parts {
			suffix := strings.Join(parts[i:], "/")
			if suffix == "" || suffix == "." {
				continue
			}
			for _, f := range idx.dirFilesLower[suffix] {
				if f != fromFile {
					out[f] = struct{}{}
				}
			}
		}
		for _, f := range idx.dirFilesByBase[segLower] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}

		// (2) File-name imports — e.g. a JS/TS relative import "./auth"
		// resolving to "auth.ts".
		for _, c := range idx.baseToFiles[segLower] {
			if c == fromFile {
				continue
			}
			lower := strings.ToLower(c)
			base := strings.ToLower(baseNameNoExt(c))
			if base == segLower || strings.HasSuffix(lower, "/"+segLower) ||
				strings.HasSuffix(lower, "/"+segLower+".go") ||
				strings.HasSuffix(lower, "/"+segLower+".py") ||
				strings.HasSuffix(lower, "/"+segLower+".ts") ||
				strings.HasSuffix(lower, "/"+segLower+".tsx") ||
				strings.HasSuffix(lower, "/"+segLower+".js") ||
				strings.HasSuffix(lower, "/"+segLower+".jsx") ||
				strings.HasSuffix(lower, "/"+segLower+".java") ||
				strings.HasSuffix(lower, "/"+segLower+".rs") {
				out[c] = struct{}{}
			}
		}
	}
	idx.importedFilesCache[fromFile] = out
	return out
}

// resolveRelativeImport maps "./socket" / "../parser/index.js" to concrete
// indexed files: exact file (with source-extension probing), or a directory
// (returning its files).
func (idx *edgeIndex) resolveRelativeImport(fromFile, raw string) []string {
	base := dirOf(fromFile)
	joined := strings.ToLower(pathJoin(base, raw))
	var out []string
	if files, ok := idx.dirFilesLower[joined]; ok {
		out = append(out, files...)
	}
	out = append(out, idx.importPathToFiles[joined]...)
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mts", ".cts", ".mjs", ".cjs", ".py", ".rs"} {
		out = append(out, idx.importPathToFiles[strings.TrimSuffix(joined, ext)]...)
	}
	// index-file convention: "./parser" → parser/index.ts
	out = append(out, idx.importPathToFiles[joined+"/index"]...)
	return out
}

// pathJoin resolves "." and ".." segments without touching the filesystem.
func pathJoin(base, rel string) string {
	segs := []string{}
	if base != "" && base != "." {
		segs = strings.Split(base, "/")
	}
	for _, s := range strings.Split(rel, "/") {
		switch s {
		case "", ".":
		case "..":
			if len(segs) > 0 {
				segs = segs[:len(segs)-1]
			}
		default:
			segs = append(segs, s)
		}
	}
	return strings.Join(segs, "/")
}

func lastImportSegment(imp string) string {
	imp = strings.Trim(imp, "\"' ;")
	// Java: dot-separated; everything else: slash-separated.
	if strings.Contains(imp, "/") {
		parts := strings.Split(imp, "/")
		return parts[len(parts)-1]
	}
	if strings.Contains(imp, ".") {
		parts := strings.Split(imp, ".")
		return parts[len(parts)-1]
	}
	return imp
}

// fileLanguage returns the language recorded for any symbol in fromFile.
func fileLanguage(idx *edgeIndex, fromFile string) string {
	for _, s := range idx.byFile[fromFile] {
		if s.Language != "" {
			return strings.ToLower(s.Language)
		}
	}
	return ""
}

// dirOf returns the directory portion of a slash-separated file path
// ("internal/ranking/budget.go" → "internal/ranking"; "main.go" → "").
func dirOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[:i]
	}
	return ""
}

// baseOf returns the last segment of a slash-separated path
// ("internal/ranking" → "ranking").
func baseOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func baseNameNoExt(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		path = path[i+1:]
	}
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		path = path[:i]
	}
	return path
}

func trimExt(path string) string {
	if i := strings.LastIndexByte(path, '.'); i > 0 {
		return path[:i]
	}
	return path
}

// stripCommentsAndStrings removes // line comments, /* */ block comments,
// # python comments, and string literals from a source body so that
// regex-based call matching does not produce false positives.
func stripCommentsAndStrings(src string) string {
	var out strings.Builder
	out.Grow(len(src))
	i, n := 0, len(src)
	for i < n {
		ch := src[i]
		// Block comment
		if ch == '/' && i+1 < n && src[i+1] == '*' {
			end := strings.Index(src[i+2:], "*/")
			if end < 0 {
				return out.String()
			}
			i += end + 4
			continue
		}
		// Line comment // and #
		if (ch == '/' && i+1 < n && src[i+1] == '/') || ch == '#' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// String literal — preserve newlines so call matching keeps line layout.
		if ch == '"' || ch == '\'' || ch == '`' {
			quote := ch
			out.WriteByte(' ')
			i++
			for i < n && src[i] != quote {
				if src[i] == '\\' && i+1 < n {
					i += 2
					continue
				}
				if src[i] == '\n' {
					out.WriteByte('\n')
				}
				i++
			}
			if i < n {
				i++ // closing quote
			}
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// buildDefinesAndImports emits "file → symbol" defines edges and
// deduplicated "file → import:path" imports edges.
func buildDefinesAndImports(symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seenImports := make(map[string]bool)
	seenFiles := make(map[string]bool)
	for _, symbol := range symbols {
		fileNode := "file:" + symbol.FilePath
		edges = append(edges, core.Edge{
			From:       fileNode,
			To:         symbol.ID,
			Type:       core.EdgeDefines,
			Confidence: 1.0,
			Source:     core.EvidenceSourceASTKit,
		})
		if seenFiles[symbol.FilePath] {
			continue
		}
		seenFiles[symbol.FilePath] = true
		for _, imp := range symbol.Imports {
			key := fileNode + "::import:" + imp
			if seenImports[key] {
				continue
			}
			seenImports[key] = true
			edges = append(edges, core.Edge{
				From:       fileNode,
				To:         "import:" + imp,
				Type:       core.EdgeImports,
				Confidence: 0.9,
				Source:     core.EvidenceSourceASTKit,
			})
		}
	}
	return edges
}

// buildContains emits parent-symbol → child-symbol edges.
func buildContains(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		if symbol.ParentSymbol == "" {
			continue
		}
		for _, parent := range idx.byName[strings.ToLower(symbol.ParentSymbol)] {
			if parent.FilePath != symbol.FilePath {
				continue
			}
			if parent.Kind != core.KindStruct && parent.Kind != core.KindClass &&
				parent.Kind != core.KindInterface && parent.Kind != core.KindTrait {
				continue
			}
			edges = append(edges, core.Edge{
				From:       parent.ID,
				To:         symbol.ID,
				Type:       core.EdgeContains,
				Confidence: 1.0,
				Source:     core.EvidenceSourceASTKit,
			})
		}
	}
	return edges
}

// extendsRe / implementsRe match the inheritance clauses of class/interface
// declarations across JS/TS/Java. Python uses parenthesized base classes.
var (
	extendsRe       = regexp.MustCompile(`\bextends\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	implementsRe    = regexp.MustCompile(`\bimplements\s+([A-Za-z_][A-Za-z0-9_.]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_.]*)*)`)
	pythonClassBase = regexp.MustCompile(`^\s*class\s+[A-Za-z_][A-Za-z0-9_]*\s*\(([^)]+)\)`)
	rustImplForRe   = regexp.MustCompile(`\bimpl\s+(?:<[^>]+>\s+)?([A-Za-z_][A-Za-z0-9_:]*)\s+for\s+([A-Za-z_][A-Za-z0-9_:]*)`)
	usesTypeIdent   = regexp.MustCompile(`\b([A-Z][A-Za-z0-9_]+)\b`)
)

// buildExtendsImplements emits inheritance edges. It reads the symbol's
// Signature (and RawText for Python/Rust where the signature is sparse) to
// detect parent classes, implemented interfaces, and trait impls.
func buildExtendsImplements(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	for _, symbol := range symbols {
		switch symbol.Language {
		case "typescript", "tsx", "javascript", "java":
			if symbol.Kind != core.KindClass && symbol.Kind != core.KindInterface {
				continue
			}
			text := symbol.Signature
			if text == "" {
				text = firstLine(symbol.RawText)
			}
			for _, name := range matchNameList(extendsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.85)...)
			}
			for _, name := range matchNameList(implementsRe, text) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeImplements, 0.85)...)
			}
		case "python":
			if symbol.Kind != core.KindClass {
				continue
			}
			line := firstLine(symbol.RawText)
			matches := pythonClassBase.FindStringSubmatch(line)
			if len(matches) < 2 {
				continue
			}
			for _, base := range splitTrim(matches[1], ',') {
				base = stripPythonBase(base)
				if base == "" {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, base, core.EdgeExtends, 0.85)...)
			}
		case "rust":
			// Rust uses `impl Trait for Type` to implement traits; we attach
			// the implements edge to the *type* symbol.
			if symbol.Kind != core.KindStruct && symbol.Kind != core.KindEnum {
				continue
			}
			body := symbol.RawText
			matches := rustImplForRe.FindAllStringSubmatch(body, -1)
			for _, m := range matches {
				traitName, typeName := m[1], m[2]
				if typeName != symbol.Name {
					continue
				}
				edges = append(edges, resolveTypeEdges(idx, symbol, traitName, core.EdgeImplements, 0.85)...)
			}
		case "go":
			// Go has structural interface satisfaction; emitting implements
			// edges accurately requires interface-method matching across the
			// graph. We skip for v0.1; struct embedding (extends) is detected
			// from RawText below.
			if symbol.Kind != core.KindStruct {
				continue
			}
			for _, name := range goEmbeddedTypes(symbol.RawText) {
				edges = append(edges, resolveTypeEdges(idx, symbol, name, core.EdgeExtends, 0.7)...)
			}
		}
	}
	return edges
}

// buildUsesType emits uses-type edges from a symbol's signature, scoped to
// same-file and imported-file symbols (per Implementation Plan). The "to"
// side of each edge is a concrete symbol ID when resolvable.
func buildUsesType(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	var edges []core.Edge
	seen := make(map[string]bool)
	for _, symbol := range symbols {
		if symbol.Signature == "" {
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)
		matches := usesTypeIdent.FindAllStringSubmatch(symbol.Signature, -1)
		for _, m := range matches {
			candidateName := m[1]
			if candidateName == symbol.Name {
				continue
			}
			for _, target := range idx.byName[strings.ToLower(candidateName)] {
				if _, inScope := scope[target.FilePath]; !inScope {
					continue
				}
				key := symbol.ID + "::uses-type::" + target.ID
				if seen[key] {
					continue
				}
				seen[key] = true
				edges = append(edges, core.Edge{
					From:       symbol.ID,
					To:         target.ID,
					Type:       core.EdgeUsesType,
					Confidence: 0.5,
					Source:     core.EvidenceSourceHeuristic,
				})
			}
		}
	}
	return edges
}

// buildTests emits test → target edges. Two evidence sources:
//
//  1. Naming conventions (TestFoo → Foo, test_foo → foo, FooTest → Foo),
//     scoped to the test file's import graph — an unscoped name match would
//     link TestOpen to every Open in the repository, inflating the test
//     evidence that certification relies on.
//  2. AST call sites: an unqualified call from a test body to a callable in
//     scope is direct evidence the test exercises it. This also covers tests
//     that don't follow naming conventions (Rust #[test] fns, table tests).
//
// Symbols count as tests if they live in a test file or carry a test
// annotation (Rust #[test], JUnit @Test, xUnit [Fact], …) — the latter allows
// same-file targets because such tests live alongside production code.
func buildTests(idx *edgeIndex, symbols []core.SymbolRecord, callEdges []core.Edge) []core.Edge {
	callAdj := map[string][]string{}
	for _, e := range callEdges {
		if e.Type == core.EdgeCalls {
			callAdj[e.From] = append(callAdj[e.From], e.To)
		}
	}
	var edges []core.Edge
	seen := make(map[string]bool)
	for i := range symbols {
		symbol := &symbols[i]
		inTestFile := isTestFile(symbol.FilePath)
		annotated := core.HasTestAnnotation(symbol)
		if !inTestFile && !annotated {
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)
		add := func(target *core.SymbolRecord, allowSameFile bool, confidence float64) {
			if target.ID == symbol.ID {
				return
			}
			if !allowSameFile && target.FilePath == symbol.FilePath {
				return
			}
			if isTestFile(target.FilePath) {
				return
			}
			if _, ok := scope[target.FilePath]; !ok {
				return
			}
			key := symbol.ID + "::tests::" + target.ID
			if seen[key] {
				return
			}
			seen[key] = true
			edges = append(edges, core.Edge{
				From:       symbol.ID,
				To:         target.ID,
				Type:       core.EdgeTests,
				Confidence: confidence,
				Source:     core.EvidenceSourceHeuristic,
			})
		}

		for _, target := range testTargets(*symbol, idx) {
			add(target, annotated, 0.8)
		}

		// Call-graph evidence: the calls edges were resolved with the full
		// narrowing machinery (receiver, local types, imports, dispatch), so
		// they are the authoritative record of what a test invokes. Walk from
		// the test through same-test-file helpers (fixtures, builders) to the
		// production symbols they reach — that's how real suites exercise
		// code. Production symbols terminate the walk: full transitive
		// closure would relate every test to everything.
		type hop struct {
			id        string
			depth     int // hops through test-file helpers
			prodDepth int // hops past the first production symbol
		}
		const maxHelperDepth = 3
		const maxProdDepth = 1
		visited := map[string]bool{symbol.ID: true}
		queue := []hop{{symbol.ID, 0, 0}}
		confByDepth := [maxHelperDepth + 1]float64{0.85, 0.75, 0.65, 0.6}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, toID := range callAdj[cur.id] {
				if visited[toID] {
					continue
				}
				visited[toID] = true
				target := idx.byID[toID]
				if target == nil {
					continue
				}
				if isTestFile(target.FilePath) {
					if cur.prodDepth == 0 && cur.depth < maxHelperDepth {
						queue = append(queue, hop{toID, cur.depth + 1, 0})
					}
					continue
				}
				if cur.prodDepth == 0 {
					add(target, true, confByDepth[cur.depth])
				} else {
					// One hop past the entry point: what the called function
					// immediately does is still review-relevant, at low
					// confidence.
					add(target, true, 0.55)
				}
				if cur.prodDepth < maxProdDepth {
					queue = append(queue, hop{toID, cur.depth, cur.prodDepth + 1})
				}
			}
		}
	}
	return edges
}

func testTargets(symbol core.SymbolRecord, idx *edgeIndex) []*core.SymbolRecord {
	name := symbol.Name
	var candidates []string
	switch {
	case strings.HasPrefix(name, "Test"):
		candidates = append(candidates, strings.TrimPrefix(name, "Test"))
	case strings.HasPrefix(name, "test_"):
		candidates = append(candidates, strings.TrimPrefix(name, "test_"))
	case strings.HasSuffix(name, "Test"):
		candidates = append(candidates, strings.TrimSuffix(name, "Test"))
	case strings.HasSuffix(name, "Spec"):
		candidates = append(candidates, strings.TrimSuffix(name, "Spec"))
	}
	var out []*core.SymbolRecord
	for _, c := range candidates {
		if c == "" {
			continue
		}
		out = append(out, idx.byName[strings.ToLower(c)]...)
	}
	return out
}

// callIdentRe extracts call-shaped identifiers ("name(") from a stripped
// body in a single pass.
var callIdentRe = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)

// maxCalleeFanout bounds how many cross-file targets a single callee name
// may resolve to. When a bare name matches more candidates than this, the
// reference is ambiguous (typically generated or templated code repeating
// one name across many files); emitting an edge to every candidate carries
// no signal and inflates Impact blast radii quadratically.
const maxCalleeFanout = 16

// resolveCallees resolves a bare callee name within symbol's import scope.
// A same-file match wins outright: in every supported language a local
// definition shadows imported ones. Cross-file matches are returned only
// when unambiguous enough to be meaningful.
func resolveCallees(idx *edgeIndex, symbol *core.SymbolRecord, calleeName string, scope map[string]struct{}, exactCase bool) ([]*core.SymbolRecord, bool) {
	var sameFile, crossFile []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(calleeName)] {
		if cand.ID == symbol.ID {
			continue
		}
		if exactCase && cand.Name != calleeName {
			continue
		}
		if cand.Kind != core.KindFunction && cand.Kind != core.KindMethod && cand.Kind != core.KindConstructor {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		if cand.FilePath == symbol.FilePath {
			sameFile = append(sameFile, cand)
		} else {
			crossFile = append(crossFile, cand)
		}
	}
	if len(sameFile) > 0 {
		return sameFile, false
	}
	if len(crossFile) > maxCalleeFanout {
		// Over the fan-out cap — but narrowing may still pin these down, so
		// return them with the flag; the caller caps AFTER narrowing.
		return crossFile, true
	}
	return crossFile, false
}

// buildCalls emits same-file + imported-file call edges with strings/comments
// stripped from the body before matching.
//
// The fallback path extracts every call-shaped identifier from the body in
// one pass and resolves it through the name index. The previous
// implementation matched a per-callable compiled regex against every other
// callable's body — O(callables²) regex scans, which on a 10K-symbol
// single-package corpus took ~40s.
func buildCalls(idx *edgeIndex, symbols []core.SymbolRecord, sat *interfaceSatisfaction) []core.Edge {
	var edges []core.Edge
	seen := make(map[string]bool)

	addEdge := func(fromID, toID string, confidence float64, source core.EvidenceSource) {
		key := fromID + "::calls::" + toID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: fromID, To: toID,
			Type: core.EdgeCalls, Confidence: confidence, Source: source,
		})
	}

	for _, symbol := range symbols {
		if symbol.Kind != core.KindFunction && symbol.Kind != core.KindMethod && symbol.Kind != core.KindConstructor {
			continue
		}
		scope := idx.importedFiles(symbol.FilePath)

		// ── Property reads (AST-extracted AttrSites) ────────────────────────
		// An attribute access ("request.blueprints") executes @property code
		// with no call syntax. Resolve strictly against property-annotated
		// methods so plain field reads never produce edges. Independent of
		// CallSites: a function may only read properties.
		if len(symbol.AttrSites) > 0 {
			attrSelfVars := callerSelfQualifiers(&symbol)
			for _, as := range symbol.AttrSites {
				name := as.Callee
				qualifier := ""
				if idx := strings.LastIndexByte(name, '.'); idx >= 0 {
					qualifier = name[:idx]
					name = name[idx+1:]
				}
				if j := strings.LastIndexByte(qualifier, '.'); j >= 0 {
					qualifier = qualifier[j+1:]
				}
				if name == "" {
					continue
				}
				cands := resolvePropertyTargets(idx, &symbol, name, scope)
				cands = narrowByReceiver(cands, &symbol, qualifier, attrSelfVars)
				if _, isSelf := attrSelfVars[qualifier]; isSelf && classLanguage(symbol.Language) && len(filterByParent(cands, symbol.ParentSymbol)) == 0 {
					if inherited := inheritedTargets(idx, &symbol, name, true); len(inherited) > 0 {
						cands = inherited
					}
				}
				for _, cand := range cands {
					addEdge(symbol.ID, cand.ID, 0.7, core.EvidenceSourceASTKit)
				}
			}
		}

		// ── High-confidence path: AST-extracted CallSites ───────────────────
		if len(symbol.CallSites) > 0 {
			selfVars := callerSelfQualifiers(&symbol)
			var localTypes map[string]string
			switch symbol.Language {
			case "go":
				localTypes = goLocalTypes(idx, &symbol)
			case "python":
				localTypes = pyLocalTypes(idx, &symbol)
			case "typescript", "tsx", "javascript":
				localTypes = tsLocalTypes(idx, &symbol)
			case "java":
				localTypes = javaLocalTypes(idx, &symbol)
			case "rust":
				localTypes = rustLocalTypes(idx, &symbol)
			case "csharp":
				localTypes = csharpLocalTypes(idx, &symbol)
			case "php":
				localTypes = phpLocalTypes(idx, &symbol)
			case "c", "cpp":
				localTypes = cFamilyLocalTypes(idx, &symbol)
			}
			var javaArgTypeCache map[string]string
			for _, cs := range symbol.CallSites {
				calleeName := cs.Callee
				// Split receiver prefix (e.g. "user.save" → qualifier "user",
				// name "save"); chains keep only the last segment ("a.b.Get" → "b").
				qualifier := ""
				if idx := strings.LastIndexByte(calleeName, '.'); idx >= 0 {
					qualifier = calleeName[:idx]
					calleeName = calleeName[idx+1:]
				}
				if j := strings.LastIndexByte(qualifier, '.'); j >= 0 {
					qualifier = qualifier[j+1:]
				}
				if calleeName == "" || calleeName == "constructor" || calleeName == "super" {
					// "new X" and "super(...)" are invocation forms, not names:
					// a bare "constructor" callee would match every class's
					// constructor in scope.
					continue
				}
				// AST-extracted names are exact by construction: case-insensitive
				// matching here let "writeContentType" (free function) claim every
				// type's "WriteContentType" method.
				cands, capped := resolveCallees(idx, &symbol, calleeName, scope, true)
				if capped && symbol.Language != "java" && symbol.Language != "rust" {
					// Only narrowing with real evidence may keep very large
					// same-name sets: Java (arity, arg types) and Rust
					// (typed receivers/qualifiers — crate-wide scope makes
					// "new" or "update" routinely exceed the cap before the
					// type evidence has had its chance). For the rest an
					// over-cap set stays dropped (dispatch rescue below
					// still applies); anything Rust's narrowing fails to
					// pin back down is re-capped after narrowing.
					cands = nil
				}
				if symbol.Language == "csharp" || symbol.Language == "php" ||
					symbol.Language == "c" || symbol.Language == "cpp" {
					// Overload disambiguation by arity. C#: JsonConvert has
					// five DeserializeObject overloads, Roslyn picks one by
					// args. PHP has no overloads but default/variadic params
					// mean a same-named method on an unrelated class with a
					// different arity is still a wrong candidate. filterByArgc
					// keeps variadic/default-friendly candidates and never
					// zeroes the set.
					cands = filterByArgc(cands, cs.Argc)
				}
				if symbol.Language == "java" {
					// Overload disambiguation: arity first, then exact
					// argument-type evidence (positive matches only — see
					// narrowOverloadsByArgTypes).
					cands = filterByArgc(cands, cs.Argc)
					if len(cands) > 1 && len(cs.Args) > 0 {
						if javaArgTypeCache == nil {
							javaArgTypeCache = javaArgTypes(&symbol)
						}
						javaResolveCallReturnTypes(idx, cs.Args, scope, javaArgTypeCache)
						cands = narrowOverloadsByArgTypes(cands, cs.Args, javaArgTypeCache)
					}
					// Static typing makes unknowns meaningful: a lowercase
					// receiver with no inferable type is almost always a JDK
					// or library object (map.isEmpty, list.forEach) — its
					// methods aren't in our index, so name collisions are
					// noise. Call-result receivers resolve through the inner
					// call's return type (append().append keeps builder
					// chains); unresolvable ones drop too.
					if qualifier != "" && qualifier != "super" {
						if localTypes == nil {
							localTypes = javaLocalTypes(idx, &symbol)
						}
						_, isSelf := selfVars[qualifier]
						_, typed := localTypes[qualifier]
						if !isSelf && !typed && !typeSymbolExists(idx, qualifier) {
							if strings.HasSuffix(qualifier, "()") {
								if ret := javaCallResultType(idx, qualifier, scope); ret != "" {
									if byType := filterByParent(cands, ret); len(byType) > 0 {
										cands = byType
									} else {
										cands = nil
									}
								} else {
									cands = nil
								}
							} else if qualifier[0] >= 'a' && qualifier[0] <= 'z' {
								cands = nil
							}
						}
					}
				}
				if symbol.Language == "rust" && qualifier == "" && calleeName == "drop" {
					// Prelude mem::drop: a bare drop(x) never targets an
					// in-repo Drop impl by name.
					continue
				}
				if symbol.Language == "php" && strings.HasSuffix(qualifier, "()") && len(cands) > 0 {
					// Fluent-chain receiver ($builder->make()->addStmt()): resolve
					// the call result's class and keep only its methods. An
					// unresolvable/ambiguous result (self-returning builder method
					// that exists on many classes) drops — mirrors Java/Rust — so
					// the chain does not fan out to every same-named method.
					if ret := phpCallResultType(idx, qualifier, scope); ret != "" {
						if byType := filterByParent(cands, ret); len(byType) > 0 {
							cands = byType
						} else {
							cands = nil
						}
					} else {
						cands = nil
					}
				}
				if (symbol.Language == "csharp" || symbol.Language == "php" ||
					symbol.Language == "c" || symbol.Language == "cpp") &&
					qualifier != "" && qualifier != "this" && qualifier != "base" &&
					qualifier != "self" && qualifier != "parent" && qualifier != "static" &&
					!strings.HasSuffix(qualifier, "()") && len(cands) > 0 {
					// Static typing, C#/PHP edition of the Java/Rust rule. A
					// qualifier names a type directly (JsonConvert.ToString,
					// Foo::bar) or a typed variable (reader.Read, $repo->save).
					// If it's neither a known indexed type nor an inferable
					// local, the receiver is a library object (sb.Append,
					// $logger->info) whose method isn't ours — a same-name
					// match is noise: drop. A resolvable type narrows by parent.
					if held, ok := localTypes[qualifier]; ok {
						if byType := filterByParent(cands, held); len(byType) > 0 {
							cands = byType
						} else {
							cands = nil
						}
					} else if byQual := filterByParent(cands, qualifier); len(byQual) > 0 {
						cands = byQual
					} else if !typeSymbolExists(idx, qualifier) {
						cands = nil
					}
				}
				if symbol.Language == "rust" && qualifier != "" && len(cands) > 0 {
					// Static typing, Rust edition of the Java rule. An
					// uppercase qualifier is a type path (PathBuf::from,
					// Regex::new): if no candidate belongs to that type and
					// no local resolves it, the callee lives outside the
					// repo — drop. A lowercase qualifier with no local type
					// is a module path or an uninferable variable: keep
					// only candidates declared in a matching module file
					// (parse::parse_low_raw → flags/parse.rs), else drop.
					_, isSelf := selfVars[qualifier]
					_, typed := localTypes[qualifier]
					if strings.HasSuffix(qualifier, "()") {
						// Call-result receiver: builder chains live here
						// (.line_number(true).build() narrows build by
						// line_number's return type). Unknown results are
						// external (.unwrap().x, .iter().y) — drop.
						if rets := rustCallResultTypes(idx, qualifier, &symbol, scope); len(rets) > 0 {
							var byType []*core.SymbolRecord
							for _, cand := range cands {
								if (cand.Kind == core.KindMethod || cand.Kind == core.KindConstructor) && rets[cand.ParentSymbol] {
									byType = append(byType, cand)
								}
							}
							cands = byType
						} else {
							cands = nil
						}
					} else if isSelf && symbol.ParentSymbol != "" && len(filterByParent(cands, symbol.ParentSymbol)) == 0 {
						// Default trait methods: self.is_match() inside
						// impl Matcher for X, where X declares no
						// is_match, executes the trait's declaration.
						if trait := rustImplTrait(&symbol); trait != "" {
							if byTrait := filterByParent(cands, trait); len(byTrait) > 0 {
								cands = byTrait
							}
						}
					} else if !isSelf && !typed && len(filterByParent(cands, qualifier)) == 0 {
						if qualifier[0] >= 'A' && qualifier[0] <= 'Z' {
							cands = nil
						} else {
							// Module-named files win; a single same-file
							// candidate stays for inline modules (mod
							// convert { fn str... } inside defs.rs), but a
							// same-named set in one file is receiver
							// ambiguity, not module scoping — drop it.
							var inModule, sameFile []*core.SymbolRecord
							for _, cand := range cands {
								base := baseNameNoExt(cand.FilePath)
								if base == qualifier || (base == "mod" && baseOf(dirOf(cand.FilePath)) == qualifier) {
									inModule = append(inModule, cand)
								} else if cand.FilePath == symbol.FilePath {
									sameFile = append(sameFile, cand)
								}
							}
							cands = inModule
							if len(cands) == 0 && len(sameFile) == 1 {
								cands = sameFile
							}
						}
					}
				}
				// super().method() / super.method() resolves on the caller's
				// base classes; bare super() invokes the base constructor.
				if qualifier == "super()" || qualifier == "super" {
					for _, cand := range narrowBySuper(idx, &symbol, cands) {
						addEdge(symbol.ID, cand.ID, 0.85, core.EvidenceSourceHeuristic)
					}
					continue
				}
				if calleeName == "super()" && symbol.ParentSymbol != "" {
					for _, base := range baseClassesFor(idx, symbol.Language, symbol.ParentSymbol, dirOf(symbol.FilePath)) {
						targets := constructorTargets(idx, base, scope)
						if len(targets) == 0 {
							// Inheritance crosses imports — but prefer the twin
							// in the caller's own package over same-named
							// classes elsewhere in a monorepo.
							for _, cand := range idx.byName["constructor"] {
								if cand.ParentSymbol == base && cand.Kind == core.KindConstructor &&
									samePackageRoot(cand.FilePath, symbol.FilePath) {
									targets = append(targets, cand)
								}
							}
						}
						for _, ctor := range targets {
							addEdge(symbol.ID, ctor.ID, 0.85, core.EvidenceSourceHeuristic)
						}
					}
					continue
				}
				narrowed := narrowByReceiver(cands, &symbol, qualifier, selfVars)
				if _, isSelf := selfVars[qualifier]; isSelf && classLanguage(symbol.Language) {
					if len(filterByParent(narrowed, symbol.ParentSymbol)) == 0 {
						// Not a method on the caller's own class: inheritance
						// reaches files import scope never sees.
						if inherited := inheritedTargets(idx, &symbol, calleeName, false); len(inherited) > 0 {
							for _, cand := range inherited {
								addEdge(symbol.ID, cand.ID, 0.85, core.EvidenceSourceHeuristic)
							}
							continue
						}
					}
				}
				if len(narrowed) == len(cands) {
					// Receiver narrowing didn't fire; try the inferred type of
					// the receiver variable, then import qualification.
					kept, dispatch, decided := narrowByLocalType(idx, sat, localTypes, qualifier, calleeName, cands, scope)
					if decided {
						narrowed = kept
						for _, m := range dispatch {
							if m.ID != symbol.ID {
								addEdge(symbol.ID, m.ID, 0.7, core.EvidenceSourceHeuristic)
							}
						}
					} else {
						narrowed = narrowByImport(idx, &symbol, qualifier, cands)
					}
				}
				if len(narrowed) > maxCalleeFanout {
					// Still unresolvably broad after every narrowing pass:
					// drop (the dispatch rescue below may still apply).
					narrowed = nil
					capped = true
				} else if len(narrowed) > 0 {
					capped = false
				}
				for _, cand := range narrowed {
					addEdge(symbol.ID, cand.ID, 0.95, core.EvidenceSourceASTKit)
				}
				// Class instantiation: "Flask(...)" executes Flask.__init__.
				// Route class-named calls to the class's constructor method;
				// "cls(...)" constructs the caller's own class, and a variable
				// holding a class (null_session_class = NullSession) constructs
				// the held class.
				if len(narrowed) == 0 && !capped {
					ctorName := calleeName
					if calleeName == "cls" && symbol.ParentSymbol != "" {
						ctorName = symbol.ParentSymbol
					} else if held, ok := localTypes[calleeName]; ok && strings.HasPrefix(held, "class:") {
						ctorName = strings.TrimPrefix(held, "class:")
					}
					ctors := constructorTargets(idx, ctorName, scope)
					if symbol.Language == "java" {
						ctors = filterByArgc(ctors, cs.Argc)
						if len(ctors) > 1 && len(cs.Args) > 0 {
							if javaArgTypeCache == nil {
								javaArgTypeCache = javaArgTypes(&symbol)
							}
							ctors = narrowOverloadsByArgTypes(ctors, cs.Args, javaArgTypeCache)
						}
					}
					for _, ctor := range ctors {
						if ctor.ID != symbol.ID {
							addEdge(symbol.ID, ctor.ID, 0.85, core.EvidenceSourceHeuristic)
						}
					}
				}
				// Fan-out the cap dropped is legitimate dynamic dispatch when an
				// in-scope interface declares the method: emit edges to its
				// implementations at reduced confidence.
				if capped && sat != nil {
					for _, m := range sat.dispatchTargets(calleeName, scope) {
						if m.ID != symbol.ID {
							addEdge(symbol.ID, m.ID, 0.7, core.EvidenceSourceHeuristic)
						}
					}
				}
			}
			continue // CallSites authoritative; skip regex fallback for this symbol
		}

		// ── Fallback: one identifier-extraction pass over the stripped body ──
		// Only for languages without AST call-site extraction: where the
		// extractor ran, an empty CallSites list is authoritative — a method
		// with zero calls would otherwise regex-match its own signature
		// ("append(final int value)" edging every sibling overload).
		if astCallSiteLanguages[symbol.Language] {
			continue
		}
		if symbol.RawText == "" {
			continue
		}
		stripped := stripCommentsAndStrings(symbol.RawText)
		seenCallee := make(map[string]bool)
		for _, m := range callIdentRe.FindAllStringSubmatch(stripped, -1) {
			calleeName := m[1]
			if seenCallee[calleeName] {
				continue
			}
			if calleeName == "constructor" || calleeName == "super" {
				continue
			}
			seenCallee[calleeName] = true
			cands, fbCapped := resolveCallees(idx, &symbol, calleeName, scope, true)
			if fbCapped {
				// The fallback has no narrowing evidence; over-cap stays dropped.
				continue
			}
			for _, cand := range cands {
				confidence := 0.6
				if cand.FilePath == symbol.FilePath {
					confidence = 0.85
				}
				addEdge(symbol.ID, cand.ID, confidence, core.EvidenceSourceRegex)
			}
		}
	}
	return edges
}

// astCallSiteLanguages lists the languages whose astkit extractors emit
// CallSites; for them the AST path is authoritative and the regex fallback
// never runs.
var astCallSiteLanguages = map[string]bool{
	"go": true, "python": true, "javascript": true, "typescript": true,
	"tsx": true, "java": true, "rust": true, "csharp": true, "php": true,
	"c": true, "cpp": true,
}

// classLanguage reports whether the language has class inheritance our
// base-class parsers understand.
func classLanguage(lang string) bool {
	return lang == "python" || lang == "typescript" || lang == "javascript" || lang == "java" || lang == "csharp" || lang == "php" || lang == "cpp"
}

// callerSelfQualifiers returns the receiver spellings that mean "a method on
// my own type" inside this symbol's body: self/this plus, for Go, the
// receiver variable parsed from the method signature ("func (r JSON) ...").
func callerSelfQualifiers(symbol *core.SymbolRecord) map[string]struct{} {
	out := map[string]struct{}{"self": {}, "this": {}, "cls": {}}
	if symbol.Language == "go" && symbol.Kind == core.KindMethod {
		if v := goReceiverVar(symbol.Signature); v != "" {
			out[v] = struct{}{}
		}
	}
	return out
}

// goReceiverVar extracts the receiver variable name from a Go method
// signature like "func (r JSON) Render(w http.ResponseWriter) error".
func goReceiverVar(signature string) string {
	rest, ok := strings.CutPrefix(signature, "func (")
	if !ok {
		return ""
	}
	end := strings.IndexByte(rest, ')')
	if end < 0 {
		return ""
	}
	recv := strings.TrimSpace(rest[:end])
	// "r JSON" / "r *JSON" → "r"; bare "*JSON" / "JSON" has no variable.
	if i := strings.IndexByte(recv, ' '); i > 0 {
		return recv[:i]
	}
	return ""
}

// narrowByReceiver tightens name-resolved callee candidates using the call
// site's receiver qualifier. Two cases resolve without a type checker:
//
//   - the qualifier is the caller's own receiver (r./self./this.) → keep only
//     methods on the caller's parent type
//   - the qualifier names a type directly (JSON.WriteContentType) → keep only
//     methods on that type
//
// When the qualifier matches neither pattern (an arbitrary local variable, an
// external type, a package alias), candidates pass through unchanged: this
// narrows known-wrong fanout, it never invents matches.
func narrowByReceiver(cands []*core.SymbolRecord, caller *core.SymbolRecord, qualifier string, selfVars map[string]struct{}) []*core.SymbolRecord {
	if qualifier == "" || len(cands) < 2 {
		return cands
	}
	if _, isSelf := selfVars[qualifier]; isSelf && caller.ParentSymbol != "" {
		if same := filterByParent(cands, caller.ParentSymbol); len(same) > 0 {
			return same
		}
		return cands
	}
	if byType := filterByParent(cands, qualifier); len(byType) > 0 {
		return byType
	}
	return cands
}

func filterByParent(cands []*core.SymbolRecord, parent string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range cands {
		// Constructors count: Rust's Type::new is a constructor-kind
		// method and must narrow by its parent like any other.
		if (cand.Kind == core.KindMethod || cand.Kind == core.KindConstructor) && cand.ParentSymbol == parent {
			out = append(out, cand)
		}
	}
	return out
}

// resolvePropertyTargets finds in-scope property-annotated methods matching
// an attribute access name. Same-file candidates win; cross-file fan-out is
// capped like calls resolution.
func resolvePropertyTargets(idx *edgeIndex, symbol *core.SymbolRecord, name string, scope map[string]struct{}) []*core.SymbolRecord {
	var sameFile, crossFile []*core.SymbolRecord
	for _, cand := range idx.byName[strings.ToLower(name)] {
		if cand.ID == symbol.ID || cand.Name != name || cand.Kind != core.KindMethod {
			continue
		}
		if !hasPropertyAnnotation(cand) {
			continue
		}
		if _, ok := scope[cand.FilePath]; !ok {
			continue
		}
		if cand.FilePath == symbol.FilePath {
			sameFile = append(sameFile, cand)
		} else {
			crossFile = append(crossFile, cand)
		}
	}
	if len(sameFile) > 0 {
		return sameFile
	}
	if len(crossFile) > maxCalleeFanout {
		return nil
	}
	return crossFile
}

func hasPropertyAnnotation(s *core.SymbolRecord) bool {
	for _, ann := range s.Annotations {
		if ann == "property" || ann == "cached_property" ||
			strings.HasSuffix(ann, ".setter") || strings.HasSuffix(ann, ".getter") || strings.HasSuffix(ann, ".deleter") {
			return true
		}
	}
	return false
}

// filterByArgc keeps candidates whose declared parameter count is
// compatible with the call site's argument count: exact match, or varargs
// ("...") accepting argc >= fixed params. Candidates whose parameter list
// can't be parsed pass through.
func filterByArgc(cands []*core.SymbolRecord, argc int) []*core.SymbolRecord {
	if len(cands) < 2 {
		return cands
	}
	var out []*core.SymbolRecord
	for _, cand := range cands {
		n, variadic, ok := declParamCount(cand)
		if !ok || n == argc || (variadic && argc >= n-1) {
			out = append(out, cand)
		}
	}
	if len(out) == 0 {
		return cands
	}
	return out
}

// declParamCount counts declared parameters from a callable's first
// balanced paren group (signature if complete, else raw text).
func declParamCount(s *core.SymbolRecord) (int, bool, bool) {
	src := s.Signature
	if !strings.Contains(src, ")") {
		src = s.RawText
	}
	params := tsDeclParams(src)
	if params == "" {
		if strings.Contains(src, "()") {
			return 0, false, true
		}
		return 0, false, false
	}
	groups := splitTopLevel(params, ',')
	n := 0
	variadic := false
	for _, g := range groups {
		if strings.TrimSpace(g) == "" {
			continue
		}
		n++
		if strings.Contains(g, "...") {
			variadic = true
		}
	}
	return n, variadic, true
}

// constructorTargets resolves a class-named call ("Flask(...)") to the
// class's constructor method (__init__, constructor, or any
// KindConstructor child) so instantiation produces a call edge to the code
// that actually runs. A class without its own constructor inherits one:
// the base-class chain is walked until a constructor is found.
func constructorTargets(idx *edgeIndex, calleeName string, scope map[string]struct{}) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cls := range idx.byName[strings.ToLower(calleeName)] {
		if cls.Name != calleeName {
			continue
		}
		if cls.Kind != core.KindClass && cls.Kind != core.KindStruct {
			continue
		}
		if _, ok := scope[cls.FilePath]; !ok {
			continue
		}
		out = append(out, classConstructors(idx, cls.Name, cls.FilePath)...)
		if len(out) == 0 {
			bases := baseClassesFor(idx, languageOfFile(idx, cls.FilePath), cls.Name, dirOf(cls.FilePath))
			for level := 0; level < 3 && len(bases) > 0 && len(out) == 0; level++ {
				var next []string
				for _, base := range bases {
					for _, baseCls := range idx.byName[strings.ToLower(base)] {
						if baseCls.Name == base && baseCls.Kind == core.KindClass {
							out = append(out, classConstructors(idx, base, baseCls.FilePath)...)
							next = append(next, baseClassesFor(idx, languageOfFile(idx, baseCls.FilePath), base, dirOf(baseCls.FilePath))...)
							break
						}
					}
				}
				bases = next
			}
		}
	}
	return out
}

func classConstructors(idx *edgeIndex, className, filePath string) []*core.SymbolRecord {
	var out []*core.SymbolRecord
	for _, cand := range idx.byFile[filePath] {
		if cand.ParentSymbol != className {
			continue
		}
		if cand.Kind == core.KindConstructor ||
			(cand.Kind == core.KindMethod && (cand.Name == "__init__" || cand.Name == "constructor")) {
			out = append(out, cand)
		}
	}
	return out
}

func languageOfFile(idx *edgeIndex, filePath string) string {
	for _, s := range idx.byFile[filePath] {
		if s.Language != "" {
			return s.Language
		}
	}
	return ""
}

// samePackageRoot reports whether two repo-relative paths share their first
// two path segments ("packages/engine.io/...") — the monorepo package
// boundary heuristic.
func samePackageRoot(a, b string) bool {
	segA, segB := strings.SplitN(a, "/", 3), strings.SplitN(b, "/", 3)
	if len(segA) < 2 || len(segB) < 2 {
		return dirOf(a) == dirOf(b)
	}
	return segA[0] == segB[0] && segA[1] == segB[1]
}

// narrowByImport handles package-qualified call sites ("json.Marshal",
// "render.New"): when the qualifier exactly matches the last segment of one
// of the caller file's imports, the call most likely targets that package —
// so candidates restrict to that import's in-repo files. An import that
// resolves to no in-repo file is an external dependency: the call can't
// target anything we index, so all candidates drop. Qualifiers that match no
// import pass through unchanged.
//
// Matching is case-exact: a field named "Session" must not be confused with
// an "internal/session" import. Methods stay in the restriction — the Go
// pattern of naming a field after its package ("h.grove.Index()") means a
// package-looking qualifier can still be a value receiver.
func narrowByImport(idx *edgeIndex, symbol *core.SymbolRecord, qualifier string, cands []*core.SymbolRecord) []*core.SymbolRecord {
	if qualifier == "" || len(cands) == 0 || strings.HasSuffix(qualifier, "()") {
		return cands
	}
	files, isImport := idx.importFilesForQualifier(symbol.FilePath, qualifier)
	if !isImport {
		// Java: an uppercase qualifier is a class reference. With no import
		// and no indexed type of that name, it's an implicit-JDK class
		// (System, Math, Objects...) — the call can't target our index.
		if symbol.Language == "java" && qualifier[0] >= 'A' && qualifier[0] <= 'Z' &&
			!typeSymbolExists(idx, qualifier) {
			return nil
		}
		return cands
	}
	var out []*core.SymbolRecord
	for _, cand := range cands {
		if _, ok := files[cand.FilePath]; ok {
			out = append(out, cand)
		}
	}
	return out
}

// importFilesForQualifier resolves the import whose last segment equals the
// qualifier (case-exact) to its in-repo files, using only the precise
// resolvers: exact path match and slash-suffix directory match. The fuzzy
// basename resolvers importedFiles uses for scope would mis-resolve external
// imports to same-named in-repo dirs here ("encoding/json" → internal/json).
// The second return reports whether such an import exists at all.
func (idx *edgeIndex) importFilesForQualifier(fromFile, qualifier string) (map[string]struct{}, bool) {
	imports, ok := idx.fileImports[fromFile]
	if !ok {
		return nil, false
	}
	found := false
	out := map[string]struct{}{}
	for imp := range imports {
		if lastImportSegment(imp) != qualifier {
			continue
		}
		found = true
		impNorm := strings.ToLower(strings.Trim(imp, "\"' ;"))
		impNorm = strings.TrimPrefix(impNorm, "./")
		// Java/Kotlin imports are dot-separated paths; the precise
		// resolvers below are slash-keyed.
		if !strings.Contains(impNorm, "/") && strings.Contains(impNorm, ".") {
			impNorm = strings.ReplaceAll(impNorm, ".", "/")
		}
		// Trim only known source extensions — a naive last-dot trim would
		// truncate module paths at their domain ("example.com/…" → "example").
		for _, ext := range []string{".go", ".py", ".ts", ".tsx", ".js", ".jsx", ".java", ".rs"} {
			impNorm = strings.TrimSuffix(impNorm, ext)
		}
		for _, f := range idx.importPathToFiles[impNorm] {
			if f != fromFile {
				out[f] = struct{}{}
			}
		}
		parts := strings.Split(impNorm, "/")
		for i := range parts {
			suffix := strings.Join(parts[i:], "/")
			if suffix == "" || suffix == "." {
				continue
			}
			for _, f := range idx.dirFilesLower[suffix] {
				if f != fromFile {
					out[f] = struct{}{}
				}
			}
		}
		// Maven/Gradle layouts prefix source dirs ("src/main/java/org/..."),
		// so the import path is a SUFFIX of the dir or file, not equal to it.
		if len(out) == 0 {
			for dir, files := range idx.dirFilesLower {
				if strings.HasSuffix(dir, "/"+impNorm) || dir == impNorm {
					for _, f := range files {
						if f != fromFile {
							out[f] = struct{}{}
						}
					}
				}
			}
			for pathKey, files := range idx.importPathToFiles {
				if strings.HasSuffix(pathKey, "/"+impNorm) {
					for _, f := range files {
						if f != fromFile {
							out[f] = struct{}{}
						}
					}
				}
			}
		}
	}
	return out, found
}

// ─── helpers ─────────────────────────────────────────────────────────────────

func matchNameList(re *regexp.Regexp, text string) []string {
	m := re.FindStringSubmatch(text)
	if len(m) < 2 {
		return nil
	}
	var out []string
	for _, name := range splitTrim(m[1], ',') {
		name = strings.TrimSpace(name)
		if i := strings.Index(name, "<"); i >= 0 {
			name = name[:i]
		}
		// Drop dotted prefixes (java.util.List → List).
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			name = name[i+1:]
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func splitTrim(s string, sep byte) []string {
	parts := strings.Split(s, string(sep))
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func stripPythonBase(b string) string {
	b = strings.TrimSpace(b)
	// Strip default ABC bases that add noise (object, Generic[T], etc.)
	if strings.HasPrefix(b, "metaclass=") || b == "object" {
		return ""
	}
	if i := strings.Index(b, "["); i >= 0 {
		b = b[:i]
	}
	if i := strings.LastIndexByte(b, '.'); i >= 0 {
		b = b[i+1:]
	}
	return b
}

func goEmbeddedTypes(body string) []string {
	// Look at lines between the first `{` and the matching `}` of the struct
	// declaration. We consider each non-empty line consisting of a single
	// identifier (or *Identifier) to be an embedded type.
	open := strings.IndexByte(body, '{')
	if open < 0 {
		return nil
	}
	body = body[open+1:]
	close := strings.LastIndexByte(body, '}')
	if close >= 0 {
		body = body[:close]
	}
	var out []string
	embeddedRe := regexp.MustCompile(`^\s*\*?([A-Z][A-Za-z0-9_]*)\s*(?://.*)?$`)
	for _, line := range strings.Split(body, "\n") {
		if m := embeddedRe.FindStringSubmatch(line); len(m) == 2 {
			out = append(out, m[1])
		}
	}
	return out
}

// resolveTypeEdges returns 0 or more edges from `symbol` to a target type
// resolved by name. If no concrete symbol is found, no edge is emitted.
func resolveTypeEdges(idx *edgeIndex, symbol core.SymbolRecord, targetName string, edgeType core.EdgeType, confidence float64) []core.Edge {
	var out []core.Edge
	for _, target := range idx.byName[strings.ToLower(targetName)] {
		if target.ID == symbol.ID {
			continue
		}
		out = append(out, core.Edge{
			From:       symbol.ID,
			To:         target.ID,
			Type:       edgeType,
			Confidence: confidence,
			Source:     core.EvidenceSourceHeuristic,
		})
	}
	return out
}

func firstLine(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return strings.TrimSpace(text[:i])
	}
	return strings.TrimSpace(text)
}
