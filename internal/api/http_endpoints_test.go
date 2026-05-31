package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

// newTestServer indexes a tiny multi-symbol fixture and returns the HTTP
// handler ready for use.
func newTestServer(t *testing.T) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(`package main

// Login authenticates the user.
func Login() error { return Logout() }

func Logout() error { return nil }

type Service struct{}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "auth_test.go"), []byte(`package main

import "testing"

func TestLogin(t *testing.T) { _ = Login() }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	engine := parser.NewEngine()
	codeGraph, _, err := index.New(engine, st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(codeGraph, engine, st, root).Handler(), root
}

func postJSON(t *testing.T, handler http.Handler, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
	}
	return rec, out
}

func TestHTTPDepsImpactTests(t *testing.T) {
	handler, _ := newTestServer(t)

	rec, out := postJSON(t, handler, "/deps", `{"file":"auth.go"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/deps: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := out["edges"]; !ok {
		t.Fatalf("/deps missing edges key: %v", out)
	}

	rec, out = postJSON(t, handler, "/impact", `{"query":"Logout","maxDepth":3}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/impact: %d %s", rec.Code, rec.Body.String())
	}
	nodes, _ := out["nodes"].([]any)
	foundLogin := false
	for _, n := range nodes {
		obj, _ := n.(map[string]any)
		if name, _ := obj["name"].(string); name == "Login" {
			foundLogin = true
		}
	}
	if !foundLogin {
		t.Fatalf("/impact for Logout should include Login, got: %v", nodes)
	}

	rec, out = postJSON(t, handler, "/tests", `{"query":"Login"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/tests: %d %s", rec.Code, rec.Body.String())
	}
	tests, _ := out["tests"].([]any)
	if len(tests) == 0 {
		t.Fatalf("/tests should return covering tests for Login")
	}
}

func TestHTTPICRAndConflicts(t *testing.T) {
	handler, _ := newTestServer(t)

	rec, out := postJSON(t, handler, "/icr", `{"intent":"Login"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/icr: %d %s", rec.Code, rec.Body.String())
	}
	exclusive, _ := out["exclusive"].([]any)
	if len(exclusive) == 0 {
		t.Fatalf("/icr should produce non-empty exclusive set")
	}

	icrJSON, _ := json.Marshal(out)
	conflictBody := `{"a":` + string(icrJSON) + `,"b":` + string(icrJSON) + `}`
	rec, out = postJSON(t, handler, "/conflicts", conflictBody)
	if rec.Code != http.StatusOK {
		t.Fatalf("/conflicts: %d %s", rec.Code, rec.Body.String())
	}
	if conflicts, _ := out["conflicts"].(bool); !conflicts {
		t.Fatalf("/conflicts should report conflict between identical ICRs: %v", out)
	}
}

func TestHTTPLockAndUnlock(t *testing.T) {
	handler, _ := newTestServer(t)

	rec, out := postJSON(t, handler, "/lock", `{"intentId":"i1","lockKeys":["grove:lock:file:auth.go"],"ttlSeconds":60}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/lock: %d %s", rec.Code, rec.Body.String())
	}
	locks, _ := out["locks"].([]any)
	if len(locks) != 1 {
		t.Fatalf("/lock should return 1 lock, got %v", out)
	}

	rec, _ = postJSON(t, handler, "/lock", `{"intentId":"i2","lockKeys":["grove:lock:file:auth.go"],"ttlSeconds":60}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("/lock should conflict for second intent, got %d %s", rec.Code, rec.Body.String())
	}

	rec, _ = postJSON(t, handler, "/unlock", `{"intentId":"i1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/unlock: %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPMCPCallTools(t *testing.T) {
	handler, _ := newTestServer(t)

	cases := []struct {
		body     string
		wantKey  string
		notEmpty bool
	}{
		{`{"name":"grove_query","arguments":{"intent":"Login","limit":5}}`, "symbols", true},
		{`{"name":"grove_symbols","arguments":{"query":"Service","limit":5}}`, "symbols", true},
		{`{"name":"grove_deps","arguments":{"file":"auth.go"}}`, "edges", true},
		{`{"name":"grove_impact","arguments":{"query":"Logout","maxDepth":3}}`, "nodes", true},
		{`{"name":"grove_tests","arguments":{"query":"Login"}}`, "tests", true},
	}
	for _, c := range cases {
		rec, out := postJSON(t, handler, "/mcp/call", c.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("mcp/call body=%s: %d %s", c.body, rec.Code, rec.Body.String())
		}
		v, ok := out[c.wantKey]
		if !ok {
			t.Fatalf("mcp/call %s: missing key %s in %v", c.body, c.wantKey, out)
		}
		if c.notEmpty {
			arr, _ := v.([]any)
			if len(arr) == 0 {
				t.Fatalf("mcp/call %s: expected non-empty %s, got %v", c.body, c.wantKey, v)
			}
		}
	}
}

func TestHTTPMCPCallUnknownTool(t *testing.T) {
	handler, _ := newTestServer(t)
	rec, _ := postJSON(t, handler, "/mcp/call", `{"name":"does_not_exist"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown tool should return 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTPMCPSSEReadyEvent(t *testing.T) {
	handler, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp/sse", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("expected ready event, got code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHTTPIndexReindex(t *testing.T) {
	handler, root := newTestServer(t)
	dirJSON, _ := json.Marshal(root)
	rec, out := postJSON(t, handler, "/index", `{"dir":`+string(dirJSON)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/index: %d %s", rec.Code, rec.Body.String())
	}
	// Should report at least the auth.go and auth_test.go files seen.
	seen, _ := out["filesSeen"].(float64)
	if int(seen) < 2 {
		t.Fatalf("/index filesSeen=%v, want >=2: %v", seen, out)
	}
}

// Sanity: every IsolatedChangeRegion field round-trips through HTTP JSON.
func TestICRSerialization(t *testing.T) {
	icr := core.IsolatedChangeRegion{IntentID: "x", Exclusive: []string{"s"}, LockKeys: []string{"lk"}}
	data, _ := json.Marshal(icr)
	var back core.IsolatedChangeRegion
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.IntentID != "x" || len(back.Exclusive) != 1 || len(back.LockKeys) != 1 {
		t.Fatalf("ICR did not round-trip: %+v", back)
	}
}
