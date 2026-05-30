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

	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

func newAPITestEnv(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	src := []byte(`package main
// Login authenticates the user.
func Login() error { return Logout() }
func Logout() error { return nil }
type Service struct{}
`)
	if err := os.WriteFile(filepath.Join(root, "auth.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	engine := parser.NewEngine()
	cg, _, err := index.New(engine, st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(cg, engine, st, root)
	return srv, srv.Handler(), root
}

func TestHTTP_QueryAndSemantic(t *testing.T) {
	_, h, _ := newAPITestEnv(t)

	rec, out := postJSON(t, h, "/query", `{"intent":"Login","limit":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/query: %d %s", rec.Code, rec.Body.String())
	}
	if syms, _ := out["symbols"].([]any); len(syms) == 0 {
		t.Fatalf("/query empty: %v", out)
	}

	rec, out = postJSON(t, h, "/semantic", `{"query":"authenticates user","limit":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("/semantic: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := out["results"]; !ok {
		t.Fatalf("/semantic missing results: %v", out)
	}
}

func TestHTTP_BadJSONReturns400(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	for _, ep := range []string{"/query", "/symbols", "/semantic", "/deps", "/impact", "/tests", "/icr", "/conflicts", "/lock", "/unlock", "/index", "/mcp/call"} {
		rec, _ := postJSON(t, h, ep, `{not json`)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400, got %d", ep, rec.Code)
		}
	}
}

func TestHTTP_MCPCallICR(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec, out := postJSON(t, h, "/mcp/call", `{"name":"grove_icr","arguments":{"intent":"Login"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("icr: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := out["exclusive"]; !ok {
		t.Fatalf("icr missing key: %v", out)
	}
}

func TestHTTP_SymbolsLimitDefault(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec, out := postJSON(t, h, "/symbols", `{"query":"Login"}`)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	if syms, _ := out["symbols"].([]any); len(syms) == 0 {
		t.Errorf("expected results: %v", out)
	}
}

func TestHTTP_UnknownPathReturns404(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/no-such", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("got %d", rec.Code)
	}
}

func TestHTTP_LockTTLDefault(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec, _ := postJSON(t, h, "/lock", `{"intentId":"x","lockKeys":["k"]}`)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
}

func TestHTTP_UnlockIdempotent(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec, _ := postJSON(t, h, "/unlock", `{"intentId":"never-locked"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("unlock should be idempotent, got %d", rec.Code)
	}
}

func TestGRPCService_AllMethods(t *testing.T) {
	srv, _, _ := newAPITestEnv(t)
	svc := &GRPCService{server: srv}
	ctx := context.Background()

	cases := []struct {
		name string
		call func() (map[string]any, error)
		key  string
	}{
		{"Health", func() (map[string]any, error) { return svc.Health(ctx, nil) }, "status"},
		{"Status", func() (map[string]any, error) { return svc.Status(ctx, nil) }, "symbolCount"},
		{"Query", func() (map[string]any, error) {
			return svc.Query(ctx, map[string]any{"intent": "Login"})
		}, "symbols"},
		{"Symbols", func() (map[string]any, error) {
			return svc.Symbols(ctx, map[string]any{"query": "Login"})
		}, "symbols"},
		{"Deps", func() (map[string]any, error) {
			return svc.Deps(ctx, map[string]any{"file": "auth.go"})
		}, "edges"},
		{"Impact", func() (map[string]any, error) {
			return svc.Impact(ctx, map[string]any{"query": "Logout"})
		}, "nodes"},
		{"Tests", func() (map[string]any, error) {
			return svc.Tests(ctx, map[string]any{"query": "Login"})
		}, "tests"},
		{"ICR", func() (map[string]any, error) {
			return svc.ICR(ctx, map[string]any{"intent": "Login"})
		}, "exclusive"},
	}
	for _, c := range cases {
		out, err := c.call()
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if _, ok := out[c.key]; !ok {
			t.Errorf("%s: missing key %s in %v", c.name, c.key, out)
		}
	}
}

func TestGRPCService_IndexAndImpactFallback(t *testing.T) {
	srv, _, root := newAPITestEnv(t)
	svc := &GRPCService{server: srv}
	out, err := svc.Index(context.Background(), map[string]any{"dir": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["filesSeen"]; !ok {
		t.Errorf("index missing filesSeen: %v", out)
	}
	// Impact with only file fallback path
	out, err = svc.Impact(context.Background(), map[string]any{"file": "Logout"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["nodes"]; !ok {
		t.Errorf("impact fallback missing nodes: %v", out)
	}
	// Tests with only file fallback path
	out, err = svc.Tests(context.Background(), map[string]any{"file": "Login"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := out["tests"]; !ok {
		t.Errorf("tests fallback missing tests: %v", out)
	}
}

func TestJSONCodec(t *testing.T) {
	c := JSONCodec{}
	if c.Name() != "json" {
		t.Error("name")
	}
	data, _ := c.Marshal(map[string]any{"x": 1})
	var back map[string]any
	if err := c.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back["x"].(float64) != 1 {
		t.Error("roundtrip")
	}
}

func TestToMap(t *testing.T) {
	m, err := toMap(struct {
		Name string `json:"name"`
	}{Name: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m["name"] != "x" {
		t.Errorf("got %v", m)
	}
}

func TestStringIntArgs(t *testing.T) {
	req := map[string]any{"a": "hi", "b": float64(5), "c": 7}
	if stringArg(req, "a") != "hi" {
		t.Error("string")
	}
	if intArg(req, "b", 0) != 5 {
		t.Error("int float64")
	}
	if intArg(req, "c", 0) != 7 {
		t.Error("int int")
	}
	if intArg(req, "missing", 99) != 99 {
		t.Error("default")
	}
}

func TestHTTP_ListenStartStop(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	err := Listen("256.256.256.256:0", h)
	if err == nil {
		t.Error("expected error from invalid addr")
	}
}

func TestMapToStruct(t *testing.T) {
	var out struct {
		Name string `json:"name"`
	}
	if err := mapToStruct(map[string]any{"name": "x"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != "x" {
		t.Errorf("got %v", out)
	}
}

func TestHTTP_MCPCallReindex(t *testing.T) {
	_, h, root := newAPITestEnv(t)
	dirJSON, _ := json.Marshal(root)
	rec, _ := postJSON(t, h, "/mcp/call", `{"name":"grove_index","arguments":{"dir":`+string(dirJSON)+`}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestHTTP_MCPCallDepsImpactTestsConflicts(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec, _ := postJSON(t, h, "/mcp/call", `{"name":"grove_conflicts","arguments":{"a":{"intentId":"x","exclusive":["s"]},"b":{"intentId":"y","exclusive":["s"]}}}`)
	if rec.Code != http.StatusOK {
		t.Errorf("conflicts: %d %s", rec.Code, rec.Body.String())
	}
}

func TestGRPCService_RegisterAndListen(t *testing.T) {
	srv, _, _ := newAPITestEnv(t)
	svc := &GRPCService{server: srv}
	// Listening on impossible addr should error.
	if err := ListenGRPC("256.256.256.256:0", svc); err == nil {
		t.Error("expected error")
	}
}

func TestAddress(t *testing.T) {
	if Address(7777) != "127.0.0.1:7777" {
		t.Error("Address wrong")
	}
}

func TestHTTP_MCPSSEReady(t *testing.T) {
	_, h, _ := newAPITestEnv(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp/sse", nil))
	if !strings.Contains(rec.Body.String(), "ready") {
		t.Errorf("missing ready event: %s", rec.Body.String())
	}
}
