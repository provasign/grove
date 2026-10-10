package parser

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/provasign/astkit"
	"github.com/provasign/grove/internal/core"
)

// Decoy invariant, C family (2026-10-10 round 2): a comment line added
// after a `{` must not change the symbols of a file, even one tree-sitter
// cannot parse. fmt, nlohmann/json and AFNetworking each had files whose
// error recovery, line-scanner window or namespace choice moved with the
// comments.

// decoyLines inserts a code-shaped comment line after every line of src
// that ends in `{`, as research's decoy injector does, and returns the
// decoy and, per decoy line (1-based), the clean line it came from (0 for
// an inserted line).
func decoyLines(src string) (string, []int) {
	const decoy = "// decoy: class Fake extends Base { void ghost() { helper(1); new Fake(); } } Widget::draw(x); don't"
	var out []string
	from := []int{0}
	for i, line := range strings.Split(src, "\n") {
		out = append(out, line)
		from = append(from, i+1)
		if strings.HasSuffix(strings.TrimSpace(line), "{") {
			out = append(out, "    "+decoy)
			from = append(from, 0)
		}
	}
	// src ends in a newline: the block starts on the last (empty) line.
	return strings.Join(out, "\n") + "/* decoy block: don't 'quote\nclass Fake2 { void ghost(); };\n*/\n", from
}

// symbolKeys lists symbols as kind/qualified name/start line, decoy lines
// mapped back to clean ones.
func symbolKeys(syms []core.SymbolRecord, from []int) []string {
	var out []string
	for _, s := range syms {
		start := s.Span.Start
		if from != nil && start < len(from) {
			start = from[start]
		}
		out = append(out, fmt.Sprintf("%s %s @%d", s.Kind, s.QualifiedName, start))
	}
	sort.Strings(out)
	return out
}

func assertDecoyTwin(t *testing.T, path, clean string) []core.SymbolRecord {
	t.Helper()
	decoy, from := decoyLines(clean)
	cleanSyms := extractFor(t, path, clean)
	decoySyms := extractFor(t, path, decoy)
	a, b := symbolKeys(cleanSyms, nil), symbolKeys(decoySyms, from)
	if strings.Join(a, "\n") != strings.Join(b, "\n") {
		t.Fatalf("decoy changed the symbols\nclean:\n  %s\ndecoy:\n  %s", strings.Join(a, "\n  "), strings.Join(b, "\n  "))
	}
	if ci, di := extractImports(DetectLanguageContent(path, []byte(clean)), clean), extractImports(DetectLanguageContent(path, []byte(decoy)), decoy); fmt.Sprint(ci) != fmt.Sprint(di) {
		t.Fatalf("decoy changed the imports: %v vs %v", ci, di)
	}
	return decoySyms
}

func TestCFamilyBrokenFileDecoyTwin(t *testing.T) {
	// `int broken( {` keeps the file broken; the rest must come out the
	// same with or without comment lines, with spans on the decoy's own
	// lines and bodies that still hold its comments.
	clean := `#include <vector>
#include "widget.h"

namespace ns {
int broken( {
}

class Widget {
 public:
  void draw() {
    helper(1);
  }
};

int helper(int x) {
  return x;
}
}  // namespace ns
`
	syms := assertDecoyTwin(t, "w.cpp", clean)
	// Tree-sitter's recovery prices every byte and line it skips, so the
	// broken file must reach the parser as the same bytes either way.
	decoy, _ := decoyLines(clean)
	eng, _ := bridge()
	inputs := map[string]string{}
	for name, src := range map[string]string{"clean": clean, "decoy": decoy} {
		b := parseBrokenCFamily(context.Background(), eng, astkit.LangCPP, "cpp", []byte(src), []byte(src), []byte(src), true)
		if b == nil {
			t.Fatalf("%s: no parse", name)
		}
		b.tree.Close()
		inputs[name] = string(b.src)
	}
	if inputs["clean"] != inputs["decoy"] {
		t.Fatalf("parse input differs:\nclean %q\ndecoy %q", inputs["clean"], inputs["decoy"])
	}
	for _, s := range syms {
		if s.QualifiedName != "ns::helper" {
			continue
		}
		if !strings.HasPrefix(s.RawText, "int helper(int x) {\n    // decoy:") || !strings.HasSuffix(s.RawText, "}") {
			t.Fatalf("helper body lost its comment or its edges: %q", s.RawText)
		}
		return
	}
	t.Fatalf("ns::helper missing:%s", describe(syms))
}

func TestObjCAvailabilityMacrosDecoyTwin(t *testing.T) {
	// AFNetworking: trailing AF_API_AVAILABLE(...) attributes, an
	// NS_ENUM, NS_ASSUME_NONNULL_BEGIN and NS_DESIGNATED_INITIALIZER
	// broke the parse; recovery then flipped with comment lines.
	clean := `#import <Foundation/Foundation.h>

typedef NS_ENUM(NSUInteger, AFSSLPinningMode) {
    AFSSLPinningModeNone,
    AFSSLPinningModeCertificate,
};

NS_ASSUME_NONNULL_BEGIN

@interface AFSecurityPolicy : NSObject
@property (readonly, nonatomic, assign) AFSSLPinningMode SSLPinningMode;
@property (nonatomic, strong) NSURLSessionTaskMetrics *metrics AF_API_AVAILABLE(ios(10), macosx(10.12));
- (instancetype)initWithMode:(AFSSLPinningMode)mode NS_DESIGNATED_INITIALIZER;
@end

@implementation AFSecurityPolicy
- (void)didFinish:(NSURLSessionTaskMetrics *)metrics AF_API_AVAILABLE(ios(10), macosx(10.12)) {
    [self reset];
}
- (void)reset {
    if (@available(macOS 10.11, *)) {
        [self description];
    }
}
@end

NS_ASSUME_NONNULL_END
`
	syms := assertDecoyTwin(t, "p.m", clean)
	for _, want := range []string{"AFSecurityPolicy", "AFSecurityPolicy.SSLPinningMode", "AFSecurityPolicy.metrics",
		"AFSecurityPolicy.initWithMode:", "AFSecurityPolicy.didFinish:", "AFSecurityPolicy.reset", "AFSSLPinningMode"} {
		found := false
		for _, s := range syms {
			found = found || s.QualifiedName == want
		}
		if !found {
			t.Errorf("%s missing:%s", want, describe(syms))
		}
	}
}

func TestObjCInterfaceInsideConditional(t *testing.T) {
	// AFNetworking's UIKit headers wrap the whole @interface in
	// `#if TARGET_OS_IOS`; a clean parse put it under preproc_if, where
	// the Objective-C walker did not look.
	src := `#import <Foundation/Foundation.h>
#if TARGET_OS_IOS
@interface AFNetworkActivityIndicatorManager : NSObject
@property (nonatomic, assign, getter = isEnabled) BOOL enabled;
- (void)incrementActivityCount;
@end
#endif
`
	syms := extractFor(t, "a.h", src)
	for _, want := range []string{"AFNetworkActivityIndicatorManager", "AFNetworkActivityIndicatorManager.enabled", "AFNetworkActivityIndicatorManager.incrementActivityCount"} {
		found := false
		for _, s := range syms {
			found = found || s.QualifiedName == want
		}
		if !found {
			t.Errorf("%s missing:%s", want, describe(syms))
		}
	}
}

func TestCPPExportMacrosDecoyTwin(t *testing.T) {
	// gtest/doctest: export macros between `class` and the name, after
	// `static`, before an operator, macro statements at namespace scope
	// and a member-declaring macro in a class body. Each threw the
	// grammar; a run of them folded gmock.h into one ERROR, and which
	// recovery won depended on the comment lines.
	clean := `namespace testing {
namespace internal {

GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(unsigned char, '\0');
GMOCK_DEFINE_DEFAULT_ACTION_FOR_RETURN_TYPE_(float, 0);

class GTEST_API_ RE {
 public:
  static DOCTEST_CONSTEXPR size_type len = 24;
  RE(const char* regex) { Init(regex); }
  friend DOCTEST_INTERFACE std::ostream& operator<<(std::ostream& s, const RE& in);
 private:
  void Init(const char* regex);
};

struct DOCTEST_INTERFACE IContextScope
{
    DOCTEST_DECLARE_INTERFACE(IContextScope)
    virtual void stringify(std::ostream*) const = 0;
};

bool IsAlpha(char ch) {
  return isalpha(ch) != 0;
}

}  // namespace internal
}  // namespace testing
`
	syms := assertDecoyTwin(t, "gtest.h", clean)
	for _, want := range []string{"testing::internal::RE", "testing::internal::RE::Init", "testing::internal::IContextScope",
		"testing::internal::IContextScope::stringify", "testing::internal::IsAlpha"} {
		found := false
		for _, s := range syms {
			found = found || s.QualifiedName == want
		}
		if !found {
			t.Errorf("%s missing:%s", want, describe(syms))
		}
	}
	for _, s := range syms {
		if strings.HasPrefix(s.Name, "GMOCK_") || s.Name == "GTEST_API_" {
			t.Errorf("macro use read as a symbol: %s %s", s.Kind, s.QualifiedName)
		}
	}
}

func TestCommentCutsMapBack(t *testing.T) {
	src := []byte("int a /* x */ = 1; // tail\n  // only a comment\n#define M(x) \\\n  // inside\nint b;\n/* one\n   two */ int c;\n")
	c := cFamilyCommentCuts("c", src)
	cut := string(c.apply(src))
	want := "int a   = 1;  \n#define M(x) \\\n \nint b;\n     int c;\n"
	if cut != want {
		t.Fatalf("cut = %q, want %q", cut, want)
	}
	// Line 2 of the cut copy is the #define (source line 3); the
	// comment-only line after its backslash stays, blank.
	for cutLine, srcLine := range map[int]int{1: 1, 2: 3, 3: 4, 4: 5, 5: 7} {
		if got := c.row(cutLine); got != srcLine {
			t.Errorf("row(%d) = %d, want %d", cutLine, got, srcLine)
		}
	}
	// `=` is column 8 of the cut line, column 14 of the source line.
	if got := c.column(0, 8); got != 14 {
		t.Errorf("column(0, 8) = %d, want 14", got)
	}
	if got := c.column(4, 5); got != 10 {
		t.Errorf("column(4, 5) = %d, want 10 (`int c` after the block comment)", got)
	}
}

func TestBraceBodyWindowCountsCodeLines(t *testing.T) {
	// The line scanner gives up 500 lines into a body. Counting blank
	// (masked comment) lines toward that let comments cut a class short.
	lines := []string{"class Big {"}
	for i := 0; i < 400; i++ {
		lines = append(lines, "  int f"+fmt.Sprint(i)+";", "")
	}
	lines = append(lines, "};")
	end, _ := extractBraceBody(lines, 0)
	if end != len(lines) {
		t.Fatalf("body ends at %d, want %d", end, len(lines))
	}
}

func TestCppNamespaceOverlappingSpans(t *testing.T) {
	// Recovered namespace spans can overlap without nesting
	// (`namespace testing {` 10-100, `namespace internal {` 11-101);
	// the innermost opening wins, whatever the line counts.
	for _, internalEnd := range []int{101, 140} {
		syms := []core.SymbolRecord{
			{Kind: core.KindNamespace, Name: "testing", QualifiedName: "testing", Span: core.LineRange{Start: 10, End: 100}},
			{Kind: core.KindNamespace, Name: "internal", QualifiedName: "internal", Span: core.LineRange{Start: 11, End: internalEnd}},
			{Kind: core.KindFunction, Name: "IsAlpha", QualifiedName: "IsAlpha", Span: core.LineRange{Start: 50, End: 52}},
		}
		syms = enrichCppNamespaces(syms, "")
		if got := syms[2].QualifiedName; got != "internal::IsAlpha" {
			t.Errorf("internal ends %d: IsAlpha = %q, want internal::IsAlpha", internalEnd, got)
		}
	}
}
