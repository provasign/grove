// grove-eval scores Grove's graph edges against typed-toolchain ground truth.
//
//	grove-eval truth --repo PATH --out truth.jsonl [--include-tests] [--commit SHA]
//	grove-eval score --repo PATH --truth truth.jsonl --out-dir DIR
//	grove-eval run   --repo PATH --out-dir DIR [--include-tests] [--commit SHA]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/provasign/grove/eval"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "truth":
		err = cmdTruth(os.Args[2:])
	case "score":
		err = cmdScore(os.Args[2:])
	case "run":
		err = cmdRun(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "grove-eval:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  grove-eval truth --repo PATH --out truth.jsonl [--include-tests] [--commit SHA]
  grove-eval score --repo PATH --truth truth.jsonl --out-dir DIR
  grove-eval run   --repo PATH --out-dir DIR [--include-tests] [--commit SHA]`)
	os.Exit(2)
}

func truthEnv() []string {
	// GOWORK=off keeps go.work files in parent directories from changing
	// what the oracle type-checks.
	return append(os.Environ(), "GOWORK=off")
}

func cmdTruth(args []string) error {
	fs := flag.NewFlagSet("truth", flag.ExitOnError)
	repo := fs.String("repo", "", "repository root")
	out := fs.String("out", "", "output truth JSONL path")
	commit := fs.String("commit", "", "commit SHA recorded in the header")
	includeTests := fs.Bool("include-tests", false, "include _test.go packages")
	_ = fs.Parse(args)
	if *repo == "" || *out == "" {
		return fmt.Errorf("truth: --repo and --out are required")
	}
	header, edges, err := generateTruth(*repo, *commit, *includeTests)
	if err != nil {
		return err
	}
	if err := eval.WriteTruth(*out, header, edges); err != nil {
		return err
	}
	fmt.Printf("truth: %d functions, %d edges -> %s\n", header.Functions, header.Edges, *out)
	return nil
}

func cmdScore(args []string) error {
	fs := flag.NewFlagSet("score", flag.ExitOnError)
	repo := fs.String("repo", "", "repository root")
	truth := fs.String("truth", "", "truth JSONL path")
	outDir := fs.String("out-dir", "", "directory for scorecard.json/.md")
	_ = fs.Parse(args)
	if *repo == "" || *truth == "" || *outDir == "" {
		return fmt.Errorf("score: --repo, --truth and --out-dir are required")
	}
	header, edges, err := eval.ReadTruth(*truth)
	if err != nil {
		return err
	}
	return score(*repo, *outDir, header, edges)
}

func cmdRun(args []string) error {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	repo := fs.String("repo", "", "repository root")
	outDir := fs.String("out-dir", "", "directory for truth + scorecard outputs")
	commit := fs.String("commit", "", "commit SHA recorded in the header")
	includeTests := fs.Bool("include-tests", false, "include _test.go packages")
	_ = fs.Parse(args)
	if *repo == "" || *outDir == "" {
		return fmt.Errorf("run: --repo and --out-dir are required")
	}
	header, edges, err := generateTruth(*repo, *commit, *includeTests)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	truthPath := filepath.Join(*outDir, "calls-truth.jsonl")
	if err := eval.WriteTruth(truthPath, header, edges); err != nil {
		return err
	}
	fmt.Printf("truth: %d functions, %d edges -> %s\n", header.Functions, header.Edges, truthPath)
	return score(*repo, *outDir, header, edges)
}

func generateTruth(repo, commit string, includeTests bool) (eval.TruthFile, []eval.TruthEdge, error) {
	header, edges, err := eval.GoCallTruth(eval.GoTruthOptions{
		RepoRoot:     repo,
		IncludeTests: includeTests,
		Env:          truthEnv(),
	})
	if err != nil {
		return header, edges, err
	}
	header.Commit = commit
	return header, edges, nil
}

func score(repo, outDir string, header eval.TruthFile, edges []eval.TruthEdge) error {
	card, err := eval.ScoreCalls(context.Background(), repo, header, edges)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	js, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "scorecard.json"), append(js, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "scorecard.md"), []byte(card.Markdown()), 0o644); err != nil {
		return err
	}
	fmt.Printf("score: universe %d/%d (%.1f%%) · grove %d edges · oracle %d edges · P %.4f R %.4f F1 %.4f\n",
		card.MatchedUniverse, card.TruthFunctions, card.SymbolMatchRate*100,
		card.GroveEdges, card.TruthEdges, card.Precision, card.Recall, card.F1)
	fmt.Printf("score: wrote %s and %s\n", filepath.Join(outDir, "scorecard.json"), filepath.Join(outDir, "scorecard.md"))
	return nil
}
