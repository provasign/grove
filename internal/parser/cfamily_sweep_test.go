package parser

import (
	"fmt"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func qualifiedKinds(syms []core.SymbolRecord) map[string]core.SymbolKind {
	out := map[string]core.SymbolKind{}
	for _, s := range syms {
		out[s.QualifiedName] = s.Kind
	}
	return out
}

func requireQualified(t *testing.T, got map[string]core.SymbolKind, want map[string]core.SymbolKind) {
	t.Helper()
	for qn, kind := range want {
		if k, ok := got[qn]; !ok {
			t.Errorf("%s not indexed (want %s); got %v", qn, kind, got)
		} else if k != kind {
			t.Errorf("%s indexed as %s, want %s", qn, k, kind)
		}
	}
}

// clang-format closes every namespace with `}  // namespace foo`. The line
// scanner's copy of the namespace then ran one comment past the AST's and
// "contained" it, so every name became foo::foo::...
func TestCppNamespaceClosingCommentDoesNotDoubleNames(t *testing.T) {
	src := "namespace foo {\nstruct A { void f(); int x; };\nnamespace bar {\nstruct B {};\n}  // namespace bar\n}  // namespace foo\n"
	got := qualifiedKinds(extractSymbols("cpp", "a.hpp", "sha", src, nil))
	requireQualified(t, got, map[string]core.SymbolKind{
		"foo": core.KindNamespace, "foo::A": core.KindStruct, "foo::A::f": core.KindMethod,
		"foo::A::x": core.KindField, "foo::bar": core.KindNamespace, "foo::bar::B": core.KindStruct,
	})
	for qn := range got {
		if strings.Contains(qn, "foo::foo") || strings.Contains(qn, "bar::bar") {
			t.Errorf("doubled namespace in %s", qn)
		}
	}
}

// The line scanner stops a brace scan after 500 lines, so its copy of
// `namespace testing {` was narrower than the AST's `namespace internal {`
// inside it and claimed the inner namespace's members (gtest.h:
// testing::Secret for testing::internal::Secret).
func TestCppNamespaceTwinPastScanWindow(t *testing.T) {
	var b strings.Builder
	b.WriteString("namespace testing {\nnamespace internal {\nclass Secret {};\n")
	for i := 0; i < 600; i++ {
		fmt.Fprintf(&b, "int v%d = %d;\n", i, i)
	}
	b.WriteString("}  // namespace internal\n}  // namespace testing\n")
	got := qualifiedKinds(extractSymbols("cpp", "g.h", "sha", b.String(), nil))
	requireQualified(t, got, map[string]core.SymbolKind{"testing::internal::Secret": core.KindClass})
}

// nlohmann/json opens every header with a namespace macro; tree-sitter read
// the macro, `namespace` and the whole body as one function named
// `namespace`, and the classes inside vanished. A macro inside a class body
// (expanding to `template<...>`) and an access-specifier macro cut the class
// short.
func TestCFamilyNamespaceAndClassMacrosAreTransparent(t *testing.T) {
	src := `#define NLOHMANN_JSON_NAMESPACE_BEGIN namespace nlohmann {
#define NLOHMANN_JSON_NAMESPACE_END }
NLOHMANN_JSON_NAMESPACE_BEGIN
namespace detail
{
template<typename BasicJsonType>
class lexer
{
    NLOHMANN_BASIC_JSON_TPL_DECLARATION
    friend class basic_json;

  JSON_PRIVATE_UNLESS_TESTED:
    int state;

  public:
    void scan() {}
};

template<typename FloatType>
JSON_HEDLEY_NON_NULL(1)
void grisu2(char* buf, FloatType value)
{
}
}  // namespace detail
NLOHMANN_JSON_NAMESPACE_END
`
	syms := extractSymbols("cpp", "lexer.hpp", "sha", src, nil)
	got := qualifiedKinds(syms)
	requireQualified(t, got, map[string]core.SymbolKind{
		"detail::lexer": core.KindClass, "detail::lexer::state": core.KindField,
		"detail::lexer::scan": core.KindMethod, "detail::grisu2": core.KindFunction,
	})
	for _, s := range syms {
		if s.Name == "namespace" || strings.HasPrefix(s.Name, "NLOHMANN_BASIC") || strings.HasPrefix(s.Name, "JSON_HEDLEY") {
			t.Errorf("macro misparse symbol %s %s", s.Kind, s.QualifiedName)
		}
		if s.QualifiedName == "detail::lexer::scan" && !strings.Contains(s.RawText, "void scan() {}") {
			t.Errorf("body must be the original source text: %q", s.RawText)
		}
	}
}

// A macro invocation that is part of an expression (`GTEST_CHECK_(x)` on its
// own line, continued by `<< "msg";`) must keep its call.
func TestCFamilyMacroBlankingKeepsExpressionCalls(t *testing.T) {
	src := "void f() {\n  GTEST_CHECK_(ok)\n      << \"not ok\";\n}\nFMT_PRAGMA_GCC(push_options)\nFMT_CONSTEXPR auto g() -> int { return h(); }\n"
	syms := extractSymbols("cpp", "m.cc", "sha", src, nil)
	calls := map[string][]string{}
	for _, s := range syms {
		for _, c := range s.CallSites {
			calls[s.Name] = append(calls[s.Name], c.Callee)
		}
	}
	if !strings.Contains(strings.Join(calls["f"], ","), "GTEST_CHECK_") {
		t.Errorf("f lost its GTEST_CHECK_ call: %v", calls)
	}
	if !strings.Contains(strings.Join(calls["g"], ","), "h") {
		t.Errorf("g (after a specifier macro) lost its call: %v", calls)
	}
}

// The line scanner matched comment prose (` * Copyright (c) 2009 ...`, a
// block comment describing key(...)) and `typedef int (*fn)(...)` as
// functions named Copyright, key and int.
func TestCFamilyLineScannerIgnoresCommentsAndTypeKeywords(t *testing.T) {
	src := `/*
 * Copyright (c) 2009-2016 Petri Lehtinen <petri@digip.org>
 *
 * hashlittle() -- hash a variable-length key into a 32-bit value
 *   k       : the key (the unaligned variable-length array of bytes)
 * Use for hash table lookup, or anything where one collision in 2^^32 is
 * acceptable.  Do NOT use for cryptographic purposes.
 */
typedef int (*get_func)(void *data);

int real(int x) { return x; }
`
	for _, lang := range []string{"c", "cpp"} {
		syms := extractSymbols(lang, "l.h", "sha", src, nil)
		for _, s := range syms {
			switch s.Name {
			case "Copyright", "hashlittle", "key", "int", "the":
				t.Errorf("%s: junk symbol %s %s at %d", lang, s.Kind, s.Name, s.Span.Start)
			}
		}
		if got := qualifiedKinds(syms); got["get_func"] != core.KindType || got["real"] != core.KindFunction {
			t.Errorf("%s: want type get_func and function real, got %v", lang, got)
		}
	}
}

// C++ spells every scope `::`, inside a namespace or not; a conversion
// operator is `operator bool`, not a method named bool.
func TestCppGlobalClassSeparatorAndConversionOperator(t *testing.T) {
	src := "class Global {\n  int g;\npublic:\n  operator bool() const { return true; }\n};\n"
	got := qualifiedKinds(extractSymbols("cpp", "g.cpp", "sha", src, nil))
	requireQualified(t, got, map[string]core.SymbolKind{
		"Global::g": core.KindField, "Global::operator bool": core.KindMethod,
	})
	for qn := range got {
		if qn == "Global.g" || strings.HasSuffix(qn, "::bool") {
			t.Errorf("unexpected %s", qn)
		}
	}
}

// Nested types keep the namespace in front of their class path.
func TestCppNestedTypeMembersCarryNamespace(t *testing.T) {
	src := "namespace outer {\nclass Box {\n  struct Node { int val; };\n  enum class Kind { Small };\n};\n}  // namespace outer\n"
	got := qualifiedKinds(extractSymbols("cpp", "b.hpp", "sha", src, nil))
	requireQualified(t, got, map[string]core.SymbolKind{
		"outer::Box::Node": core.KindStruct, "outer::Box::Node::val": core.KindField,
		"outer::Box::Kind": core.KindEnum, "outer::Box::Kind::Small": core.KindConst,
	})
}

func TestCppJoinScope(t *testing.T) {
	for _, c := range []struct{ ns, owner, want string }{
		{"outer", "Box", "outer::Box"},
		{"outer", "Box::Node", "outer::Box::Node"},
		{"nlohmann", "nlohmann::detail::X", "nlohmann::detail::X"},
		{"nlohmann::detail", "detail::X", "nlohmann::detail::X"},
		{"foo", "foo", "foo::foo"}, // class foo inside namespace foo
		{"a", "::b::C", "b::C"},
	} {
		if got := cppJoinScope(c.ns, c.owner); got != c.want {
			t.Errorf("cppJoinScope(%q, %q) = %q, want %q", c.ns, c.owner, got, c.want)
		}
	}
}

// When the #if-only re-parse wins, a macro defined in the #else branch is
// still a definition and stays indexed.
func TestBlankPreprocessorBranchesKeepsElseDefines(t *testing.T) {
	src := "#if HAS_EXCEPTIONS\n#define JSON_CATCH(e) catch(e)\n#else\n#define JSON_CATCH(e) \\\n    if (false)\nint else_only();\n#endif\n"
	out := string(blankPreprocessorBranches([]byte(src)))
	if strings.Count(out, "#define JSON_CATCH") != 2 || !strings.Contains(out, "if (false)") {
		t.Fatalf("#else #define dropped:\n%s", out)
	}
	if strings.Contains(out, "else_only") {
		t.Fatalf("#else code kept:\n%s", out)
	}
	if strings.Count(out, "\n") != strings.Count(src, "\n") {
		t.Fatal("line count changed")
	}
}
