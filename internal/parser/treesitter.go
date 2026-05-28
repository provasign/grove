// Package parser — Tree-sitter based symbol extractor.
//
// This file is the primary extraction engine.  extractSymbolsFromAST() parses
// source with Tree-sitter and walks the AST to produce SymbolRecord values.
// All CGO usage is contained in this file and the imported grammar packages.
//
// Language coverage: Go, TypeScript, TSX, JavaScript (+ JSX), Python, Java, Rust.
package parser

import (
	"context"
	"fmt"
	"strings"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
	tsc "github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	tstsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	tstype "github.com/smacker/go-tree-sitter/typescript/typescript"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

const ParseTimeout = 2 * time.Second

// ParseTree validates that src is syntactically valid for the given language.
// Used by API/MCP validation endpoints and tests.
func (e *Engine) ParseTree(language string, src []byte) error {
	lang, ok := treeSitterLanguage(language)
	if !ok {
		return nil
	}
	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	ctx, cancel := context.WithTimeout(context.Background(), ParseTimeout)
	defer cancel()
	tree, err := parser.ParseCtx(ctx, nil, src)
	if err != nil {
		return fmt.Errorf("tree-sitter parse failed for %s: %w", language, err)
	}
	if tree != nil {
		tree.Close()
	}
	return nil
}

func treeSitterLanguage(language string) (*sitter.Language, bool) {
	switch language {
	case "go":
		return golang.GetLanguage(), true
	case "typescript":
		return tstype.GetLanguage(), true
	case "tsx":
		return tstsx.GetLanguage(), true
	case "javascript":
		return javascript.GetLanguage(), true
	case "python":
		return python.GetLanguage(), true
	case "java":
		return java.GetLanguage(), true
	case "rust":
		return rust.GetLanguage(), true
	case "c":
		return tsc.GetLanguage(), true
	case "cpp":
		return cpp.GetLanguage(), true
	case "csharp":
		return csharp.GetLanguage(), true
	case "php":
		return php.GetLanguage(), true
	default:
		return nil, false
	}
}

// extractSymbolsFromAST parses src with Tree-sitter and extracts symbols.
// Returns (symbols, ok, hasErrors):
//   - ok=false when the language is unsupported or parsing times out; callers
//     must fall back to the regex extractor.
//   - ok=true, hasErrors=false: clean parse; symbols are complete.
//   - ok=true, hasErrors=true: tree-sitter recovered from one or more syntax
//     errors; symbols inside ERROR subtrees are absent from the AST result.
//     Callers should merge in the regex fallback for those gaps.
func extractSymbolsFromAST(language, filePath, blobSHA string, src []byte, fileImports []string) (syms []core.SymbolRecord, ok bool, hasErrors bool) {
	lang, supported := treeSitterLanguage(language)
	if !supported {
		return nil, false, false
	}

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	ctx, cancel := context.WithTimeout(context.Background(), ParseTimeout)
	defer cancel()
	tree, err := parser.ParseCtx(ctx, nil, src)
	if err != nil || tree == nil {
		return nil, false, false
	}
	defer tree.Close()

	root := tree.RootNode()
	if root == nil {
		return nil, false, false
	}

	hasErrors = root.HasError()

	switch language {
	case "go":
		syms = extractGoNodes(root, filePath, blobSHA, src, fileImports)
	case "typescript", "tsx":
		syms = extractJSNodes(root, filePath, blobSHA, language, src, fileImports)
	case "javascript":
		syms = extractJSNodes(root, filePath, blobSHA, language, src, fileImports)
	case "python":
		syms = extractPythonNodes(root, filePath, blobSHA, src, fileImports)
	case "java":
		syms = extractJavaNodes(root, filePath, blobSHA, src, fileImports)
	case "rust":
		syms = extractRustNodes(root, filePath, blobSHA, src, fileImports)
	case "c", "cpp":
		syms = extractCNodes(root, filePath, blobSHA, language, src, fileImports)
	case "csharp":
		syms = extractCSharpNodes(root, filePath, blobSHA, src, fileImports)
	case "php":
		syms = extractPHPNodes(root, filePath, blobSHA, src, fileImports)
	}
	if syms == nil {
		syms = []core.SymbolRecord{} // non-nil signals "extraction succeeded with zero symbols"
	}
	return syms, true, hasErrors
}

// ─── Go ───────────────────────────────────────────────────────────────────────

// extractGoNodes walks the top-level children of a Go source_file node.
// Only package-level declarations are extracted; symbols inside function
// bodies (local vars, closures) are intentionally skipped.
func extractGoNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for i := 0; i < int(root.ChildCount()); i++ {
		n := root.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_declaration":
			if sym := goFuncSym(n, filePath, blobSHA, src, imports); sym != nil {
				out = append(out, *sym)
			}
		case "method_declaration":
			if sym := goMethodSym(n, filePath, blobSHA, src, imports); sym != nil {
				out = append(out, *sym)
			}
		case "type_declaration":
			out = append(out, goTypeDecl(n, filePath, blobSHA, src, imports)...)
		case "const_declaration":
			out = append(out, goConstDecl(n, filePath, blobSHA, src, imports)...)
		case "var_declaration":
			out = append(out, goVarDecl(n, filePath, blobSHA, src, imports)...)
		}
	}
	return out
}

func goFuncSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) *core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	body := n.ChildByFieldName("body")
	return &core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "go",
		Kind:           core.KindFunction,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           nodeSpan(n),
		Exports:        isCapitalized(name),
		RawText:        raw,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		TypeParameters: goTypeParameters(n, src),
		CallSites:      goCallSites(body, src),
	}
}

func goMethodSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) *core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	receiver := goReceiverTypeName(n, src)
	raw := n.Content(src)
	body := n.ChildByFieldName("body")
	return &core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "go",
		Kind:           core.KindMethod,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           nodeSpan(n),
		Exports:        isCapitalized(name),
		RawText:        raw,
		ParentSymbol:   receiver,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		TypeParameters: goTypeParameters(n, src),
		CallSites:      goCallSites(body, src),
	}
}

// goReceiverTypeName extracts the bare type name from a method receiver.
// For `func (s *Service) Login(...)` it returns "Service".
func goReceiverTypeName(method *sitter.Node, src []byte) string {
	recv := method.ChildByFieldName("receiver")
	if recv == nil {
		return ""
	}
	for i := 0; i < int(recv.ChildCount()); i++ {
		param := recv.Child(i)
		if param == nil || param.Type() != "parameter_declaration" {
			continue
		}
		typeNode := param.ChildByFieldName("type")
		if typeNode == nil {
			continue
		}
		switch typeNode.Type() {
		case "pointer_type":
			for j := 0; j < int(typeNode.ChildCount()); j++ {
				c := typeNode.Child(j)
				if c != nil && c.Type() == "type_identifier" {
					return c.Content(src)
				}
			}
		case "type_identifier":
			return typeNode.Content(src)
		}
	}
	return ""
}

// goTypeDecl handles `type X struct{}`, `type X interface{}`, `type X Y`
// and grouped `type (X ...; Y ...)` declarations.
func goTypeDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for i := 0; i < int(n.ChildCount()); i++ {
		spec := n.Child(i)
		if spec == nil || spec.Type() != "type_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		kind := core.KindType
		typeNode := spec.ChildByFieldName("type")
		if typeNode != nil {
			switch typeNode.Type() {
			case "struct_type":
				kind = core.KindStruct
			case "interface_type":
				kind = core.KindInterface
			}
		}
		raw := spec.Content(src)
		out = append(out, core.SymbolRecord{
			ID:            symID(filePath, name, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "go",
			Kind:          kind,
			Name:          name,
			QualifiedName: name,
			Signature:     firstLine(raw),
			Span:          nodeSpan(spec),
			Exports:       isCapitalized(name),
			RawText:       raw,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
		})
	}
	return out
}

// goConstDecl handles `const X = ...` and grouped `const (X = ...; Y = ...)`.
func goConstDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for i := 0; i < int(n.ChildCount()); i++ {
		spec := n.Child(i)
		if spec == nil || spec.Type() != "const_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		raw := spec.Content(src)
		out = append(out, core.SymbolRecord{
			ID:            symID(filePath, name, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "go",
			Kind:          core.KindConst,
			Name:          name,
			QualifiedName: name,
			Signature:     strings.TrimSpace(raw),
			Span:          nodeSpan(spec),
			Exports:       isCapitalized(name),
			RawText:       raw,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
		})
	}
	return out
}

// goVarDecl handles `var X = ...` and grouped `var (X = ...; Y = ...)`.
// Only package-level var declarations reach this function (called from the root walk).
func goVarDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for i := 0; i < int(n.ChildCount()); i++ {
		spec := n.Child(i)
		if spec == nil || spec.Type() != "var_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		raw := spec.Content(src)
		for _, name := range goIdentifierNames(nameNode, src) {
			out = append(out, core.SymbolRecord{
				ID:            symID(filePath, name, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      "go",
				Kind:          core.KindVariable,
				Name:          name,
				QualifiedName: name,
				Signature:     firstLine(raw),
				Span:          nodeSpan(spec),
				Exports:       isCapitalized(name),
				RawText:       raw,
				Imports:       imports,
				TokenEstimate: estimateTokens(raw),
			})
		}
	}
	return out
}

// goIdentifierNames extracts one or more identifier names from a node that may
// be a bare "identifier" or a comma-separated list.
func goIdentifierNames(n *sitter.Node, src []byte) []string {
	if n.Type() == "identifier" {
		return []string{n.Content(src)}
	}
	var names []string
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == "identifier" {
			names = append(names, c.Content(src))
		}
	}
	return names
}

// ─── TypeScript / JavaScript (including TSX and JSX) ─────────────────────────
//
// JS/TS export detection: the `exported` parameter is set to true when the
// symbol is a direct child of an `export_statement` node. This correctly marks
// lowercase symbols like `export function login()` as exported.

func extractJSNodes(root *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	jsVisit(root, filePath, blobSHA, language, src, imports, "", false, &out)
	return out
}

func jsVisit(node *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		jsVisitChild(node.Child(i), filePath, blobSHA, language, src, imports, parentClass, exported, out)
	}
}

func jsVisitChild(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]core.SymbolRecord) {
	if n == nil {
		return
	}
	switch n.Type() {
	case "function_declaration", "generator_function_declaration":
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, core.KindFunction, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "class_declaration":
		jsClassDecl(n, filePath, blobSHA, language, src, imports, parentClass, exported, out)
	case "interface_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, core.KindInterface, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "type_alias_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, core.KindType, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "enum_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, core.KindEnum, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "internal_module", "module": // TS `namespace Foo {}` / `module Foo {}`
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, core.KindNamespace, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
			body := n.ChildByFieldName("body")
			if body != nil {
				jsVisit(body, filePath, blobSHA, language, src, imports, sym.Name, false, out)
			}
		}
	case "method_definition":
		// Class methods are never themselves exported even if the class is.
		jsMethodDef(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "public_field_definition", "field_definition":
		jsFieldDef(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "export_statement":
		// Unwrap export_statement and mark children as exported.
		jsUnwrapExport(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "lexical_declaration", "variable_declaration":
		// const Foo = () => ... / const Foo = function() { ... }
		jsArrowDecl(n, filePath, blobSHA, language, src, imports, parentClass, exported, out)
	}
}

func jsClassDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, className, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       language,
		Kind:           core.KindClass,
		Name:           className,
		QualifiedName:  className,
		Signature:      firstLine(raw),
		Span:           nodeSpan(n),
		Exports:        exported,
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      jsModifiers(n, src),
		TypeParameters: jsTypeParameters(n, src),
		Annotations:    jsDecorators(n, src),
	})
	// Visit class body for methods. Methods are never directly exported.
	body := n.ChildByFieldName("body")
	if body != nil {
		jsVisit(body, filePath, blobSHA, language, src, imports, className, false, out)
	}
}

func jsMethodDef(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	kind := core.KindMethod
	if name == "constructor" {
		kind = core.KindConstructor
	}
	body := n.ChildByFieldName("body")
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       language,
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           nodeSpan(n),
		Exports:        false, // methods are accessed via their class, not exported directly
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      jsModifiers(n, src),
		TypeParameters: jsTypeParameters(n, src),
		Annotations:    jsDecorators(n, src),
		CallSites:      jsCallSites(body, src),
	})
}

// jsFieldDef emits a Field symbol for a TypeScript/JS class field.
func jsFieldDef(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          core.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       false,
		RawText:       raw,
		ParentSymbol:  parentClass,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
		Modifiers:     jsModifiers(n, src),
		Annotations:   jsDecorators(n, src),
	})
}

func jsNamedSym(n *sitter.Node, field, filePath, blobSHA, language string, src []byte, imports []string, kind core.SymbolKind, parentClass string, exported bool) *core.SymbolRecord {
	nameNode := n.ChildByFieldName(field)
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	k := kind
	if parentClass != "" && kind == core.KindFunction {
		k = core.KindMethod
	}
	body := n.ChildByFieldName("body")
	var callSites []core.CallSite
	if k == core.KindFunction || k == core.KindMethod {
		callSites = jsCallSites(body, src)
	}
	return &core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       language,
		Kind:           k,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           nodeSpan(n),
		Exports:        exported,
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      jsModifiers(n, src),
		TypeParameters: jsTypeParameters(n, src),
		Annotations:    jsDecorators(n, src),
		CallSites:      callSites,
	}
}

// jsUnwrapExport unwraps an export_statement and visits its children with
// exported=true, so that `export function login()` correctly sets Exports=true.
func jsUnwrapExport(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	decl := n.ChildByFieldName("declaration")
	if decl != nil {
		jsVisitChild(decl, filePath, blobSHA, language, src, imports, parentClass, true, out)
		return
	}
	// export default <expr> — iterate direct children for known declaration types
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "function_declaration", "class_declaration",
			"interface_declaration", "type_alias_declaration",
			"lexical_declaration", "variable_declaration",
			"enum_declaration", "generator_function_declaration":
			jsVisitChild(c, filePath, blobSHA, language, src, imports, parentClass, true, out)
		}
	}
}

func jsArrowDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]core.SymbolRecord) {
	for i := 0; i < int(n.ChildCount()); i++ {
		decl := n.Child(i)
		if decl == nil || decl.Type() != "variable_declarator" {
			continue
		}
		nameNode := decl.ChildByFieldName("name")
		valueNode := decl.ChildByFieldName("value")
		if nameNode == nil || valueNode == nil {
			continue
		}
		switch valueNode.Type() {
		case "arrow_function", "function", "function_expression":
			name := nameNode.Content(src)
			raw := decl.Content(src)
			k := core.KindFunction
			if parentClass != "" {
				k = core.KindMethod
			}
			body := valueNode.ChildByFieldName("body")
			*out = append(*out, core.SymbolRecord{
				ID:             symID(filePath, name, blobSHA),
				FilePath:       filePath,
				BlobSHA:        blobSHA,
				Language:       language,
				Kind:           k,
				Name:           name,
				QualifiedName:  name,
				Signature:      firstLine(raw),
				Span:           nodeSpan(decl),
				Exports:        exported,
				RawText:        raw,
				ParentSymbol:   parentClass,
				Imports:        imports,
				TokenEstimate:  estimateTokens(raw),
				TypeParameters: jsTypeParameters(valueNode, src),
				CallSites:      jsCallSites(body, src),
			})
		}
	}
}

// ─── Python ──────────────────────────────────────────────────────────────────

func extractPythonNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	pythonVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func pythonVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		pythonVisitDefinition(n, filePath, blobSHA, src, imports, parentClass, nil, out)
	}
}

func pythonVisitDefinition(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, decorators []string, out *[]core.SymbolRecord) {
	switch n.Type() {
	case "function_definition":
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := nameNode.Content(src)
		kind := core.KindFunction
		if parentClass != "" {
			kind = core.KindMethod
			if name == "__init__" {
				kind = core.KindConstructor
			}
		}
		raw := n.Content(src)
		body := n.ChildByFieldName("body")
		*out = append(*out, core.SymbolRecord{
			ID:            symID(filePath, name, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "python",
			Kind:          kind,
			Name:          name,
			QualifiedName: name,
			Signature:     firstLine(raw),
			Span:          nodeSpan(n),
			Exports:       !strings.HasPrefix(name, "_"),
			RawText:       raw,
			ParentSymbol:  parentClass,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
			Modifiers:     pythonModifiers(name),
			Annotations:   decorators,
			CallSites:     pythonCallSites(body, src),
		})
	case "class_definition":
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		className := nameNode.Content(src)
		raw := n.Content(src)
		*out = append(*out, core.SymbolRecord{
			ID:            symID(filePath, className, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "python",
			Kind:          core.KindClass,
			Name:          className,
			QualifiedName: className,
			Signature:     firstLine(raw),
			Span:          nodeSpan(n),
			Exports:       !strings.HasPrefix(className, "_"),
			RawText:       raw,
			ParentSymbol:  parentClass,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
			Modifiers:     pythonModifiers(className),
			Annotations:   decorators,
		})
		body := n.ChildByFieldName("body")
		if body != nil {
			pythonVisit(body, filePath, blobSHA, src, imports, className, out)
		}
	case "decorated_definition":
		decos := pythonDecorators(n, src)
		for j := 0; j < int(n.ChildCount()); j++ {
			inner := n.Child(j)
			if inner != nil && (inner.Type() == "function_definition" || inner.Type() == "class_definition") {
				pythonVisitDefinition(inner, filePath, blobSHA, src, imports, parentClass, decos, out)
				return
			}
		}
	}
}

// ─── Java ─────────────────────────────────────────────────────────────────────

func extractJavaNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	javaVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func javaVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "class_declaration", "record_declaration":
			javaTypeDecl(n, core.KindClass, filePath, blobSHA, src, imports, parentClass, out)
		case "interface_declaration":
			javaTypeDecl(n, core.KindInterface, filePath, blobSHA, src, imports, parentClass, out)
		case "enum_declaration":
			javaTypeDecl(n, core.KindEnum, filePath, blobSHA, src, imports, parentClass, out)
		case "annotation_type_declaration":
			javaTypeDecl(n, core.KindAnnotation, filePath, blobSHA, src, imports, parentClass, out)
		case "method_declaration":
			if parentClass == "" {
				continue
			}
			javaMethodDecl(n, core.KindMethod, filePath, blobSHA, src, imports, parentClass, out)
		case "constructor_declaration":
			if parentClass == "" {
				continue
			}
			javaMethodDecl(n, core.KindConstructor, filePath, blobSHA, src, imports, parentClass, out)
		case "field_declaration":
			if parentClass == "" {
				continue
			}
			javaFieldDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		}
	}
}

func javaTypeDecl(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	sig := firstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
			break
		}
	}
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, className, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "java",
		Kind:           kind,
		Name:           className,
		QualifiedName:  className,
		Signature:      sig,
		Span:           nodeSpan(n),
		Exports:        exports,
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      modifiers,
		TypeParameters: javaTypeParameters(n, src),
		Annotations:    javaAnnotations(n, src),
	})
	body := n.ChildByFieldName("body")
	if body != nil {
		javaVisit(body, filePath, blobSHA, src, imports, className, out)
	}
}

func javaMethodDecl(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	sig := firstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
		}
	}
	body := n.ChildByFieldName("body")
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "java",
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      sig,
		Span:           nodeSpan(n),
		Exports:        exports,
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      modifiers,
		TypeParameters: javaTypeParameters(n, src),
		Annotations:    javaAnnotations(n, src),
		CallSites:      javaCallSites(body, src),
	})
}

func javaFieldDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	decl := findChildByType(n, "variable_declarator")
	if decl == nil {
		return
	}
	nameNode := decl.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	sig := firstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
		}
	}
	*out = append(*out, core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      "java",
		Kind:          core.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     sig,
		Span:          nodeSpan(n),
		Exports:       exports,
		RawText:       raw,
		ParentSymbol:  parentClass,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
		Modifiers:     modifiers,
		Annotations:   javaAnnotations(n, src),
	})
}

// ─── Rust ─────────────────────────────────────────────────────────────────────

func extractRustNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	rustVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func rustVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, implType string, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_item":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				continue
			}
			name := nameNode.Content(src)
			raw := n.Content(src)
			kind := core.KindFunction
			if implType != "" {
				kind = core.KindMethod
				if name == "new" {
					kind = core.KindConstructor
				}
			}
			body := n.ChildByFieldName("body")
			*out = append(*out, core.SymbolRecord{
				ID:             symID(filePath, name, blobSHA),
				FilePath:       filePath,
				BlobSHA:        blobSHA,
				Language:       "rust",
				Kind:           kind,
				Name:           name,
				QualifiedName:  name,
				Signature:      funcSig(n, src),
				Span:           nodeSpan(n),
				Exports:        strings.HasPrefix(strings.TrimSpace(raw), "pub"),
				RawText:        raw,
				ParentSymbol:   implType,
				Imports:        imports,
				TokenEstimate:  estimateTokens(raw),
				Modifiers:      rustModifiers(n, src),
				TypeParameters: rustTypeParameters(n, src),
				Annotations:    rustAttributes(n, src),
				CallSites:      rustCallSites(body, src),
			})
		case "struct_item":
			rustNamedItem(n, core.KindStruct, filePath, blobSHA, src, imports, out)
			rustStructFields(n, filePath, blobSHA, src, imports, out)
		case "enum_item":
			rustNamedItem(n, core.KindEnum, filePath, blobSHA, src, imports, out)
		case "trait_item":
			rustNamedItem(n, core.KindTrait, filePath, blobSHA, src, imports, out)
		case "type_item":
			rustNamedItem(n, core.KindType, filePath, blobSHA, src, imports, out)
		case "impl_item":
			rustImplItem(n, filePath, blobSHA, src, imports, out)
		}
	}
}

func rustNamedItem(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA string, src []byte, imports []string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "rust",
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      firstLine(raw),
		Span:           nodeSpan(n),
		Exports:        strings.HasPrefix(strings.TrimSpace(raw), "pub"),
		RawText:        raw,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      rustModifiers(n, src),
		TypeParameters: rustTypeParameters(n, src),
		Annotations:    rustAttributes(n, src),
	})
}

// rustStructFields emits a Field symbol for each named field of a Rust struct.
func rustStructFields(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, out *[]core.SymbolRecord) {
	structName := ""
	if nm := n.ChildByFieldName("name"); nm != nil {
		structName = nm.Content(src)
	}
	body := findChildByType(n, "field_declaration_list")
	if body == nil {
		return
	}
	for i := 0; i < int(body.ChildCount()); i++ {
		fd := body.Child(i)
		if fd == nil || fd.Type() != "field_declaration" {
			continue
		}
		nameNode := fd.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		raw := fd.Content(src)
		*out = append(*out, core.SymbolRecord{
			ID:            symID(filePath, name, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "rust",
			Kind:          core.KindField,
			Name:          name,
			QualifiedName: name,
			Signature:     firstLine(raw),
			Span:          nodeSpan(fd),
			Exports:       strings.HasPrefix(strings.TrimSpace(raw), "pub"),
			RawText:       raw,
			ParentSymbol:  structName,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
			Modifiers:     rustModifiers(fd, src),
			Annotations:   rustAttributes(fd, src),
		})
	}
}

func rustImplItem(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, out *[]core.SymbolRecord) {
	typeNode := n.ChildByFieldName("type")
	if typeNode == nil {
		return
	}
	typeName := typeNode.Content(src)
	// Strip generic parameters: "Service<T>" → "Service"
	if idx := strings.IndexByte(typeName, '<'); idx >= 0 {
		typeName = typeName[:idx]
	}
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	rustVisit(body, filePath, blobSHA, src, imports, typeName, out)
}

// ─── C / C++ ─────────────────────────────────────────────────────────────────
//
// C and C++ share the same extractor — the C++ grammar is a superset of C, and
// both use identical node types for the constructs we care about (functions,
// structs, enums, classes in C++).

func extractCNodes(root *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for i := 0; i < int(root.ChildCount()); i++ {
		n := root.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_definition":
			if sym := cFuncSym(n, filePath, blobSHA, language, src, imports, ""); sym != nil {
				out = append(out, *sym)
			}
		case "declaration":
			// Catches typedef struct, extern function declarations, etc.
			out = append(out, cDeclarationSyms(n, filePath, blobSHA, language, src, imports)...)
		case "struct_specifier", "union_specifier":
			if sym := cTaggedTypeSym(n, core.KindStruct, filePath, blobSHA, language, src, imports); sym != nil {
				out = append(out, *sym)
			}
		case "enum_specifier":
			if sym := cTaggedTypeSym(n, core.KindEnum, filePath, blobSHA, language, src, imports); sym != nil {
				out = append(out, *sym)
			}
		case "type_definition":
			out = append(out, cTypedefSyms(n, filePath, blobSHA, language, src, imports)...)
		// C++ only
		case "class_specifier":
			out = append(out, cppClassSym(n, filePath, blobSHA, language, src, imports)...)
		case "namespace_definition":
			if sym := cppNamespaceSym(n, filePath, blobSHA, language, src, imports); sym != nil {
				out = append(out, *sym)
			}
			// Recurse into namespace body to find enclosed classes, functions, etc.
			if body := n.ChildByFieldName("body"); body != nil {
				out = append(out, extractCNodes(body, filePath, blobSHA, language, src, imports)...)
			}
		case "template_declaration":
			out = append(out, cppTemplateDecl(n, filePath, blobSHA, language, src, imports)...)
		}
	}
	return out
}

func cFuncSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string) *core.SymbolRecord {
	// function_definition: type declarator body
	declarator := n.ChildByFieldName("declarator")
	if declarator == nil {
		return nil
	}
	name := cDeclaratorName(declarator, src)
	if name == "" {
		return nil
	}
	raw := n.Content(src)
	kind := core.KindFunction
	if parentClass != "" {
		kind = core.KindMethod
	}
	return &core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     funcSig(n, src),
		Span:          nodeSpan(n),
		Exports:       !strings.HasPrefix(name, "_"),
		RawText:       raw,
		ParentSymbol:  parentClass,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
	}
}

// cDeclaratorName walks nested declarator nodes (pointer_declarator,
// function_declarator, etc.) to extract the final identifier name.
func cDeclaratorName(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	switch n.Type() {
	case "identifier", "field_identifier":
		return n.Content(src)
	case "pointer_declarator", "function_declarator",
		"abstract_pointer_declarator", "qualified_identifier":
		for i := 0; i < int(n.ChildCount()); i++ {
			if name := cDeclaratorName(n.Child(i), src); name != "" {
				return name
			}
		}
	case "destructor_name": // C++ ~ClassName
		return n.Content(src)
	case "operator_name": // C++ operator overload
		return n.Content(src)
	}
	return ""
}

func cDeclarationSyms(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	// Pick up top-level variable/function declarations (e.g. extern declarations).
	var out []core.SymbolRecord
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "function_declarator" {
			name := cDeclaratorName(child, src)
			if name == "" {
				continue
			}
			raw := n.Content(src)
			out = append(out, core.SymbolRecord{
				ID:            symID(filePath, name, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      language,
				Kind:          core.KindFunction,
				Name:          name,
				QualifiedName: name,
				Signature:     strings.TrimSpace(raw),
				Span:          nodeSpan(n),
				Exports:       !strings.HasPrefix(name, "_"),
				RawText:       raw,
				Imports:       imports,
				TokenEstimate: estimateTokens(raw),
			})
		}
	}
	return out
}

// cTypedefSyms handles `typedef struct {...} Name` and similar typedef forms.
// The alias name lives in the child with field "declarator".
func cTypedefSyms(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	declaratorNode := n.ChildByFieldName("declarator")
	if declaratorNode == nil {
		return nil
	}
	name := ""
	switch declaratorNode.Type() {
	case "type_identifier":
		name = declaratorNode.Content(src)
	default:
		// Pointer typedef: typedef struct {...} *PName — find type_identifier child.
		for i := 0; i < int(declaratorNode.ChildCount()); i++ {
			if c := declaratorNode.Child(i); c != nil && c.Type() == "type_identifier" {
				name = c.Content(src)
				break
			}
		}
	}
	if name == "" {
		return nil
	}
	kind := core.KindType
	if typeNode := n.ChildByFieldName("type"); typeNode != nil {
		switch typeNode.Type() {
		case "struct_specifier", "union_specifier":
			kind = core.KindStruct
		case "enum_specifier":
			kind = core.KindEnum
		}
	}
	raw := n.Content(src)
	return []core.SymbolRecord{{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       true,
		RawText:       raw,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
	}}
}

func cTaggedTypeSym(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA, language string, src []byte, imports []string) *core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	return &core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       true,
		RawText:       raw,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
	}
}

func cppClassSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	out := []core.SymbolRecord{{
		ID:            symID(filePath, className, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          core.KindClass,
		Name:          className,
		QualifiedName: className,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       true,
		RawText:       raw,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
	}}
	// Extract member functions from the class body.
	body := n.ChildByFieldName("body")
	if body == nil {
		return out
	}
	for i := 0; i < int(body.ChildCount()); i++ {
		child := body.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "function_definition" {
			if sym := cFuncSym(child, filePath, blobSHA, language, src, imports, className); sym != nil {
				out = append(out, *sym)
			}
		}
	}
	return out
}

func cppNamespaceSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) *core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	return &core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      language,
		Kind:          core.KindNamespace,
		Name:          name,
		QualifiedName: name,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       true,
		RawText:       raw,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
	}
}

func cppTemplateDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []core.SymbolRecord {
	// template<...> function_definition | class_specifier
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "function_definition":
			if sym := cFuncSym(child, filePath, blobSHA, language, src, imports, ""); sym != nil {
				return []core.SymbolRecord{*sym}
			}
		case "class_specifier":
			return cppClassSym(child, filePath, blobSHA, language, src, imports)
		}
	}
	return nil
}

// ─── C# ───────────────────────────────────────────────────────────────────────

func extractCSharpNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	csVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func csVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "namespace_declaration":
			nameNode := n.ChildByFieldName("name")
			if nameNode == nil {
				break
			}
			nsName := nameNode.Content(src)
			raw := n.Content(src)
			*out = append(*out, core.SymbolRecord{
				ID:            symID(filePath, nsName, blobSHA),
				FilePath:      filePath,
				BlobSHA:       blobSHA,
				Language:      "csharp",
				Kind:          core.KindNamespace,
				Name:          nsName,
				QualifiedName: nsName,
				Signature:     firstLine(raw),
				Span:          nodeSpan(n),
				Exports:       true,
				RawText:       raw,
				Imports:       imports,
				TokenEstimate: estimateTokens(raw),
			})
			if body := n.ChildByFieldName("body"); body != nil {
				csVisit(body, filePath, blobSHA, src, imports, "", out)
			}
		case "class_declaration", "record_declaration":
			csTypeDecl(n, core.KindClass, filePath, blobSHA, src, imports, parentClass, out)
		case "struct_declaration":
			csTypeDecl(n, core.KindStruct, filePath, blobSHA, src, imports, parentClass, out)
		case "interface_declaration":
			csTypeDecl(n, core.KindInterface, filePath, blobSHA, src, imports, parentClass, out)
		case "enum_declaration":
			csTypeDecl(n, core.KindEnum, filePath, blobSHA, src, imports, parentClass, out)
		case "method_declaration", "constructor_declaration", "destructor_declaration":
			csMethodDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		case "property_declaration":
			csPropertyDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		case "field_declaration":
			csFieldDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		}
	}
}

func csTypeDecl(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "csharp",
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      firstLine(raw),
		Span:           nodeSpan(n),
		Exports:        csIsExported(modifiers),
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      modifiers,
		TypeParameters: csTypeParams(n, src),
		Annotations:    csAttributes(n, src),
	})
	if body := n.ChildByFieldName("body"); body != nil {
		csVisit(body, filePath, blobSHA, src, imports, name, out)
	}
}

func csMethodDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	kind := core.KindMethod
	if n.Type() == "constructor_declaration" {
		kind = core.KindConstructor
	}
	*out = append(*out, core.SymbolRecord{
		ID:             symID(filePath, name, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       "csharp",
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           nodeSpan(n),
		Exports:        csIsExported(modifiers),
		RawText:        raw,
		ParentSymbol:   parentClass,
		Imports:        imports,
		TokenEstimate:  estimateTokens(raw),
		Modifiers:      modifiers,
		TypeParameters: csTypeParams(n, src),
		Annotations:    csAttributes(n, src),
	})
}

func csPropertyDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	*out = append(*out, core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      "csharp",
		Kind:          core.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       csIsExported(modifiers),
		RawText:       raw,
		ParentSymbol:  parentClass,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
		Modifiers:     modifiers,
		Annotations:   csAttributes(n, src),
	})
}

func csFieldDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	modifiers := csModifiers(n, src)
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child == nil || child.Type() != "variable_declaration" {
			continue
		}
		nameNode := child.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		name := nameNode.Content(src)
		raw := child.Content(src)
		*out = append(*out, core.SymbolRecord{
			ID:            symID(filePath, name, blobSHA),
			FilePath:      filePath,
			BlobSHA:       blobSHA,
			Language:      "csharp",
			Kind:          core.KindField,
			Name:          name,
			QualifiedName: name,
			Signature:     firstLine(raw),
			Span:          nodeSpan(child),
			Exports:       csIsExported(modifiers),
			RawText:       raw,
			ParentSymbol:  parentClass,
			Imports:       imports,
			TokenEstimate: estimateTokens(raw),
			Modifiers:     modifiers,
		})
	}
}

func csModifiers(n *sitter.Node, src []byte) []string {
	var mods []string
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child != nil && child.Type() == "modifier" {
			mods = append(mods, child.Content(src))
		}
	}
	return mods
}

func csIsExported(modifiers []string) bool {
	for _, m := range modifiers {
		if m == "public" || m == "protected" || m == "internal" {
			return true
		}
	}
	return false
}

func csTypeParams(n *sitter.Node, src []byte) []string {
	tp := findChildByType(n, "type_parameter_list")
	if tp == nil {
		return nil
	}
	var params []string
	for i := 0; i < int(tp.ChildCount()); i++ {
		child := tp.Child(i)
		if child != nil && child.Type() == "type_parameter" {
			params = append(params, child.Content(src))
		}
	}
	return params
}

func csAttributes(n *sitter.Node, src []byte) []string {
	var attrs []string
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child != nil && child.Type() == "attribute_list" {
			attrs = append(attrs, child.Content(src))
		}
	}
	return attrs
}

// ─── PHP ─────────────────────────────────────────────────────────────────────

func extractPHPNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []core.SymbolRecord {
	var out []core.SymbolRecord
	// PHP files have a program node; sometimes wrapped in php_tag + program.
	phpVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func phpVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]core.SymbolRecord) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "function_definition":
			if sym := phpFuncSym(n, filePath, blobSHA, src, imports, parentClass); sym != nil {
				*out = append(*out, *sym)
			}
		case "class_declaration":
			phpClassDecl(n, core.KindClass, filePath, blobSHA, src, imports, out)
		case "interface_declaration":
			phpClassDecl(n, core.KindInterface, filePath, blobSHA, src, imports, out)
		case "trait_declaration":
			phpClassDecl(n, core.KindTrait, filePath, blobSHA, src, imports, out)
		case "enum_declaration":
			phpClassDecl(n, core.KindEnum, filePath, blobSHA, src, imports, out)
		case "method_declaration":
			if sym := phpFuncSym(n, filePath, blobSHA, src, imports, parentClass); sym != nil {
				*out = append(*out, *sym)
			}
		default:
			// Recurse into program, namespace_definition, compound_statement, etc.
			phpVisit(n, filePath, blobSHA, src, imports, parentClass, out)
		}
	}
}

func phpFuncSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string) *core.SymbolRecord {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	kind := core.KindFunction
	if parentClass != "" {
		kind = core.KindMethod
		if strings.EqualFold(name, "__construct") {
			kind = core.KindConstructor
		}
	}
	return &core.SymbolRecord{
		ID:            symID(filePath, name, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      "php",
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     funcSig(n, src),
		Span:          nodeSpan(n),
		Exports:       phpIsExported(n, src),
		RawText:       raw,
		ParentSymbol:  parentClass,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
		Modifiers:     phpModifiers(n, src),
	}
}

func phpClassDecl(n *sitter.Node, kind core.SymbolKind, filePath, blobSHA string, src []byte, imports []string, out *[]core.SymbolRecord) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, core.SymbolRecord{
		ID:            symID(filePath, className, blobSHA),
		FilePath:      filePath,
		BlobSHA:       blobSHA,
		Language:      "php",
		Kind:          kind,
		Name:          className,
		QualifiedName: className,
		Signature:     firstLine(raw),
		Span:          nodeSpan(n),
		Exports:       true,
		RawText:       raw,
		Imports:       imports,
		TokenEstimate: estimateTokens(raw),
		Modifiers:     phpModifiers(n, src),
	})
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	phpVisit(body, filePath, blobSHA, src, imports, className, out)
}

func phpModifiers(n *sitter.Node, src []byte) []string {
	var mods []string
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "visibility_modifier", "static_modifier", "abstract_modifier", "final_modifier":
			mods = append(mods, child.Content(src))
		}
	}
	return mods
}

func phpIsExported(n *sitter.Node, src []byte) bool {
	for _, m := range phpModifiers(n, src) {
		if m == "public" {
			return true
		}
	}
	// Top-level functions (no class parent) are always accessible.
	return true
}

// ─── Shared helpers ───────────────────────────────────────────────────────────

// symID returns the canonical symbol ID: "{filePath}::{name}@{blobSHA}".
func symID(filePath, name, blobSHA string) string {
	return fmt.Sprintf("%s::%s@%s", filePath, name, blobSHA)
}

// nodeSpan converts Tree-sitter (0-indexed) row numbers to 1-indexed LineRange.
func nodeSpan(n *sitter.Node) core.LineRange {
	return core.LineRange{
		Start: int(n.StartPoint().Row) + 1,
		End:   int(n.EndPoint().Row) + 1,
	}
}

// funcSig returns the function/method signature without the body.
func funcSig(n *sitter.Node, src []byte) string {
	body := n.ChildByFieldName("body")
	if body == nil {
		return strings.TrimSpace(n.Content(src))
	}
	start := n.StartByte()
	bodyStart := body.StartByte()
	if bodyStart <= start {
		return firstLine(n.Content(src))
	}
	sig := strings.TrimSpace(string(src[start:bodyStart]))
	sig = strings.TrimRight(sig, " \t\n{")
	return strings.TrimSpace(sig)
}

// firstLine returns the first non-empty line of text.
func firstLine(text string) string {
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		return strings.TrimSpace(text[:idx])
	}
	return strings.TrimSpace(text)
}

// isCapitalized reports whether name begins with an uppercase letter
// (Go export convention). Do NOT use for JS/TS — use the exported parameter.
func isCapitalized(name string) bool {
	return len(name) > 0 && name[0] >= 'A' && name[0] <= 'Z'
}
