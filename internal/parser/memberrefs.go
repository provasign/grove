package parser

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/provasign/astkit"
	"github.com/provasign/grove/internal/core"
	sitter "github.com/smacker/go-tree-sitter"
)

// MemberLanguages maps a declaration's language to the languages whose
// files can reference it: C members are reached from C++ and Objective-C
// translation units, Java members from Kotlin, JS/TS modules from each other.
func MemberLanguages(lang string) []string {
	switch lang {
	case "c", "cpp", "objc":
		return []string{"c", "cpp", "objc"}
	case "java", "kotlin":
		return []string{"java", "kotlin"}
	case "typescript", "tsx", "javascript":
		return []string{"typescript", "tsx", "javascript"}
	case "":
		return nil
	}
	return []string{lang}
}

// MemberOccurrences returns every syntactic occurrence of the data-member
// name `name` (a field, property, constant, or variable) in the files of the
// given languages under root, classified by shape (see core.MemberOccurrence).
// Comments and string literals are excluded by construction — only
// identifier nodes are inspected. The second return counts files that could
// not be read, so a partial scan is never reported as a complete one.
//
// Like References this is resolution-free: it says HOW each occurrence
// reaches the name (receiver text, literal type, bare identifier), and the
// graph decides which declaration each one binds to.
func (e *Engine) MemberOccurrences(root, name string, languages []string) ([]core.MemberOccurrence, int, error) {
	leaf := leafName(name)
	if leaf == "" {
		return nil, 0, nil
	}
	want := map[astkit.LanguageKey]bool{}
	for _, l := range languages {
		want[astkit.LanguageKey(l)] = true
	}
	leafBytes := []byte(leaf)
	type task struct{ abs, rel string }
	var tasks []task
	var skipped atomic.Int64
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			skipped.Add(1)
			return nil
		}
		if info.IsDir() {
			if p != root && refSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if lang := astkit.DetectLanguage(p, ""); lang != astkit.LangUnknown {
			// .h may be C or C++; content decides later, so admit any
			// C-family header when any C-family language is wanted.
			if want[lang] || (strings.EqualFold(filepath.Ext(p), ".h") && (want[astkit.LangC] || want[astkit.LangCPP] || want[astkit.LangObjC])) {
				tasks = append(tasks, task{abs: p, rel: filepath.ToSlash(relPath(root, p))})
			}
		}
		return nil
	})
	results := make([][]core.MemberOccurrence, len(tasks))
	if len(tasks) > 0 {
		workers := runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8
		}
		if workers > len(tasks) {
			workers = len(tasks)
		}
		ch := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				eng := astkit.NewEngine()
				for i := range ch {
					t := tasks[i]
					src, rerr := os.ReadFile(t.abs)
					if rerr != nil {
						skipped.Add(1)
						continue
					}
					if !bytes.Contains(src, leafBytes) {
						continue
					}
					lang := astkit.DetectLanguage(t.abs, string(src))
					if lang == astkit.LangObjC && !want[lang] && strings.EqualFold(filepath.Ext(t.abs), ".h") {
						lang = astkit.LangC
					}
					tree, perr := eng.Parse(context.Background(), lang, src)
					if perr != nil || tree == nil {
						skipped.Add(1)
						continue
					}
					results[i] = memberOccurrencesIn(tree.RootNode(), src, string(lang), leaf, t.rel)
				}
			}()
		}
		for i := range tasks {
			ch <- i
		}
		close(ch)
		wg.Wait()
	}
	var out []core.MemberOccurrence
	for _, r := range results {
		out = append(out, r...)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, int(skipped.Load()), err
}

// memberHitTypes are the identifier node kinds a member name can occupy.
var memberHitTypes = map[string]bool{
	"identifier": true, "field_identifier": true, "property_identifier": true,
	"shorthand_property_identifier": true, "name": true, "simple_identifier": true,
	"private_property_identifier": true, "type_identifier": false,
}

func memberOccurrencesIn(root *sitter.Node, src []byte, lang, leaf, file string) []core.MemberOccurrence {
	lines := bytes.Split(src, []byte("\n"))
	pkg := ""
	if lang == "go" {
		for i := 0; i < int(root.NamedChildCount()); i++ {
			if c := root.NamedChild(i); c.Type() == "package_clause" && c.NamedChildCount() > 0 {
				pkg = c.NamedChild(0).Content(src)
				break
			}
		}
	}
	var out []core.MemberOccurrence
	var walk func(n *sitter.Node)
	walk = func(n *sitter.Node) {
		if n == nil {
			return
		}
		hit := memberHitTypes[n.Type()] || (lang == "go" && n.Type() == "type_identifier")
		if hit && memberNameMatches(n.Content(src), leaf) {
			if occ, ok := classifyMemberHit(n, src, lang); ok {
				line := int(n.StartPoint().Row) + 1
				occ.File, occ.Line, occ.Language, occ.Package = file, line, lang, pkg
				occ.Col = int(n.StartPoint().Column)
				if line-1 < len(lines) {
					occ.Text = strings.TrimRight(string(lines[line-1]), "\r")
				}
				out = append(out, occ)
			}
		}
		for i := 0; i < int(n.ChildCount()); i++ {
			walk(n.Child(i))
		}
	}
	walk(root)
	return out
}

// memberNameMatches compares an identifier node's text with the member name;
// a TS/JS private name "#x" matches a member named either "#x" or "x".
func memberNameMatches(text, leaf string) bool {
	return text == leaf || strings.TrimPrefix(text, "#") == strings.TrimPrefix(leaf, "#")
}

func sameNode(a, b *sitter.Node) bool {
	return a != nil && b != nil && a.StartByte() == b.StartByte() && a.EndByte() == b.EndByte() && a.Type() == b.Type()
}

// compactText is node text with all whitespace removed ("c . engine" and a
// receiver split over lines compare equal to "c.engine").
func compactText(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	return strings.Join(strings.Fields(n.Content(src)), "")
}

// classifyMemberHit decides the shape of one identifier occurrence. ok=false
// drops it (method/function names, type names, labels, import specifiers —
// positions where a data member cannot be meant).
func classifyMemberHit(n *sitter.Node, src []byte, lang string) (core.MemberOccurrence, bool) {
	p := n.Parent()
	if p == nil {
		return core.MemberOccurrence{}, false
	}
	switch lang {
	case "go":
		return classifyGoHit(n, p, src)
	case "java":
		return classifyJavaHit(n, p, src)
	case "c", "cpp", "objc":
		return classifyCHit(n, p, src)
	case "python":
		return classifyPythonHit(n, p, src)
	case "typescript", "tsx", "javascript":
		return classifyTSHit(n, p, src)
	case "php":
		return classifyPHPHit(n, p, src)
	case "csharp":
		return classifyCSharpHit(n, p, src)
	case "rust":
		return classifyRustHit(n, p, src)
	case "kotlin", "swift":
		return classifyNavHit(n, p, src)
	}
	return core.MemberOccurrence{}, false
}

// access builds an "access" occurrence for the member node `expr` (the whole
// x.f expression) with receiver `recv`, deriving write/call flags from the
// expression's position.
func access(expr *sitter.Node, recv string) core.MemberOccurrence {
	return core.MemberOccurrence{Form: "access", Receiver: recv, Write: isWriteTarget(expr), Call: isCallee(expr)}
}

// isWriteTarget reports whether expr is the target of an assignment or an
// increment/decrement, across grammars.
func isWriteTarget(expr *sitter.Node) bool {
	cur := expr
	for i := 0; i < 3 && cur != nil; i++ {
		p := cur.Parent()
		if p == nil {
			return false
		}
		switch p.Type() {
		case "assignment_expression", "assignment", "augmented_assignment", "augmented_assignment_expression",
			"compound_assignment_expr", "assignment_statement":
			if l := p.ChildByFieldName("left"); l != nil {
				return sameNode(l, cur) || (l.Type() == "expression_list" && sameNode(cur.Parent(), l))
			}
			if t := p.ChildByFieldName("target"); t != nil {
				return sameNode(t, cur)
			}
			return p.NamedChildCount() > 0 && sameNode(p.NamedChild(0), cur)
		case "update_expression", "inc_statement", "dec_statement", "postfix_expression", "prefix_expression":
			return true
		case "expression_list", "directly_assignable_expression", "parenthesized_expression":
			if p.Type() == "directly_assignable_expression" {
				return true
			}
			cur = p
			continue
		}
		return false
	}
	return false
}

// isCallee reports whether expr is the function position of a call.
func isCallee(expr *sitter.Node) bool {
	p := expr.Parent()
	if p == nil {
		return false
	}
	switch p.Type() {
	case "call_expression", "call", "invocation_expression":
		if fn := p.ChildByFieldName("function"); fn != nil {
			return sameNode(fn, expr)
		}
		return p.NamedChildCount() > 0 && sameNode(p.NamedChild(0), expr)
	}
	return false
}

func bare(n *sitter.Node) (core.MemberOccurrence, bool) {
	return core.MemberOccurrence{Form: "bare", Write: isWriteTarget(n), Call: isCallee(n)}, true
}

func classifyGoHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "selector_expression":
		if f := p.ChildByFieldName("field"); sameNode(f, n) {
			return access(p, compactText(p.ChildByFieldName("operand"), src)), true
		}
		return bare(n) // operand position: a plain identifier
	case "field_declaration":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return core.MemberOccurrence{}, false
	case "literal_element":
		kv := p.Parent()
		if kv != nil && kv.Type() == "keyed_element" && kv.NamedChildCount() > 0 && sameNode(kv.NamedChild(0), p) {
			return core.MemberOccurrence{Form: "key", Receiver: goLiteralType(kv, src)}, true
		}
		return bare(n)
	case "keyed_element": // older grammar: (keyed_element (field_identifier) value)
		if p.NamedChildCount() > 0 && sameNode(p.NamedChild(0), n) {
			return core.MemberOccurrence{Form: "key", Receiver: goLiteralType(p, src)}, true
		}
		return bare(n)
	case "var_spec", "const_spec":
		for i := 0; i < int(p.ChildCount()); i++ {
			if sameNode(p.Child(i), n) {
				if goTopLevel(p) {
					return core.MemberOccurrence{Form: "decl"}, true
				}
				return core.MemberOccurrence{Form: "localdecl"}, true
			}
		}
	case "parameter_declaration", "variadic_parameter_declaration", "range_clause":
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "expression_list":
		if gp := p.Parent(); gp != nil && gp.Type() == "short_var_declaration" {
			if l := gp.ChildByFieldName("left"); sameNode(l, p) {
				return core.MemberOccurrence{Form: "localdecl"}, true
			}
		}
		if gp := p.Parent(); gp != nil && gp.Type() == "range_clause" {
			if l := gp.ChildByFieldName("left"); sameNode(l, p) {
				return core.MemberOccurrence{Form: "localdecl"}, true
			}
		}
	case "qualified_type":
		// `c.handlers[c.index](c)` parses as a generic call whose type
		// argument is the qualified type c.index; inside type arguments
		// of a call that shape is really a selector.
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) && goInCallTypeArgs(p) {
			return core.MemberOccurrence{Form: "access", Receiver: compactText(p.ChildByFieldName("package"), src)}, true
		}
		return core.MemberOccurrence{}, false
	case "function_declaration", "method_declaration", "method_spec", "method_elem", "labeled_statement",
		"label_name", "type_spec", "import_spec", "package_clause":
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "field_identifier" || n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

// goInCallTypeArgs recognizes a qualified type that tree-sitter produced
// from an index-then-call expression: `c.handlers[c.index](c)` parses as a
// conversion to the generic type c.handlers[c.index]. Both qualified types
// in that shape are really selectors.
func goInCallTypeArgs(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "type_elem", "type_arguments", "generic_type":
			continue
		case "type_conversion_expression", "call_expression":
			return true
		}
		return false
	}
	return false
}

func goTopLevel(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "function_declaration", "method_declaration", "func_literal":
			return false
		case "source_file":
			return true
		}
	}
	return true
}

// goLiteralType names the struct type a keyed element initializes: the
// composite literal's own type, or — for an elided inner literal
// ([]T{{F: 1}}, map[K]T{k: {F: 1}}) — the element type of the enclosing one.
func goLiteralType(keyed *sitter.Node, src []byte) string {
	lv := keyed.Parent() // literal_value
	depth := 0
	for lv != nil && lv.Type() == "literal_value" {
		pp := lv.Parent()
		if pp == nil {
			return ""
		}
		switch pp.Type() {
		case "composite_literal":
			t := pp.ChildByFieldName("type")
			for i := 0; i < depth && t != nil; i++ {
				t = goElementType(t)
			}
			return goTypeName(t, src)
		case "literal_element":
			depth++
			up := pp.Parent()
			if up != nil && up.Type() == "keyed_element" {
				up = up.Parent()
			}
			lv = up
		default:
			return ""
		}
	}
	return ""
}

func goElementType(t *sitter.Node) *sitter.Node {
	switch t.Type() {
	case "slice_type", "array_type", "implicit_length_array_type":
		return t.ChildByFieldName("element")
	case "map_type":
		return t.ChildByFieldName("value")
	case "pointer_type":
		if t.NamedChildCount() > 0 {
			return goElementType(t.NamedChild(0))
		}
	}
	return nil
}

func goTypeName(t *sitter.Node, src []byte) string {
	if t == nil {
		return ""
	}
	switch t.Type() {
	case "pointer_type":
		if t.NamedChildCount() > 0 {
			return goTypeName(t.NamedChild(0), src)
		}
		return ""
	case "generic_type":
		return goTypeName(t.ChildByFieldName("type"), src)
	case "qualified_type":
		return compactText(t.ChildByFieldName("name"), src)
	case "type_identifier":
		return t.Content(src)
	}
	return ""
}

func classifyJavaHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "field_access":
		if f := p.ChildByFieldName("field"); sameNode(f, n) {
			return access(p, compactText(p.ChildByFieldName("object"), src)), true
		}
		return bare(n)
	case "variable_declarator":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			if gp := p.Parent(); gp != nil && (gp.Type() == "field_declaration" || gp.Type() == "constant_declaration") {
				return core.MemberOccurrence{Form: "decl"}, true
			}
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
	case "formal_parameter", "catch_formal_parameter", "spread_parameter", "enhanced_for_statement", "inferred_parameters", "lambda_expression", "resource":
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "method_invocation":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{}, false
		}
	case "method_declaration", "constructor_declaration", "class_declaration", "interface_declaration",
		"enum_declaration", "record_declaration", "labeled_statement", "break_statement", "continue_statement",
		"scoped_identifier", "import_declaration", "package_declaration", "method_reference", "annotation", "marker_annotation":
		return core.MemberOccurrence{}, false
	case "enum_constant":
		return core.MemberOccurrence{Form: "decl"}, true
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func classifyCHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "field_expression":
		if f := p.ChildByFieldName("field"); sameNode(f, n) {
			return access(p, compactText(p.ChildByFieldName("argument"), src)), true
		}
		return bare(n)
	case "field_designator":
		return core.MemberOccurrence{Form: "key", Receiver: cInitializerType(p, src)}, true
	case "field_declaration":
		return core.MemberOccurrence{Form: "decl"}, true
	case "qualified_identifier":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			if gp := p.Parent(); gp != nil && gp.Type() == "function_declarator" {
				return core.MemberOccurrence{}, false // out-of-line method definition name
			}
			return access(p, compactText(p.ChildByFieldName("scope"), src)), true
		}
		return core.MemberOccurrence{}, false
	case "init_declarator", "pointer_declarator", "array_declarator", "declaration", "reference_declarator", "attributed_declarator":
		decl := p
		for decl != nil && decl.Type() != "declaration" && decl.Type() != "field_declaration" && decl.Type() != "parameter_declaration" {
			decl = decl.Parent()
		}
		if p.Type() == "init_declarator" {
			if d := p.ChildByFieldName("declarator"); !sameNode(d, n) {
				return bare(n) // the initializer value side
			}
		}
		if decl != nil && decl.Type() == "parameter_declaration" {
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
		if decl != nil && decl.Type() == "field_declaration" {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		if decl != nil && cTopLevel(decl) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "parameter_declaration":
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "function_declarator", "labeled_statement", "goto_statement", "preproc_def", "preproc_function_def",
		"struct_specifier", "enum_specifier", "union_specifier", "type_definition":
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "field_identifier" {
		// A field declarator nested in pointer/array/function declarators
		// inside a field_declaration.
		for cur := p; cur != nil; cur = cur.Parent() {
			if cur.Type() == "field_declaration" {
				return core.MemberOccurrence{Form: "decl"}, true
			}
			if cur.Type() == "compound_statement" {
				break
			}
		}
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func cTopLevel(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "function_definition", "compound_statement", "lambda_expression":
			return false
		case "translation_unit":
			return true
		}
	}
	return true
}

// cInitializerType names the struct type a designated initializer targets:
// the declared type of `T w = {.f = 1}` or a compound literal `(T){.f = 1}`.
func cInitializerType(designator *sitter.Node, src []byte) string {
	for cur := designator.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "compound_literal_expression":
			return cTypeName(cur.ChildByFieldName("type"), src)
		case "declaration", "field_declaration":
			return cTypeName(cur.ChildByFieldName("type"), src)
		case "initializer_list", "initializer_pair", "init_declarator", "field_designator":
			if cur.Type() == "initializer_list" {
				if pp := cur.Parent(); pp != nil && pp.Type() == "initializer_list" {
					return "" // nested: element type unknown
				}
			}
			continue
		default:
			return ""
		}
	}
	return ""
}

func cTypeName(t *sitter.Node, src []byte) string {
	if t == nil {
		return ""
	}
	switch t.Type() {
	case "struct_specifier", "union_specifier", "class_specifier":
		return compactText(t.ChildByFieldName("name"), src)
	case "type_descriptor":
		return cTypeName(t.ChildByFieldName("type"), src)
	case "qualified_identifier":
		return compactText(t.ChildByFieldName("name"), src)
	case "template_type":
		return compactText(t.ChildByFieldName("name"), src)
	}
	return compactText(t, src)
}

func classifyPythonHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "attribute":
		if a := p.ChildByFieldName("attribute"); sameNode(a, n) {
			return access(p, compactText(p.ChildByFieldName("object"), src)), true
		}
		return bare(n)
	case "keyword_argument":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			recv := ""
			if args := p.Parent(); args != nil {
				if call := args.Parent(); call != nil && call.Type() == "call" {
					recv = compactText(call.ChildByFieldName("function"), src)
				}
			}
			return core.MemberOccurrence{Form: "key", Receiver: recv}, true
		}
		return bare(n)
	case "assignment", "augmented_assignment", "type":
		if l := p.ChildByFieldName("left"); sameNode(l, n) || p.Type() == "type" {
			if pyInClassBody(p) {
				return core.MemberOccurrence{Form: "decl", Write: true}, true
			}
			if pyInFunction(p) {
				return core.MemberOccurrence{Form: "localdecl", Write: true}, true
			}
			return core.MemberOccurrence{Form: "decl", Write: true}, true
		}
		return bare(n)
	case "parameters", "default_parameter", "typed_parameter", "typed_default_parameter", "lambda_parameters",
		"list_splat_pattern", "dictionary_splat_pattern", "for_statement", "for_in_clause", "as_pattern_target", "global_statement", "nonlocal_statement":
		if p.Type() == "for_statement" || p.Type() == "for_in_clause" {
			if l := p.ChildByFieldName("left"); !sameNode(l, n) {
				return bare(n)
			}
		}
		if p.Type() == "default_parameter" || p.Type() == "typed_default_parameter" {
			if nm := p.ChildByFieldName("name"); !sameNode(nm, n) {
				return bare(n)
			}
		}
		if p.Type() == "global_statement" || p.Type() == "nonlocal_statement" {
			return core.MemberOccurrence{}, false
		}
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "dotted_name":
		gp := p.Parent()
		if gp != nil && gp.Type() == "aliased_import" {
			gp = gp.Parent()
		}
		if gp != nil && gp.Type() == "import_from_statement" {
			if mod := gp.ChildByFieldName("module_name"); mod != nil && !sameNode(mod, p) {
				return core.MemberOccurrence{Form: "import", Receiver: compactText(mod, src)}, true
			}
		}
		return core.MemberOccurrence{}, false
	case "function_definition", "class_definition", "aliased_import", "import_from_statement", "import_statement":
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func pyInClassBody(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "function_definition", "lambda":
			return false
		case "class_definition":
			return true
		}
	}
	return false
}

func pyInFunction(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "function_definition", "lambda":
			return true
		case "class_definition", "module":
			return false
		}
	}
	return false
}

func classifyTSHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "member_expression":
		if pr := p.ChildByFieldName("property"); sameNode(pr, n) {
			return access(p, compactText(p.ChildByFieldName("object"), src)), true
		}
		return bare(n)
	case "pair":
		if k := p.ChildByFieldName("key"); sameNode(k, n) {
			return core.MemberOccurrence{Form: "key", Receiver: tsObjectLiteralType(p.Parent(), src)}, true
		}
		return bare(n)
	case "object":
		if n.Type() == "shorthand_property_identifier" {
			return core.MemberOccurrence{Form: "key", Receiver: tsObjectLiteralType(p, src)}, true
		}
	case "public_field_definition", "field_definition", "property_signature":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		if nm := p.ChildByFieldName("property"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return bare(n)
	case "variable_declarator":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			if tsTopLevel(p) {
				return core.MemberOccurrence{Form: "decl"}, true
			}
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
		return bare(n)
	case "required_parameter", "optional_parameter", "formal_parameters", "arrow_function", "catch_clause",
		"shorthand_property_identifier_pattern", "object_pattern", "array_pattern", "rest_pattern", "assignment_pattern":
		if p.Type() == "assignment_pattern" {
			if l := p.ChildByFieldName("left"); !sameNode(l, n) {
				return bare(n)
			}
		}
		if p.Type() == "required_parameter" || p.Type() == "optional_parameter" {
			if pat := p.ChildByFieldName("pattern"); !sameNode(pat, n) {
				return bare(n)
			}
		}
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "import_specifier":
		for cur := p.Parent(); cur != nil; cur = cur.Parent() {
			if cur.Type() == "import_statement" {
				return core.MemberOccurrence{Form: "import", Receiver: compactText(cur.ChildByFieldName("source"), src)}, true
			}
		}
		return core.MemberOccurrence{}, false
	case "method_definition", "method_signature", "function_declaration", "class_declaration", "interface_declaration",
		"export_specifier", "namespace_import", "import_clause", "labeled_statement",
		"type_alias_declaration", "enum_declaration", "abstract_method_signature", "generator_function_declaration":
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func tsTopLevel(n *sitter.Node) bool {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		switch cur.Type() {
		case "statement_block", "arrow_function", "function", "function_expression", "class_body", "method_definition":
			return false
		case "program":
			return true
		}
	}
	return true
}

// tsObjectLiteralType names the declared type an object literal initializes
// when the syntax states one: `const x: T = {...}`, `{...} as T`,
// `<T>{...}`, `{...} satisfies T`. Empty otherwise.
func tsObjectLiteralType(obj *sitter.Node, src []byte) string {
	cur := obj
	for cur != nil {
		p := cur.Parent()
		if p == nil {
			return ""
		}
		switch p.Type() {
		case "variable_declarator", "public_field_definition", "required_parameter", "optional_parameter":
			if ta := p.ChildByFieldName("type"); ta != nil {
				return tsTypeName(ta, src)
			}
			return ""
		case "as_expression", "satisfies_expression", "type_assertion":
			for i := int(p.NamedChildCount()) - 1; i >= 0; i-- {
				c := p.NamedChild(i)
				if !sameNode(c, cur) {
					if t := tsTypeName(c, src); t != "" {
						return t
					}
					break
				}
			}
			cur = p // `as any`/`as unknown`: the declaration may still name it
		case "parenthesized_expression", "non_null_expression":
			cur = p
		default:
			return ""
		}
	}
	return ""
}

func tsTypeName(t *sitter.Node, src []byte) string {
	if t == nil {
		return ""
	}
	switch t.Type() {
	case "type_identifier":
		return t.Content(src)
	case "generic_type":
		return tsTypeName(t.ChildByFieldName("name"), src)
	case "nested_type_identifier":
		return compactText(t.ChildByFieldName("name"), src)
	case "type_annotation":
		if t.NamedChildCount() > 0 {
			return tsTypeName(t.NamedChild(0), src)
		}
	}
	return ""
}

func classifyPHPHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "member_access_expression", "nullsafe_member_access_expression":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return access(p, compactText(p.ChildByFieldName("object"), src)), true
		}
		return core.MemberOccurrence{}, false
	case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{}, false // a method call, not a property
		}
		return core.MemberOccurrence{}, false
	case "class_constant_access_expression":
		if cnt := int(p.NamedChildCount()); cnt >= 2 && sameNode(p.NamedChild(cnt-1), n) {
			return access(p, compactText(p.NamedChild(0), src)), true
		}
		return core.MemberOccurrence{}, false
	case "const_element":
		return core.MemberOccurrence{Form: "decl"}, true
	case "variable_name":
		gp := p.Parent()
		if gp == nil {
			return core.MemberOccurrence{}, false
		}
		switch gp.Type() {
		case "property_element":
			return core.MemberOccurrence{Form: "decl"}, true
		case "scoped_property_access_expression":
			if nm := gp.ChildByFieldName("name"); sameNode(nm, p) {
				return access(gp, compactText(gp.ChildByFieldName("scope"), src)), true
			}
		case "property_promotion_parameter":
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return core.MemberOccurrence{}, false // a $local variable, never a property
	case "named_arguments", "argument":
		if p.Type() == "argument" {
			if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
				return core.MemberOccurrence{Form: "key"}, true
			}
		}
	}
	return core.MemberOccurrence{}, false
}

func classifyCSharpHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "member_access_expression":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			recv := compactText(p.ChildByFieldName("expression"), src)
			if recv == "" {
				recv = "this"
			}
			return access(p, recv), true
		}
		return bare(n)
	case "conditional_access_expression", "member_binding_expression":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return access(p, ""), true
		}
	case "variable_declarator":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			for cur := p.Parent(); cur != nil; cur = cur.Parent() {
				switch cur.Type() {
				case "field_declaration", "event_field_declaration":
					return core.MemberOccurrence{Form: "decl"}, true
				case "local_declaration_statement", "block", "for_statement", "using_statement":
					return core.MemberOccurrence{Form: "localdecl"}, true
				}
			}
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
		if n.Type() == "identifier" && p.NamedChildCount() > 0 && sameNode(p.NamedChild(0), n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return bare(n)
	case "property_declaration", "enum_member_declaration", "event_declaration":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return core.MemberOccurrence{}, false
	case "assignment_expression":
		if l := p.ChildByFieldName("left"); sameNode(l, n) {
			if gp := p.Parent(); gp != nil && gp.Type() == "initializer_expression" {
				recv := ""
				if oc := gp.Parent(); oc != nil && oc.Type() == "object_creation_expression" {
					recv = cTypeName(oc.ChildByFieldName("type"), src)
				}
				return core.MemberOccurrence{Form: "key", Receiver: recv, Write: true}, true
			}
		}
		return bare(n)
	case "parameter", "foreach_statement", "catch_declaration", "declaration_expression":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
		if p.Type() == "foreach_statement" {
			if l := p.ChildByFieldName("left"); sameNode(l, n) {
				return core.MemberOccurrence{Form: "localdecl"}, true
			}
		}
		return bare(n)
	case "method_declaration", "class_declaration", "struct_declaration", "interface_declaration", "invocation_expression",
		"qualified_name", "using_directive", "namespace_declaration", "constructor_declaration", "generic_name", "type_parameter":
		if p.Type() == "invocation_expression" {
			return bare(n)
		}
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func classifyRustHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "field_expression":
		if f := p.ChildByFieldName("field"); sameNode(f, n) {
			return access(p, compactText(p.ChildByFieldName("value"), src)), true
		}
		return bare(n)
	case "field_initializer":
		if f := p.ChildByFieldName("field"); sameNode(f, n) {
			return core.MemberOccurrence{Form: "key", Receiver: rustStructExprType(p, src)}, true
		}
		return bare(n)
	case "shorthand_field_initializer":
		return core.MemberOccurrence{Form: "key", Receiver: rustStructExprType(p, src)}, true
	case "field_declaration":
		return core.MemberOccurrence{Form: "decl"}, true
	case "static_item", "const_item":
		if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
			return core.MemberOccurrence{Form: "decl"}, true
		}
		return bare(n)
	case "let_declaration", "parameter", "closure_parameters", "for_expression", "tuple_pattern", "identifier_pattern":
		if p.Type() == "let_declaration" {
			if pat := p.ChildByFieldName("pattern"); !sameNode(pat, n) {
				return bare(n)
			}
		}
		if p.Type() == "parameter" {
			if pat := p.ChildByFieldName("pattern"); !sameNode(pat, n) {
				return bare(n)
			}
		}
		if p.Type() == "for_expression" {
			if pat := p.ChildByFieldName("pattern"); !sameNode(pat, n) {
				return bare(n)
			}
		}
		return core.MemberOccurrence{Form: "localdecl"}, true
	case "function_item", "struct_item", "enum_item", "trait_item", "impl_item", "use_declaration", "scoped_identifier", "mod_item":
		if p.Type() == "scoped_identifier" {
			if nm := p.ChildByFieldName("name"); sameNode(nm, n) {
				return access(p, compactText(p.ChildByFieldName("path"), src)), true
			}
		}
		return core.MemberOccurrence{}, false
	}
	if n.Type() == "identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}

func rustStructExprType(n *sitter.Node, src []byte) string {
	for cur := n.Parent(); cur != nil; cur = cur.Parent() {
		if cur.Type() == "struct_expression" {
			nm := cur.ChildByFieldName("name")
			if nm != nil && (nm.Type() == "scoped_type_identifier" || nm.Type() == "generic_type") {
				if inner := nm.ChildByFieldName("name"); inner != nil {
					return compactText(inner, src)
				}
				if inner := nm.ChildByFieldName("type"); inner != nil {
					return compactText(inner, src)
				}
			}
			return compactText(nm, src)
		}
		if cur.Type() != "field_initializer_list" && cur.Type() != "field_initializer" && cur.Type() != "shorthand_field_initializer" {
			return ""
		}
	}
	return ""
}

// classifyNavHit handles the Kotlin and Swift grammars, which share the
// navigation_expression/navigation_suffix shape for member access.
func classifyNavHit(n, p *sitter.Node, src []byte) (core.MemberOccurrence, bool) {
	switch p.Type() {
	case "navigation_suffix":
		expr := p.Parent()
		if expr == nil {
			return core.MemberOccurrence{}, false
		}
		recv := ""
		if t := expr.ChildByFieldName("target"); t != nil {
			recv = compactText(t, src)
		} else if expr.NamedChildCount() > 0 {
			recv = compactText(expr.NamedChild(0), src)
		}
		occ := access(expr, recv)
		if expr.Type() == "directly_assignable_expression" {
			occ.Write = true
		}
		return occ, true
	case "variable_declaration", "class_parameter", "pattern":
		for cur := p.Parent(); cur != nil; cur = cur.Parent() {
			switch cur.Type() {
			case "function_body", "statements", "lambda_literal", "function_declaration":
				return core.MemberOccurrence{Form: "localdecl"}, true
			case "class_body", "class_declaration", "primary_constructor", "source_file", "object_declaration":
				return core.MemberOccurrence{Form: "decl"}, true
			}
		}
		return core.MemberOccurrence{Form: "decl"}, true
	case "value_argument":
		if p.NamedChildCount() > 1 && sameNode(p.NamedChild(0), n) {
			recv := ""
			for cur := p.Parent(); cur != nil; cur = cur.Parent() {
				if cur.Type() == "call_expression" {
					if cur.NamedChildCount() > 0 {
						recv = compactText(cur.NamedChild(0), src)
					}
					break
				}
			}
			return core.MemberOccurrence{Form: "key", Receiver: recv}, true
		}
		return bare(n)
	case "parameter", "function_declaration", "class_declaration", "import_header", "user_type", "type_identifier":
		if p.Type() == "parameter" {
			return core.MemberOccurrence{Form: "localdecl"}, true
		}
		return core.MemberOccurrence{}, false
	case "directly_assignable_expression":
		return core.MemberOccurrence{Form: "bare", Write: true}, true
	}
	if n.Type() == "simple_identifier" {
		return bare(n)
	}
	return core.MemberOccurrence{}, false
}
