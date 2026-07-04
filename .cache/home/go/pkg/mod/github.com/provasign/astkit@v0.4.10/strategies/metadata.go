package strategies

import (
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/internalast"
)

var _ = internalast.NodeSpan // imported by extractors.go callers

// ─── Generic call-site walker ────────────────────────────────────────────────
//
// collectCallSites walks every descendant of `body` and returns a CallSite for
// every call-expression node it encounters. Per-language wrappers map the
// idiomatic node types and callee field name.

type callSpec struct {
	nodeTypes []string // call-expression node types in this language
	calleeFn  func(call *sitter.Node, src []byte) string
}

// qualifierName reduces a callee's receiver operand to one identifier so
// CallSite.Callee can honor the documented "Receiver.callee" form:
//
//	r.Write(b)            → "r"
//	c.Writer.Flush()      → "Writer"  (last selector segment)
//	c.Writer.Header().Set → "Header()" (receiver is a call result)
//
// Unknown operand shapes (index expressions, parenthesized expressions)
// return "" and the callee stays bare — same behavior as before.
// selectorTypes maps this grammar's selector node type to its name field;
// callTypes maps its call node type to its function field.
func qualifierName(op *sitter.Node, src []byte, identTypes []string, selectorTypes, callTypes map[string]string) string {
	if op == nil {
		return ""
	}
	t := op.Type()
	for _, it := range identTypes {
		if t == it {
			return string(op.Content(src))
		}
	}
	if nameField, ok := selectorTypes[t]; ok {
		if f := op.ChildByFieldName(nameField); f != nil {
			return string(f.Content(src))
		}
		return ""
	}
	if fnField, ok := callTypes[t]; ok {
		inner := op.ChildByFieldName(fnField)
		if inner == nil {
			return ""
		}
		if name := qualifierName(inner, src, identTypes, selectorTypes, callTypes); name != "" {
			return name + "()"
		}
		// The inner function is itself a selector: reuse its name segment.
		for selType, nameField := range selectorTypes {
			if inner.Type() == selType {
				if f := inner.ChildByFieldName(nameField); f != nil {
					return string(f.Content(src)) + "()"
				}
			}
		}
		return ""
	}
	return ""
}

func joinQualified(qualifier, name string) string {
	if qualifier == "" {
		return name
	}
	// Identifier tokens can't contain whitespace; reject anything that
	// slipped through except the deliberate "name()" call-result marker.
	bare := strings.TrimSuffix(qualifier, "()")
	if strings.ContainsAny(bare, " \t\n().") {
		return name
	}
	return qualifier + "." + name
}

func collectCallSites(body *sitter.Node, src []byte, spec callSpec) []astkit.CallSite {
	if body == nil {
		return nil
	}
	var out []astkit.CallSite
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		for _, t := range spec.nodeTypes {
			if n.Type() == t {
				callee := spec.calleeFn(n, src)
				if callee != "" {
					out = append(out, astkit.CallSite{
						Callee: callee,
						Line:   int(n.StartPoint().Row) + 1,
						Argc:   callArgc(n),
						Args:   callArgs(n, src),
					})
				}
				break
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(body)
	return out
}

// callArgc counts argument expressions in a call node's argument list.
func callArgc(call *sitter.Node) int {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return 0
	}
	return int(args.NamedChildCount())
}

// callArgs returns, per argument: the identifier text for bare identifiers,
// a "#type" literal marker for literals (so consumers can type-match
// overloads), or "" for complex expressions. Nil when nothing is known.
func callArgs(call *sitter.Node, src []byte) []string {
	args := call.ChildByFieldName("arguments")
	if args == nil {
		return nil
	}
	n := int(args.NamedChildCount())
	out := make([]string, n)
	any := false
	for i := 0; i < n; i++ {
		c := args.NamedChild(i)
		if c == nil {
			continue
		}
		if v := argToken(c, src); v != "" {
			out[i] = v
			any = true
		}
	}
	if !any {
		return nil
	}
	return out
}

// argToken classifies one argument node: identifier name, or a literal
// type marker. Covers the literal node types of the Java/TS/JS/Go/Python
// grammars; unknown shapes return "".
func argToken(c *sitter.Node, src []byte) string {
	switch c.Type() {
	case "identifier":
		return string(c.Content(src))
	case "string_literal", "interpreted_string_literal", "raw_string_literal", "string":
		return "#String"
	case "character_literal", "rune_literal":
		return "#char"
	case "decimal_integer_literal", "hex_integer_literal", "octal_integer_literal",
		"binary_integer_literal", "int_literal", "integer":
		text := string(c.Content(src))
		switch {
		case strings.HasSuffix(text, "L") || strings.HasSuffix(text, "l"):
			return "#long"
		case strings.HasSuffix(text, "F") || strings.HasSuffix(text, "f"):
			return "#float"
		case strings.HasSuffix(text, "D") || strings.HasSuffix(text, "d"):
			return "#double"
		}
		return "#int"
	case "decimal_floating_point_literal", "float_literal", "float":
		text := string(c.Content(src))
		if strings.HasSuffix(text, "F") || strings.HasSuffix(text, "f") {
			return "#float"
		}
		return "#double"
	case "true", "false":
		return "#boolean"
	case "cast_expression":
		// "(char[]) obj" — the canonical Java overload disambiguator.
		if t := c.ChildByFieldName("type"); t != nil {
			typ := strings.TrimSpace(string(t.Content(src)))
			if i := strings.IndexByte(typ, '<'); i >= 0 {
				typ = typ[:i]
			}
			return "#" + typ
		}
	case "field_access":
		// array.length is always int.
		if f := c.ChildByFieldName("field"); f != nil && string(f.Content(src)) == "length" {
			return "#int"
		}
	case "method_invocation", "call_expression", "call":
		// Consumers can resolve the called function's return type.
		if n := c.ChildByFieldName("name"); n != nil {
			return "call:" + string(n.Content(src))
		}
		if fn := c.ChildByFieldName("function"); fn != nil && fn.Type() == "identifier" {
			return "call:" + string(fn.Content(src))
		}
	}
	return ""
}

func goCallSites(body *sitter.Node, src []byte) []astkit.CallSite {
	identTypes := []string{"identifier"}
	selectorTypes := map[string]string{"selector_expression": "field"}
	callTypes := map[string]string{"call_expression": "function"}
	return collectCallSites(body, src, callSpec{
		nodeTypes: []string{"call_expression"},
		calleeFn: func(call *sitter.Node, src []byte) string {
			fn := call.ChildByFieldName("function")
			if fn == nil {
				return ""
			}
			switch fn.Type() {
			case "identifier":
				return fn.Content(src)
			case "selector_expression":
				field := fn.ChildByFieldName("field")
				if field != nil {
					qual := qualifierName(fn.ChildByFieldName("operand"), src, identTypes, selectorTypes, callTypes)
					return joinQualified(qual, string(field.Content(src)))
				}
			}
			return ""
		},
	})
}

func jsCallSites(body *sitter.Node, src []byte) []astkit.CallSite {
	return collectCallSites(body, src, callSpec{
		nodeTypes: []string{"call_expression", "new_expression"},
		calleeFn: func(call *sitter.Node, src []byte) string {
			fn := call.ChildByFieldName("function")
			if fn == nil {
				// new_expression uses "constructor" field
				fn = call.ChildByFieldName("constructor")
			}
			if fn == nil {
				return ""
			}
			switch fn.Type() {
			case "super":
				// super(...) invokes the base class constructor; graph
				// consumers resolve it through the extends relation.
				return "super()"
			case "identifier", "property_identifier":
				return fn.Content(src)
			case "member_expression":
				prop := fn.ChildByFieldName("property")
				if prop != nil {
					qual := qualifierName(fn.ChildByFieldName("object"), src,
						[]string{"identifier", "property_identifier", "this", "super"},
						map[string]string{"member_expression": "property"},
						map[string]string{"call_expression": "function"})
					return joinQualified(qual, string(prop.Content(src)))
				}
			}
			return ""
		},
	})
}

func pythonCallSites(body *sitter.Node, src []byte) []astkit.CallSite {
	return collectCallSites(body, src, callSpec{
		nodeTypes: []string{"call"},
		calleeFn: func(call *sitter.Node, src []byte) string {
			fn := call.ChildByFieldName("function")
			if fn == nil {
				return ""
			}
			switch fn.Type() {
			case "identifier":
				return fn.Content(src)
			case "attribute":
				attr := fn.ChildByFieldName("attribute")
				if attr != nil {
					qual := qualifierName(fn.ChildByFieldName("object"), src,
						[]string{"identifier"},
						map[string]string{"attribute": "attribute"},
						map[string]string{"call": "function"})
					return joinQualified(qual, string(attr.Content(src)))
				}
			}
			return ""
		},
	})
}

// pythonAttrSites collects attribute accesses that are not the function of
// a call ("self.db", "request.blueprints") — the access pattern through
// which @property code executes. One site per distinct access text.
func pythonAttrSites(body *sitter.Node, src []byte) []astkit.CallSite {
	if body == nil {
		return nil
	}
	identTypes := []string{"identifier"}
	selectorTypes := map[string]string{"attribute": "attribute"}
	callTypes := map[string]string{"call": "function"}
	seen := map[string]bool{}
	var out []astkit.CallSite
	var walk func(*sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		if n.Type() == "attribute" {
			parent := n.Parent()
			isCallFn := false
			if parent != nil && parent.Type() == "call" {
				// Node lookups aren't pointer-stable; compare by span.
				if fn := parent.ChildByFieldName("function"); fn != nil {
					isCallFn = fn.StartByte() == n.StartByte() && fn.EndByte() == n.EndByte()
				}
			}
			if !isCallFn {
				if attr := n.ChildByFieldName("attribute"); attr != nil {
					qual := qualifierName(n.ChildByFieldName("object"), src, identTypes, selectorTypes, callTypes)
					name := joinQualified(qual, string(attr.Content(src)))
					if !seen[name] {
						seen[name] = true
						out = append(out, astkit.CallSite{
							Callee: name,
							Line:   int(n.StartPoint().Row) + 1,
						})
					}
				}
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(body)
	return out
}

func javaCallSites(body *sitter.Node, src []byte) []astkit.CallSite {
	return collectCallSites(body, src, callSpec{
		nodeTypes: []string{"method_invocation", "object_creation_expression"},
		calleeFn: func(call *sitter.Node, src []byte) string {
			if call.Type() == "object_creation_expression" {
				t := call.ChildByFieldName("type")
				if t != nil {
					// "new Range<>(...)" → callee "Range", not "Range<>".
					name := strings.TrimSpace(t.Content(src))
					if i := strings.IndexByte(name, '<'); i >= 0 {
						name = name[:i]
					}
					return name
				}
				return ""
			}
			name := call.ChildByFieldName("name")
			if name != nil {
				qual := qualifierName(call.ChildByFieldName("object"), src,
					[]string{"identifier", "this"},
					map[string]string{"field_access": "field"},
					map[string]string{"method_invocation": "name"})
				return joinQualified(qual, string(name.Content(src)))
			}
			return ""
		},
	})
}

func rustCallSites(body *sitter.Node, src []byte) []astkit.CallSite {
	return collectCallSites(body, src, callSpec{
		nodeTypes: []string{"call_expression", "macro_invocation"},
		calleeFn: func(call *sitter.Node, src []byte) string {
			if call.Type() == "macro_invocation" {
				m := call.ChildByFieldName("macro")
				if m != nil {
					return m.Content(src) + "!"
				}
				return ""
			}
			fn := call.ChildByFieldName("function")
			if fn == nil {
				return ""
			}
			switch fn.Type() {
			case "identifier":
				return fn.Content(src)
			case "field_expression":
				field := fn.ChildByFieldName("field")
				if field != nil {
					qual := qualifierName(fn.ChildByFieldName("value"), src,
						[]string{"identifier", "self"},
						map[string]string{"field_expression": "field"},
						map[string]string{"call_expression": "function"})
					return joinQualified(qual, string(field.Content(src)))
				}
			case "scoped_identifier":
				name := fn.ChildByFieldName("name")
				if name != nil {
					return name.Content(src)
				}
			}
			return ""
		},
	})
}

// ─── Modifiers ───────────────────────────────────────────────────────────────

// jsModifiers extracts TS/JS modifier tokens (accessibility, static, async,
// readonly, abstract, override, declare). Recurses into the node's children
// looking for nodes whose type matches a known modifier keyword.
func jsModifiers(n *sitter.Node, src []byte) []string {
	wanted := map[string]bool{
		"public": true, "private": true, "protected": true,
		"static": true, "readonly": true, "abstract": true,
		"async": true, "override": true, "declare": true,
		"accessibility_modifier": true,
	}
	var out []string
	seen := map[string]bool{}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if wanted[c.Type()] {
			text := strings.TrimSpace(c.Content(src))
			if text == "" {
				continue
			}
			if !seen[text] {
				seen[text] = true
				out = append(out, text)
			}
		}
	}
	return out
}

// javaModifiers walks the "modifiers" child node and returns each token.
// Annotations (children of type "marker_annotation" / "annotation") are
// emitted via javaAnnotations and not duplicated here.
func javaModifiers(n *sitter.Node, src []byte) []string {
	mods := findChildByType(n, "modifiers")
	if mods == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(mods.ChildCount()); i++ {
		c := mods.Child(i)
		if c == nil {
			continue
		}
		switch c.Type() {
		case "marker_annotation", "annotation":
			continue
		default:
			text := strings.TrimSpace(c.Content(src))
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// rustModifiers returns the visibility token(s) for a Rust item.
// Most Rust items expose a "visibility_modifier" node: pub, pub(crate), pub(super), pub(in path).
func rustModifiers(n *sitter.Node, src []byte) []string {
	vis := findChildByType(n, "visibility_modifier")
	if vis == nil {
		return nil
	}
	return []string{strings.TrimSpace(vis.Content(src))}
}

// pythonModifiers maps Python visibility convention to a single modifier.
// Functions/methods starting with `__` are considered private,
// those starting with `_` are protected, others are public.
func pythonModifiers(name string) []string {
	switch {
	case strings.HasPrefix(name, "__") && !strings.HasSuffix(name, "__"):
		return []string{"private"}
	case strings.HasPrefix(name, "_"):
		return []string{"protected"}
	default:
		return []string{"public"}
	}
}

// ─── Type parameters (generics) ──────────────────────────────────────────────

// jsTypeParameters extracts TypeScript generic parameter names from a
// "type_parameters" child node: `<T extends Foo, U>` → ["T", "U"].
func jsTypeParameters(n *sitter.Node, src []byte) []string {
	tp := n.ChildByFieldName("type_parameters")
	if tp == nil {
		tp = findChildByType(n, "type_parameters")
	}
	if tp == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(tp.ChildCount()); i++ {
		c := tp.Child(i)
		if c == nil || c.Type() != "type_parameter" {
			continue
		}
		name := c.ChildByFieldName("name")
		if name == nil {
			// First identifier child is conventionally the parameter name.
			for j := 0; j < int(c.ChildCount()); j++ {
				if cc := c.Child(j); cc != nil && cc.Type() == "type_identifier" {
					name = cc
					break
				}
			}
		}
		if name != nil {
			out = append(out, strings.TrimSpace(name.Content(src)))
		}
	}
	return out
}

// javaTypeParameters extracts Java generic parameter names from a
// "type_parameters" child node.
func javaTypeParameters(n *sitter.Node, src []byte) []string {
	tp := findChildByType(n, "type_parameters")
	if tp == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(tp.ChildCount()); i++ {
		c := tp.Child(i)
		if c == nil || c.Type() != "type_parameter" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			if cc := c.Child(j); cc != nil && cc.Type() == "type_identifier" {
				out = append(out, cc.Content(src))
				break
			}
		}
	}
	return out
}

// rustTypeParameters returns the names from a Rust "type_parameters" node.
func rustTypeParameters(n *sitter.Node, src []byte) []string {
	tp := findChildByType(n, "type_parameters")
	if tp == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(tp.ChildCount()); i++ {
		c := tp.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "type_identifier" || c.Type() == "lifetime" || c.Type() == "constrained_type_parameter" {
			text := strings.TrimSpace(c.Content(src))
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// goTypeParameters returns the names of Go type parameters from a
// "type_parameter_list" child of a function/method/type declaration.
func goTypeParameters(n *sitter.Node, src []byte) []string {
	tp := findChildByType(n, "type_parameter_list")
	if tp == nil {
		// Inside type_declaration → type_spec → type_parameter_list
		for i := 0; i < int(n.ChildCount()); i++ {
			c := n.Child(i)
			if c != nil && (c.Type() == "type_spec" || c.Type() == "alias_declaration") {
				if inner := findChildByType(c, "type_parameter_list"); inner != nil {
					tp = inner
					break
				}
			}
		}
	}
	if tp == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(tp.ChildCount()); i++ {
		c := tp.Child(i)
		if c == nil || c.Type() != "type_parameter_declaration" {
			continue
		}
		for j := 0; j < int(c.ChildCount()); j++ {
			if cc := c.Child(j); cc != nil && cc.Type() == "identifier" {
				out = append(out, cc.Content(src))
			}
		}
	}
	return out
}

// ─── Annotations / decorators ────────────────────────────────────────────────

// pythonDecorators returns the decorator names attached to a function or
// class via a parent "decorated_definition" node.
func pythonDecorators(decoratedDef *sitter.Node, src []byte) []string {
	if decoratedDef == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(decoratedDef.ChildCount()); i++ {
		c := decoratedDef.Child(i)
		if c == nil || c.Type() != "decorator" {
			continue
		}
		// Skip the leading "@".
		text := strings.TrimSpace(c.Content(src))
		text = strings.TrimPrefix(text, "@")
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

// jsDecorators returns the names of decorators applied to a TS class/method.
// Decorators are previous siblings of type "decorator".
func jsDecorators(n *sitter.Node, src []byte) []string {
	parent := n.Parent()
	if parent == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(parent.ChildCount()); i++ {
		c := parent.Child(i)
		if c == nil {
			continue
		}
		// Decorators precede the symbol node in the parent.
		if c.Type() != "decorator" {
			continue
		}
		if c.StartByte() >= n.StartByte() {
			break
		}
		text := strings.TrimSpace(c.Content(src))
		text = strings.TrimPrefix(text, "@")
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

// javaAnnotations extracts `@Annotation` tokens from a Java declaration's
// "modifiers" child.
func javaAnnotations(n *sitter.Node, src []byte) []string {
	mods := findChildByType(n, "modifiers")
	if mods == nil {
		return nil
	}
	var out []string
	for i := 0; i < int(mods.ChildCount()); i++ {
		c := mods.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "marker_annotation" || c.Type() == "annotation" {
			text := strings.TrimSpace(c.Content(src))
			text = strings.TrimPrefix(text, "@")
			if text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// rustAttributes returns the `#[...]` attribute macros attached to a Rust
// item. In tree-sitter-rust, outer attributes appear as previous-sibling
// `attribute_item` nodes; inner attributes appear as child `inner_attribute_item`
// nodes. We collect both.
func rustAttributes(n *sitter.Node, src []byte) []string {
	var out []string
	clean := func(text string) string {
		text = strings.TrimSpace(text)
		text = strings.TrimPrefix(text, "#")
		text = strings.TrimPrefix(text, "!")
		text = strings.TrimPrefix(text, "[")
		text = strings.TrimSuffix(text, "]")
		return strings.TrimSpace(text)
	}
	// Outer attributes — previous siblings of types attribute_item.
	for prev := n.PrevSibling(); prev != nil; prev = prev.PrevSibling() {
		if prev.Type() != "attribute_item" {
			break
		}
		if text := clean(prev.Content(src)); text != "" {
			out = append([]string{text}, out...)
		}
	}
	// Inner attributes — direct children of the item itself.
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c == nil {
			continue
		}
		if c.Type() == "attribute_item" || c.Type() == "inner_attribute_item" {
			if text := clean(c.Content(src)); text != "" {
				out = append(out, text)
			}
		}
	}
	return out
}

// ─── Utilities ───────────────────────────────────────────────────────────────

func findChildByType(n *sitter.Node, typeName string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c := n.Child(i)
		if c != nil && c.Type() == typeName {
			return c
		}
	}
	return nil
}
