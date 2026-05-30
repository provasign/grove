package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

func newCovEnv(t *testing.T) *Server {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte(`package main
func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	eng := parser.NewEngine()
	cg, _, err := index.New(eng, st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	return NewServer(root, cg, eng, st)
}

func TestCallTool_IndexAndConflicts(t *testing.T) {
	s := newCovEnv(t)
	// grove_index reindex
	out, err := s.callTool("grove_index", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil result")
	}
	// grove_conflicts requires a, b ICR shapes
	out, err = s.callTool("grove_conflicts", map[string]any{
		"a": map[string]any{"intentId": "i1", "exclusive": []string{"s"}},
		"b": map[string]any{"intentId": "i2", "exclusive": []string{"s"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil {
		t.Error("nil result")
	}
}

func TestCallTool_UnknownReturnsError(t *testing.T) {
	s := newCovEnv(t)
	_, err := s.callTool("nope", nil)
	if err == nil {
		t.Error("expected error")
	}
}

func TestCallTool_ConflictsBadMap(t *testing.T) {
	s := newCovEnv(t)
	_, err := s.callTool("grove_conflicts", map[string]any{
		"a": make(chan int), // not JSON-serializable
		"b": map[string]any{},
	})
	if err == nil {
		t.Error("expected marshal error")
	}
}

func TestMapToStruct(t *testing.T) {
	var out struct {
		X int `json:"x"`
	}
	if err := mapToStruct(map[string]any{"x": 5}, &out); err != nil {
		t.Fatal(err)
	}
	if out.X != 5 {
		t.Errorf("got %v", out)
	}
	if err := mapToStruct(make(chan int), &out); err == nil {
		t.Error("expected marshal err")
	}
}

func TestInvalidParams(t *testing.T) {
	e := invalidParams(errors.New("bad"))
	if e.Code != -32602 || e.Message != "bad" {
		t.Errorf("got %+v", e)
	}
}

func TestStringIntArgFallbacks(t *testing.T) {
	if stringArg(nil, "k", "default") != "default" {
		t.Error("string fallback")
	}
	if stringArg(map[string]any{"k": ""}, "k", "fb") != "fb" {
		t.Error("empty string -> fallback")
	}
	if intArg(map[string]any{"k": 7}, "k", 0) != 7 {
		t.Error("int int")
	}
	if intArg(map[string]any{"k": float64(3)}, "k", 0) != 3 {
		t.Error("int float64")
	}
	if intArg(nil, "k", 9) != 9 {
		t.Error("int fallback")
	}
}
