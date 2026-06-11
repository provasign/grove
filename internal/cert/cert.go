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

const (
	reportVersion         = 1
	invalidHunkRangeError = "diff_malformed: invalid hunk range %q"
)

// FileSHAFunc returns the current content SHA for a repo-relative path.
// ok=false means the file could not be read (missing, unreadable).
type FileSHAFunc func(path string) (sha string, ok bool)

// RepoFileSHA returns a FileSHAFunc that hashes repo-relative paths under
// root with the same content hash the indexer records per file.
func RepoFileSHA(root string) FileSHAFunc {
	return func(path string) (string, bool) {
		sha, err := parser.FileBlobSHA(filepath.Join(root, filepath.FromSlash(path)))
		return sha, err == nil
	}
}

// CertifyDiff maps a unified diff onto the indexed graph and emits a
// conservative report. It only certifies structural facts Grove can observe;
// unresolved or heuristic-only coverage is surfaced as manual review.
func CertifyDiff(codeGraph *graph.CodeGraph, input core.DiffInput) core.CertificationReport {
	return CertifyDiffWithStaleness(codeGraph, input, nil)
}

// CertifyDiffWithStaleness is CertifyDiff plus an index-freshness gate: when
// fileSHA is provided, every changed file whose indexed blob SHA no longer
// matches the content on disk is reported as index_stale and escalated to
// manual review. Without this check a stale index would map hunk line
// numbers onto outdated symbol spans and silently certify the wrong symbols.
func CertifyDiffWithStaleness(codeGraph *graph.CodeGraph, input core.DiffInput, fileSHA FileSHAFunc) core.CertificationReport {
	policy := input.Policy
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
	report.ChangedSymbols = sortedSymbols(collectChangedSymbols(files, symbolsByFile(symbols), &report, fileSHA))
	addDerivedGraphFacts(&report, symbols, edges)

	if policy.RequireTestsForCode {
		report.Unknowns = append(report.Unknowns, missingTestFindings(report.ChangedSymbols, report.Tests, edges)...)
	}

	if len(report.Unknowns) > 0 {
		report.Verdict = core.VerdictManualReview
	}
	return report
}

func collectChangedSymbols(files []DiffFile, byFile map[string][]core.SymbolRecord, report *core.CertificationReport, fileSHA FileSHAFunc) map[string]core.SymbolRecord {
	changedByID := map[string]core.SymbolRecord{}
	for _, file := range files {
		fileSymbols := byFile[file.Path]
		unknown, ok := fileLevelUnknown(file, fileSymbols)
		if ok {
			report.Unknowns = append(report.Unknowns, unknown)
			continue
		}
		if fileSHA != nil {
			current, ok := fileSHA(file.Path)
			if !ok || current != fileSymbols[0].BlobSHA {
				report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "index_stale",
					"indexed content does not match the file on disk; reindex before certifying"))
				continue
			}
		}
		if !addChangedSymbolsForFile(file, fileSymbols, changedByID) {
			report.Unknowns = append(report.Unknowns, fileUnknown(file.Path, "hunk_unmapped", "changed lines did not intersect any indexed symbol span"))
		}
	}
	return changedByID
}

func fileLevelUnknown(file DiffFile, fileSymbols []core.SymbolRecord) (core.CertificationFinding, bool) {
	switch {
	case file.Binary:
		return fileUnknown(file.Path, "binary_change", "binary diff cannot be mapped to indexed symbols"), true
	case file.Deleted:
		return fileUnknown(file.Path, "deleted_file", "deleted file cannot be certified against the current index"), true
	case len(file.Hunks) == 0:
		return fileUnknown(file.Path, "diff_no_hunks", "changed file has no parseable hunks"), true
	case len(fileSymbols) == 0:
		return fileUnknown(file.Path, "file_not_indexed", "changed file is unsupported, ignored, sensitive, or missing from the Grove index"), true
	default:
		return core.CertificationFinding{}, false
	}
}

func addChangedSymbolsForFile(file DiffFile, symbols []core.SymbolRecord, changedByID map[string]core.SymbolRecord) bool {
	matched := false
	for _, hunk := range file.Hunks {
		for _, symbol := range symbols {
			if rangesOverlap(hunk.NewRange, symbol.Span) {
				changedByID[symbol.ID] = symbol
				matched = true
			}
		}
	}
	return matched
}

func addDerivedGraphFacts(report *core.CertificationReport, symbols []core.SymbolRecord, edges []core.Edge) {
	impactedByID := impactedSymbols(report.ChangedSymbols, symbols, edges, 3)
	for _, changed := range report.ChangedSymbols {
		delete(impactedByID, changed.ID)
	}
	report.ImpactedSymbols = sortedSymbols(impactedByID)
	report.Tests = sortedSymbols(coveringTests(report.ChangedSymbols, symbols, edges))
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
	byID := symbolMap(symbols)
	visited := make(map[string]int, len(changed))
	queue := seedTraversal(changed, visited)
	traverseInbound(inboundIndex(edges), visited, queue, maxDepth)
	return materializeVisited(visited, byID)
}

// inboundIndex maps node → inbound edges so BFS is O(V+E) instead of one
// full edge scan per visited node.
func inboundIndex(edges []core.Edge) map[string][]*core.Edge {
	idx := make(map[string][]*core.Edge, len(edges))
	for i := range edges {
		idx[edges[i].To] = append(idx[edges[i].To], &edges[i])
	}
	return idx
}

func symbolMap(symbols []core.SymbolRecord) map[string]core.SymbolRecord {
	byID := make(map[string]core.SymbolRecord, len(symbols))
	for _, symbol := range symbols {
		byID[symbol.ID] = symbol
	}
	return byID
}

func seedTraversal(changed []core.SymbolRecord, visited map[string]int) []string {
	queue := make([]string, 0, len(changed))
	for _, symbol := range changed {
		visited[symbol.ID] = 0
		queue = append(queue, symbol.ID)
	}
	return queue
}

func traverseInbound(inbound map[string][]*core.Edge, visited map[string]int, queue []string, maxDepth int) {
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		if visited[node] >= maxDepth {
			continue
		}
		for _, edge := range inbound[node] {
			if !impactEdge(edge.Type) {
				continue
			}
			if _, ok := visited[edge.From]; ok {
				continue
			}
			visited[edge.From] = visited[node] + 1
			queue = append(queue, edge.From)
		}
	}
}

func materializeVisited(visited map[string]int, byID map[string]core.SymbolRecord) map[string]core.SymbolRecord {
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

func missingTestFindings(changed []core.SymbolRecord, tests []core.SymbolRecord, edges []core.Edge) []core.CertificationFinding {
	testIDs := make(map[string]struct{}, len(tests))
	for _, t := range tests {
		testIDs[t.ID] = struct{}{}
	}
	inbound := inboundIndex(edges)
	var findings []core.CertificationFinding
	for _, symbol := range changed {
		if !requiresTestEvidence(symbol) {
			continue
		}
		if !symbolHasCoveringTest(symbol.ID, testIDs, inbound) {
			findings = append(findings, core.CertificationFinding{
				Severity: core.FindingWarning,
				Code:     "tests_unknown",
				Message:  "code changes require test evidence, but Grove found no covering test symbols",
				Evidence: []core.EvidenceRef{symbolEvidence(symbol, core.EvidenceSourceHeuristic, 0.8, "test coverage is inferred from graph edges and test naming")},
			})
		}
	}
	return findings
}

func symbolHasCoveringTest(symbolID string, testIDs map[string]struct{}, inbound map[string][]*core.Edge) bool {
	for _, edge := range inbound[symbolID] {
		if _, ok := testIDs[edge.From]; ok {
			return true
		}
	}
	return false
}

func requiresTestEvidence(symbol core.SymbolRecord) bool {
	return symbol.Language != "" && !parser.IsPlaintext(symbol.Language) && !isTestSymbol(symbol)
}

func isTestSymbol(symbol core.SymbolRecord) bool {
	return core.IsTestSymbol(&symbol)
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

	parser := diffParser{}
	for _, line := range strings.Split(diff, "\n") {
		if err := parser.handleLine(line); err != nil {
			return nil, err
		}
	}
	return parser.finish()
}

type diffParser struct {
	files   []DiffFile
	current *DiffFile
	hunk    *DiffHunk
	newLine int
	sawDiff bool
}

func (p *diffParser) handleLine(line string) error {
	switch {
	case strings.HasPrefix(line, "diff --git "):
		p.startFile(line)
	case strings.HasPrefix(line, "+++ "):
		return p.handleNewFileHeader(line)
	case strings.HasPrefix(line, "deleted file mode "):
		p.markDeleted()
	case strings.HasPrefix(line, "Binary files ") || strings.HasPrefix(line, "GIT binary patch"):
		p.markBinary(line)
	case strings.HasPrefix(line, "@@ "):
		return p.startHunk(line)
	case p.hunk != nil:
		return p.handleHunkLine(line)
	}
	return nil
}

func (p *diffParser) startFile(line string) {
	p.sawDiff = true
	p.files = append(p.files, DiffFile{})
	p.current = &p.files[len(p.files)-1]
	p.hunk = nil
	if path := parseGitPath(line); path != "" {
		p.current.Path = path
	}
}

func (p *diffParser) handleNewFileHeader(line string) error {
	if p.current == nil {
		return errors.New("diff_malformed: +++ header before file header")
	}
	path := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
	if path == "/dev/null" {
		p.current.Deleted = true
		return nil
	}
	p.current.Path = cleanDiffPath(path)
	return nil
}

func (p *diffParser) markDeleted() {
	if p.current != nil {
		p.current.Deleted = true
	}
}

func (p *diffParser) markBinary(line string) {
	if p.current == nil {
		p.files = append(p.files, DiffFile{Path: binaryPath(line), Binary: true})
		p.current = &p.files[len(p.files)-1]
		p.sawDiff = true
		return
	}
	p.current.Binary = true
}

func (p *diffParser) startHunk(line string) error {
	if p.current == nil {
		return errors.New("diff_malformed: hunk before file header")
	}
	start, _, err := parseNewRange(line)
	if err != nil {
		return err
	}
	// NewRange starts empty and is grown from the actual +/- lines by
	// handleHunkLine. Seeding it from the @@ header would cover the whole
	// hunk including context lines, misreporting untouched neighbouring
	// symbols as changed.
	p.current.Hunks = append(p.current.Hunks, DiffHunk{})
	p.hunk = &p.current.Hunks[len(p.current.Hunks)-1]
	p.newLine = start
	return nil
}

func (p *diffParser) handleHunkLine(line string) error {
	if line == `\ No newline at end of file` {
		return nil
	}
	if line == "" {
		p.newLine++
		return nil
	}
	switch line[0] {
	case '+':
		extendHunkRange(p.hunk, p.newLine)
		p.newLine++
	case '-':
		// A deletion has no post-image line of its own; attribute it to the
		// post-image position it was removed from so the enclosing symbol is
		// still reported as changed for deletion-only hunks.
		extendHunkRange(p.hunk, p.newLine)
	case ' ':
		p.newLine++
	default:
		return fmt.Errorf("diff_malformed: unexpected hunk line %q", line)
	}
	return nil
}

func (p *diffParser) finish() ([]DiffFile, error) {
	if !p.sawDiff && len(p.files) == 0 {
		return nil, errors.New("diff_malformed: no unified diff file headers found")
	}
	out := p.files[:0]
	for i := range p.files {
		if p.files[i].Path == "" {
			return nil, errors.New("diff_malformed: changed file path is missing")
		}
		if len(p.files[i].Hunks) == 0 && !p.files[i].Binary && !p.files[i].Deleted {
			continue
		}
		out = append(out, p.files[i])
	}
	return out, nil
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
	// Traditional `diff -u` appends "\t<timestamp>" to header paths.
	if i := strings.IndexByte(path, '\t'); i >= 0 {
		path = path[:i]
	}
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
		if !strings.HasPrefix(field, "+") {
			continue
		}
		start, count, err := parseRangeField(strings.TrimPrefix(field, "+"))
		if err != nil {
			return 0, 0, fmt.Errorf(invalidHunkRangeError, header)
		}
		return start, count, nil
	}
	return 0, 0, fmt.Errorf("diff_malformed: missing new-file range %q", header)
}

func parseRangeField(field string) (int, int, error) {
	parts := strings.SplitN(field, ",", 2)
	start, err := atoiNonNegative(parts[0])
	if err != nil || (start == 0 && len(parts) != 2) {
		return 0, 0, errors.New("invalid range start")
	}
	count, err := rangeCount(parts)
	if err != nil {
		return 0, 0, err
	}
	if start == 0 && count != 0 {
		return 0, 0, errors.New("zero start with non-zero count")
	}
	return start, count, nil
}

func rangeCount(parts []string) (int, error) {
	if len(parts) == 1 {
		return 1, nil
	}
	return atoiNonNegative(parts[1])
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
