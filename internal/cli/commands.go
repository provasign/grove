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
	case "doctor":
		return doctor(args[1:])
	case "symbols":
		return symbols(engine, codeGraph, args[1:])
	case "deps":
		return deps(engine, codeGraph, args[1:])
	case "impact":
		return impact(engine, codeGraph, args[1:])
	case "change-impact":
		return changeImpact(engine, codeGraph, args[1:])
	case "missing-implementations":
		return missingImplementations(engine, codeGraph, args[1:])
	case "rename-plan":
		return renamePlan(engine, codeGraph, args[1:])
	case "dead-code":
		return deadCode(engine, codeGraph, args[1:])
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
	cfg, err := config.Resolve(argOrDefault(args, 0, "."))
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
	cfg, err := config.Resolve(argOrDefault(args, 0, "."))
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
		content := []byte("version: 1\nstore: .grove/grove.db\n")
		if err := os.WriteFile(configPath, content, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return printJSON(map[string]any{"initialized": true, "config": configPath})
}

func indexCommand(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	dir, nativeCfg, opts, err := parseNativeIndexArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cfg, err := config.Resolve(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	root := cfg.Root
	result, err := indexRootFull(engine, codeGraph, root, nativeCfg, opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

func status(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	cfg, err := config.Resolve(argOrDefault(args, 0, "."))
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

func doctor(args []string) int {
	cfg, err := config.Resolve(argOrDefault(args, 0, "."))
	if err != nil {
		return printJSON(map[string]any{
			"status": "error",
			"error":  err.Error(),
		})
	}

	indexStatus := core.Status{}
	warnings := []string{}
	state := "ok"
	if !storeExists(cfg.Root) {
		state = "warning"
		warnings = append(warnings, "repository is not indexed; run grove index")
	} else {
		db, err := store.Open(cfg.Root)
		if err != nil {
			return printJSON(map[string]any{
				"status": "error",
				"root":   cfg.Root,
				"error":  err.Error(),
			})
		}
		defer db.Close()
		indexStatus, err = db.Status(context.Background())
		if err != nil {
			return printJSON(map[string]any{
				"status": "error",
				"root":   cfg.Root,
				"error":  err.Error(),
			})
		}
	}

	return printJSON(map[string]any{
		"status":       state,
		"version":      version.Version,
		"root":         cfg.Root,
		"storePath":    cfg.StorePath,
		"index":        indexStatus,
		"warnings":     warnings,
		"capabilities": core.CurrentCapabilities(),
	})
}

func symbols(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove symbols <query> [dir] [--refresh]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
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


func deps(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove deps <file> [dir] [--refresh]")
		return 2
	}
	filePath := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
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
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
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

// changeImpact prints the type-resolved change-set for a method signature
// change: declaration, override/implementation family, super-declarations,
// and resolved callers — the task-shaped alternative to name-seeded impact.
func changeImpact(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove change-impact 'Type.method' | 'Type.method(ParamType, ...)' [dir] [--refresh]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result, err := codeGraph.ChangeImpact(query)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

// missingImplementations prints every type in the contract's subtype closure
// that fails to implement the member — the interface-evolution companion to
// change-impact (who is broken, rather than what must change).
func missingImplementations(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove missing-implementations 'Type.method' | 'Type.method(ParamType, ...)' [dir] [--refresh]")
		return 2
	}
	query := args[0]
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result, err := codeGraph.MissingImplementations(query)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

// renamePlan converts a change-impact set into concrete line edits with
// suggested substitutions. Confirmed edits are safe to apply; Ambiguous
// lines need receiver-type verification first (the containing method also
// calls a same-named non-family method).
func renamePlan(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: grove rename-plan 'Type.method' <NewName> [dir] [--refresh]")
		return 2
	}
	query, newName := args[0], args[1]
	cfg, err := config.Resolve(argOrDefault(args, 2, "."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	result, err := codeGraph.RenamePlan(query, newName)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(result)
}

func deadCode(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	var roots []string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--roots" && i+1 < len(args) {
			for _, r := range strings.Split(args[i+1], ",") {
				if r = strings.TrimSpace(r); r != "" {
					roots = append(roots, r)
				}
			}
			i++
			continue
		}
		rest = append(rest, args[i])
	}
	cfg, err := config.Resolve(argOrDefault(rest, 0, "."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, refresh); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return printJSON(codeGraph.DeadCode(roots))
}

func icr(engine *parser.Engine, codeGraph *graph.CodeGraph, args []string) int {
	args, refresh := stripRefresh(args)
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: grove icr <intent> [dir] [--refresh]")
		return 2
	}
	intent := args[0]
	root := argOrDefault(args, 1, ".")
	cfg, err := config.Resolve(root)
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
	cfg, err := config.Resolve(argOrDefault(args, 1, "."))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := prepareReadGraph(engine, codeGraph, cfg.Root, false); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	report := cert.CertifyDiffWithStaleness(codeGraph, core.DiffInput{UnifiedDiff: string(diffData)}, cert.RepoFileSHA(cfg.Root))
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
	cfg, err := config.Resolve(args[1])
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
	cfg, err := config.Resolve(args[1])
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
	return indexRootFull(engine, codeGraph, root, native.ConfigFromEnv(), index.Options{})
}

func indexRootFull(engine *parser.Engine, codeGraph *graph.CodeGraph, root string, nativeCfg native.Config, opts index.Options) (any, error) {
	store, err := store.Open(root)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	idx := index.NewWithNativeConfig(engine, store, nativeCfg)
	// Incremental edge construction needs the previous state as its
	// baseline. codeGraph is the caller's resident graph — empty for a
	// one-shot CLI run (so this is a no-op and the full build stands), but
	// populated under `grove watch` and the long-lived server, which is
	// exactly where re-resolving every caller on every edit is felt.
	if os.Getenv("GROVE_INCREMENTAL") != "0" && codeGraph != nil && opts.PrevEdges == nil {
		opts.PrevSymbols, opts.PrevEdges = codeGraph.BaselineRef()
	}
	indexedGraph, result, err := idx.IndexWithOptions(context.Background(), root, opts)
	if err != nil {
		return nil, err
	}
	symbols, edges := indexedGraph.Snapshot()
	codeGraph.ReplaceWithStoredEdges(symbols, edges, result.FilesSeen)
	return result, nil
}

func parseNativeIndexArgs(args []string) (string, native.Config, index.Options, error) {
	cfg := native.ConfigFromEnv()
	var opts index.Options
	var positional []string
	for _, arg := range args {
		switch {
		case arg == "--force":
			opts.Force = true
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
				return "", cfg, opts, fmt.Errorf("invalid --native-timeout: %s", value)
			}
			cfg.Timeout = d
			cfg.TimeoutPinned = true
		default:
			if strings.HasPrefix(arg, "--native") {
				return "", cfg, opts, fmt.Errorf("unknown native flag: %s", arg)
			}
			positional = append(positional, arg)
		}
	}
	return argOrDefault(positional, 0, "."), cfg, opts, nil
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
	codeGraph.ReplaceWithStoredEdges(symbols, edges, len(symbols))
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
	// Try plain JSON first: short JSON strings can also be valid base64,
	// and decoding those first corrupts the input.
	if json.Unmarshal([]byte(input), value) == nil {
		return nil
	}
	decoded, err := base64.StdEncoding.DecodeString(input)
	if err != nil {
		return fmt.Errorf("ICR argument is neither JSON nor base64-encoded JSON")
	}
	return json.Unmarshal(decoded, value)
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
  grove index [dir] [--force] [--no-native] [--native=false] [--native-languages=go,rust] [--native-disabled-languages=python] [--native-timeout=5s]
  grove status [dir] [--refresh]
  grove doctor [dir]
  grove symbols <query> [dir] [--refresh]        lexical substring search over names/paths/signatures
  grove deps <file> [dir] [--refresh]
  grove impact <symbol-or-file-query> [dir] [--refresh]
  grove change-impact <Type.method(Params)> [dir]   type-resolved change-set: declaration + override family + callers
  grove missing-implementations <Type.method> [dir]  types claiming the contract that do not implement the member
  grove rename-plan <Type.method> <NewName> [dir]  change-set as concrete line edits with substitutions
  grove dead-code [dir] [--roots a,b]            unreachable production functions/methods (precision-first)
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
