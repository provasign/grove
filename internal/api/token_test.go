package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadOrCreateTokenCreatesOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	tok, err := LoadOrCreateToken(dir)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if len(tok) != 64 {
		t.Errorf("token length: got %d want 64", len(tok))
	}
	// File must be created with mode 0600 (Unix only — Windows ignores chmod).
	info, err := os.Stat(filepath.Join(dir, ".token"))
	if err != nil {
		t.Fatalf("token file missing: %v", err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("token file perm: got %o want 600", perm)
		}
	}
}

func TestLoadOrCreateTokenIsStable(t *testing.T) {
	dir := t.TempDir()
	tok1, _ := LoadOrCreateToken(dir)
	tok2, _ := LoadOrCreateToken(dir)
	if tok1 != tok2 {
		t.Error("token changed between two calls on the same dir")
	}
}

func TestTokenMiddlewareAllowsHealth(t *testing.T) {
	handler := TokenMiddleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("/health: got %d want 200", rr.Code)
	}
}

func TestTokenMiddlewareRejectsNoToken(t *testing.T) {
	handler := TokenMiddleware("secret", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/index", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d want 401", rr.Code)
	}
}

func TestTokenMiddlewareRejectsWrongToken(t *testing.T) {
	handler := TokenMiddleware("correcttoken", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/index", nil)
	req.Header.Set("Authorization", "Bearer wrongtoken")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d want 401", rr.Code)
	}
}

func TestTokenMiddlewareAcceptsCorrectToken(t *testing.T) {
	const tok = "abc123"
	handler := TokenMiddleware(tok, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/symbols", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("correct token: got %d want 200", rr.Code)
	}
}

func TestAddressIsLocalhost(t *testing.T) {
	addr := Address(7777)
	if addr != "127.0.0.1:7777" {
		t.Errorf("Address(7777) = %q, want 127.0.0.1:7777", addr)
	}
}
