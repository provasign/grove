package api

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const tokenFileName = ".token"

// LoadOrCreateToken reads the shared-secret token from groveDir/.token,
// creating it (with mode 0600) if it does not exist.
// The token is 32 random bytes encoded as 64 lowercase hex digits.
func LoadOrCreateToken(groveDir string) (string, error) {
	path := filepath.Join(groveDir, tokenFileName)
	data, err := os.ReadFile(path)
	if err == nil {
		tok := strings.TrimSpace(string(data))
		if len(tok) == 64 {
			return tok, nil
		}
	}
	if !errors.Is(err, os.ErrNotExist) && err != nil {
		return "", err
	}
	// Generate a new token.
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	tok := hex.EncodeToString(raw[:])
	if err := os.MkdirAll(groveDir, 0o755); err != nil {
		return "", err
	}
	// 0600: only the owning user can read the token.
	if err := os.WriteFile(path, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// TokenMiddleware returns an http.Handler that requires every request (except
// GET /health) to carry the correct bearer token.
//
//   Authorization: Bearer <token>
//
// /health is exempt so that prism's EnsureRunning probe works before it has
// loaded the token from disk.
func TokenMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(auth, prefix) || auth[len(prefix):] != token {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
