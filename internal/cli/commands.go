package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tabladrum/grove-suite/grove/internal/config"
	"github.com/tabladrum/grove-suite/grove/internal/core"
	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/mcp"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
	"github.com/tabladrum/grove-suite/grove/internal/version"
)

func Run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}

	engine := parser.NewEngine()
	codeGraph := graph.New()

	switch args[0] {
	case "version", "--version", "-v":
		return printJSON(map[string]string{"version": version.Version})
	case "init":
		return initWorkspace(args[1:])
	case "index":
		return indexCommand(engine, codeGraph, args[1:])
	case "status":
		return status(engine, codeGraph, args[1:])
	case "symbols":
		return symbols(engine, codeGraph, args[1:])
	case "query":
		return query(engine, codeGraph, args[1:])
	case "deps":
		return deps(engine, codeGraph, args[1:])
	case "impact":
		return impact(engine, codeGraph, args[1:])
	case "tests":
		return tests(engine, codeGraph, args[1:])
	case "icr":
		return icr(engine, codeGraph, args[1:])
	case "conflicts":
		return conflicts(args[1:])
	case "lock":
		return lockCommand(args[1:])
	case "unlock":
		return unlockCommand(args[1:])
	case "mcp":
		return mcpCommand(engine, codeGraph, args[1:])
	case "help", "--help", "-h":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		usage()
		return 2
	}
}

func mcpCommand(engine *parser.Engine, _ *graph.CodeGraph, args []string) int {
	cfg, err := config.Resolve(argOrDefault(args, 0, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.Open(cfg.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	codeGraph, _, err := index.New(engine, st).Index(context.Background(), cfg.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := mcp.NewServer(cfg.Root, codeGraph, engine, st).Serve(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func initWorkspace(args []string) int {
	cfg, err := config.Resolve(argOrDefault(args, 0, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	st, err := store.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	configPath := filepath.Join(root, ".grove", "config.yaml")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		content := []byte("version: 1\nstore: .grove/grove.db\nserver:\n  port: 7777\n")
		if err := os.WriteFile(configPath, content, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return printJSON(map[string]any{"initialized": true, "config": configPath})
}

func indexCommand(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	cfg, err := config.Resolve(argOrDefault(args, 0, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	result, err := indexRoot(engine, codeGraph, root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

func status(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	cfg, err := config.Resolve(argOrDefault(args, 0, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	store, err := store.Open(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer store.Close()
	status, err := store.Status(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(status)
}

func symbols(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove symbols <query> [dir]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"symbols": codeGraph.Search(query, 50)})
}

// query is the semantic-search CLI: free-text intent → Model2Vec-ranked
// symbols. Distinct from `symbols`, which does lexical substring matching
// across name/qualifiedName/filePath/signature. The two commands map to
// the two distinct retrieval surfaces Grove exposes.
func query(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove query <intent> [dir]")
		return 2
	}
	intent := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	scored := codeGraph.SemanticSearch(intent, 20)
	results := make([]map[string]any, 0, len(scored))
	for _, s := range scored {
		results = append(results, map[string]any{
			"symbol": s.Symbol,
			"score":  s.Score,
		})
	}
	return printJSON(map[string]any{"results": results})
}

func deps(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove deps <file> [dir]")
		return 2
	}
	filePath := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"edges": codeGraph.Deps(filePath)})
}

func impact(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove impact <symbol-or-file-query> [dir]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"nodes": codeGraph.Impact(query, 3)})
}

func tests(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	query := ""
	root := "."
	if len(args) > 0 {
		query = args[0]
	}
	if len(args) > 1 {
		root = args[1]
	}
	cfg, err := config.Resolve(root, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root = cfg.Root
	if _, err := indexRoot(engine, codeGraph, root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"tests": codeGraph.TestsFor(query)})
}

func icr(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove icr <intent> [dir]")
		return 2
	}
	intent := args[0]
	root := argOrDefault(args, 1, ".")
	cfg, err := config.Resolve(root, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if _, err := indexRoot(engine, codeGraph, cfg.Root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(codeGraph.ComputeICR(intent))
}

func conflicts(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: grove conflicts <icr-json-or-base64-a> <icr-json-or-base64-b>")
		return 2
	}
	var a, b core.IsolatedChangeRegion
	if err := decodeICR(args[0], &a); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := decodeICR(args[1], &b); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(graph.DetectConflicts(a, b))
}

func lockCommand(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: grove lock <intent-id> <dir> <lock-key>...")
		return 2
	}
	intentID := args[0]
	cfg, err := config.Resolve(args[1], 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.Open(cfg.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	records, err := st.AcquireLocks(context.Background(), intentID, args[2:], 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"locks": records})
}

func unlockCommand(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: grove unlock <intent-id> <dir>")
		return 2
	}
	cfg, err := config.Resolve(args[1], 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	st, err := store.Open(cfg.Root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	count, err := st.ReleaseLocks(context.Background(), args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"released": count})
}

func indexRoot(engine *parser.Engine, codeGraph *graph.CodeGraph, root string) (any, error) {
	store, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	indexedGraph, result, err := index.New(engine, store).Index(context.Background(), root)
	if err != nil {
		return nil, err
	}
	symbols, _ := indexedGraph.Snapshot()
	codeGraph.Replace(symbols, result.FilesSeen)
	return result, nil
}

func decodeICR(input string, value *core.IsolatedChangeRegion) error {
	data := []byte(input)
	if decoded, err := base64.StdEncoding.DecodeString(input); err == nil {
		data = decoded
	}
	return json.Unmarshal(data, value)
}

func argOrDefault(args []string, index int, fallback string) string {
	if len(args) > index && args[index] != "" {
		return args[index]
	}
	return fallback
}

func printJSON(value any) int {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `grove - code intelligence graph

Usage:
  grove version
  grove init [dir]
  grove index [dir]
  grove status [dir]
  grove symbols <query> [dir]        lexical substring search over names/paths/signatures
  grove query <intent> [dir]         semantic search (Model2Vec embeddings)
  grove deps <file> [dir]
  grove impact <symbol-or-file-query> [dir]
  grove tests <file> [dir]
  grove icr <intent>
  grove conflicts <icr-a> <icr-b>
  grove mcp [dir]                    stdio MCP server

Grove is an embedded library: Prism, Fuse, and Relay link against it
directly. No HTTP daemon, no ports, no tokens.
`)
}
