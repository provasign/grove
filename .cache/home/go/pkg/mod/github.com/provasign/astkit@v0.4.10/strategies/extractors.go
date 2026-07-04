package strategies

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/internalast"
)

// ─── Go ───────────────────────────────────────────────────────────────────────

// extractGoNodes walks the top-level children of a Go source_file node.
// Only package-level declarations are extracted; symbols inside function
// bodies (local vars, closures) are intentionally skipped.
func extractGoNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
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

func goFuncSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) *astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	body := n.ChildByFieldName("body")
	return &astkit.Symbol{
		Kind:           astkit.KindFunction,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           internalast.NodeSpan(n),
		Exported:       internalast.IsCapitalized(name),
		Body:           raw,
		TypeParameters: goTypeParameters(n, src),
		CallSites:      goCallSites(body, src),
	}
}

func goMethodSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) *astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	receiver := goReceiverTypeName(n, src)
	raw := n.Content(src)
	body := n.ChildByFieldName("body")
	return &astkit.Symbol{
		Kind:           astkit.KindMethod,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           internalast.NodeSpan(n),
		Exported:       internalast.IsCapitalized(name),
		Body:           raw,
		ParentName:     receiver,
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
func goTypeDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
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
		kind := astkit.KindType
		typeNode := spec.ChildByFieldName("type")
		if typeNode != nil {
			switch typeNode.Type() {
			case "struct_type":
				kind = astkit.KindStruct
			case "interface_type":
				kind = astkit.KindInterface
			}
		}
		raw := goStandaloneDecl("type", spec.Content(src))
		out = append(out, astkit.Symbol{
			Kind:          kind,
			Name:          name,
			QualifiedName: name,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(spec),
			Exported:      internalast.IsCapitalized(name),
			Body:          raw,
		})
	}
	return out
}

// goConstDecl handles `const X = ...` and grouped `const (X = ...; Y = ...)`.
func goConstDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
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
		raw := goStandaloneDecl("const", spec.Content(src))
		out = append(out, astkit.Symbol{
			Kind:          astkit.KindConst,
			Name:          name,
			QualifiedName: name,
			Signature:     strings.TrimSpace(raw),
			Span:          internalast.NodeSpan(spec),
			Exported:      internalast.IsCapitalized(name),
			Body:          raw,
		})
	}
	return out
}

// goVarDecl handles `var X = ...` and grouped `var (X = ...; Y = ...)`.
// Only package-level var declarations reach this function (called from the root walk).
func goVarDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	for i := 0; i < int(n.ChildCount()); i++ {
		spec := n.Child(i)
		if spec == nil || spec.Type() != "var_spec" {
			continue
		}
		nameNode := spec.ChildByFieldName("name")
		if nameNode == nil {
			continue
		}
		raw := goStandaloneDecl("var", spec.Content(src))
		for _, name := range goIdentifierNames(nameNode, src) {
			out = append(out, astkit.Symbol{
				Kind:          astkit.KindVariable,
				Name:          name,
				QualifiedName: name,
				Signature:     internalast.FirstLine(raw),
				Span:          internalast.NodeSpan(spec),
				Exported:      internalast.IsCapitalized(name),
				Body:          raw,
			})
		}
	}
	return out
}

func goStandaloneDecl(keyword, raw string) string {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, keyword+" ") || strings.HasPrefix(trimmed, keyword+"(") {
		return raw
	}
	return keyword + " " + raw
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

func extractJSNodes(root *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	jsVisit(root, filePath, blobSHA, language, src, imports, "", false, &out)
	return out
}

func jsVisit(node *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]astkit.Symbol) {
	for i := 0; i < int(node.ChildCount()); i++ {
		jsVisitChild(node.Child(i), filePath, blobSHA, language, src, imports, parentClass, exported, out)
	}
}

func jsVisitChild(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]astkit.Symbol) {
	if n == nil {
		return
	}
	switch n.Type() {
	case "function_declaration", "generator_function_declaration":
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, astkit.KindFunction, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "class_declaration", "abstract_class_declaration":
		jsClassDecl(n, filePath, blobSHA, language, src, imports, parentClass, exported, out)
	case "interface_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, astkit.KindInterface, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "type_alias_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, astkit.KindType, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "enum_declaration": // TypeScript / TSX
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, astkit.KindEnum, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
		}
	case "internal_module", "module": // TS `namespace Foo {}` / `module Foo {}`
		if sym := jsNamedSym(n, "name", filePath, blobSHA, language, src, imports, astkit.KindNamespace, parentClass, exported); sym != nil {
			*out = append(*out, *sym)
			body := n.ChildByFieldName("body")
			if body != nil {
				jsVisit(body, filePath, blobSHA, language, src, imports, sym.Name, false, out)
			}
		}
	case "method_definition", "abstract_method_signature":
		// Class methods are never themselves exported even if the class is.
		// Abstract method signatures are real declarations: they're the
		// dispatch point subclass implementations override, so graph
		// consumers need them as symbols.
		jsMethodDef(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "public_field_definition", "field_definition":
		jsFieldDef(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "export_statement":
		// Unwrap export_statement and mark children as exported.
		jsUnwrapExport(n, filePath, blobSHA, language, src, imports, parentClass, out)
	case "lexical_declaration", "variable_declaration":
		// const Foo = () => ... / const Foo = function() { ... }
		jsArrowDecl(n, filePath, blobSHA, language, src, imports, parentClass, exported, out)
	case "expression_statement":
		// app.listen = function listen() {} / exports.render = () => {} —
		// the assignment-style declarations CommonJS codebases are built on.
		jsAssignFunc(n, filePath, blobSHA, language, src, imports, parentClass, out)
	}
}

// jsAssignFunc extracts `obj.prop = function...` / `obj.prop = (...) => ...`
// assignments as function symbols named by the property. The receiver chain's
// last identifier becomes ParentName ("app.listen = ..." → parent "app"),
// except module-export forms (exports/module.exports/prototype chains keep
// the prototype's class name).
func jsAssignFunc(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	for i := 0; i < int(n.ChildCount()); i++ {
		assign := n.Child(i)
		if assign == nil || assign.Type() != "assignment_expression" {
			continue
		}
		left := assign.ChildByFieldName("left")
		right := assign.ChildByFieldName("right")
		if left == nil || right == nil || left.Type() != "member_expression" {
			continue
		}
		switch right.Type() {
		case "arrow_function", "function", "function_expression":
		default:
			continue
		}
		prop := left.ChildByFieldName("property")
		if prop == nil {
			continue
		}
		name := prop.Content(src)
		parent := parentClass
		if obj := left.ChildByFieldName("object"); obj != nil && parent == "" {
			switch obj.Type() {
			case "identifier":
				if o := obj.Content(src); o != "exports" && o != "module" {
					parent = o
				}
			case "member_expression":
				// X.prototype.method = ... → parent X
				if inner := obj.ChildByFieldName("property"); inner != nil && inner.Content(src) == "prototype" {
					if base := obj.ChildByFieldName("object"); base != nil && base.Type() == "identifier" {
						parent = base.Content(src)
					}
				}
			}
		}
		raw := assign.Content(src)
		k := astkit.KindFunction
		if parent != "" {
			k = astkit.KindMethod
		}
		body := right.ChildByFieldName("body")
		*out = append(*out, astkit.Symbol{
			Kind:          k,
			Name:          name,
			QualifiedName: name,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(assign),
			Exported:      true,
			Body:          raw,
			ParentName:    parent,
			CallSites:     jsCallSites(body, src),
		})
	}
}

func jsClassDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, astkit.Symbol{
		Kind:           astkit.KindClass,
		Name:           className,
		QualifiedName:  className,
		Signature:      internalast.FirstLine(raw),
		Span:           internalast.NodeSpan(n),
		Exported:       exported,
		Body:           raw,
		ParentName:     parentClass,
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

func jsMethodDef(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	kind := astkit.KindMethod
	if name == "constructor" {
		kind = astkit.KindConstructor
	}
	body := n.ChildByFieldName("body")
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           internalast.NodeSpan(n),
		Exported:       false, // methods are accessed via their class, not exported directly
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      jsModifiers(n, src),
		TypeParameters: jsTypeParameters(n, src),
		Annotations:    jsDecorators(n, src),
		CallSites:      jsCallSites(body, src),
	})
}

// jsFieldDef emits a Field symbol for a TypeScript/JS class field.
func jsFieldDef(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, astkit.Symbol{
		Kind:          astkit.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      false,
		Body:          raw,
		ParentName:    parentClass,
		Modifiers:     jsModifiers(n, src),
		Annotations:   jsDecorators(n, src),
	})
}

func jsNamedSym(n *sitter.Node, field, filePath, blobSHA, language string, src []byte, imports []string, kind astkit.SymbolKind, parentClass string, exported bool) *astkit.Symbol {
	nameNode := n.ChildByFieldName(field)
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	k := kind
	if parentClass != "" && kind == astkit.KindFunction {
		k = astkit.KindMethod
	}
	body := n.ChildByFieldName("body")
	var callSites []astkit.CallSite
	if k == astkit.KindFunction || k == astkit.KindMethod {
		callSites = jsCallSites(body, src)
	}
	return &astkit.Symbol{
		Kind:           k,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           internalast.NodeSpan(n),
		Exported:       exported,
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      jsModifiers(n, src),
		TypeParameters: jsTypeParameters(n, src),
		Annotations:    jsDecorators(n, src),
		CallSites:      callSites,
	}
}

// jsUnwrapExport unwraps an export_statement and visits its children with
// exported=true, so that `export function login()` correctly sets Exports=true.
func jsUnwrapExport(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
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
			"abstract_class_declaration",
			"interface_declaration", "type_alias_declaration",
			"lexical_declaration", "variable_declaration",
			"enum_declaration", "generator_function_declaration":
			jsVisitChild(c, filePath, blobSHA, language, src, imports, parentClass, true, out)
		}
	}
}

func jsArrowDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string, exported bool, out *[]astkit.Symbol) {
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
			k := astkit.KindFunction
			if parentClass != "" {
				k = astkit.KindMethod
			}
			body := valueNode.ChildByFieldName("body")
			*out = append(*out, astkit.Symbol{
				Kind:           k,
				Name:           name,
				QualifiedName:  name,
				Signature:      internalast.FirstLine(raw),
				Span:           internalast.NodeSpan(decl),
				Exported:       exported,
				Body:           raw,
				ParentName:     parentClass,
				TypeParameters: jsTypeParameters(valueNode, src),
				CallSites:      jsCallSites(body, src),
			})
		}
	}
}

// ─── Python ──────────────────────────────────────────────────────────────────

func extractPythonNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	pythonVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func pythonVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		pythonVisitDefinition(n, filePath, blobSHA, src, imports, parentClass, nil, out)
	}
}

func pythonVisitDefinition(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, decorators []string, out *[]astkit.Symbol) {
	switch n.Type() {
	case "function_definition":
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		name := nameNode.Content(src)
		kind := astkit.KindFunction
		if parentClass != "" {
			kind = astkit.KindMethod
			if name == "__init__" {
				kind = astkit.KindConstructor
			}
		}
		raw := n.Content(src)
		body := n.ChildByFieldName("body")
		*out = append(*out, astkit.Symbol{
			Kind:          kind,
			Name:          name,
			QualifiedName: name,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(n),
			Exported:      !strings.HasPrefix(name, "_"),
			Body:          raw,
			ParentName:    parentClass,
			Modifiers:     pythonModifiers(name),
			Annotations:   decorators,
			CallSites:     pythonCallSites(body, src),
			AttrSites:     pythonAttrSites(body, src),
		})
	case "class_definition":
		nameNode := n.ChildByFieldName("name")
		if nameNode == nil {
			return
		}
		className := nameNode.Content(src)
		raw := n.Content(src)
		*out = append(*out, astkit.Symbol{
			Kind:          astkit.KindClass,
			Name:          className,
			QualifiedName: className,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(n),
			Exported:      !strings.HasPrefix(className, "_"),
			Body:          raw,
			ParentName:    parentClass,
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

func extractJavaNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	javaVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func javaVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	for i := 0; i < int(node.ChildCount()); i++ {
		n := node.Child(i)
		if n == nil {
			continue
		}
		switch n.Type() {
		case "class_declaration", "record_declaration":
			javaTypeDecl(n, astkit.KindClass, filePath, blobSHA, src, imports, parentClass, out)
		case "interface_declaration":
			javaTypeDecl(n, astkit.KindInterface, filePath, blobSHA, src, imports, parentClass, out)
		case "enum_declaration":
			javaTypeDecl(n, astkit.KindEnum, filePath, blobSHA, src, imports, parentClass, out)
		case "annotation_type_declaration":
			javaTypeDecl(n, astkit.KindAnnotation, filePath, blobSHA, src, imports, parentClass, out)
		case "method_declaration":
			if parentClass == "" {
				continue
			}
			javaMethodDecl(n, astkit.KindMethod, filePath, blobSHA, src, imports, parentClass, out)
		case "constructor_declaration":
			if parentClass == "" {
				continue
			}
			javaMethodDecl(n, astkit.KindConstructor, filePath, blobSHA, src, imports, parentClass, out)
		case "field_declaration":
			if parentClass == "" {
				continue
			}
			javaFieldDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		}
	}
}

func javaTypeDecl(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	sig := internalast.FirstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
			break
		}
	}
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           className,
		QualifiedName:  className,
		Signature:      sig,
		Span:           internalast.NodeSpan(n),
		Exported:       exports,
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      modifiers,
		TypeParameters: javaTypeParameters(n, src),
		Annotations:    javaAnnotations(n, src),
	})
	body := n.ChildByFieldName("body")
	if body != nil {
		javaVisit(body, filePath, blobSHA, src, imports, className, out)
	}
}

func javaMethodDecl(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	sig := internalast.FirstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
		}
	}
	body := n.ChildByFieldName("body")
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      sig,
		Span:           internalast.NodeSpan(n),
		Exported:       exports,
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      modifiers,
		TypeParameters: javaTypeParameters(n, src),
		Annotations:    javaAnnotations(n, src),
		CallSites:      javaCallSites(body, src),
	})
}

func javaFieldDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	decl := internalast.FindChildByType(n, "variable_declarator")
	if decl == nil {
		return
	}
	nameNode := decl.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	sig := internalast.FirstLine(raw)
	modifiers := javaModifiers(n, src)
	exports := strings.Contains(sig, "public")
	for _, m := range modifiers {
		if m == "public" {
			exports = true
		}
	}
	*out = append(*out, astkit.Symbol{
		Kind:          astkit.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     sig,
		Span:          internalast.NodeSpan(n),
		Exported:      exports,
		Body:          raw,
		ParentName:    parentClass,
		Modifiers:     modifiers,
		Annotations:   javaAnnotations(n, src),
	})
}

// ─── Rust ─────────────────────────────────────────────────────────────────────

func extractRustNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	rustVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func rustVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, implType string, out *[]astkit.Symbol) {
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
			kind := astkit.KindFunction
			if implType != "" {
				kind = astkit.KindMethod
				if name == "new" {
					kind = astkit.KindConstructor
				}
			}
			body := n.ChildByFieldName("body")
			*out = append(*out, astkit.Symbol{
				Kind:           kind,
				Name:           name,
				QualifiedName:  name,
				Signature:      funcSig(n, src),
				Span:           internalast.NodeSpan(n),
				Exported:       strings.HasPrefix(strings.TrimSpace(raw), "pub"),
				Body:           raw,
				ParentName:     implType,
				Modifiers:      rustModifiers(n, src),
				TypeParameters: rustTypeParameters(n, src),
				Annotations:    rustAttributes(n, src),
				CallSites:      rustCallSites(body, src),
			})
		case "struct_item":
			rustNamedItem(n, astkit.KindStruct, filePath, blobSHA, src, imports, out)
			rustStructFields(n, filePath, blobSHA, src, imports, out)
		case "enum_item":
			rustNamedItem(n, astkit.KindEnum, filePath, blobSHA, src, imports, out)
		case "trait_item":
			rustNamedItem(n, astkit.KindTrait, filePath, blobSHA, src, imports, out)
		case "type_item":
			rustNamedItem(n, astkit.KindType, filePath, blobSHA, src, imports, out)
		case "impl_item":
			rustImplItem(n, filePath, blobSHA, src, imports, out)
		}
	}
}

func rustNamedItem(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA string, src []byte, imports []string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      internalast.FirstLine(raw),
		Span:           internalast.NodeSpan(n),
		Exported:       strings.HasPrefix(strings.TrimSpace(raw), "pub"),
		Body:           raw,
		Modifiers:      rustModifiers(n, src),
		TypeParameters: rustTypeParameters(n, src),
		Annotations:    rustAttributes(n, src),
	})
}

// rustStructFields emits a Field symbol for each named field of a Rust struct.
func rustStructFields(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, out *[]astkit.Symbol) {
	structName := ""
	if nm := n.ChildByFieldName("name"); nm != nil {
		structName = nm.Content(src)
	}
	body := internalast.FindChildByType(n, "field_declaration_list")
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
		*out = append(*out, astkit.Symbol{
			Kind:          astkit.KindField,
			Name:          name,
			QualifiedName: name,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(fd),
			Exported:      strings.HasPrefix(strings.TrimSpace(raw), "pub"),
			Body:          raw,
			ParentName:    structName,
			Modifiers:     rustModifiers(fd, src),
			Annotations:   rustAttributes(fd, src),
		})
	}
}

func rustImplItem(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, out *[]astkit.Symbol) {
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

func extractCNodes(root *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
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
			if sym := cTaggedTypeSym(n, astkit.KindStruct, filePath, blobSHA, language, src, imports); sym != nil {
				out = append(out, *sym)
			}
		case "enum_specifier":
			if sym := cTaggedTypeSym(n, astkit.KindEnum, filePath, blobSHA, language, src, imports); sym != nil {
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

func cFuncSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string, parentClass string) *astkit.Symbol {
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
	kind := astkit.KindFunction
	if parentClass != "" {
		kind = astkit.KindMethod
	}
	return &astkit.Symbol{
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     funcSig(n, src),
		Span:          internalast.NodeSpan(n),
		Exported:      !strings.HasPrefix(name, "_"),
		Body:          raw,
		ParentName:    parentClass,
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

func cDeclarationSyms(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
	// Pick up top-level variable/function declarations (e.g. extern declarations).
	var out []astkit.Symbol
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
			out = append(out, astkit.Symbol{
				Kind:          astkit.KindFunction,
				Name:          name,
				QualifiedName: name,
				Signature:     strings.TrimSpace(raw),
				Span:          internalast.NodeSpan(n),
				Exported:      !strings.HasPrefix(name, "_"),
				Body:          raw,
			})
		}
	}
	return out
}

// cTypedefSyms handles `typedef struct {...} Name` and similar typedef forms.
// The alias name lives in the child with field "declarator".
func cTypedefSyms(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
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
	kind := astkit.KindType
	if typeNode := n.ChildByFieldName("type"); typeNode != nil {
		switch typeNode.Type() {
		case "struct_specifier", "union_specifier":
			kind = astkit.KindStruct
		case "enum_specifier":
			kind = astkit.KindEnum
		}
	}
	raw := n.Content(src)
	return []astkit.Symbol{{
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      true,
		Body:          raw,
	}}
}

func cTaggedTypeSym(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA, language string, src []byte, imports []string) *astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	return &astkit.Symbol{
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      true,
		Body:          raw,
	}
}

func cppClassSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	out := []astkit.Symbol{{
		Kind:          astkit.KindClass,
		Name:          className,
		QualifiedName: className,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      true,
		Body:          raw,
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

func cppNamespaceSym(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) *astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	return &astkit.Symbol{
		Kind:          astkit.KindNamespace,
		Name:          name,
		QualifiedName: name,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      true,
		Body:          raw,
	}
}

func cppTemplateDecl(n *sitter.Node, filePath, blobSHA, language string, src []byte, imports []string) []astkit.Symbol {
	// template<...> function_definition | class_specifier
	for i := 0; i < int(n.ChildCount()); i++ {
		child := n.Child(i)
		if child == nil {
			continue
		}
		switch child.Type() {
		case "function_definition":
			if sym := cFuncSym(child, filePath, blobSHA, language, src, imports, ""); sym != nil {
				return []astkit.Symbol{*sym}
			}
		case "class_specifier":
			return cppClassSym(child, filePath, blobSHA, language, src, imports)
		}
	}
	return nil
}

// ─── C# ───────────────────────────────────────────────────────────────────────

func extractCSharpNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	csVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func csVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
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
			*out = append(*out, astkit.Symbol{
				Kind:          astkit.KindNamespace,
				Name:          nsName,
				QualifiedName: nsName,
				Signature:     internalast.FirstLine(raw),
				Span:          internalast.NodeSpan(n),
				Exported:      true,
				Body:          raw,
			})
			if body := n.ChildByFieldName("body"); body != nil {
				csVisit(body, filePath, blobSHA, src, imports, "", out)
			}
		case "class_declaration", "record_declaration":
			csTypeDecl(n, astkit.KindClass, filePath, blobSHA, src, imports, parentClass, out)
		case "struct_declaration":
			csTypeDecl(n, astkit.KindStruct, filePath, blobSHA, src, imports, parentClass, out)
		case "interface_declaration":
			csTypeDecl(n, astkit.KindInterface, filePath, blobSHA, src, imports, parentClass, out)
		case "enum_declaration":
			csTypeDecl(n, astkit.KindEnum, filePath, blobSHA, src, imports, parentClass, out)
		case "method_declaration", "constructor_declaration", "destructor_declaration":
			csMethodDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		case "property_declaration":
			csPropertyDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		case "field_declaration":
			csFieldDecl(n, filePath, blobSHA, src, imports, parentClass, out)
		}
	}
}

func csTypeDecl(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      internalast.FirstLine(raw),
		Span:           internalast.NodeSpan(n),
		Exported:       csIsExported(modifiers),
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      modifiers,
		TypeParameters: csTypeParams(n, src),
		Annotations:    csAttributes(n, src),
	})
	if body := n.ChildByFieldName("body"); body != nil {
		csVisit(body, filePath, blobSHA, src, imports, name, out)
	}
}

func csMethodDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	kind := astkit.KindMethod
	if n.Type() == "constructor_declaration" {
		kind = astkit.KindConstructor
	}
	*out = append(*out, astkit.Symbol{
		Kind:           kind,
		Name:           name,
		QualifiedName:  name,
		Signature:      funcSig(n, src),
		Span:           internalast.NodeSpan(n),
		Exported:       csIsExported(modifiers),
		Body:           raw,
		ParentName:     parentClass,
		Modifiers:      modifiers,
		TypeParameters: csTypeParams(n, src),
		Annotations:    csAttributes(n, src),
	})
}

func csPropertyDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	modifiers := csModifiers(n, src)
	*out = append(*out, astkit.Symbol{
		Kind:          astkit.KindField,
		Name:          name,
		QualifiedName: name,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      csIsExported(modifiers),
		Body:          raw,
		ParentName:    parentClass,
		Modifiers:     modifiers,
		Annotations:   csAttributes(n, src),
	})
}

func csFieldDecl(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
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
		*out = append(*out, astkit.Symbol{
			Kind:          astkit.KindField,
			Name:          name,
			QualifiedName: name,
			Signature:     internalast.FirstLine(raw),
			Span:          internalast.NodeSpan(child),
			Exported:      csIsExported(modifiers),
			Body:          raw,
			ParentName:    parentClass,
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
	tp := internalast.FindChildByType(n, "type_parameter_list")
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

func extractPHPNodes(root *sitter.Node, filePath, blobSHA string, src []byte, imports []string) []astkit.Symbol {
	var out []astkit.Symbol
	// PHP files have a program node; sometimes wrapped in php_tag + program.
	phpVisit(root, filePath, blobSHA, src, imports, "", &out)
	return out
}

func phpVisit(node *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string, out *[]astkit.Symbol) {
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
			phpClassDecl(n, astkit.KindClass, filePath, blobSHA, src, imports, out)
		case "interface_declaration":
			phpClassDecl(n, astkit.KindInterface, filePath, blobSHA, src, imports, out)
		case "trait_declaration":
			phpClassDecl(n, astkit.KindTrait, filePath, blobSHA, src, imports, out)
		case "enum_declaration":
			phpClassDecl(n, astkit.KindEnum, filePath, blobSHA, src, imports, out)
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

func phpFuncSym(n *sitter.Node, filePath, blobSHA string, src []byte, imports []string, parentClass string) *astkit.Symbol {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return nil
	}
	name := nameNode.Content(src)
	raw := n.Content(src)
	kind := astkit.KindFunction
	if parentClass != "" {
		kind = astkit.KindMethod
		if strings.EqualFold(name, "__construct") {
			kind = astkit.KindConstructor
		}
	}
	return &astkit.Symbol{
		Kind:          kind,
		Name:          name,
		QualifiedName: name,
		Signature:     funcSig(n, src),
		Span:          internalast.NodeSpan(n),
		Exported:      phpIsExported(n, src),
		Body:          raw,
		ParentName:    parentClass,
		Modifiers:     phpModifiers(n, src),
	}
}

func phpClassDecl(n *sitter.Node, kind astkit.SymbolKind, filePath, blobSHA string, src []byte, imports []string, out *[]astkit.Symbol) {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	className := nameNode.Content(src)
	raw := n.Content(src)
	*out = append(*out, astkit.Symbol{
		Kind:          kind,
		Name:          className,
		QualifiedName: className,
		Signature:     internalast.FirstLine(raw),
		Span:          internalast.NodeSpan(n),
		Exported:      true,
		Body:          raw,
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

// funcSig returns the function/method signature without the body.
func funcSig(n *sitter.Node, src []byte) string {
	body := n.ChildByFieldName("body")
	if body == nil {
		return strings.TrimSpace(n.Content(src))
	}
	start := n.StartByte()
	bodyStart := body.StartByte()
	if bodyStart <= start {
		return internalast.FirstLine(n.Content(src))
	}
	sig := strings.TrimSpace(string(src[start:bodyStart]))
	sig = strings.TrimRight(sig, " \t\n{")
	return strings.TrimSpace(sig)
}
