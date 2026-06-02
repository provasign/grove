// Package languages_test is an end-to-end smoke test that creates a tiny
// multi-language fixture repo on disk, indexes it through the full
// engine→store→graph pipeline, and asserts that:
//
//   - Every supported language extracts at least one symbol of the expected kind.
//   - The 8 edge types are produced (where applicable).
//   - FTS5 search returns symbols by name.
//   - Re-indexing the unchanged tree skips every file (delta indexing).
package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

type fixture struct {
	relPath  string
	contents string
}

var languageFixtures = []fixture{
	{
		"go/auth/auth.go",
		`package auth

import "fmt"

// Login authenticates the caller.
func Login(user string) error {
	fmt.Println(user)
	return nil
}

// Service handles auth requests.
type Service struct{}

func (s *Service) Run() error { return Login("alice") }
`,
	},
	{
		"go/auth/auth_test.go",
		`package auth

import "testing"

func TestLogin(t *testing.T) { _ = Login("x") }
`,
	},
	{
		"ts/src/auth.ts",
		`import { Logger } from "./logger";

/** Authenticate the caller. */
export class AuthService extends Logger {
    login(user: string): boolean { return true; }
}

export function helper(): void {}
`,
	},
	{
		"ts/src/logger.ts",
		`export class Logger {
    info(msg: string) {}
}
`,
	},
	{
		"py/auth.py",
		`class Base:
    pass


class User(Base):
    """A user account."""
    def login(self):
        """Authenticate the caller."""
        return True
`,
	},
	{
		"java/com/example/AuthService.java",
		`package com.example;

public class AuthService implements Runnable {
    /** Login the user. */
    public void login() {}

    @Override
    public void run() { login(); }
}

interface Runnable {
    void run();
}
`,
	},
	{
		"rust/src/lib.rs",
		`/// Authenticate the caller.
pub fn login() -> bool { true }

pub trait Greet { fn greet(&self); }

pub struct User { pub name: String }

impl Greet for User {
    fn greet(&self) {}
}
`,
	},
	{
		"js/src/login.js",
		`/** Authenticate the caller. */
export function login(user) { return true; }
`,
	},
}

func writeFixtures(t *testing.T, root string) {
	t.Helper()
	for _, f := range languageFixtures {
		path := filepath.Join(root, f.relPath)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f.contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAllLanguagesProduceSymbolsAndEdges(t *testing.T) {
	root := t.TempDir()
	writeFixtures(t, root)
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	codeGraph, result, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if result.FilesUpdated != len(languageFixtures) {
		t.Fatalf("expected %d files updated, got %d (errors: %v)", len(languageFixtures), result.FilesUpdated, result.Errors)
	}

	symbols, edges := codeGraph.Snapshot()

	// Per-language: at least one named symbol present.
	wantNames := map[string]string{
		"Login":       "go function",
		"Service":     "go struct",
		"TestLogin":   "go test",
		"AuthService": "ts/java class",
		"Logger":      "ts class",
		"helper":      "ts function",
		"User":        "python/rust struct",
		"Base":        "python class",
		"login":       "py method or rust fn or js function",
	}
	gotNames := map[string]bool{}
	for _, s := range symbols {
		gotNames[s.Name] = true
	}
	for name, label := range wantNames {
		if !gotNames[name] {
			t.Errorf("missing expected symbol %q (%s)", name, label)
		}
	}

	// 8 edge types present (allow extends to be missing if no extends in fixture).
	have := edgeTypeSet(edges)
	for _, e := range []core.EdgeType{core.EdgeDefines, core.EdgeImports, core.EdgeContains, core.EdgeExtends, core.EdgeImplements, core.EdgeCalls, core.EdgeTests} {
		if !have[e] {
			t.Errorf("missing edge type %q in graph", e)
		}
	}

	// Docstrings populated.
	if !anyDocMatches(symbols, "Authenticate the caller") {
		t.Errorf("expected at least one symbol to carry the 'Authenticate the caller' docstring")
	}

	// FTS5 search hits.
	hits, err := st.SearchFTS5(context.Background(), "Login", 10)
	if err != nil {
		t.Fatalf("fts5: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("FTS5 returned no hits for Login")
	}

	// Re-index: every file skipped (delta).
	_, second, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if second.FilesSkipped != len(languageFixtures) || second.FilesUpdated != 0 {
		t.Fatalf("reindex did not skip all files: %+v", second)
	}
}

func edgeTypeSet(edges []core.Edge) map[core.EdgeType]bool {
	out := map[core.EdgeType]bool{}
	for _, e := range edges {
		out[e.Type] = true
	}
	return out
}

func anyDocMatches(symbols []core.SymbolRecord, needle string) bool {
	for _, s := range symbols {
		if strings.Contains(s.Docstring, needle) {
			return true
		}
	}
	return false
}
