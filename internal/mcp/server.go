package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/index"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
	"github.com/provasign/grove/internal/version"
)

type Server struct {
	mu     sync.RWMutex
	root   string
	graph  *graph.CodeGraph
	parser *parser.Engine
	store  *store.Store
}

func NewServer(root string, codeGraph *graph.CodeGraph, engine *parser.Engine, st *store.Store) *Server {
	return &Server{root: root, graph: codeGraph, parser: engine, store: st}
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func (s *Server) Serve(r io.Reader, w io.Writer) error {
	reader := bufio.NewReader(r)
	for {
		message, err := readMessage(reader)
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		var req request
		if err := json.Unmarshal(message, &req); err != nil {
			continue
		}
		if req.ID == nil {
			continue
		}
		result, rpcErr := s.handle(req.Method, req.Params)
		if err := writeMessage(w, req.ID, result, rpcErr); err != nil {
			return err
		}
	}
}

// defaultProtocolVersion is the latest MCP revision these servers target.
const defaultProtocolVersion = "2025-03-26"

// supportedProtocolVersions are the MCP revisions this server can speak.
var supportedProtocolVersions = map[string]bool{
	"2024-11-05": true,
	"2025-03-26": true,
	"2025-06-18": true,
}

// negotiateProtocolVersion echoes the client's requested protocolVersion when
// it is one we support (required by the MCP spec), otherwise falls back to our
// latest. Maximizes compatibility across clients (Claude Code, Cursor, VS Code,
// Copilot) that each pin different revisions.
func negotiateProtocolVersion(params json.RawMessage) string {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(params, &p); err == nil && supportedProtocolVersions[p.ProtocolVersion] {
		return p.ProtocolVersion
	}
	return defaultProtocolVersion
}

func (s *Server) handle(method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "initialize":
		return map[string]any{"protocolVersion": negotiateProtocolVersion(params), "serverInfo": map[string]string{"name": "grove", "version": version.Version}, "capabilities": map[string]any{"tools": map[string]any{}}}, nil
	case "tools/list":
		return map[string]any{"tools": tools()}, nil
	case "tools/call":
		var call struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(params, &call); err != nil {
			return nil, invalidParams(err)
		}
		value, err := s.callTool(call.Name, call.Arguments)
		if err != nil {
			return nil, &rpcError{Code: -32000, Message: err.Error()}
		}
		encoded, _ := json.MarshalIndent(value, "", "  ")
		return map[string]any{"content": []map[string]string{{"type": "text", "text": string(encoded)}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callTool(name string, args map[string]any) (any, error) {
	switch name {
	case "grove_index":
		root := stringArg(args, "dir", s.root)
		codeGraph, result, err := index.New(s.parser, s.store).Index(context.Background(), root)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.graph = codeGraph
		s.mu.Unlock()
		return result, nil
	case "grove_query":
		return map[string]any{"results": semanticResults(s.currentGraph(), stringArg(args, "intent", ""), intArg(args, "limit", 50))}, nil
	case "grove_symbols":
		return map[string]any{"symbols": s.currentGraph().Search(stringArg(args, "query", ""), intArg(args, "limit", 50))}, nil
	case "grove_deps":
		return map[string]any{"edges": s.currentGraph().Deps(stringArg(args, "file", ""))}, nil
	case "grove_impact":
		query := stringArg(args, "query", stringArg(args, "file", ""))
		return map[string]any{"nodes": s.currentGraph().Impact(query, intArg(args, "maxDepth", 3))}, nil
	case "grove_tests":
		return map[string]any{"tests": s.currentGraph().TestsFor(stringArg(args, "query", stringArg(args, "file", "")))}, nil
	case "grove_icr":
		return s.currentGraph().ComputeICR(stringArg(args, "intent", "")), nil
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

func semanticResults(codeGraph *graph.CodeGraph, intent string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 50
	}
	scored := codeGraph.SemanticSearch(intent, limit)
	results := make([]map[string]any, 0, len(scored))
	for _, s := range scored {
		results = append(results, map[string]any{
			"symbol": s.Symbol,
			"score":  s.Score,
		})
	}
	return results
}

func (s *Server) currentGraph() *graph.CodeGraph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graph
}

func tools() []map[string]any {
	names := []string{"grove_index", "grove_query", "grove_impact", "grove_deps", "grove_tests", "grove_icr", "grove_conflicts", "grove_symbols"}
	tools := make([]map[string]any, 0, len(names))
	for _, name := range names {
		tools = append(tools, map[string]any{"name": name, "description": "Grove code graph tool: " + name, "inputSchema": map[string]any{"type": "object", "additionalProperties": true}})
	}
	return tools
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func invalidParams(err error) *rpcError { return &rpcError{Code: -32602, Message: err.Error()} }

func readMessage(reader *bufio.Reader) ([]byte, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(strings.ToLower(line), "content-length:") {
		return []byte(strings.TrimSpace(line)), nil
	}
	lengthText := strings.TrimSpace(strings.TrimPrefix(strings.ToLower(line), "content-length:"))
	length, err := strconv.Atoi(lengthText)
	if err != nil {
		return nil, err
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			break
		}
	}
	payload := make([]byte, length)
	_, err = io.ReadFull(reader, payload)
	return payload, err
}

func writeMessage(w io.Writer, id any, result any, rpcErr *rpcError) error {
	response := map[string]any{"jsonrpc": "2.0", "id": id}
	if rpcErr != nil {
		response["error"] = rpcErr
	} else {
		response["result"] = result
	}
	payload, err := json.Marshal(response)
	if err != nil {
		return err
	}
	// MCP stdio transport requires newline-delimited JSON (one compact JSON
	// object per line, no embedded newlines). json.Marshal already produces a
	// compact, newline-free payload. Emitting LSP-style "Content-Length"
	// framing here makes every newline-delimited MCP client (Claude Code,
	// Cursor, VS Code, Copilot) block waiting for a terminating newline and
	// time out the connection.
	_, err = fmt.Fprintf(w, "%s\n", payload)
	return err
}

func stringArg(args map[string]any, key string, fallback string) string {
	if value, ok := args[key].(string); ok && value != "" {
		return value
	}
	return fallback
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
