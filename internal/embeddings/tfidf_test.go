// Tests for the TF-IDF semantic embedding engine.
package embeddings

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func makeSymbol(name, qualified, sig, doc string) core.SymbolRecord {
	return core.SymbolRecord{
		ID:            "x::" + name,
		Name:          name,
		QualifiedName: qualified,
		Signature:     sig,
		Docstring:     doc,
		Kind:          core.KindFunction,
	}
}

func TestTFIDF_TokenizationSplitsCamelAndSnake(t *testing.T) {
	got := tokenize("getUserById get_user_by_id httpRequest HTTPRequest")
	want := []string{
		"get", "user", "by", "id",
		"get", "user", "by", "id",
		"http", "request",
		"http", "request",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tokenize:\n got  %v\n want %v", got, want)
	}
}

func TestTFIDF_StopwordsAreFiltered(t *testing.T) {
	got := tokenize("the func of return")
	if len(got) != 0 {
		t.Fatalf("expected all stopwords filtered, got %v", got)
	}
}

func TestTFIDF_QueryRanksLexicallyOverlappingSymbolsFirst(t *testing.T) {
	syms := []core.SymbolRecord{
		makeSymbol("renderTemplate", "html.renderTemplate", "func renderTemplate(t Template) string", "Render an HTML template to a string."),
		makeSymbol("parseInt", "math.parseInt", "func parseInt(s string) (int, error)", "Parse an integer from a string."),
		makeSymbol("authenticateUser", "auth.authenticateUser", "func authenticateUser(u User) bool", "Authenticate the user against the credentials store."),
		makeSymbol("logout", "auth.logout", "func logout(u User) error", "Log the user out of all sessions."),
	}
	eng := NewTFIDF()
	eng.Index(syms)

	results := eng.Query("authenticate a user against credentials", 3)
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Symbol.Name != "authenticateUser" {
		t.Fatalf("top result = %q, want authenticateUser", results[0].Symbol.Name)
	}
	// Score must be in (0, 1].
	if results[0].Score <= 0 || results[0].Score > 1+1e-9 {
		t.Fatalf("score = %v, want in (0, 1]", results[0].Score)
	}
}

func TestTFIDF_EmptyQueryReturnsNoResults(t *testing.T) {
	eng := NewTFIDF()
	eng.Index([]core.SymbolRecord{makeSymbol("x", "x", "", "")})
	if r := eng.Query("", 5); r != nil {
		t.Fatalf("expected nil, got %v", r)
	}
}

func TestTFIDF_NoSymbolsReturnsNoResults(t *testing.T) {
	eng := NewTFIDF()
	eng.Index(nil)
	if r := eng.Query("anything", 5); r != nil {
		t.Fatalf("expected nil, got %v", r)
	}
}

func TestTFIDF_RespectsLimit(t *testing.T) {
	// Mix in a distinct doc so "authenticate" doesn't get df==N and IDF==0.
	syms := []core.SymbolRecord{
		makeSymbol("authA", "a", "func authA()", "authenticate user"),
		makeSymbol("authB", "b", "func authB()", "authenticate user"),
		makeSymbol("authC", "c", "func authC()", "authenticate user"),
		makeSymbol("render", "r", "func render()", "render template html"),
		makeSymbol("parse", "p", "func parse()", "parse json bytes"),
	}
	eng := NewTFIDF()
	eng.Index(syms)
	r := eng.Query("authenticate user", 2)
	if len(r) != 2 {
		t.Fatalf("got %d, want 2", len(r))
	}
}

func TestSplitCamel_HandlesAcronymRunsAndDigits(t *testing.T) {
	got := splitCamel("loadJSONAt2024")
	want := []string{"load", "JSON", "At", "2024"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("splitCamel = %v, want %v", got, want)
	}
}
