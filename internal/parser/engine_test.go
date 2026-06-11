package parser

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestDetectLanguage(t *testing.T) {
	tests := map[string]string{
		"main.go":       "go",
		"component.tsx": "tsx",        // TSX uses its own grammar
		"component.ts":  "typescript", // plain TS grammar
		"widget.jsx":    "javascript", // JS grammar handles JSX natively
		"server.cjs":    "javascript",
		"tool.py":       "python",
		"Widget.java":   "java",
		"lib.rs":        "rust",
		"README.md":     "plaintext", // non-code docs are now indexed
	}

	for path, want := range tests {
		if got := DetectLanguage(path); got != want {
			t.Fatalf("DetectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestWalkExtractsGoSymbols(t *testing.T) {
	engine := NewEngine()
	symbols, filesIndexed, err := engine.Walk("../../testdata/repos/simple-go")
	if err != nil {
		t.Fatal(err)
	}
	if filesIndexed != 2 {
		t.Fatalf("filesIndexed = %d, want 2", filesIndexed)
	}

	wanted := map[string]bool{"AuthService": false, "Login": false, "main": false}
	for _, symbol := range symbols {
		if _, ok := wanted[symbol.Name]; ok {
			wanted[symbol.Name] = true
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("expected symbol %q in extracted symbols", name)
		}
	}
}

func TestGoMethodExtractionSetsParentAndKind(t *testing.T) {
	src := `package auth

type Service struct{}

func (s *Service) Login(user string) error {
	return nil
}

func (s Service) Logout(token string) error {
	return nil
}

func NewService() *Service {
	return &Service{}
}
`
	engine := NewEngine()
	symbols, err := extractSymbolsFromString("go", "auth/service.go", src)
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	login, ok := byName["Login"]
	if !ok {
		t.Fatal("Login symbol not found")
	}
	if login.Kind != core.KindMethod {
		t.Fatalf("Login kind = %q, want %q", login.Kind, core.KindMethod)
	}
	if login.ParentSymbol != "Service" {
		t.Fatalf("Login parentSymbol = %q, want %q", login.ParentSymbol, "Service")
	}

	logout, ok := byName["Logout"]
	if !ok {
		t.Fatal("Logout symbol not found")
	}
	if logout.Kind != core.KindMethod {
		t.Fatalf("Logout kind = %q, want %q", logout.Kind, core.KindMethod)
	}
	if logout.ParentSymbol != "Service" {
		t.Fatalf("Logout parentSymbol = %q, want %q", logout.ParentSymbol, "Service")
	}

	ns, ok := byName["NewService"]
	if !ok {
		t.Fatal("NewService not found")
	}
	if ns.Kind != core.KindFunction {
		t.Fatalf("NewService kind = %q, want %q", ns.Kind, core.KindFunction)
	}
	if ns.ParentSymbol != "" {
		t.Fatalf("NewService parentSymbol = %q, want empty", ns.ParentSymbol)
	}
	_ = engine
}

func TestGoSpanEndIsMultiLine(t *testing.T) {
	src := `package main

func Foo() {
	x := 1
	_ = x
}

func Bar() {}
`
	symbols, err := extractSymbolsFromString("go", "main.go", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	foo, ok := byName["Foo"]
	if !ok {
		t.Fatal("Foo not found")
	}
	if foo.Span.Start == foo.Span.End {
		t.Fatalf("Foo span start==end (%d): body extraction not working", foo.Span.Start)
	}
	if foo.Span.End < foo.Span.Start+3 {
		t.Fatalf("Foo span end=%d, want >= %d", foo.Span.End, foo.Span.Start+3)
	}

	bar, ok := byName["Bar"]
	if !ok {
		t.Fatal("Bar not found")
	}
	// Single-line function: start == end is acceptable
	if bar.Span.Start == 0 {
		t.Fatal("Bar span.Start is 0")
	}
}

func TestGoImportExtractionScopedToImportBlock(t *testing.T) {
	src := `package main

import (
	"fmt"
	"os"
)

import "path/filepath"

func main() {
	x := "not/an/import"
	_ = x
	fmt.Println(os.Args)
	_ = filepath.Separator
}
`
	imports := extractGoImports(src)

	want := map[string]bool{"fmt": true, "os": true, "path/filepath": true}
	for _, imp := range imports {
		if !want[imp] {
			t.Fatalf("unexpected import %q extracted", imp)
		}
		delete(want, imp)
	}
	for missing := range want {
		t.Fatalf("expected import %q not found", missing)
	}

	// "not/an/import" inside a string literal must NOT be extracted
	for _, imp := range imports {
		if imp == "not/an/import" {
			t.Fatalf("string literal %q incorrectly extracted as import", imp)
		}
	}
}

func TestGoConstBlockExtraction(t *testing.T) {
	src := `package billing

const (
	PlanFree    = "free"
	PlanPro     = "pro"
	PlanEnterprise = "enterprise"
)

const Singleton = "one"
`
	symbols, err := extractSymbolsFromString("go", "billing.go", src)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool)
	for _, s := range symbols {
		if s.Kind == core.KindConst {
			names[s.Name] = true
		}
	}
	for _, want := range []string{"PlanFree", "PlanPro", "PlanEnterprise", "Singleton"} {
		if !names[want] {
			t.Fatalf("expected const %q not found; got %v", want, names)
		}
	}
}

func TestGoRawTextContainsBody(t *testing.T) {
	src := `package main

func Caller() {
	result := Called()
	_ = result
}

func Called() string {
	return "hello"
}
`
	symbols, err := extractSymbolsFromString("go", "main.go", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	caller, ok := byName["Caller"]
	if !ok {
		t.Fatal("Caller not found")
	}
	if !strings.Contains(caller.RawText, "Called()") {
		t.Fatalf("Caller.RawText does not contain 'Called()': %q", caller.RawText)
	}
}

func TestParseTreeValidatesSupportedLanguage(t *testing.T) {
	engine := NewEngine()
	if err := engine.ParseTree("go", []byte("package main\nfunc main() {}\n")); err != nil {
		t.Fatal(err)
	}
}

// TestGoTypeAndConstExtraction verifies that Tree-sitter correctly extracts
// type aliases, struct types, and grouped const blocks — cases that the old
// regex extractor missed (it skipped them when they followed a var declaration
// because the body-skip advanced past the next declaration).
func TestGoTypeAndConstExtraction(t *testing.T) {
	src := `package billing

var ErrBadAmount = errors.New("bad amount")

type Plan string

const (
	PlanFree       Plan = "free"
	PlanPro        Plan = "pro"
	PlanEnterprise Plan = "enterprise"
)

type Invoice struct {
	ID     int
	Amount float64
}
`
	symbols, err := extractSymbolsFromString("go", "billing.go", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	// type alias
	if p, ok := byName["Plan"]; !ok {
		t.Fatal("Plan type not extracted")
	} else if p.Kind != core.KindType {
		t.Fatalf("Plan.Kind = %q, want %q", p.Kind, core.KindType)
	}

	// grouped consts
	for _, want := range []string{"PlanFree", "PlanPro", "PlanEnterprise"} {
		if s, ok := byName[want]; !ok {
			t.Fatalf("const %q not extracted; got names: %v", want, mapNames(byName))
		} else if s.Kind != core.KindConst {
			t.Fatalf("%s.Kind = %q, want const", want, s.Kind)
		}
	}

	// struct type
	if inv, ok := byName["Invoice"]; !ok {
		t.Fatal("Invoice struct not extracted")
	} else if inv.Kind != core.KindStruct {
		t.Fatalf("Invoice.Kind = %q, want struct", inv.Kind)
	}

	// var before the type decls
	if _, ok := byName["ErrBadAmount"]; !ok {
		t.Fatal("ErrBadAmount var not extracted")
	}
}

// TestGoNoLocalVarsExtracted verifies that variables declared inside function
// bodies (e.g. `var req struct{...}`) are never extracted as top-level symbols.
func TestGoNoLocalVarsExtracted(t *testing.T) {
	src := `package api

func HandleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string
		Password string
	}
	_ = req
}

func HandleLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string
	}
	_ = req
}
`
	symbols, err := extractSymbolsFromString("go", "handler.go", src)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range symbols {
		if s.Name == "req" {
			t.Fatalf("local variable 'req' was incorrectly extracted as symbol: %+v", s)
		}
	}
	// Only the two functions should be extracted
	if len(symbols) != 2 {
		t.Fatalf("expected 2 symbols (HandleLogin, HandleLogout), got %d: %v", len(symbols), mapNames2(symbols))
	}
}

// TestTypeScriptClassAndMethodExtraction verifies Tree-sitter extraction for TS.
func TestTypeScriptClassAndMethodExtraction(t *testing.T) {
	src := `
export class AuthService {
  private users: Map<string, User> = new Map();

  register(username: string, email: string): User {
    const user = { id: 1, username, email };
    this.users.set(username, user);
    return user;
  }

  login(username: string, password: string): string {
    return "token-" + username;
  }
}

export interface TokenStore {
  save(token: string, userID: number): void;
  lookup(token: string): number | null;
}

export type Plan = "free" | "pro" | "enterprise";
`
	symbols, err := extractSymbolsFromString("typescript", "auth.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["AuthService"]; !ok {
		t.Fatal("AuthService class not extracted")
	} else if s.Kind != core.KindClass {
		t.Fatalf("AuthService.Kind = %q, want class", s.Kind)
	}

	if s, ok := byName["register"]; !ok {
		t.Fatal("register method not extracted")
	} else if s.Kind != core.KindMethod {
		t.Fatalf("register.Kind = %q, want method", s.Kind)
	} else if s.ParentSymbol != "AuthService" {
		t.Fatalf("register.ParentSymbol = %q, want AuthService", s.ParentSymbol)
	}

	if _, ok := byName["TokenStore"]; !ok {
		t.Fatal("TokenStore interface not extracted")
	}

	if s, ok := byName["Plan"]; !ok {
		t.Fatal("Plan type alias not extracted")
	} else if s.Kind != core.KindType {
		t.Fatalf("Plan.Kind = %q, want type", s.Kind)
	}
}

// TestPythonClassAndMethodExtraction verifies Tree-sitter extraction for Python.
func TestPythonClassAndMethodExtraction(t *testing.T) {
	src := `
class AuthService:
    def __init__(self, token_store):
        self._store = token_store
        self._users = {}

    def register(self, username, email):
        user = {"id": len(self._users) + 1, "username": username}
        self._users[username] = user
        return user

    def login(self, username, password):
        return f"tok-{username}"


def create_service(store):
    return AuthService(store)
`
	symbols, err := extractSymbolsFromString("python", "auth.py", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["AuthService"]; !ok {
		t.Fatal("AuthService class not extracted")
	} else if s.Kind != core.KindClass {
		t.Fatalf("AuthService.Kind = %q, want class", s.Kind)
	}

	if s, ok := byName["register"]; !ok {
		t.Fatal("register method not extracted")
	} else if s.Kind != core.KindMethod {
		t.Fatalf("register.Kind = %q, want method", s.Kind)
	} else if s.ParentSymbol != "AuthService" {
		t.Fatalf("register.ParentSymbol = %q, want AuthService", s.ParentSymbol)
	}

	if s, ok := byName["create_service"]; !ok {
		t.Fatal("create_service function not extracted")
	} else if s.Kind != core.KindFunction {
		t.Fatalf("create_service.Kind = %q, want function", s.Kind)
	}
}

func TestPythonDecoratedDefinitionsExtraction(t *testing.T) {
	src := `
def route(path):
    def decorator(fn):
        return fn
    return decorator

@route("/login")
def login_handler(request):
    return {"ok": True}

@dataclass
class Session:
    token: str

    @property
    def expired(self):
        return False
`
	symbols, err := extractSymbolsFromString("python", "handlers.py", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["login_handler"]; !ok {
		t.Fatal("decorated login_handler function not extracted")
	} else if s.Kind != core.KindFunction {
		t.Fatalf("login_handler.Kind = %q, want function", s.Kind)
	}

	if s, ok := byName["Session"]; !ok {
		t.Fatal("decorated Session class not extracted")
	} else if s.Kind != core.KindClass {
		t.Fatalf("Session.Kind = %q, want class", s.Kind)
	}

	if s, ok := byName["expired"]; !ok {
		t.Fatal("decorated expired method not extracted")
	} else if s.Kind != core.KindMethod {
		t.Fatalf("expired.Kind = %q, want method", s.Kind)
	} else if s.ParentSymbol != "Session" {
		t.Fatalf("expired.ParentSymbol = %q, want Session", s.ParentSymbol)
	}
}

// TestJavaScriptExportedFlagLowercaseSymbols verifies that lowercase exported
// symbols (export function login, export const makeToken) get Exports=true.
// This was broken when isCapitalized() was used as the export heuristic for JS.
func TestJavaScriptExportedFlagLowercaseSymbols(t *testing.T) {
	src := `
export function login(username, password) {
  return fetch('/api/login', { method: 'POST' });
}

export const makeToken = (username) => "tok-" + username;

export default function logout(token) {
  return fetch('/api/logout');
}

class BillingService {
  charge(amount) { return { amount }; }
}

function internalHelper() {}
`
	symbols, err := extractSymbolsFromString("javascript", "api.js", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	// Exported lowercase function
	if s, ok := byName["login"]; !ok {
		t.Fatal("login function not extracted")
	} else if !s.Exports {
		t.Fatal("login.Exports = false, want true (it is explicitly exported)")
	}

	// Exported arrow function
	if s, ok := byName["makeToken"]; !ok {
		t.Fatal("makeToken not extracted")
	} else if !s.Exports {
		t.Fatal("makeToken.Exports = false, want true")
	}

	// export default function
	if s, ok := byName["logout"]; !ok {
		t.Fatal("logout function not extracted")
	} else if !s.Exports {
		t.Fatal("logout.Exports = false, want true")
	}

	// Class (not exported)
	if s, ok := byName["BillingService"]; !ok {
		t.Fatal("BillingService not extracted")
	} else if s.Exports {
		t.Fatal("BillingService.Exports = true, but it has no export keyword")
	}

	// Internal helper (not exported)
	if s, ok := byName["internalHelper"]; !ok {
		t.Fatal("internalHelper not extracted")
	} else if s.Exports {
		t.Fatal("internalHelper.Exports = true, but it has no export keyword")
	}
}

// TestJavaClassAndMethodExtraction verifies Tree-sitter extraction for Java.
func TestJavaClassAndMethodExtraction(t *testing.T) {
	src := `
public class AuthService {
    private Map<String, User> users = new HashMap<>();

    public User register(String username, String email) {
        User u = new User(username, email);
        users.put(username, u);
        return u;
    }

    public String login(String username, String password) {
        return "tok-" + username;
    }

    protected void logout(String token) {
        // noop
    }
}

interface TokenStore {
    String save(String token, int userID);
    User lookup(String token);
}
`
	symbols, err := extractSymbolsFromString("java", "AuthService.java", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["AuthService"]; !ok {
		t.Fatal("AuthService class not extracted")
	} else if s.Kind != core.KindClass {
		t.Fatalf("AuthService.Kind = %q, want class", s.Kind)
	} else if !s.Exports {
		t.Fatal("AuthService.Exports = false, want true (public class)")
	}

	if s, ok := byName["register"]; !ok {
		t.Fatal("register method not extracted")
	} else if s.Kind != core.KindMethod {
		t.Fatalf("register.Kind = %q, want method", s.Kind)
	} else if s.ParentSymbol != "AuthService" {
		t.Fatalf("register.ParentSymbol = %q, want AuthService", s.ParentSymbol)
	} else if !s.Exports {
		t.Fatal("register.Exports = false, want true (public method)")
	}

	if s, ok := byName["login"]; !ok {
		t.Fatal("login method not extracted")
	} else if s.Kind != core.KindMethod {
		t.Fatalf("login.Kind = %q, want method", s.Kind)
	}

	if _, ok := byName["TokenStore"]; !ok {
		t.Fatal("TokenStore interface not extracted")
	}
}

// TestRustStructAndImplExtraction verifies Tree-sitter extraction for Rust.
func TestRustStructAndImplExtraction(t *testing.T) {
	src := `
pub struct Service {
    users: std::collections::HashMap<String, User>,
}

impl Service {
    pub fn new() -> Self {
        Service { users: std::collections::HashMap::new() }
    }

    pub fn register(&mut self, username: &str) -> User {
        let user = User::new(username);
        self.users.insert(username.to_string(), user.clone());
        user
    }

    fn internal_validate(&self, token: &str) -> bool {
        true
    }
}

pub fn create_service() -> Service {
    Service::new()
}

pub enum Plan {
    Free,
    Pro,
    Enterprise,
}

pub trait AuthProvider {
    fn authenticate(&self, username: &str, password: &str) -> bool;
}
`
	symbols, err := extractSymbolsFromString("rust", "service.rs", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["Service"]; !ok {
		t.Fatal("Service struct not extracted")
	} else if s.Kind != core.KindStruct {
		t.Fatalf("Service.Kind = %q, want struct", s.Kind)
	} else if !s.Exports {
		t.Fatal("Service.Exports = false, want true (pub struct)")
	}

	if s, ok := byName["new"]; !ok {
		t.Fatal("new method not extracted")
	} else if s.Kind != core.KindConstructor {
		t.Fatalf("new.Kind = %q, want constructor", s.Kind)
	} else if s.ParentSymbol != "Service" {
		t.Fatalf("new.ParentSymbol = %q, want Service", s.ParentSymbol)
	}

	if s, ok := byName["register"]; !ok {
		t.Fatal("register method not extracted")
	} else if s.ParentSymbol != "Service" {
		t.Fatalf("register.ParentSymbol = %q, want Service", s.ParentSymbol)
	}

	if s, ok := byName["create_service"]; !ok {
		t.Fatal("create_service function not extracted")
	} else if s.Kind != core.KindFunction {
		t.Fatalf("create_service.Kind = %q, want function", s.Kind)
	}

	if s, ok := byName["Plan"]; !ok {
		t.Fatal("Plan enum not extracted")
	} else if s.Kind != core.KindEnum {
		t.Fatalf("Plan.Kind = %q, want enum", s.Kind)
	}

	if s, ok := byName["AuthProvider"]; !ok {
		t.Fatal("AuthProvider trait not extracted")
	} else if s.Kind != core.KindTrait {
		t.Fatalf("AuthProvider.Kind = %q, want trait", s.Kind)
	}
}

// TestTSXComponentExtraction verifies that .tsx files use the TSX grammar
// (which understands JSX syntax) rather than the plain TypeScript grammar.
// This was broken: tsx was mapped to "typescript" language, which caused
// extractSymbolsFromAST to return an empty result for JSX syntax.
func TestTSXComponentExtraction(t *testing.T) {
	src := `
import React from 'react';

interface ButtonProps {
  label: string;
  onClick: () => void;
}

export const Button: React.FC<ButtonProps> = ({ label, onClick }) => {
  return <button onClick={onClick}>{label}</button>;
};

export default function App() {
  return <div className="app"><Button label="Click" onClick={() => {}} /></div>;
}

export class Widget extends React.Component<ButtonProps> {
  render() {
    return <div>{this.props.label}</div>;
  }
}
`
	symbols, err := extractSymbolsFromString("tsx", "Widget.tsx", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) == 0 {
		t.Fatal("TSX file extracted zero symbols — TSX grammar not used (got plain TS grammar for JSX source)")
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if _, ok := byName["ButtonProps"]; !ok {
		t.Fatal("ButtonProps interface not extracted from TSX")
	}

	if s, ok := byName["Button"]; !ok {
		t.Fatal("Button component not extracted from TSX")
	} else if !s.Exports {
		t.Fatal("Button.Exports = false, want true")
	}

	if s, ok := byName["App"]; !ok {
		t.Fatal("App function not extracted from TSX")
	} else if !s.Exports {
		t.Fatal("App.Exports = false, want true")
	}

	if s, ok := byName["Widget"]; !ok {
		t.Fatal("Widget class not extracted from TSX")
	} else if s.Kind != core.KindClass {
		t.Fatalf("Widget.Kind = %q, want class", s.Kind)
	}
}

func TestTSXImportExtraction(t *testing.T) {
	src := `
import React from 'react';
import { Button } from './Button';

export function App() {
  return <Button />;
}
`
	imports := extractImports("tsx", src)
	want := map[string]bool{"react": false, "./Button": false}
	for _, imp := range imports {
		if _, ok := want[imp]; ok {
			want[imp] = true
		}
	}
	for imp, found := range want {
		if !found {
			t.Fatalf("expected TSX import %q not found in %v", imp, imports)
		}
	}
}

// TestJSXComponentExtraction verifies that .jsx files (language="javascript")
// extract React component functions and classes correctly.
// The tree-sitter-javascript grammar supports JSX natively.
func TestJSXComponentExtraction(t *testing.T) {
	src := `
import React from 'react';

export function Greeting({ name }) {
  return <div>Hello {name}</div>;
}

export const Card = ({ title, children }) => (
  <div className="card">
    <h2>{title}</h2>
    {children}
  </div>
);

class Form extends React.Component {
  handleSubmit(e) {
    e.preventDefault();
  }

  render() {
    return <form onSubmit={this.handleSubmit}>{this.props.children}</form>;
  }
}
`
	symbols, err := extractSymbolsFromString("javascript", "Form.jsx", src)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) == 0 {
		t.Fatal("JSX file extracted zero symbols")
	}
	byName := make(map[string]core.SymbolRecord)
	for _, s := range symbols {
		byName[s.Name] = s
	}

	if s, ok := byName["Greeting"]; !ok {
		t.Fatal("Greeting function not extracted")
	} else if !s.Exports {
		t.Fatal("Greeting.Exports = false, want true")
	}

	if s, ok := byName["Card"]; !ok {
		t.Fatal("Card arrow function not extracted")
	} else if !s.Exports {
		t.Fatal("Card.Exports = false, want true")
	}

	if s, ok := byName["Form"]; !ok {
		t.Fatal("Form class not extracted")
	} else if s.Kind != core.KindClass {
		t.Fatalf("Form.Kind = %q, want class", s.Kind)
	}

	if s, ok := byName["handleSubmit"]; !ok {
		t.Fatal("handleSubmit method not extracted")
	} else if s.ParentSymbol != "Form" {
		t.Fatalf("handleSubmit.ParentSymbol = %q, want Form", s.ParentSymbol)
	}
}

// ─── helpers ──────────────────────────────────────────────────────────────────

func mapNames(m map[string]core.SymbolRecord) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}

func mapNames2(syms []core.SymbolRecord) []string {
	names := make([]string, 0, len(syms))
	for _, s := range syms {
		names = append(names, s.Name)
	}
	return names
}

// extractSymbolsFromString is a test helper that extracts symbols from
// in-memory source content (not a file on disk).
func extractSymbolsFromString(language, filePath, src string) ([]core.SymbolRecord, error) {
	blobSHA := "testsha"
	imports := extractImports(language, src)
	symbols := extractSymbols(language, filePath, blobSHA, src, imports)
	ensureUniqueIDs(symbols)
	return symbols, nil
}

// TestSameNameSymbolsGetDistinctIDs guards against the ID-collision bug where
// same-named members in one file produced identical IDs and all but one were
// silently dropped by the store's PRIMARY KEY dedup.
func TestSameNameSymbolsGetDistinctIDs(t *testing.T) {
	cases := []struct {
		language string
		filePath string
		src      string
		name     string
		want     int
	}{
		{"go", "conn.go", `package p

type A struct{}
type B struct{}

func (a *A) Close() error { return nil }
func (b *B) Close() error { return nil }
`, "Close", 2},
		{"python", "models.py", `class User:
    def __init__(self):
        pass
    def save(self):
        pass

class Account:
    def __init__(self):
        pass
    def save(self):
        pass
`, "save", 2},
		{"cpp", "repo.hpp", "class Repo {\npublic:\n    void Save();\n    void Save(int retries);\n};\n", "Save", 2},
	}
	for _, tc := range cases {
		syms, err := extractSymbolsFromString(tc.language, tc.filePath, tc.src)
		if err != nil {
			t.Fatalf("%s: %v", tc.language, err)
		}
		ids := map[string]bool{}
		count := 0
		for _, s := range syms {
			if s.Name != tc.name {
				continue
			}
			count++
			if ids[s.ID] {
				t.Errorf("%s: duplicate symbol ID %q for %s", tc.language, s.ID, tc.name)
			}
			ids[s.ID] = true
		}
		if count != tc.want {
			t.Errorf("%s: %s symbol count = %d, want %d; symbols=%#v", tc.language, tc.name, count, tc.want, syms)
		}
	}
}

// TestGoMethodQualifiedNameIncludesReceiver pins the receiver-qualified
// naming that keeps method IDs distinct across receiver types.
func TestGoMethodQualifiedNameIncludesReceiver(t *testing.T) {
	src := `package p

type Service struct{}

func (s *Service) Login() {}
`
	syms, err := extractSymbolsFromString("go", "svc.go", src)
	if err != nil {
		t.Fatal(err)
	}
	byQualified := qualifiedNameIndex(syms)
	login, ok := byQualified["Service.Login"]
	if !ok {
		t.Fatalf("Service.Login not found; got %v", symbolQualifiedNames(syms))
	}
	if login.Kind != core.KindMethod || login.ParentSymbol != "Service" {
		t.Fatalf("Service.Login = kind %q parent %q, want method/Service", login.Kind, login.ParentSymbol)
	}
}

func symbolQualifiedNames(syms []core.SymbolRecord) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.QualifiedName)
	}
	return out
}

// TestExtractSymbolsWithSyntaxError verifies that a file with an AST syntax
// error (e.g. a function being actively typed) still yields the well-formed
// symbols before the error AND recovers the partially-declared symbol via the
// regex fallback merge.
func TestExtractSymbolsWithSyntaxError(t *testing.T) {
	// Valid function followed by a function whose closing brace is missing —
	// tree-sitter will mark the second declaration (and beyond) as an ERROR node.
	src := `package demo

func Stable() string {
	return "ok"
}

func InProgress( // syntax error: missing closing paren + body
`
	syms, err := extractSymbolsFromString("go", "demo.go", src)
	if err != nil {
		t.Fatal(err)
	}

	byName := make(map[string]core.SymbolRecord, len(syms))
	for _, s := range syms {
		byName[s.Name] = s
	}

	// The well-formed function must always be present.
	if _, ok := byName["Stable"]; !ok {
		t.Error("Stable (syntactically valid) is missing from extraction result")
	}

	// The in-progress function must appear via the regex fallback merge.
	if _, ok := byName["InProgress"]; !ok {
		t.Error("InProgress (inside ERROR subtree) should be recovered via regex merge")
	}
}

// TestMergeSymbolsPreservesASTOverRegex verifies that when the same name
// appears in both AST and regex results, the AST version (more accurate) wins.
func TestMergeSymbolsPreservesASTOverRegex(t *testing.T) {
	ast := []core.SymbolRecord{
		{Name: "Foo", Signature: "ast-sig", Kind: core.KindFunction},
	}
	regex := []core.SymbolRecord{
		{Name: "Foo", Signature: "regex-sig", Kind: core.KindFunction},
		{Name: "Bar", Signature: "regex-only", Kind: core.KindFunction},
	}
	merged := mergeSymbols(ast, regex)
	if len(merged) != 2 {
		t.Fatalf("want 2 merged symbols, got %d: %v", len(merged), merged)
	}
	fooIdx := -1
	for i, s := range merged {
		if s.Name == "Foo" {
			fooIdx = i
		}
	}
	if fooIdx < 0 {
		t.Fatal("Foo missing from merged result")
	}
	if merged[fooIdx].Signature != "ast-sig" {
		t.Errorf("Foo.Signature = %q, want AST version %q", merged[fooIdx].Signature, "ast-sig")
	}
	barFound := false
	for _, s := range merged {
		if s.Name == "Bar" {
			barFound = true
		}
	}
	if !barFound {
		t.Error("Bar (regex-only) should be present in merged result")
	}
}

// ─── New language tests ───────────────────────────────────────────────────────

func TestCFunctionAndStructExtraction(t *testing.T) {
	src := `#include <stdio.h>

typedef struct {
    int x;
    int y;
} Point;

int add(int a, int b) {
    return a + b;
}

static void _internal(void) {}
`
	syms, err := extractSymbolsFromString("c", "demo.c", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := nameIndex(syms)
	if _, ok := byName["add"]; !ok {
		t.Error("add function missing")
	}
	if _, ok := byName["Point"]; !ok {
		t.Error("Point struct missing")
	}
	// Imports: #include should be captured.
	imports := extractImports("c", src)
	found := false
	for _, imp := range imports {
		if imp == "stdio.h" {
			found = true
		}
	}
	if !found {
		t.Error("#include <stdio.h> not captured as import")
	}
}

func TestCppClassAndMethodExtraction(t *testing.T) {
	src := `#include <string>

namespace net {

class Server {
public:
    Server(int port);
    void Start();
    void Stop();
private:
    int port_;
};

Server::Server(int port) : port_(port) {}

void Server::Start() {
    // listen
}

} // namespace net
`
	syms, err := extractSymbolsFromString("cpp", "server.cpp", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := nameIndex(syms)
	if _, ok := byName["Server"]; !ok {
		t.Error("Server class missing")
	}
	if _, ok := byName["net"]; !ok {
		t.Error("net namespace missing")
	}
}

func TestCSharpClassAndMethodExtraction(t *testing.T) {
	src := `using System;
using System.Collections.Generic;

namespace MyApp.Services {

    public class UserService {
        private readonly IUserRepository _repo;

        public UserService(IUserRepository repo) {
            _repo = repo;
        }

        public async Task<User> GetByIdAsync(int id) {
            return await _repo.FindAsync(id);
        }

        private void Validate(User user) {}
    }

    public interface IUserRepository {
        Task<User> FindAsync(int id);
    }
}
`
	syms, err := extractSymbolsFromString("csharp", "UserService.cs", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := nameIndex(syms)
	if _, ok := byName["UserService"]; !ok {
		t.Error("UserService class missing")
	}
	if _, ok := byName["IUserRepository"]; !ok {
		t.Error("IUserRepository interface missing")
	}
	imports := extractImports("csharp", src)
	foundSystem := false
	for _, imp := range imports {
		if imp == "System" {
			foundSystem = true
		}
	}
	if !foundSystem {
		t.Error("'using System' not captured as import")
	}
}

func TestPHPClassAndFunctionExtraction(t *testing.T) {
	src := `<?php

namespace App\Services;

use App\Models\User;

class AuthService {
    private UserRepository $repo;

    public function __construct(UserRepository $repo) {
        $this->repo = $repo;
    }

    public function login(string $email, string $password): ?User {
        return $this->repo->findByEmail($email);
    }
}

interface Authenticatable {
    public function authenticate(): bool;
}

function hashPassword(string $password): string {
    return password_hash($password, PASSWORD_BCRYPT);
}
`
	syms, err := extractSymbolsFromString("php", "AuthService.php", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := nameIndex(syms)
	if _, ok := byName["AuthService"]; !ok {
		t.Error("AuthService class missing")
	}
	if _, ok := byName["Authenticatable"]; !ok {
		t.Error("Authenticatable interface missing")
	}
	if _, ok := byName["hashPassword"]; !ok {
		t.Error("hashPassword function missing")
	}
}

func TestLanguageDetectionNewLanguages(t *testing.T) {
	cases := map[string]string{
		"main.c":     "c",
		"util.h":     "c",
		"engine.cpp": "cpp",
		"server.cc":  "cpp",
		"types.hpp":  "cpp",
		"Service.cs": "csharp",
		"index.php":  "php",
		"page.phtml": "php",
	}
	for path, want := range cases {
		got := DetectLanguage(path)
		if got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCPPClassMemberExtraction(t *testing.T) {
	src := `class Repo {
public:
    Repo();
    static void Save();
};
`
	syms, err := extractSymbolsFromString("cpp", "repo.hpp", src)
	if err != nil {
		t.Fatal(err)
	}
	byName := nameIndex(syms)
	repo, ok := symbolByKind(syms, "Repo", core.KindClass)
	if !ok {
		t.Fatal("Repo class missing")
	}
	if repo.Kind != core.KindClass {
		t.Fatalf("Repo kind = %q, want %q", repo.Kind, core.KindClass)
	}
	save, ok := byName["Save"]
	if !ok {
		t.Fatal("Save method missing")
	}
	if save.Kind != core.KindMethod {
		t.Fatalf("Save kind = %q, want %q", save.Kind, core.KindMethod)
	}
	if save.ParentSymbol != "Repo" {
		t.Fatalf("Save parentSymbol = %q, want Repo", save.ParentSymbol)
	}
	byQualified := qualifiedNameIndex(syms)
	ctor := byQualified["Repo.Repo"]
	if ctor.Kind != core.KindConstructor {
		t.Fatalf("Repo constructor kind = %q, want %q", ctor.Kind, core.KindConstructor)
	}
}

func TestCPPOverloadedMemberExtraction(t *testing.T) {
	src := `class Repo {
public:
    void Save();
    void Save(int retries);
};
`
	syms, err := extractSymbolsFromString("cpp", "repo.hpp", src)
	if err != nil {
		t.Fatal(err)
	}
	var saveCount int
	for _, s := range syms {
		if s.Name == "Save" && s.Kind == core.KindMethod && s.ParentSymbol == "Repo" {
			saveCount++
		}
	}
	if saveCount != 2 {
		t.Fatalf("Save method count = %d, want 2; symbols=%#v", saveCount, syms)
	}
}

// nameIndex builds a map[name]SymbolRecord for assertion helpers.
func nameIndex(syms []core.SymbolRecord) map[string]core.SymbolRecord {
	m := make(map[string]core.SymbolRecord, len(syms))
	for _, s := range syms {
		m[s.Name] = s
	}
	return m
}

func qualifiedNameIndex(syms []core.SymbolRecord) map[string]core.SymbolRecord {
	m := make(map[string]core.SymbolRecord, len(syms))
	for _, s := range syms {
		m[s.QualifiedName] = s
	}
	return m
}

func symbolByKind(syms []core.SymbolRecord, name string, kind core.SymbolKind) (core.SymbolRecord, bool) {
	for _, s := range syms {
		if s.Name == name && s.Kind == kind {
			return s, true
		}
	}
	return core.SymbolRecord{}, false
}
