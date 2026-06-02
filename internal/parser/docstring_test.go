package parser

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestGoDocstringFromLineComment(t *testing.T) {
	src := `package x

// Login authenticates the given user.
// Returns an error on failure.
func Login() error { return nil }
`
	syms := mustExtract(t, "go", "a.go", src)
	requireSymbolWithDoc(t, syms, "Login", "Login authenticates the given user.\nReturns an error on failure.")
}

func TestTypeScriptDocstringFromJSDoc(t *testing.T) {
	src := `/**
 * Authenticate the user against the directory.
 * @param u User
 */
export function login(u: User) {}
`
	syms := mustExtract(t, "typescript", "auth.ts", src)
	requireSymbolWithDoc(t, syms, "login", "Authenticate the user against the directory.")
}

func TestPythonDocstringTripleQuoted(t *testing.T) {
	src := `def login(user):
    """Authenticate the user.

    Returns True on success.
    """
    return True
`
	syms := mustExtract(t, "python", "auth.py", src)
	requireSymbolWithDoc(t, syms, "login", "Authenticate the user.")
}

func TestRustDocstringFromTripleSlash(t *testing.T) {
	src := `/// Authenticate the caller.
/// Returns Ok on success.
pub fn login() -> Result<(), Error> { Ok(()) }
`
	syms := mustExtract(t, "rust", "auth.rs", src)
	requireSymbolWithDoc(t, syms, "login", "Authenticate the caller.\nReturns Ok on success.")
}

func TestJavaDocstringFromJavadoc(t *testing.T) {
	src := `public class AuthService {
    /**
     * Login the user.
     */
    public void login() {}
}
`
	syms := mustExtract(t, "java", "AuthService.java", src)
	requireSymbolWithDoc(t, syms, "login", "Login the user.")
}

func mustExtract(t *testing.T, language, filePath, src string) []core.SymbolRecord {
	t.Helper()
	syms, err := extractSymbolsFromString(language, filePath, src)
	if err != nil {
		t.Fatalf("extract %s: %v", language, err)
	}
	return syms
}

func requireSymbolWithDoc(t *testing.T, syms []core.SymbolRecord, name, wantContains string) {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			if !strings.Contains(s.Docstring, wantContains) {
				t.Fatalf("docstring for %q = %q; want to contain %q", name, s.Docstring, wantContains)
			}
			return
		}
	}
	t.Fatalf("symbol %q not found; got: %+v", name, namesOf(syms))
}

func namesOf(syms []core.SymbolRecord) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Name)
	}
	return out
}
