package cert

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/graph"
	"github.com/provasign/grove/internal/parser"
)

const reportVersion = 1

// CertifyDiff maps a unified diff onto the indexed graph and emits a
// conservative report. It only certifies structural facts Grove can observe;
// unresolved or heuristic-only coverage is surfaced as manual review.
func CertifyDiff(codeGraph *graph.CodeGraph, input core.DiffInput) core.CertificationReport {
	policy := input.Policy
	if !policy.RequireTestsForCode {
		policy.RequireTestsForCode = true
	}
	report := core.CertificationReport{
		Version: reportVersion,
		BaseRef: input.BaseRef,
		HeadRef: input.HeadRef,
		Verdict: core.VerdictAllow,
	}

	files, err := ParseUnifiedDiff(input.UnifiedDiff)
	if err != nil {
		report.Verdict = core.VerdictBlock
		report.Findings = append(report.Findings, finding(core.FindingError, "diff_malformed", err.Error(), nil))
		return report
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, file := range files {
		report.ChangedFiles = append(report.ChangedFiles, file.Path)
	}

	symbols, edges := codeGraph.Snapshot()
	byFile := symbolsByFile(symbols)
	changedByID := map[string]core.SymbolRecord{}

	for _, file := range files {
		fileSymbols := byFile[file.Path]
		if file.Binary {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "binary_change", "binary diff cannot be mapped to indexed symbols"))
			continue
		}
		if file.Deleted {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "deleted_file", "deleted file cannot be certified against the current index"))
			continue
		}
		if len(file.Hunks) == 0 {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "diff_no_hunks", "changed file has no parseable hunks"))
			continue
		}
		if len(fileSymbols) == 0 {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "file_not_indexed", "changed file is unsupported, ignored, sensitive, or missing from the Grove index"))
			continue
		}
		matched := false
		for _, hunk := range file.Hunks {
			for _, symbol := range fileSymbols {
				if rangesOverlap(hunk.NewRange, symbol.Span) {
					changedByID[symbol.ID] = symbol
					matched = true
				}
			}
		}
		if !matched {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "hunk_unmapped", "changed lines did not intersect any indexed symbol span"))
		}
	}

	report.ChangedSymbols = sortedSymbols(changedByID)
	impactedByID := impactedSymbols(report.ChangedSymbols, symbols, edges, 3)
	for _, changed := range report.ChangedSymbols {
		delete(impactedByID, changed.ID)
	}
	report.ImpactedSymbols = sortedSymbols(impactedByID)
	report.Tests = sortedSymbols(coveringTests(report.ChangedSymbols, symbols, edges))

	if policy.RequireTestsForCode {
		report.Unknowns = append(report.Unknowns, missingTestFindings(report.ChangedSymbols, report.Tests)...)
	}

	if len(report.Unknowns) > 0 {
		report.Verdict = core.VerdictManualReview
	}
	return report
}

func symbolsByFile(symbols []core.SymbolRecord) map[string][]core.SymbolRecord {
	out := make(map[string][]core.SymbolRecord)
	for _, symbol := range symbols {
		out[symbol.FilePath] = append(out[symbol.FilePath], symbol)
	}
	for file := range out {
		sort.Slice(out[file], func(i, j int) bool {
			return out[file][i].Span.Start < out[file][j].Span.Start
		})
	}
	return out
}

func impactedSymbols(changed []core.SymbolRecord, symbols []core.SymbolRecord, edges []core.Edge, maxDepth int) map[string]core.SymbolRecord {
	byID := make(map[string]core.SymbolRecord, len(symbols))
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	visited := make(map[string]int, len(changed))
	queue := make([]string, 0, len(changed))
	for _, symbol := range changed {
		visited[symbol.ID] = 0
		queue = append(queue, symbol.ID)
	}
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		depth := visited[node]
		if depth >= maxDepth {
			continue
		}
		for _, edge := range edges {
			if edge.To != node || !impactEdge(edge.Type) {
				continue
			}
			if _, ok := visited[edge.From]; ok {
				continue
			}
			visited[edge.From] = depth + 1
			queue = append(queue, edge.From)
		}
	}
	out := make(map[string]core.SymbolRecord)
	for id := range visited {
		if symbol, ok := byID[id]; ok {
			out[id] = symbol
		}
	}
	return out
}

func coveringTests(changed []core.SymbolRecord, symbols []core.SymbolRecord, edges []core.Edge) map[string]core.SymbolRecord {
	reachable := impactedSymbols(changed, symbols, edges, 6)
	out := make(map[string]core.SymbolRecord)
	for _, symbol := range reachable {
		if isTestSymbol(symbol) {
			out[symbol.ID] = symbol
		}
	}
	return out
}

func missingTestFindings(changed []core.SymbolRecord, tests []core.SymbolRecord) []core.CertificationFinding {
	if len(tests) > 0 {
		return nil
	}
	for _, symbol := range changed {
		if !requiresTestEvidence(symbol) {
			continue
		}
		return []core.CertificationFinding{{
			Severity: core.FindingWarning,
			Code:     "tests_unknown",
			Message:  "code changes require test evidence, but Grove found no covering test symbols",
			Evidence: []core.EvidenceRef{symbolEvidence(symbol, core.EvidenceSourceHeuristic, 0.8, "test coverage is inferred from graph edges and test naming")},
		}}
	}
	return nil
}

func requiresTestEvidence(symbol core.SymbolRecord) bool {
	return symbol.Language != "" && !parser.IsPlaintext(symbol.Language) && !isTestSymbol(symbol)
}

func isTestSymbol(symbol core.SymbolRecord) bool {
	base := filepath.Base(symbol.FilePath)
	lower := strings.ToLower(base)
	return strings.HasSuffix(lower, "_test.go") ||
		(strings.HasPrefix(lower, "test_") && strings.HasSuffix(lower, ".py")) ||
		strings.HasSuffix(lower, "_test.py") ||
		strings.HasSuffix(lower, ".test.ts") ||
		strings.HasSuffix(lower, ".spec.ts") ||
		strings.HasSuffix(lower, ".test.js") ||
		strings.HasSuffix(lower, ".spec.js") ||
		strings.HasSuffix(base, "Test.java") ||
		strings.HasSuffix(base, "Spec.java")
}

func impactEdge(edgeType core.EdgeType) bool {
	switch edgeType {
	case core.EdgeCalls, core.EdgeTests, core.EdgeContains, core.EdgeImplements, core.EdgeExtends, core.EdgeUsesType:
		return true
	default:
		return false
	}
}

func rangesOverlap(a, b core.LineRange) bool {
	if a.Start <= 0 || a.End <= 0 || b.Start <= 0 || b.End <= 0 {
		return false
	}
	return a.Start <= b.End && b.Start <= a.End
}

func sortedSymbols(symbols map[string]core.SymbolRecord) []core.SymbolRecord {
	out := make([]core.SymbolRecord, 0, len(symbols))
	for _, symbol := range symbols {
		out = append(out, symbol)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].FilePath == out[j].FilePath {
			if out[i].Span.Start == out[j].Span.Start {
				return out[i].ID < out[j].ID
			}
			return out[i].Span.Start < out[j].Span.Start
		}
		return out[i].FilePath < out[j].FilePath
	})
	return out
}

func fileUnknown(filePath, code, message string) core.CertificationFinding {
	return finding(core.FindingWarning, code, message, []core.EvidenceRef{{
		FilePath:   filePath,
		Source:     core.EvidenceSourceUnknown,
		Confidence: 0,
		Reason:     "Grove cannot certify this file structurally",
	}})
}

func symbolEvidence(symbol core.SymbolRecord, source core.EvidenceSource, confidence float64, reason string) core.EvidenceRef {
	return core.EvidenceRef{
		FilePath:   symbol.FilePath,
		BlobSHA:    symbol.BlobSHA,
		Span:       symbol.Span,
		SymbolID:   symbol.ID,
		Source:     source,
		Confidence: confidence,
		Reason:     reason,
	}
}

func finding(severity core.FindingSeverity, code, message string, evidence []core.EvidenceRef) core.CertificationFinding {
	return core.CertificationFinding{Severity: severity, Code: code, Message: message, Evidence: evidence}
}

type DiffFile struct {
	Path    string
	Hunks   []DiffHunk
	Deleted bool
	Binary  bool
}

type DiffHunk struct {
	NewRange core.LineRange
}

func ParseUnifiedDiff(diff string) ([]DiffFile, error) {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	diff = strings.TrimRight(diff, "\n")
	if strings.TrimSpace(diff) == "" {
		return nil, nil
	}

	var files []DiffFile
	var current *DiffFile
	var hunk *DiffHunk
	newLine := 0
	sawDiff := false

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			sawDiff = true
			files = append(files, DiffFile{})
			current = &files[len(files)-1]
			hunk = nil
			if path := parseGitPath(line); path != "" {
				current.Path = path
			}
		case strings.HasPrefix(line, "+++ "):
			if current == nil {
				return nil, errors.New("diff_malformed: +++ header before file header")
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if path == "/dev/null" {
				current.Deleted = true
				continue
			}
			current.Path = cleanDiffPath(path)
		case strings.HasPrefix(line, "deleted file mode "):
			if current != nil {
				current.Deleted = true
			}
		case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
			if current == nil {
				files = append(files, DiffFile{Path: binaryPath(line), Binary: true})
				current = &files[len(files)-1]
				sawDiff = true
				continue
			}
			current.Binary = true
		case strings.HasPrefix(line, "@@ "):
			if current == nil {
				return nil, errors.New("diff_malformed: hunk before file header")
			}
			start, count, err := parseNewRange(line)
			if err != nil {
				return nil, err
			}
			rangeEnd := start + count - 1
			if count == 0 {
				rangeEnd = start
			}
			current.Hunks = append(current.Hunks, DiffHunk{NewRange: core.LineRange{Start: start, End: rangeEnd}})
			hunk = &current.Hunks[len(current.Hunks)-1]
			newLine = start
		case hunk != nil:
			if line == `\ No newline at end of file` {
				continue
			}
			if line == "" {
				newLine++
				continue
			}
			switch line[0] {
			case '+':
				extendHunkRange(hunk, newLine)
				newLine++
			case '-':
			case ' ':
				newLine++
			default:
				return nil, fmt.Errorf("diff_malformed: unexpected hunk line %q", line)
			}
		}
	}
	if !sawDiff && len(files) == 0 {
		return nil, errors.New("diff_malformed: no unified diff file headers found")
	}
	for i := range files {
		if files[i].Path == "" {
			return nil, errors.New("diff_malformed: changed file path is missing")
		}
		if len(files[i].Hunks) == 0 && !files[i].Binary && !files[i].Deleted {
			return nil, fmt.Errorf("diff_malformed: %s has no hunks", files[i].Path)
		}
	}
	return files, nil
}

func parseGitPath(line string) string {
	parts := strings.Fields(line)
	if len(parts) < 4 {
		return ""
	}
	return cleanDiffPath(parts[3])
}

func binaryPath(line string) string {
	parts := strings.Fields(line)
	for _, part := range parts {
		if strings.HasPrefix(part, "b/") {
			return cleanDiffPath(part)
		}
	}
	return ""
}

func cleanDiffPath(path string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	path = strings.Trim(path, `"`)
	return filepath.ToSlash(path)
}

func parseNewRange(header string) (int, int, error) {
	second := strings.Index(header[2:], "@@")
	if second < 0 {
		return 0, 0, fmt.Errorf("diff_malformed: invalid hunk header %q", header)
	}
	body := strings.TrimSpace(header[2 : second+2])
	for _, field := range strings.Fields(body) {
		if strings.HasPrefix(field, "+") {
			field = strings.TrimPrefix(field, "+")
			parts := strings.SplitN(field, ",", 2)
			start, err := atoiNonNegative(parts[0])
			if err != nil || (start == 0 && len(parts) != 2) {
				return 0, 0, fmt.Errorf("diff_malformed: invalid hunk range %q", header)
			}
			count := 1
			if len(parts) == 2 {
				count, err = atoiNonNegative(parts[1])
				if err != nil {
					return 0, 0, fmt.Errorf("diff_malformed: invalid hunk range %q", header)
				}
			}
			if start == 0 && count != 0 {
				return 0, 0, fmt.Errorf("diff_malformed: invalid hunk range %q", header)
			}
			return start, count, nil
		}
	}
	return 0, 0, fmt.Errorf("diff_malformed: missing new-file range %q", header)
}

func atoiNonNegative(value string) (int, error) {
	var n int
	if value == "" {
		return 0, errors.New("empty")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errors.New("not numeric")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func extendHunkRange(hunk *DiffHunk, line int) {
	if line <= 0 {
		return
	}
	if hunk.NewRange.Start == 0 || line < hunk.NewRange.Start {
		hunk.NewRange.Start = line
	}
	if line > hunk.NewRange.End {
		hunk.NewRange.End = line
	}
}
