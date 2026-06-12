package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func sym(file, qualified, name string, kind core.SymbolKind, start int, signature, body string, exported bool) core.SymbolRecord {
	return core.SymbolRecord{
		ID:            file + "::" + qualified + "@sha-" + signature + body,
		FilePath:      file,
		Kind:          kind,
		Name:          name,
		QualifiedName: qualified,
		Signature:     signature,
		RawText:       body,
		Span:          core.LineRange{Start: start, End: start + 3},
		Exports:       exported,
	}
}

func TestDiffSymbolsDetectsAddRemoveChange(t *testing.T) {
	before := []core.SymbolRecord{
		sym("auth.go", "Login", "Login", core.KindFunction, 10, "func Login(u string) error", "func Login(u string) error { return nil }", true),
		sym("auth.go", "logout", "logout", core.KindFunction, 20, "func logout()", "func logout() {}", false),
		sym("auth.go", "Gone", "Gone", core.KindFunction, 30, "func Gone()", "func Gone() {}", true),
	}
	after := []core.SymbolRecord{
		// Signature change on exported Login → changed + breaking.
		sym("auth.go", "Login", "Login", core.KindFunction, 10, "func Login(u, p string) error", "func Login(u, p string) error { return nil }", true),
		// Body-only change on unexported logout → changed, not breaking.
		sym("auth.go", "logout", "logout", core.KindFunction, 20, "func logout()", "func logout() { clear() }", false),
		// New symbol.
		sym("auth.go", "Refresh", "Refresh", core.KindFunction, 40, "func Refresh()", "func Refresh() {}", true),
		// Gone removed.
	}

	diff := DiffSymbols(before, after)

	if len(diff.Added) != 1 || diff.Added[0].Name != "Refresh" {
		t.Fatalf("added = %+v", diff.Added)
	}
	if len(diff.Removed) != 1 || diff.Removed[0].Name != "Gone" {
		t.Fatalf("removed = %+v", diff.Removed)
	}
	if len(diff.Changed) != 2 {
		t.Fatalf("changed = %+v", diff.Changed)
	}
	login := diff.Changed[0]
	if login.Before.Name != "Login" || !login.SignatureChanged || !login.BodyChanged {
		t.Fatalf("Login change = %+v", login)
	}
	logout := diff.Changed[1]
	if logout.Before.Name != "logout" || logout.SignatureChanged || !logout.BodyChanged {
		t.Fatalf("logout change = %+v", logout)
	}
	// Breaking: exported signature change (Login) + exported removal (Gone).
	if len(diff.BreakingChanges) != 2 {
		t.Fatalf("breaking = %+v", diff.BreakingChanges)
	}
	if diff.BreakingChanges[0].Before.Name != "Login" || diff.BreakingChanges[1].Before.Name != "Gone" {
		t.Fatalf("breaking order = %+v", diff.BreakingChanges)
	}
	if diff.BreakingChanges[1].After != nil {
		t.Fatalf("removed breaking change should have nil After: %+v", diff.BreakingChanges[1])
	}
}

// TestDiffSymbolsIgnoresLineShiftAndIDChurn is the property that makes the
// diff usable: editing the top of a file changes every symbol's span and ID
// (content SHA), but only actually-edited symbols may be reported.
func TestDiffSymbolsIgnoresLineShiftAndIDChurn(t *testing.T) {
	before := []core.SymbolRecord{
		sym("svc.go", "Serve", "Serve", core.KindFunction, 10, "func Serve()", "func Serve() { run() }", true),
	}
	shifted := sym("svc.go", "Serve", "Serve", core.KindFunction, 50, "func Serve()", "func Serve() { run() }", true)
	shifted.ID = "svc.go::Serve@completely-different-sha"
	shifted.BlobSHA = "completely-different-sha"

	diff := DiffSymbols(before, []core.SymbolRecord{shifted})
	if !diff.Empty() {
		t.Fatalf("span/ID-only churn must not register as change: %+v", diff)
	}
}

func TestDiffSymbolsPairsOverloadsPositionally(t *testing.T) {
	before := []core.SymbolRecord{
		sym("repo.hpp", "Repo.Save", "Save", core.KindMethod, 10, "void Save()", "void Save();", true),
		sym("repo.hpp", "Repo.Save", "Save", core.KindMethod, 11, "void Save(int retries)", "void Save(int retries);", true),
	}
	// One overload dropped.
	after := []core.SymbolRecord{
		sym("repo.hpp", "Repo.Save", "Save", core.KindMethod, 10, "void Save()", "void Save();", true),
	}
	diff := DiffSymbols(before, after)
	if len(diff.Removed) != 1 || diff.Removed[0].Signature != "void Save(int retries)" {
		t.Fatalf("removed = %+v", diff.Removed)
	}
	if len(diff.Changed) != 0 {
		t.Fatalf("changed = %+v", diff.Changed)
	}
}

func TestDiffSymbolsDetectsRename(t *testing.T) {
	body := "func NAME(user string) error {\n\tvalidate(user)\n\treturn store.Save(user)\n}"
	before := []core.SymbolRecord{
		sym("auth.go", "Login", "Login", core.KindFunction, 10, "func Login(user string) error", strings.ReplaceAll(body, "NAME", "Login"), true),
		sym("auth.go", "Logout", "Logout", core.KindFunction, 30, "func Logout()", "func Logout() { sessions.Clear() }", true),
	}
	after := []core.SymbolRecord{
		// Login renamed to SignIn, body otherwise identical.
		sym("auth.go", "SignIn", "SignIn", core.KindFunction, 10, "func SignIn(user string) error", strings.ReplaceAll(body, "NAME", "SignIn"), true),
		sym("auth.go", "Logout", "Logout", core.KindFunction, 30, "func Logout()", "func Logout() { sessions.Clear() }", true),
	}

	diff := DiffSymbols(before, after)
	if len(diff.Added) != 0 || len(diff.Removed) != 0 {
		t.Fatalf("rename must not surface as add/remove: added=%+v removed=%+v", diff.Added, diff.Removed)
	}
	if len(diff.Renamed) != 1 || diff.Renamed[0].Before.Name != "Login" || diff.Renamed[0].After.Name != "SignIn" {
		t.Fatalf("renamed = %+v", diff.Renamed)
	}
	// Exported rename breaks callers of the old name — exactly one breaking
	// entry, carrying both sides (not a bare removal).
	if len(diff.BreakingChanges) != 1 || diff.BreakingChanges[0].After == nil || diff.BreakingChanges[0].After.Name != "SignIn" {
		t.Fatalf("breaking = %+v", diff.BreakingChanges)
	}
	if diff.Empty() {
		t.Fatal("rename is a structural change; diff must not be empty")
	}
}

func TestDiffSymbolsFileMoveIsRenameNotBreaking(t *testing.T) {
	body := "func Helper(x int) int {\n\treturn x * defaultFactor\n}"
	before := []core.SymbolRecord{
		sym("util.go", "Helper", "Helper", core.KindFunction, 5, "func Helper(x int) int", body, true),
	}
	after := []core.SymbolRecord{
		sym("helpers.go", "Helper", "Helper", core.KindFunction, 12, "func Helper(x int) int", body, true),
	}
	diff := DiffSymbols(before, after)
	if len(diff.Renamed) != 1 || diff.Renamed[0].After.FilePath != "helpers.go" {
		t.Fatalf("renamed = %+v", diff.Renamed)
	}
	if len(diff.BreakingChanges) != 0 {
		t.Fatalf("a pure file move keeps the qualified name; breaking = %+v", diff.BreakingChanges)
	}
	if len(diff.Added)+len(diff.Removed) != 0 {
		t.Fatalf("move must not surface as add/remove: %+v", diff)
	}
}

func TestDiffSymbolsAmbiguousBodiesStayAddRemove(t *testing.T) {
	// Two removed and two added symbols share one boilerplate body —
	// pairing would be a guess, so nothing is paired.
	body := "func NAME() error {\n\treturn errNotImplemented\n}"
	mk := func(name string, start int) core.SymbolRecord {
		return sym("svc.go", name, name, core.KindFunction, start, "func "+name+"() error", strings.ReplaceAll(body, "NAME", name), false)
	}
	before := []core.SymbolRecord{mk("oldA", 1), mk("oldB", 10)}
	after := []core.SymbolRecord{mk("newA", 1), mk("newB", 10)}

	diff := DiffSymbols(before, after)
	if len(diff.Renamed) != 0 {
		t.Fatalf("ambiguous bodies must not pair: %+v", diff.Renamed)
	}
	if len(diff.Added) != 2 || len(diff.Removed) != 2 {
		t.Fatalf("expected raw add/remove, got %+v", diff)
	}
}

func TestDiffSymbolsTinyBodiesNotPaired(t *testing.T) {
	before := []core.SymbolRecord{
		sym("a.go", "Old", "Old", core.KindFunction, 1, "func Old()", "func Old() {}", true),
	}
	after := []core.SymbolRecord{
		sym("a.go", "New", "New", core.KindFunction, 1, "func New()", "func New() {}", true),
	}
	diff := DiffSymbols(before, after)
	if len(diff.Renamed) != 0 {
		t.Fatalf("trivial bodies must not pair as rename: %+v", diff.Renamed)
	}
}

func TestDiffSymbolsEmptySnapshots(t *testing.T) {
	if diff := DiffSymbols(nil, nil); !diff.Empty() {
		t.Fatalf("nil diff not empty: %+v", diff)
	}
	added := DiffSymbols(nil, []core.SymbolRecord{
		sym("a.go", "F", "F", core.KindFunction, 1, "func F()", "func F() {}", true),
	})
	if len(added.Added) != 1 || len(added.BreakingChanges) != 0 {
		t.Fatalf("fresh-index diff = %+v", added)
	}
}
