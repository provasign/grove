package invariance

// Group P: grove internal/parser. P6-P15 put a syntax error in BOTH forms so
// the file takes the regex recovery path (extractSymbols merges regex-found
// names the AST missed); the decoy hides code-looking text in comments or
// strings that recovery must not read.

func casesP() []twinCase {
	return []twinCase{
		// --- P1 precedingCommentBlock docstring bleed ---
		{
			id: "P1-java-trailing-comment",
			clean: one("A.java", `/** The A class. */
class A {
  int x;
  void b() {}
}
`),
			decoy: one("A.java", `/** The A class. */
class A {
  int x; /* trailing */
  void b() {}
}
`),
			doc:     true,
			want:    []string{"S A.b"},
			wantNot: []string{"S A.b doc~int_x", "S A.b doc~trailing", "S A.b doc~class_A"},
		},
		{
			id: "P1-js-glob-string",
			clean: one("a.js", `const glob = "src";
/* helper */
function f() {}
`),
			decoy: one("a.js", `const glob = "src/**";
/* helper */
function f() {}
`),
			doc:     true,
			want:    []string{"S f"},
			wantNot: []string{"S f doc~const_glob"},
		},
		{
			id: "P1-go-trailing-comment",
			clean: one("a.go", `/** Package p. */
package p

var x = 1

func F() {}
`),
			decoy: one("a.go", `/** Package p. */
package p

var x = 1 /* trailing */
func F() {}
`),
			doc:     true,
			want:    []string{"S F"},
			wantNot: []string{"S F doc~var_x", "S F doc~trailing", "S F doc~package"},
		},
		// --- P2-P5 clean files ---
		{
			id: "P2-cpp-raw-string",
			clean: one("a.cpp", `const char* kSrc = "";
int real(int y) { return y; }
`),
			decoy: one("a.cpp", `const char* kSrc = R"(
int phantom(int x) {
  return x;
}
)";
int real(int y) { return y; }
`),
			want:    []string{"S real"},
			wantNot: []string{"S phantom"},
		},
		{
			id: "P3-c-multiline-define",
			clean: one("a.c", `#define DECL(n) (n)
int real(void) { return 0; }
`),
			decoy: one("a.c", `#define DECL(n) \
  static int helper(void) { \
    return n; \
  }
int real(void) { return 0; }
`),
			want:    []string{"S real"},
			wantNot: []string{"S helper"},
		},
		{
			id: "P4-cpp-digit-separator",
			clean: one("a.cpp", `namespace foo { struct Widget { int go() { return 1; } }; }
namespace other { struct Widget { int go() { return 2; } }; }
constexpr int kMax = 1000;
using namespace foo;
int work() { Widget w; return w.go(); }
`),
			decoy: one("a.cpp", `namespace foo { struct Widget { int go() { return 1; } }; }
namespace other { struct Widget { int go() { return 2; } }; }
constexpr int kMax = 1'000;
using namespace foo;
int work() { Widget w; return w.go(); }
`),
			want:    []string{"E work calls foo.Widget.go"},
			wantNot: []string{"E work calls other.Widget.go"},
		},
		{
			id: "P4-cpp-error-apostrophe",
			clean: one("a.cpp", `namespace foo { struct Widget { int go() { return 1; } }; }
namespace other { struct Widget { int go() { return 2; } }; }
#if 0
#error do not
#endif
using namespace foo;
int work() { Widget w; return w.go(); }
`),
			decoy: one("a.cpp", `namespace foo { struct Widget { int go() { return 1; } }; }
namespace other { struct Widget { int go() { return 2; } }; }
#if 0
#error don't
#endif
using namespace foo;
int work() { Widget w; return w.go(); }
`),
			want:    []string{"E work calls foo.Widget.go"},
			wantNot: []string{"E work calls other.Widget.go"},
		},
		{
			id: "P5-js-export-default-string",
			clean: one("a.js", `function helper() { return "x"; }
export function other() {}
`),
			decoy: one("a.js", `function helper() { return "export default"; }
export function other() {}
`),
			want:    []string{"S helper unexported", "S other exported"},
			wantNot: []string{"S helper mod=default-export", "S helper mod=default", "S helper exported"},
		},
		// --- P6-P14 regex recovery (syntax error in both forms) ---
		{
			id: "P6-go-recovery",
			clean: one("a.go", `package p

var s = ""

func Real() int { return 1 }

func Broken( {
`),
			decoy: one("a.go", `package p

/*
func Phantom() {}
type Ghost struct{}
*/
var s = `+"`"+`
func Phantom2() {}
type Ghost2 struct{}
`+"`"+`

func Real() int { return 1 }

func Broken( {
`),
			want:    []string{"S Real"},
			wantNot: []string{"S Phantom", "S Ghost", "S Phantom2", "S Ghost2"},
		},
		{
			id: "P7-java-text-block-recovery",
			clean: one("A.java", `class A {
  String s = "";
  void real() {}
  void broken( {
}
`),
			decoy: one("A.java", `class A {
  String s = """
      class Phantom { void ghost() {} }
      """;
  void real() {}
  void broken( {
}
`),
			want:    []string{"S A.real"},
			wantNot: []string{"S Phantom", "S ghost"},
		},
		{
			id: "P8-csharp-raw-verbatim-recovery",
			clean: one("A.cs", `class A {
  string s = "";
  string v = "";
  void Real() {}
  void Broken( {
}
`),
			decoy: one("A.cs", `class A {
  string s = """
    class Phantom { }
    """;
  string v = @"
class Phantom2 { }
";
  void Real() {}
  void Broken( {
}
`),
			want:    []string{"S A.Real"},
			wantNot: []string{"S Phantom", "S Phantom2"},
		},
		{
			id: "P9-ts-template-recovery",
			clean: one("a.ts", `const tpl = "";
export function real() { return 1; }
export function broken( {
`),
			decoy:   one("a.ts", "const tpl = `\nclass Phantom {\nfunction ghost(a) {\n`;\nexport function real() { return 1; }\nexport function broken( {\n"),
			want:    []string{"S real"},
			wantNot: []string{"S Phantom", "S ghost"},
		},
		{
			id: "P9-tsx-jsx-text-recovery",
			clean: one("v.tsx", `export function View() {
  return <div>
    styles are applied
  </div>;
}
export function broken( {
`),
			decoy: one("v.tsx", `export function View() {
  return <div>
    class names are applied
  </div>;
}
export function broken( {
`),
			want:    []string{"S View"},
			wantNot: []string{"S names"},
		},
		{
			id: "P10-php-heredoc-html-recovery",
			clean: one("a.php", `<?php
$s = "";
function real() { return 1; }
function broken( {
?>
<p>
  styles here
</p>
`),
			decoy: one("a.php", `<?php
$s = <<<EOT
class Phantom {
function ghost($a) {
EOT;
function real() { return 1; }
function broken( {
?>
<p>
  class names here
</p>
`),
			want:    []string{"S real"},
			wantNot: []string{"S Phantom", "S ghost", "S names"},
		},
		{
			id: "P11-rust-raw-string-recovery",
			clean: one("lib.rs", `const S: &str = "";
pub fn real() -> i32 { 1 }
pub fn broken( {
`),
			decoy: one("lib.rs", `const S: &str = r#"
fn ghost() {}
struct Phantom;
"#;
pub fn real() -> i32 { 1 }
pub fn broken( {
`),
			want:    []string{"S real"},
			wantNot: []string{"S ghost", "S Phantom"},
		},
		{
			id: "P12-python-continued-string-recovery",
			clean: one("a.py", `x = "abc"


def real():
    return 1


def broken(:
    pass
`),
			decoy: one("a.py", `x = "abc \
class Phantom:"


def real():
    return 1


def broken(:
    pass
`),
			want:    []string{"S real"},
			wantNot: []string{"S Phantom"},
		},
		{
			id: "P13-c-if0",
			clean: one("a.c", `int real(void) { return 0; }
int broken( {
`),
			decoy: one("a.c", `#if 0
int ghost(int a) {
#endif
int real(void) { return 0; }
int broken( {
`),
			want:    []string{"S real"},
			wantNot: []string{"S ghost"},
		},
		{
			id: "P13-cpp-if0-namespace",
			clean: one("a.cpp", `int real(void) { return 0; }
int broken( {
`),
			decoy: one("a.cpp", `#if 0
namespace legacy {
int ghost(int a) {
#endif
int real(void) { return 0; }
int broken( {
`),
			want:    []string{"S real"},
			wantNot: []string{"S legacy", "S ghost", "S legacy.real"},
		},
		{
			id: "P13-csharp-if-false",
			clean: one("A.cs", `public class Real { public void Go() {} }
public class Broken { void X( { }
`),
			decoy: one("A.cs", `#if false
public class Legacy {
#endif
public class Real { public void Go() {} }
public class Broken { void X( { }
`),
			want:    []string{"S Real", "S Real.Go"},
			wantNot: []string{"S Legacy", "S Legacy.Real"},
		},
		{
			id: "P14-cpp-comment-qualified-method",
			clean: one("a.cpp", `int broken( {
void helper(int x) {
  (void)x;
}
`),
			decoy: one("a.cpp", `int broken( {
void helper(int x) { // see Widget::draw(x)
  (void)x;
}
`),
			want:    []string{"S helper"},
			wantNot: []string{"S Widget.draw", "S draw"},
		},
		// --- P15 recovered spans: the symbol follows the syntax error (so
		// recovery, not the AST, supplies it); decoys keep line numbers and
		// spans are compared ---
		{
			id: "P15-java-span-block-comment-brace",
			clean: one("A.java", `class A {
  void broken( {
  void real() {
    int a = 0; /* x */
    a++;
  }
  void after() {}
}
`),
			decoy: one("A.java", `class A {
  void broken( {
  void real() {
    int a = 0; /* } */
    a++;
  }
  void after() {}
}
`),
			spans: true,
			want:  []string{"S real end=6"},
		},
		{
			id:    "P15-ts-span-template-brace",
			clean: one("a.ts", "export function broken( {\nexport function real() {\n  const s = `\nx\n`;\n  return s;\n}\nexport function after() {}\n"),
			decoy: one("a.ts", "export function broken( {\nexport function real() {\n  const s = `\n}\n`;\n  return s;\n}\nexport function after() {}\n"),
			spans: true,
			want:  []string{"S real end=7"},
		},
		{
			id: "P15-python-span-docstring-col0",
			clean: one("a.py", `x = foo(


def real():
    """Doc.
    end
    """
    return 1


def after():
    return 2
`),
			decoy: one("a.py", `x = foo(


def real():
    """Doc.
end
"""
    return 1


def after():
    return 2
`),
			spans: true,
			want:  []string{"S real start=4", "S real doc~end"},
		},
		{
			// TS overload widening: a call statement above a function was
			// taken as an overload signature and widened the span upward.
			// Not a comment/string twin: the clean form has a blank line
			// where the decoy has the call (same line numbers), and makes
			// the same call after the function, so both forms carry the
			// real <top-level> -> foo call.
			id: "P16-ts-overload-widening",
			clean: one("a.ts", `
function foo(x: number) {
  return x;
}
foo(1);
`),
			decoy: one("a.ts", `foo(1);
function foo(x: number) {
  return x;
}
`),
			want: []string{"S foo start=2"},
		},
		{
			id: "P17-c-header-namespace-comment",
			clean: one("a.h", `/*
 * Shared declarations.
 */
int real(void);
`),
			decoy: one("a.h", `/*
namespace handling is not supported here
 */
int real(void);
`),
			want:    []string{"S real lang=c"},
			wantNot: []string{"S real lang=cpp"},
		},
	}
}
