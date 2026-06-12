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

	"github.com/provasign/grove/internal/cert"
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
		// Compact JSON: results land in an agent's context window, and
		// indentation is pure token overhead.
		encoded, _ := json.Marshal(value)
		return map[string]any{"content": []map[string]string{{"type": "text", "text": string(encoded)}}}, nil
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (s *Server) callTool(name string, args map[string]any) (any, error) {
	switch name {
	case "grove_index":
		root := stringArg(args, "dir", s.root)
		opts := index.Options{Force: boolArg(args, "force")}
		codeGraph, result, err := index.New(s.parser, s.store).IndexWithOptions(context.Background(), root, opts)
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
		return map[string]any{"symbols": slimSymbols(s.currentGraph().Search(stringArg(args, "query", ""), intArg(args, "limit", 50)), 0)}, nil
	case "grove_deps":
		return map[string]any{"edges": s.currentGraph().Deps(stringArg(args, "file", ""))}, nil
	case "grove_impact":
		query := stringArg(args, "query", stringArg(args, "file", ""))
		nodes := s.currentGraph().Impact(query, intArg(args, "maxDepth", 3))
		out := map[string]any{"count": len(nodes), "nodes": impactRefs(nodes, maxImpactNodes)}
		if len(nodes) > maxImpactNodes {
			out["note"] = fmt.Sprintf("showing %d of %d impacted symbols", maxImpactNodes, len(nodes))
		}
		return out, nil
	case "grove_tests":
		return map[string]any{"tests": slimSymbols(s.currentGraph().TestsFor(stringArg(args, "query", stringArg(args, "file", ""))), 0)}, nil
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
	case "grove_certify":
		diff := stringArg(args, "diff", "")
		if diff == "" {
			return nil, fmt.Errorf("grove_certify: diff is required")
		}
		input := core.DiffInput{
			UnifiedDiff: diff,
			Policy:      core.CertificationPolicy{RequireTestsForCode: boolArg(args, "requireTests")},
		}
		return cert.CertifyDiffWithStaleness(s.currentGraph(), input, cert.RepoFileSHA(s.root)), nil
	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

// maxImpactNodes caps blast-radius payloads: a hot symbol on a monorepo can
// reach thousands of dependents (4,468 measured for "Dashboard" on grafana),
// and dumping them all into an agent's context window helps no one. The
// exact count is always reported.
const maxImpactNodes = 50

// SlimSymbol is the MCP wire shape for a symbol: everything an agent needs
// to locate and reason about it, none of the bulk. Full bodies (RawText)
// made a single grove_query response cost ~10k tokens; agents that want a
// body should read the file (or use Prism, which compresses re-reads).
type SlimSymbol struct {
	ID            string `json:"id"`
	FilePath      string `json:"filePath"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
	QualifiedName string `json:"qualifiedName"`
	Signature     string `json:"signature,omitempty"`
	Docstring     string `json:"docstring,omitempty"`
	SpanStart     int    `json:"spanStart"`
	SpanEnd       int    `json:"spanEnd"`
	Exported      bool   `json:"exported"`
}

func slimSymbol(s core.SymbolRecord) SlimSymbol {
	doc := s.Docstring
	if i := strings.IndexByte(doc, '\n'); i >= 0 {
		doc = doc[:i] // first line carries the summary; bodies are bulk
	}
	return SlimSymbol{
		ID:            s.ID,
		FilePath:      s.FilePath,
		Kind:          string(s.Kind),
		Name:          s.Name,
		QualifiedName: s.QualifiedName,
		Signature:     s.Signature,
		Docstring:     doc,
		SpanStart:     s.Span.Start,
		SpanEnd:       s.Span.End,
		Exported:      s.Exports,
	}
}

// ImpactRef is the minimal blast-radius entry: enough to locate the
// dependent and look it up, nothing more. Impact lists run long (capped at
// 50 of potentially thousands), so every field is paid 50×.
type ImpactRef struct {
	FilePath      string `json:"filePath"`
	QualifiedName string `json:"qualifiedName"`
	Kind          string `json:"kind"`
	Line          int    `json:"line"`
}

func impactRefs(symbols []core.SymbolRecord, limit int) []ImpactRef {
	if limit > 0 && len(symbols) > limit {
		symbols = symbols[:limit]
	}
	out := make([]ImpactRef, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, ImpactRef{
			FilePath:      s.FilePath,
			QualifiedName: s.QualifiedName,
			Kind:          string(s.Kind),
			Line:          s.Span.Start,
		})
	}
	return out
}

// slimSymbols converts symbols to the wire shape, truncating to limit when
// limit > 0.
func slimSymbols(symbols []core.SymbolRecord, limit int) []SlimSymbol {
	if limit > 0 && len(symbols) > limit {
		symbols = symbols[:limit]
	}
	out := make([]SlimSymbol, 0, len(symbols))
	for _, s := range symbols {
		out = append(out, slimSymbol(s))
	}
	return out
}

func semanticResults(codeGraph *graph.CodeGraph, intent string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 50
	}
	scored := codeGraph.SemanticSearch(intent, limit)
	results := make([]map[string]any, 0, len(scored))
	for _, s := range scored {
		if s.Symbol == nil {
			continue
		}
		results = append(results, map[string]any{
			"symbol": slimSymbol(*s.Symbol),
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

// schema builders keep the tool definitions below readable.
func prop(typ, description string) map[string]any {
	return map[string]any{"type": typ, "description": description}
}

func objectSchema(required []string, props map[string]any) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func tools() []map[string]any {
	dirProp := prop("string", "Directory to operate on. Defaults to the workspace root the server was started with.")
	limitProp := prop("integer", "Maximum number of results to return (default 50).")
	return []map[string]any{
		{
			"name":        "grove_index",
			"description": "Index or reindex the workspace into the Grove code graph. Unchanged files are skipped via content-hash delta; when nothing changed the persisted graph is reused as-is. Run this after making file changes so queries see them.",
			"inputSchema": objectSchema(nil, map[string]any{
				"dir":   dirProp,
				"force": prop("boolean", "Re-run native analyzers and rebuild all edges even if no files changed (use after installing a language toolchain)."),
			}),
		},
		{
			"name":        "grove_query",
			"description": "Semantic search: rank indexed symbols against a free-text intent using embeddings. Use for 'where is the code that does X' questions. Returns symbols with similarity scores.",
			"inputSchema": objectSchema([]string{"intent"}, map[string]any{
				"intent": prop("string", "Free-text description of what you are looking for, e.g. 'parse unified diff hunk headers'."),
				"limit":  limitProp,
			}),
		},
		{
			"name":        "grove_symbols",
			"description": "Lexical symbol search: case-insensitive substring match over symbol names, qualified names, file paths, and signatures. Use when you know (part of) the identifier.",
			"inputSchema": objectSchema([]string{"query"}, map[string]any{
				"query": prop("string", "Substring to match, e.g. 'CertifyDiff' or 'store.Open'."),
				"limit": limitProp,
			}),
		},
		{
			"name":        "grove_impact",
			"description": "Blast radius: every symbol that transitively depends on the given symbol (callers, subtypes, tests) up to maxDepth. Answers 'what breaks if I change this?'.",
			"inputSchema": objectSchema([]string{"query"}, map[string]any{
				"query":    prop("string", "Symbol name, qualified name, or symbol ID to compute the blast radius for."),
				"maxDepth": prop("integer", "Maximum BFS depth over inbound edges (default 3)."),
			}),
		},
		{
			"name":        "grove_deps",
			"description": "Dependency edges touching a file: its defines/imports edges plus edges in and out of the symbols it defines.",
			"inputSchema": objectSchema([]string{"file"}, map[string]any{
				"file": prop("string", "Repo-relative file path, e.g. 'internal/store/store.go'."),
			}),
		},
		{
			"name":        "grove_tests",
			"description": "Tests covering a symbol or file, directly or transitively through the call graph.",
			"inputSchema": objectSchema([]string{"query"}, map[string]any{
				"query": prop("string", "Symbol name, qualified name, or file path whose covering tests you want."),
			}),
		},
		{
			"name":        "grove_icr",
			"description": "Isolated Change Region for an intent: the symbols/files a change would own exclusively, shared reads, boundary edges, and lock keys for multi-agent coordination. Empty with low confidence when the intent matches no indexed symbol.",
			"inputSchema": objectSchema([]string{"intent"}, map[string]any{
				"intent": prop("string", "Free-text task intent used to seed the region."),
			}),
		},
		{
			"name":        "grove_conflicts",
			"description": "Check whether two Isolated Change Regions overlap on exclusive symbols or files (would two concurrent tasks collide?).",
			"inputSchema": objectSchema([]string{"a", "b"}, map[string]any{
				"a": prop("object", "First ICR, as returned by grove_icr."),
				"b": prop("object", "Second ICR, as returned by grove_icr."),
			}),
		},
		{
			"name":        "grove_certify",
			"description": "Conservative structural certification of a unified diff against the indexed graph: changed/impacted symbols, covering tests, unknowns, and a verdict (allow / manual_review / block). Stale-index and unmappable changes escalate to manual_review, never allow.",
			"inputSchema": objectSchema([]string{"diff"}, map[string]any{
				"diff":         prop("string", "Unified diff text (git diff format)."),
				"requireTests": prop("boolean", "Require test evidence for changed code symbols; uncovered symbols become unknowns (default false)."),
			}),
		},
	}
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

func boolArg(args map[string]any, key string) bool {
	value, _ := args[key].(bool)
	return value
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
