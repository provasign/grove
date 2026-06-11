package graph

import (
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
