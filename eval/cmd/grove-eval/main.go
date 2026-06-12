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
	"strings"

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
	case "score-tests":
		err = cmdScoreTests(os.Args[2:])
	case "score-impact":
		err = cmdScoreImpact(os.Args[2:])
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
	baseline := fs.String("baseline", "", "baseline JSON; exit 1 if precision/recall regress below it")
	_ = fs.Parse(args)
	if *repo == "" || *truth == "" || *outDir == "" {
		return fmt.Errorf("score: --repo, --truth and --out-dir are required")
	}
	header, edges, err := eval.ReadTruth(*truth)
	if err != nil {
		return err
	}
	card, err := score(*repo, *outDir, header, edges)
	if err != nil {
		return err
	}
	if *baseline != "" {
		return gate(card, *baseline)
	}
	return nil
}

func cmdScoreTests(args []string) error {
	fs := flag.NewFlagSet("score-tests", flag.ExitOnError)
	repo := fs.String("repo", "", "repository root")
	truth := fs.String("truth", "", "tests truth JSONL path")
	outDir := fs.String("out-dir", "", "directory for scorecard outputs")
	baseline := fs.String("baseline", "", "tests baseline JSON; exit 1 if precision/hit-rate regress")
	_ = fs.Parse(args)
	if *repo == "" || *truth == "" || *outDir == "" {
		return fmt.Errorf("score-tests: --repo, --truth and --out-dir are required")
	}
	header, edges, err := eval.ReadTruth(*truth)
	if err != nil {
		return err
	}
	card, err := eval.ScoreTests(context.Background(), *repo, header, edges)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	js, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "tests-scorecard.json"), append(js, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*outDir, "tests-scorecard.md"), []byte(card.Markdown()), 0o644); err != nil {
		return err
	}
	fmt.Printf("score-tests: universe %d/%d (%.1f%%) · grove %d edges · oracle %d edges · P %.4f R %.4f · fn hit rate %.4f (%d/%d)\n",
		card.MatchedUniverse, card.TruthFunctions, card.SymbolMatchRate*100,
		card.GroveEdges, card.TruthEdges, card.Precision, card.Recall,
		card.FunctionHitRate, card.FunctionsHit, card.FunctionsCovered)
	if *baseline != "" {
		return gateTests(card, *baseline)
	}
	return nil
}

func cmdScoreImpact(args []string) error {
	fs := flag.NewFlagSet("score-impact", flag.ExitOnError)
	repo := fs.String("repo", "", "repository root")
	truth := fs.String("truth", "", "calls truth JSONL path")
	depth := fs.Int("depth", 2, "reverse-reachability depth")
	minConf := fs.Float64("min-path-conf", 0, "prune when path confidence product falls below this")
	sweep := fs.Bool("sweep", false, "print a table over thresholds instead of one run")
	_ = fs.Parse(args)
	if *repo == "" || *truth == "" {
		return fmt.Errorf("score-impact: --repo and --truth are required")
	}
	header, edges, err := eval.ReadTruth(*truth)
	if err != nil {
		return err
	}
	thresholds := []float64{*minConf}
	if *sweep {
		thresholds = []float64{0, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9}
	}
	for _, t := range thresholds {
		card, err := eval.ScoreImpact(context.Background(), *repo, header, edges, *depth, t)
		if err != nil {
			return err
		}
		fmt.Println("score-impact:", card.SummaryLine())
	}
	return nil
}

// testsBaselineEntry is one repo's accepted tests-edge floor: edge precision
// plus the function hit rate (the RFC #5 signal-quality metric).
type testsBaselineEntry struct {
	Commit    string  `json:"commit"`
	Precision float64 `json:"precision"`
	HitRate   float64 `json:"hitRate"`
	Tolerance float64 `json:"tolerance"`
}

func gateTests(card eval.Scorecard, baselinePath string) error {
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		return err
	}
	var entries map[string]testsBaselineEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%s: %w", baselinePath, err)
	}
	entry, ok := entries[card.Repo]
	if !ok {
		return fmt.Errorf("gate: no tests baseline entry for repo %q in %s", card.Repo, baselinePath)
	}
	tol := entry.Tolerance
	var failures []string
	if card.Precision < entry.Precision-tol {
		failures = append(failures, fmt.Sprintf("precision %.4f < baseline %.4f (tol %.4f)", card.Precision, entry.Precision, tol))
	}
	if card.FunctionHitRate < entry.HitRate-tol {
		failures = append(failures, fmt.Sprintf("function hit rate %.4f < baseline %.4f (tol %.4f)", card.FunctionHitRate, entry.HitRate, tol))
	}
	if len(failures) > 0 {
		return fmt.Errorf("gate: %s tests edges regressed: %s", card.Repo, strings.Join(failures, "; "))
	}
	fmt.Printf("gate: %s tests edges within baseline (P %.4f ≥ %.4f, hit %.4f ≥ %.4f, tol %.4f)\n",
		card.Repo, card.Precision, entry.Precision, card.FunctionHitRate, entry.HitRate, tol)
	return nil
}

// baselineEntry is one repo's accepted floor in a baseline JSON file
// (keyed by repo name). Tolerance absorbs benign jitter.
type baselineEntry struct {
	Commit    string  `json:"commit"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	Tolerance float64 `json:"tolerance"`
}

func gate(card eval.Scorecard, baselinePath string) error {
	raw, err := os.ReadFile(baselinePath)
	if err != nil {
		return err
	}
	var entries map[string]baselineEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%s: %w", baselinePath, err)
	}
	entry, ok := entries[card.Repo]
	if !ok {
		return fmt.Errorf("gate: no baseline entry for repo %q in %s", card.Repo, baselinePath)
	}
	tol := entry.Tolerance
	var failures []string
	if card.Precision < entry.Precision-tol {
		failures = append(failures, fmt.Sprintf("precision %.4f < baseline %.4f (tol %.4f)", card.Precision, entry.Precision, tol))
	}
	if card.Recall < entry.Recall-tol {
		failures = append(failures, fmt.Sprintf("recall %.4f < baseline %.4f (tol %.4f)", card.Recall, entry.Recall, tol))
	}
	if len(failures) > 0 {
		return fmt.Errorf("gate: %s regressed: %s", card.Repo, strings.Join(failures, "; "))
	}
	fmt.Printf("gate: %s within baseline (P %.4f ≥ %.4f, R %.4f ≥ %.4f, tol %.4f)\n",
		card.Repo, card.Precision, entry.Precision, card.Recall, entry.Recall, tol)
	return nil
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
	_, err = score(*repo, *outDir, header, edges)
	return err
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

func score(repo, outDir string, header eval.TruthFile, edges []eval.TruthEdge) (eval.Scorecard, error) {
	card, err := eval.ScoreCalls(context.Background(), repo, header, edges)
	if err != nil {
		return card, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return card, err
	}
	js, err := json.MarshalIndent(card, "", "  ")
	if err != nil {
		return card, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "scorecard.json"), append(js, '\n'), 0o644); err != nil {
		return card, err
	}
	if err := os.WriteFile(filepath.Join(outDir, "scorecard.md"), []byte(card.Markdown()), 0o644); err != nil {
		return card, err
	}
	fmt.Printf("score: universe %d/%d (%.1f%%) · grove %d edges · oracle %d edges · P %.4f R %.4f F1 %.4f\n",
		card.MatchedUniverse, card.TruthFunctions, card.SymbolMatchRate*100,
		card.GroveEdges, card.TruthEdges, card.Precision, card.Recall, card.F1)
	fmt.Printf("score: wrote %s and %s\n", filepath.Join(outDir, "scorecard.json"), filepath.Join(outDir, "scorecard.md"))
	return card, nil
}
