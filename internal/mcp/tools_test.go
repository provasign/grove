package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

// newMCPTestServer indexes a small fixture and returns a ready server.
func newMCPTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "auth.go"), []byte(`package main

func Login() error { return Logout() }

func Logout() error { return nil }
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
	return NewServer(root, codeGraph, engine, st), root
}

func rpcCall(t *testing.T, s *Server, method string, params any) map[string]any {
	t.Helper()
	body := map[string]any{"jsonrpc": "2.0", "id": 1, "method": method}
	if params != nil {
		raw, _ := json.Marshal(params)
		body["params"] = json.RawMessage(raw)
	}
	payload, _ := json.Marshal(body)
	input := bytes.NewBufferString(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload))
	var output bytes.Buffer
	if err := s.Serve(input, &output); err != nil {
		t.Fatalf("serve: %v", err)
	}
	// Strip Content-Length headers from output.
	out := output.String()
	idx := strings.Index(out, "{")
	if idx < 0 {
		t.Fatalf("no JSON in output: %q", out)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out[idx:]), &result); err != nil {
		t.Fatalf("parse response: %v in %q", err, out)
	}
	return result
}

func TestMCPInitialize(t *testing.T) {
	s, _ := newMCPTestServer(t)
	resp := rpcCall(t, s, "initialize", nil)
	result, _ := resp["result"].(map[string]any)
	if result == nil {
		t.Fatalf("missing result: %v", resp)
	}
	if pv, _ := result["protocolVersion"].(string); pv == "" {
		t.Fatalf("missing protocolVersion: %v", result)
	}
}

func TestMCPToolsListReturnsEightTools(t *testing.T) {
	s, _ := newMCPTestServer(t)
	resp := rpcCall(t, s, "tools/list", nil)
	result, _ := resp["result"].(map[string]any)
	list, _ := result["tools"].([]any)
	if len(list) != 8 {
		t.Fatalf("expected 8 tools, got %d: %v", len(list), list)
	}
	want := map[string]bool{
		"grove_index": false, "grove_query": false, "grove_impact": false,
		"grove_deps": false, "grove_tests": false, "grove_icr": false,
		"grove_conflicts": false, "grove_symbols": false,
	}
	for _, tool := range list {
		obj, _ := tool.(map[string]any)
		if n, _ := obj["name"].(string); n != "" {
			want[n] = true
		}
	}
	for name, ok := range want {
		if !ok {
			t.Errorf("missing tool %s", name)
		}
	}
}

func TestMCPCallEveryTool(t *testing.T) {
	s, _ := newMCPTestServer(t)

	cases := []struct {
		name string
		args map[string]any
	}{
		{"grove_query", map[string]any{"intent": "Login", "limit": 5}},
		{"grove_symbols", map[string]any{"query": "Logout", "limit": 5}},
		{"grove_deps", map[string]any{"file": "auth.go"}},
		{"grove_impact", map[string]any{"query": "Logout", "maxDepth": 3}},
		{"grove_tests", map[string]any{"query": "Login"}},
		{"grove_icr", map[string]any{"intent": "Login"}},
	}
	for _, c := range cases {
		resp := rpcCall(t, s, "tools/call", map[string]any{"name": c.name, "arguments": c.args})
		if errObj, ok := resp["error"].(map[string]any); ok {
			t.Fatalf("%s error: %v", c.name, errObj)
		}
		result, _ := resp["result"].(map[string]any)
		content, _ := result["content"].([]any)
		if len(content) == 0 {
			t.Fatalf("%s returned empty content: %v", c.name, resp)
		}
		first, _ := content[0].(map[string]any)
		text, _ := first["text"].(string)
		if text == "" || text == "null" {
			t.Fatalf("%s returned empty text: %q", c.name, text)
		}
	}
}

func TestMCPCallUnknownToolReturnsRPCError(t *testing.T) {
	s, _ := newMCPTestServer(t)
	resp := rpcCall(t, s, "tools/call", map[string]any{"name": "does_not_exist"})
	if _, hasErr := resp["error"]; !hasErr {
		t.Fatalf("expected error, got: %v", resp)
	}
}

func TestMCPLineDelimitedFraming(t *testing.T) {
	s, _ := newMCPTestServer(t)
	payload := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	var output bytes.Buffer
	if err := s.Serve(bytes.NewBufferString(payload), &output); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !strings.Contains(output.String(), "grove_query") {
		t.Fatalf("expected response with tools, got: %q", output.String())
	}
}
