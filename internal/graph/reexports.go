package graph

import (
	"fmt"
	"path"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// JSExportScanner lists `export { name [as alias] } [from "m"]` specifiers
// of name in the project's TS/JS sources. The int counts unreadable files.
type JSExportScanner func(name string) ([]core.JSExportSpecifier, int)

// SetJSExportScanner installs the source scanner that finds re-export and
// alias sites for TS/JS function change impact. Without one, function
// results carry no ReExports (the pre-2026-09-26 behavior).
func (g *CodeGraph) SetJSExportScanner(s JSExportScanner) {
	g.mu.Lock()
	g.jsExportScanner = s
	g.mu.Unlock()
}

func isJSLanguage(lang string) bool {
	switch lang {
	case "typescript", "tsx", "javascript":
		return true
	}
	return false
}

// jsReExportsLocked returns the export-specifier lines that re-export or
// alias one of decls (zod: `export { _gte as gte } from "../core/index.js"`
// in mini/checks.ts, and `_gte as _min` beside the declaration). Barrel files
// hold no symbols, so no call edge ever reaches these lines, yet a rename or
// signature change of _gte is visible to every consumer of `gte`.
//
// A specifier binds to a declaration when it is a local export in the
// declaring file, or its `from` module resolves (relative path, extension
// and /index stripped) to the declaring file or a directory containing it.
// A bare package specifier binds only when the name has one TS/JS
// declaration in the index.
func (g *CodeGraph) jsReExportsLocked(decls []core.SymbolRecord) []MemberAccess {
	if g.jsExportScanner == nil || len(decls) == 0 {
		return nil
	}
	var jsDecls []core.SymbolRecord
	for _, d := range decls {
		if isJSLanguage(d.Language) {
			jsDecls = append(jsDecls, d)
		}
	}
	if len(jsDecls) == 0 {
		return nil
	}
	name := jsDecls[0].Name
	unique := 0
	for _, id := range g.idsNamed(name) {
		if s := g.symbols[id]; isJSLanguage(s.Language) && s.ParentSymbol == "" {
			unique++
		}
	}
	specs, _ := g.jsExportScanner(name)
	var out []MemberAccess
	seen := map[string]bool{}
	for _, sp := range specs {
		var bound *core.SymbolRecord
		for i := range jsDecls {
			d := &jsDecls[i]
			switch {
			case sp.Source == "":
				if sp.File == d.FilePath {
					bound = d
				}
			case strings.HasPrefix(sp.Source, "."):
				if jsModuleCovers(path.Join(path.Dir(sp.File), sp.Source), d.FilePath) {
					bound = d
				}
			default:
				if unique == 1 {
					bound = d
				}
			}
			if bound != nil {
				break
			}
		}
		if bound == nil {
			continue
		}
		key := fmt.Sprintf("%s:%d", sp.File, sp.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		evidence := "re-exported"
		if sp.Exported != sp.Local {
			evidence = "re-exported as " + sp.Exported
		}
		if sp.Source != "" {
			evidence += " from " + sp.Source
		}
		out = append(out, MemberAccess{
			FilePath: sp.File, Line: sp.Line, Access: "re-export",
			Evidence: evidence, Text: sp.Text,
		})
	}
	return out
}

// jsModuleCovers reports whether module (a relative import target already
// joined to the importing file's directory) is the declaring file itself or
// a directory (barrel) containing it.
func jsModuleCovers(module, declFile string) bool {
	module = path.Clean(module)
	for _, ext := range []string{".js", ".mjs", ".cjs", ".jsx", ".ts", ".mts", ".cts", ".tsx"} {
		module = strings.TrimSuffix(module, ext)
	}
	module = strings.TrimSuffix(module, "/index")
	declNoExt := strings.TrimSuffix(declFile, path.Ext(declFile))
	if declNoExt == module {
		return true
	}
	dir := path.Dir(declFile)
	return dir == module || strings.HasPrefix(dir, module+"/")
}
