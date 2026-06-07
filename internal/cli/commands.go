package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/provasign/grove/internal/cert"
	"github.com/provasign/grove/internal/config"
	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/index"
	"github.com/provasign/grove/internal/mcp"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
	"github.com/provasign/grove/internal/version"
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
	case "certify":
		return certify(engine, codeGraph, args[1:])
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
	codeGraph := graph.New()
	if err := loadGraphFromStore(codeGraph, st); err != nil {
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
	dir, nativeCfg, err := parseNativeIndexArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg, err := config.Resolve(dir, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	result, err := indexRootWithNativeConfig(engine, codeGraph, root, nativeCfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

func status(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	cfg, err := config.Resolve(argOrDefault(args, 0, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if refresh {
		if _, err := indexRoot(engine, codeGraph, root); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if !refresh && !storeExists(root) {
		return printJSON(core.Status{})
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
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove symbols <query> [dir] [--refresh]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if err := prepareReadGraph(engine, codeGraph, root, refresh); err != nil {
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
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove query <intent> [dir] [--refresh]")
		return 2
	}
	intent := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if err := prepareReadGraph(engine, codeGraph, root, refresh); err != nil {
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
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove deps <file> [dir] [--refresh]")
		return 2
	}
	filePath := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if err := prepareReadGraph(engine, codeGraph, root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"edges": codeGraph.Deps(filePath)})
}

func impact(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove impact <symbol-or-file-query> [dir] [--refresh]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	if err := prepareReadGraph(engine, codeGraph, root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"nodes": codeGraph.Impact(query, 3)})
}

func tests(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
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
	if err := prepareReadGraph(engine, codeGraph, root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(map[string]any{"tests": codeGraph.TestsFor(query)})
}

func icr(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove icr <intent> [dir] [--refresh]")
		return 2
	}
	intent := args[0]
	root := argOrDefault(args, 1, ".")
	cfg, err := config.Resolve(root, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(codeGraph.ComputeICR(intent))
}

func certify(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove certify <diff-file-or-> [dir]")
		return 2
	}
	diffData, err := readDiffArg(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	cfg, err := config.Resolve(argOrDefault(args, 1, "."), 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	report := cert.CertifyDiff(codeGraph, core.DiffInput{UnifiedDiff: string(diffData)})
	if code := printJSON(report); code != 0 {
		return code
	}
	switch report.Verdict {
	case core.VerdictAllow:
		return 0
	case core.VerdictManualReview:
		return 2
	case core.VerdictBlock:
		return 3
	default:
		return 1
	}
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
	return indexRootWithNativeConfig(engine, codeGraph, root, native.ConfigFromEnv())
}

func indexRootWithNativeConfig(engine *parser.Engine, codeGraph *graph.CodeGraph, root string, nativeCfg native.Config) (any, error) {
	store, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	idx := index.NewWithNativeConfig(engine, store, nativeCfg)
	indexedGraph, result, err := idx.Index(context.Background(), root)
	if err != nil {
		return nil, err
	}
	symbols, edges := indexedGraph.Snapshot()
	codeGraph.ReplaceWithEdges(symbols, edges, result.FilesSeen)
	return result, nil
}

func parseNativeIndexArgs(args []string) (string, native.Config, error) {
	cfg := native.ConfigFromEnv()
	var positional []string
	for _, arg := range args {
		switch {
		case arg == "--no-native":
			cfg.Enabled = false
		case strings.HasPrefix(arg, "--native="):
			cfg.Enabled = !cliFalse(strings.TrimPrefix(arg, "--native="))
		case strings.HasPrefix(arg, "--native-languages="):
			cfg.Languages = cliLanguageSet(strings.TrimPrefix(arg, "--native-languages="))
		case strings.HasPrefix(arg, "--native-disabled-languages="):
			cfg.DisabledLanguages = cliLanguageSet(strings.TrimPrefix(arg, "--native-disabled-languages="))
		case strings.HasPrefix(arg, "--native-timeout="):
			value := strings.TrimPrefix(arg, "--native-timeout=")
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return "", cfg, fmt.Errorf("invalid --native-timeout: %s", value)
			}
			cfg.Timeout = d
		default:
			if strings.HasPrefix(arg, "--native") {
				return "", cfg, fmt.Errorf("unknown native flag: %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	return argOrDefault(positional, 0, "."), cfg, nil
}

func cliFalse(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off", "disabled":
		return true
	default:
		return false
	}
}

func cliLanguageSet(value string) map[string]bool {
	out := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		item = strings.ToLower(strings.TrimSpace(item))
		if item != "" {
			out[item] = true
		}
	}
	return out
}

func prepareReadGraph(engine *parser.Engine, codeGraph *graph.CodeGraph, root string, refresh bool) error {
	if refresh {
		_, err := indexRoot(engine, codeGraph, root)
		return err
	}
	if !storeExists(root) {
		codeGraph.Replace(nil, 0)
		return nil
	}
	st, err := store.Open(root)
	if err != nil {
		return err
	}
	defer st.Close()
	return loadGraphFromStore(codeGraph, st)
}

func storeExists(root string) bool {
	_, err := os.Stat(filepath.Join(root, ".grove", "grove.db"))
	return err == nil
}

func loadGraphFromStore(codeGraph *graph.CodeGraph, st *store.Store) error {
	symbols, err := st.AllSymbols(context.Background())
	if err != nil {
		return err
	}
	edges, err := st.AllEdges(context.Background())
	if err != nil {
		return err
	}
	codeGraph.ReplaceWithEdges(symbols, edges, len(symbols))
	return nil
}

func readDiffArg(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func stripRefresh(args []string) ([]string, bool) {
	if len(args) == 0 {
		return args, false
	}
	out := make([]string, 0, len(args))
	refresh := false
	for _, arg := range args {
		if arg == "--refresh" {
			refresh = true
			continue
		}
		out = append(out, arg)
	}
	return out, refresh
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
  grove index [dir] [--no-native] [--native=false] [--native-languages=go,rust] [--native-disabled-languages=python] [--native-timeout=5s]
  grove status [dir] [--refresh]
  grove symbols <query> [dir] [--refresh]        lexical substring search over names/paths/signatures
  grove query <intent> [dir] [--refresh]         semantic search (Model2Vec embeddings)
  grove deps <file> [dir] [--refresh]
  grove impact <symbol-or-file-query> [dir] [--refresh]
  grove tests <file> [dir] [--refresh]
  grove icr <intent> [dir] [--refresh]
  grove certify <diff-file-or-> [dir]
  grove conflicts <icr-a> <icr-b>
  grove mcp [dir]                    stdio MCP server

Grove is an embedded library: Prism, Fuse, and Relay link against it
directly. No HTTP daemon, no ports, no tokens.

Native analyzer environment overrides:
  GROVE_NATIVE=false
  GROVE_NATIVE_LANGUAGES=go,rust
  GROVE_NATIVE_DISABLED_LANGUAGES=python
  GROVE_NATIVE_TIMEOUT=5s
`)
}
