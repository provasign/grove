// Package strategies provides per-language astkit.Strategy implementations
// built on the shared tree-sitter extractors defined in extractors.go.
//
// Each strategy is a small adapter: Language() / Extensions() / Extract() /
// ExtractImports(). Extraction is delegated to the per-language
// extract*Nodes function; import scanning is performed via AST traversal
// using the internalast helper package.
package strategies

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/internalast"
)

// Default returns a Registry pre-populated with every strategy astkit ships.
// TypeScript is registered both under "typescript" and "tsx"; JavaScript under
// "javascript". C++ shares its strategy registration distinct from C.
func Default() *astkit.Registry {
	r := astkit.NewRegistry()
	r.Register(NewGo())
	r.Register(NewPython())
	r.Register(NewJava())
	r.Register(NewRust())
	r.Register(NewJavaScript())
	ts := NewTypeScript(false)
	r.Register(ts)
	r.RegisterAs(astkit.LangTSX, NewTypeScript(true))
	r.Register(NewC())
	r.Register(NewCPP())
	r.Register(NewCSharp())
	r.Register(NewPHP())
	return r
}

// ─── Go ───────────────────────────────────────────────────────────────────────

type goStrategy struct{}

func NewGo() *goStrategy                           { return &goStrategy{} }
func (g *goStrategy) Language() astkit.LanguageKey { return astkit.LangGo }
func (g *goStrategy) Extensions() []string         { return []string{".go"} }
func (g *goStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractGoNodes(tree.RootNode(), "", "", src, nil), nil
}
func (g *goStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imports []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "import_declaration" {
			return
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c == nil {
				continue
			}
			switch c.Type() {
			case "import_spec":
				if imp := goImportSpec(c, src); imp != nil {
					imports = append(imports, *imp)
				}
			case "import_spec_list":
				for j := 0; j < int(c.ChildCount()); j++ {
					cc := c.Child(j)
					if cc != nil && cc.Type() == "import_spec" {
						if imp := goImportSpec(cc, src); imp != nil {
							imports = append(imports, *imp)
						}
					}
				}
			}
		}
	})
	return imports, nil
}

func goImportSpec(n *sitter.Node, src []byte) *astkit.ImportStatement {
	pathNode := n.ChildByFieldName("path")
	if pathNode == nil {
		return nil
	}
	rawPath := internalast.NodeText(pathNode, src)
	cleaned := strings.Trim(rawPath, `"`)
	alias := ""
	if nameNode := n.ChildByFieldName("name"); nameNode != nil {
		alias = internalast.NodeText(nameNode, src)
	}
	return &astkit.ImportStatement{
		Raw:   internalast.NodeText(n, src),
		Path:  cleaned,
		Alias: alias,
		Group: goImportGroup(cleaned),
		Line:  int(n.StartPoint().Row) + 1,
	}
}

func goImportGroup(path string) string {
	if strings.HasPrefix(path, ".") {
		return "relative"
	}
	first := path
	if idx := strings.Index(path, "/"); idx >= 0 {
		first = path[:idx]
	}
	if strings.Contains(first, ".") {
		return "external"
	}
	return "stdlib"
}

// ─── Python ───────────────────────────────────────────────────────────────────

type pythonStrategy struct{}

func NewPython() *pythonStrategy                       { return &pythonStrategy{} }
func (p *pythonStrategy) Language() astkit.LanguageKey { return astkit.LangPython }
func (p *pythonStrategy) Extensions() []string         { return []string{".py"} }
func (p *pythonStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractPythonNodes(tree.RootNode(), "", "", src, nil), nil
}
func (p *pythonStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "import_statement":
			for i := 0; i < int(n.ChildCount()); i++ {
				c := n.Child(i)
				if c == nil {
					continue
				}
				if c.Type() == "dotted_name" || c.Type() == "aliased_import" {
					imps = append(imps, astkit.ImportStatement{
						Raw:  internalast.NodeText(n, src),
						Path: internalast.NodeText(c, src),
						Line: int(n.StartPoint().Row) + 1,
					})
				}
			}
		case "import_from_statement":
			mod := internalast.NodeText(n.ChildByFieldName("module_name"), src)
			imps = append(imps, astkit.ImportStatement{
				Raw:  internalast.NodeText(n, src),
				Path: mod,
				Line: int(n.StartPoint().Row) + 1,
			})
		}
	})
	return imps, nil
}

// ─── Java ─────────────────────────────────────────────────────────────────────

type javaStrategy struct{}

func NewJava() *javaStrategy                         { return &javaStrategy{} }
func (j *javaStrategy) Language() astkit.LanguageKey { return astkit.LangJava }
func (j *javaStrategy) Extensions() []string         { return []string{".java"} }
func (j *javaStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractJavaNodes(tree.RootNode(), "", "", src, nil), nil
}
func (j *javaStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "import_declaration" {
			return
		}
		raw := strings.TrimSuffix(strings.TrimSpace(internalast.NodeText(n, src)), ";")
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "import"))
		raw = strings.TrimSpace(strings.TrimPrefix(raw, "static"))
		imps = append(imps, astkit.ImportStatement{
			Raw:  internalast.NodeText(n, src),
			Path: raw,
			Line: int(n.StartPoint().Row) + 1,
		})
	})
	return imps, nil
}

// ─── Rust ─────────────────────────────────────────────────────────────────────

type rustStrategy struct{}

func NewRust() *rustStrategy                         { return &rustStrategy{} }
func (r *rustStrategy) Language() astkit.LanguageKey { return astkit.LangRust }
func (r *rustStrategy) Extensions() []string         { return []string{".rs"} }
func (r *rustStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractRustNodes(tree.RootNode(), "", "", src, nil), nil
}
func (r *rustStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "use_declaration" {
			return
		}
		raw := strings.TrimSpace(internalast.NodeText(n, src))
		path := strings.TrimSuffix(strings.TrimPrefix(raw, "use "), ";")
		imps = append(imps, astkit.ImportStatement{
			Raw:  raw,
			Path: strings.TrimSpace(path),
			Line: int(n.StartPoint().Row) + 1,
		})
	})
	return imps, nil
}

// ─── JavaScript / TypeScript / TSX ────────────────────────────────────────────

type jsLike struct {
	lang astkit.LanguageKey
	exts []string
	tsx  bool
}

func NewJavaScript() *jsLike {
	return &jsLike{lang: astkit.LangJavaScript, exts: []string{".js", ".jsx", ".mjs", ".cjs"}}
}
func NewTypeScript(tsx bool) *jsLike {
	if tsx {
		return &jsLike{lang: astkit.LangTSX, exts: []string{".tsx"}, tsx: true}
	}
	return &jsLike{lang: astkit.LangTypeScript, exts: []string{".ts"}}
}
func (j *jsLike) Language() astkit.LanguageKey { return j.lang }
func (j *jsLike) Extensions() []string         { return j.exts }
func (j *jsLike) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	// extractJSNodes accepts a language string in the original Grove form;
	// it only uses it for the dropped Language field, so we pass a stable
	// label.
	lang := "javascript"
	switch j.lang {
	case astkit.LangTypeScript:
		lang = "typescript"
	case astkit.LangTSX:
		lang = "tsx"
	}
	return extractJSNodes(tree.RootNode(), "", "", lang, src, nil), nil
}
func (j *jsLike) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "import_statement" {
			return
		}
		sourceNode := n.ChildByFieldName("source")
		path := strings.Trim(internalast.NodeText(sourceNode, src), `"'`+"`")
		imps = append(imps, astkit.ImportStatement{
			Raw:  internalast.NodeText(n, src),
			Path: path,
			Line: int(n.StartPoint().Row) + 1,
		})
	})
	return imps, nil
}

// ─── C / C++ ──────────────────────────────────────────────────────────────────

type cStrategy struct {
	lang astkit.LanguageKey
	exts []string
	cpp  bool
}

func NewC() *cStrategy {
	return &cStrategy{lang: astkit.LangC, exts: []string{".c", ".h"}}
}
func NewCPP() *cStrategy {
	return &cStrategy{lang: astkit.LangCPP, exts: []string{".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx"}, cpp: true}
}
func (c *cStrategy) Language() astkit.LanguageKey { return c.lang }
func (c *cStrategy) Extensions() []string         { return c.exts }
func (c *cStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	lang := "c"
	if c.cpp {
		lang = "cpp"
	}
	return extractCNodes(tree.RootNode(), "", "", lang, src, nil), nil
}
func (c *cStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "preproc_include" {
			return
		}
		pathNode := n.ChildByFieldName("path")
		raw := internalast.NodeText(n, src)
		p := strings.Trim(internalast.NodeText(pathNode, src), `"<>`)
		imps = append(imps, astkit.ImportStatement{
			Raw:  raw,
			Path: p,
			Line: int(n.StartPoint().Row) + 1,
		})
	})
	return imps, nil
}

// ─── C# ───────────────────────────────────────────────────────────────────────

type csharpStrategy struct{}

func NewCSharp() *csharpStrategy                       { return &csharpStrategy{} }
func (c *csharpStrategy) Language() astkit.LanguageKey { return astkit.LangCSharp }
func (c *csharpStrategy) Extensions() []string         { return []string{".cs"} }
func (c *csharpStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractCSharpNodes(tree.RootNode(), "", "", src, nil), nil
}
func (c *csharpStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		if n.Type() != "using_directive" {
			return
		}
		raw := strings.TrimSpace(internalast.NodeText(n, src))
		path := strings.TrimSuffix(strings.TrimPrefix(raw, "using "), ";")
		imps = append(imps, astkit.ImportStatement{
			Raw:  raw,
			Path: strings.TrimSpace(path),
			Line: int(n.StartPoint().Row) + 1,
		})
	})
	return imps, nil
}

// ─── PHP ──────────────────────────────────────────────────────────────────────

type phpStrategy struct{}

func NewPHP() *phpStrategy                          { return &phpStrategy{} }
func (p *phpStrategy) Language() astkit.LanguageKey { return astkit.LangPHP }
func (p *phpStrategy) Extensions() []string         { return []string{".php"} }
func (p *phpStrategy) Extract(tree *sitter.Tree, src []byte) ([]astkit.Symbol, error) {
	if tree == nil {
		return nil, nil
	}
	return extractPHPNodes(tree.RootNode(), "", "", src, nil), nil
}
func (p *phpStrategy) ExtractImports(tree *sitter.Tree, src []byte) ([]astkit.ImportStatement, error) {
	if tree == nil {
		return nil, nil
	}
	var imps []astkit.ImportStatement
	internalast.WalkChildren(tree.RootNode(), func(n *sitter.Node) {
		switch n.Type() {
		case "namespace_use_declaration", "require_expression", "require_once_expression", "include_expression", "include_once_expression":
			raw := strings.TrimSpace(internalast.NodeText(n, src))
			imps = append(imps, astkit.ImportStatement{
				Raw:  raw,
				Path: raw,
				Line: int(n.StartPoint().Row) + 1,
			})
		}
	})
	return imps, nil
}
