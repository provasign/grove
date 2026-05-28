package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
	"google.golang.org/grpc"
)

func TestHTTPStatusSymbolsAndStrictJSON(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(`package main

type AuthService struct{}

func Login() {}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	engine := parser.NewEngine()
	codeGraph, _, err := index.New(engine, st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewServer(codeGraph, engine, st, root).Handler()

	statusReq := httptest.NewRequest(http.MethodGet, "/status", nil)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"symbolCount":2`) {
		t.Fatalf("unexpected status response: code=%d body=%s", statusRec.Code, statusRec.Body.String())
	}

	symbolsReq := httptest.NewRequest(http.MethodPost, "/symbols", strings.NewReader(`{"query":"Auth","limit":10}`))
	symbolsRec := httptest.NewRecorder()
	handler.ServeHTTP(symbolsRec, symbolsReq)
	if symbolsRec.Code != http.StatusOK || !strings.Contains(symbolsRec.Body.String(), "AuthService") {
		t.Fatalf("unexpected symbols response: code=%d body=%s", symbolsRec.Code, symbolsRec.Body.String())
	}

	badReq := httptest.NewRequest(http.MethodPost, "/symbols", strings.NewReader(`{"query":"Auth","unknown":true}`))
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("bad request code = %d, want %d", badRec.Code, http.StatusBadRequest)
	}
}

func TestHTTPVersionHealthOnEmptyGraph(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	handler := NewServer(graph.New(), parser.NewEngine(), st, root).Handler()
	for _, path := range []string{"/health", "/version"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "version") {
			t.Fatalf("unexpected %s response: code=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestGRPCRegistration(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	server := grpc.NewServer()
	RegisterGRPC(server, NewGRPCService(graph.New(), parser.NewEngine(), st, root))
}
