package cli

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

// goFixture creates a tmpdir with a minimal Go source file containing
// recognisable symbols so CLI commands that index and search have something
// to work with.
func goFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	src := []byte(`package auth

import "fmt"

// Login logs the user in.
func Login(username, password string) bool {
	fmt.Println("login", username)
	return true
}

// Logout logs the user out.
func Logout(username string) {
	fmt.Println("logout", username)
}
`)
	if err := os.WriteFile(filepath.Join(dir, "auth.go"), src, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return dir
}

// --- dispatch ---

func TestRun_NoArgs(t *testing.T) {
	if got := Run(nil); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestRun_Version(t *testing.T) {
	if got := Run([]string{"version"}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_VersionFlag(t *testing.T) {
	if got := Run([]string{"--version"}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_Help(t *testing.T) {
	for _, cmd := range []string{"help", "--help", "-h"} {
		if got := Run([]string{cmd}); got != 0 {
			t.Errorf("Run([%q]) = %d, want 0", cmd, got)
		}
	}
}

func TestRun_Unknown(t *testing.T) {
	if got := Run([]string{"notacommand"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// --- init ---

func TestRun_Init(t *testing.T) {
	dir := t.TempDir()
	if got := Run([]string{"init", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
	if _, err := os.Stat(filepath.Join(dir, ".grove", "config.yaml")); err != nil {
		t.Errorf("config.yaml not created: %v", err)
	}
}

// --- index ---

func TestRun_Index(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"index", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_IndexDefaultDir(t *testing.T) {
	// default dir "." must resolve to cwd without error
	wd, _ := os.Getwd()
	dir := goFixture(t)
	if err := os.Chdir(dir); err != nil {
		t.Skip("cannot chdir:", err)
	}
	defer os.Chdir(wd)
	if got := Run([]string{"index"}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- status ---

func TestRun_Status(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"status", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- symbols / query ---

func TestRun_Symbols(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"symbols", "Login", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_SymbolsNoArgs(t *testing.T) {
	if got := Run([]string{"symbols"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestRun_Query(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"query", "auth", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_QueryNoArgs(t *testing.T) {
	if got := Run([]string{"query"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// --- deps ---

func TestRun_Deps(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"deps", "auth.go", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_DepsNoArgs(t *testing.T) {
	if got := Run([]string{"deps"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// --- impact ---

func TestRun_Impact(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"impact", "Login", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_ImpactNoArgs(t *testing.T) {
	if got := Run([]string{"impact"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// --- tests ---

func TestRun_Tests(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"tests", "Login", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_TestsNoQuery(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"tests", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- icr ---

func TestRun_ICR(t *testing.T) {
	dir := goFixture(t)
	if got := Run([]string{"icr", "add authentication", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_ICRNoArgs(t *testing.T) {
	if got := Run([]string{"icr"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

// --- conflicts ---

func TestRun_ConflictsTooFewArgs(t *testing.T) {
	if got := Run([]string{"conflicts"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestRun_ConflictsJSON(t *testing.T) {
	a := core.IsolatedChangeRegion{IntentID: "add-auth"}
	b := core.IsolatedChangeRegion{IntentID: "add-logging"}
	aJSON, _ := json.Marshal(a)
	bJSON, _ := json.Marshal(b)
	if got := Run([]string{"conflicts", string(aJSON), string(bJSON)}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_ConflictsBase64(t *testing.T) {
	a := core.IsolatedChangeRegion{IntentID: "add-auth"}
	aJSON, _ := json.Marshal(a)
	aB64 := base64.StdEncoding.EncodeToString(aJSON)
	b := core.IsolatedChangeRegion{IntentID: "add-logging"}
	bJSON, _ := json.Marshal(b)
	if got := Run([]string{"conflicts", aB64, string(bJSON)}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_ConflictsInvalidFirstICR(t *testing.T) {
	// "invalid" is odd-length: not valid padded base64, not valid JSON as ICR.
	bJSON := []byte(`{}`)
	if got := Run([]string{"conflicts", "invalid!!!", string(bJSON)}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

func TestRun_ConflictsInvalidSecondICR(t *testing.T) {
	a := core.IsolatedChangeRegion{IntentID: "x"}
	aJSON, _ := json.Marshal(a)
	if got := Run([]string{"conflicts", string(aJSON), "invalid!!!"}); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
}

// --- lock / unlock ---

func TestRun_LockNoArgs(t *testing.T) {
	if got := Run([]string{"lock"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestRun_Lock(t *testing.T) {
	dir := t.TempDir()
	if got := Run([]string{"lock", "intent1", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_LockWithKeys(t *testing.T) {
	dir := t.TempDir()
	if got := Run([]string{"lock", "intent2", dir, "key1", "key2"}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_UnlockNoArgs(t *testing.T) {
	if got := Run([]string{"unlock"}); got != 2 {
		t.Fatalf("want 2, got %d", got)
	}
}

func TestRun_Unlock(t *testing.T) {
	dir := t.TempDir()
	if got := Run([]string{"unlock", "intent1", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

func TestRun_UnlockAfterLock(t *testing.T) {
	dir := t.TempDir()
	_ = Run([]string{"lock", "intent3", dir, "key1"})
	if got := Run([]string{"unlock", "intent3", dir}); got != 0 {
		t.Fatalf("want 0, got %d", got)
	}
}

// --- argOrDefault helper ---

func TestArgOrDefault(t *testing.T) {
	if got := argOrDefault([]string{"a", "b"}, 0, "def"); got != "a" {
		t.Errorf("got %q", got)
	}
	if got := argOrDefault([]string{}, 0, "def"); got != "def" {
		t.Errorf("got %q", got)
	}
	if got := argOrDefault([]string{""}, 0, "def"); got != "def" {
		t.Errorf("got %q", got)
	}
}
