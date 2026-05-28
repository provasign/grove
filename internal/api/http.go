package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/tabladrum/grove-suite/grove/internal/core"
	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
	"github.com/tabladrum/grove-suite/grove/internal/version"
)

type Server struct {
	mu     sync.RWMutex
	graph  *graph.CodeGraph
	parser *parser.Engine
	store  *store.Store
	root   string
}

func NewServer(graph *graph.CodeGraph, parser *parser.Engine, store *store.Store, root string) *Server {
	return &Server{graph: graph, parser: parser, store: store, root: root}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.health)
	mux.HandleFunc("GET /version", s.version)
	mux.HandleFunc("GET /status", s.status)
	mux.HandleFunc("POST /index", s.index)
	mux.HandleFunc("POST /query", s.query)
	mux.HandleFunc("POST /symbols", s.symbols)
	mux.HandleFunc("POST /deps", s.deps)
	mux.HandleFunc("POST /impact", s.impact)
	mux.HandleFunc("POST /tests", s.tests)
	mux.HandleFunc("POST /icr", s.icr)
	mux.HandleFunc("POST /conflicts", s.conflicts)
	mux.HandleFunc("POST /lock", s.lock)
	mux.HandleFunc("POST /unlock", s.unlock)
	mux.HandleFunc("POST /semantic", s.semantic)
	mux.HandleFunc("GET /mcp/sse", s.mcpSSE)
	mux.HandleFunc("POST /mcp/call", s.mcpCall)
	return mux
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version.Version})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version.Version})
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	status, err := s.store.Status(context.Background())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Dir string `json:"dir"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Dir == "" {
		request.Dir = s.root
	}
	codeGraph, result, err := index.New(s.parser, s.store).Index(context.Background(), request.Dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.mu.Lock()
	s.graph = codeGraph
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Intent string `json:"intent"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if request.Limit <= 0 {
		request.Limit = 50
	}
	cg := s.currentGraph()

	// Use TF-IDF semantic search for natural-language intents. Supplement with
	// keyword substring matches so single-token queries (e.g. "Login") still
	// return exact hits at the top.
	scored := cg.SemanticSearch(request.Intent, request.Limit)
	seen := make(map[string]bool, len(scored))
	symbols := make([]core.SymbolRecord, 0, request.Limit)
	for _, sc := range scored {
		seen[sc.Symbol.ID] = true
		symbols = append(symbols, *sc.Symbol)
	}
	// Supplement with keyword substring matches for exact-name queries.
	for _, sym := range cg.Search(request.Intent, request.Limit) {
		if !seen[sym.ID] {
			symbols = append(symbols, sym)
		}
		if len(symbols) >= request.Limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"symbols": symbols})
}

func (s *Server) symbols(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	graph := s.currentGraph()
	writeJSON(w, http.StatusOK, map[string]any{"symbols": graph.Search(request.Query, request.Limit)})
}

// semantic returns TF-IDF-ranked results (cosine similarity over identifier +
// signature + docstring tokens). The response carries both the symbol and its
// score so callers can blend this signal into a composite ranking.
func (s *Server) semantic(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	graph := s.currentGraph()
	scored := graph.SemanticSearch(request.Query, request.Limit)
	results := make([]map[string]any, 0, len(scored))
	for _, sc := range scored {
		results = append(results, map[string]any{
			"symbol": sc.Symbol,
			"score":  sc.Score,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Server) deps(w http.ResponseWriter, r *http.Request) {
	var request struct {
		File string `json:"file"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	graph := s.currentGraph()
	writeJSON(w, http.StatusOK, map[string]any{"edges": graph.Deps(request.File)})
}

func (s *Server) impact(w http.ResponseWriter, r *http.Request) {
	var request struct {
		File     string `json:"file"`
		Query    string `json:"query"`
		Line     int    `json:"line"`
		MaxDepth int    `json:"maxDepth"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	query := request.Query
	if query == "" {
		query = request.File
	}
	graph := s.currentGraph()
	writeJSON(w, http.StatusOK, map[string]any{"nodes": graph.Impact(query, request.MaxDepth)})
}

func (s *Server) tests(w http.ResponseWriter, r *http.Request) {
	var request struct {
		File  string `json:"file"`
		Query string `json:"query"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	query := request.Query
	if query == "" {
		query = request.File
	}
	graph := s.currentGraph()
	writeJSON(w, http.StatusOK, map[string]any{"tests": graph.TestsFor(query)})
}

func (s *Server) icr(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Intent string `json:"intent"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.currentGraph().ComputeICR(request.Intent))
}

func (s *Server) conflicts(w http.ResponseWriter, r *http.Request) {
	var request struct {
		A core.IsolatedChangeRegion `json:"a"`
		B core.IsolatedChangeRegion `json:"b"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, graph.DetectConflicts(request.A, request.B))
}

func (s *Server) lock(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IntentID string   `json:"intentId"`
		LockKeys []string `json:"lockKeys"`
		TTL      int      `json:"ttlSeconds"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	records, err := s.store.AcquireLocks(context.Background(), request.IntentID, request.LockKeys, time.Duration(request.TTL)*time.Second)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"locks": records})
}

func (s *Server) unlock(w http.ResponseWriter, r *http.Request) {
	var request struct {
		IntentID string `json:"intentId"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	count, err := s.store.ReleaseLocks(context.Background(), request.IntentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"released": count})
}

func (s *Server) mcpSSE(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	_, _ = fmt.Fprintf(w, "event: ready\ndata: %s\n\n", `{"endpoint":"/mcp/call"}`)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) mcpCall(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.callTool(request.Name, request.Arguments)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) callTool(name string, args map[string]any) (any, error) {
	switch name {
	case "grove_index":
		root, _ := args["dir"].(string)
		if root == "" {
			root = s.root
		}
		codeGraph, result, err := index.New(s.parser, s.store).Index(context.Background(), root)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.graph = codeGraph
		s.mu.Unlock()
		return result, nil
	case "grove_query":
		intent := stringArg(args, "intent")
		limit := intArg(args, "limit", 50)
		if limit <= 0 {
			limit = 50
		}
		cg := s.currentGraph()
		scored := cg.SemanticSearch(intent, limit)
		seen := make(map[string]bool, len(scored))
		symbols := make([]core.SymbolRecord, 0, limit)
		for _, sc := range scored {
			seen[sc.Symbol.ID] = true
			symbols = append(symbols, *sc.Symbol)
		}
		for _, sym := range cg.Search(intent, limit) {
			if !seen[sym.ID] {
				symbols = append(symbols, sym)
			}
			if len(symbols) >= limit {
				break
			}
		}
		return map[string]any{"symbols": symbols}, nil
	case "grove_symbols":
		return map[string]any{"symbols": s.currentGraph().Search(stringArg(args, "query"), intArg(args, "limit", 50))}, nil
	case "grove_deps":
		return map[string]any{"edges": s.currentGraph().Deps(stringArg(args, "file"))}, nil
	case "grove_impact":
		query := stringArg(args, "query")
		if query == "" {
			query = stringArg(args, "file")
		}
		return map[string]any{"nodes": s.currentGraph().Impact(query, intArg(args, "maxDepth", 3))}, nil
	case "grove_tests":
		query := stringArg(args, "query")
		if query == "" {
			query = stringArg(args, "file")
		}
		return map[string]any{"tests": s.currentGraph().TestsFor(query)}, nil
	case "grove_icr":
		return s.currentGraph().ComputeICR(stringArg(args, "intent")), nil
	case "grove_conflicts":
		var first, second core.IsolatedChangeRegion
		if err := mapToStruct(args["a"], &first); err != nil {
			return nil, err
		}
		if err := mapToStruct(args["b"], &second); err != nil {
			return nil, err
		}
		return graph.DetectConflicts(first, second), nil
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func decodeJSON(r *http.Request, value any) error {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("request body must contain a single JSON object")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func Listen(addr string, handler http.Handler) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return server.ListenAndServe()
}

// Address returns a localhost-only listen address for the given port.
// Binding to 127.0.0.1 (not 0.0.0.0) prevents remote machines on the same
// LAN from reaching the Grove API and exfiltrating source code.
func Address(port int) string {
	return fmt.Sprintf("127.0.0.1:%d", port)
}

func (s *Server) currentGraph() *graph.CodeGraph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graph
}

func stringArg(args map[string]any, key string) string {
	if value, ok := args[key].(string); ok {
		return value
	}
	return ""
}

func intArg(args map[string]any, key string, fallback int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

func mapToStruct(value any, out any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, out)
}
