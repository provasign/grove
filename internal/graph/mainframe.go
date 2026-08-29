package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// Mainframe call resolution, kept apart from the modern-language path on
// purpose (docs/mainframe-build-plan.md): resolveCallEdges dispatches here
// for cobol/jcl symbols and the modern machinery never sees them.
//
// Semantics differ from modern languages in two ways:
//   - PERFORM targets are paragraphs/sections in the SAME compilation unit;
//     CALL and EXEC PGM= targets are programs resolved ESTATE-WIDE by
//     declared program name (flat namespace, no imports involved).
//   - A dynamic CALL through a variable is constant-propagated within the
//     unit: if the variable's declaration carries a VALUE 'LITERAL' clause,
//     the literal is the candidate program name, at reduced confidence
//     (spec R-5.2, intra-unit propagation).

var reValueLiteral = regexp.MustCompile(`(?i)\bVALUE\s+(?:IS\s+)?['"]([^'"]+)['"]`)

func mainframeCallerKind(kind core.SymbolKind) bool {
	switch string(kind) {
	case "paragraph", "section", "program", "step", "job", "jcl-procedure":
		return true
	}
	return false
}

func resolveMainframeCallEdges(idx *edgeIndex, symbol core.SymbolRecord) []core.Edge {
	if !mainframeCallerKind(symbol.Kind) {
		return nil
	}
	var edges []core.Edge
	seen := make(map[string]bool)
	add := func(toID string, confidence float64, source core.EvidenceSource, reason core.EdgeReason) {
		key := symbol.ID + "::calls::" + toID
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, core.Edge{
			From: symbol.ID, To: toID,
			Type: core.EdgeCalls, Confidence: confidence, Source: source, Reason: reason,
		})
	}

	for _, cs := range symbol.CallSites {
		name := strings.ToLower(cs.Callee)
		dynamic := len(cs.Args) == 1 && cs.Args[0] == "dynamic"

		if dynamic {
			// Trace the variable's VALUE clause within this unit.
			for _, cand := range idx.byName[name] {
				if cand.FilePath != symbol.FilePath || string(cand.Kind) != "data-item" {
					continue
				}
				if m := reValueLiteral.FindStringSubmatch(cand.Signature); m != nil {
					target := strings.ToLower(strings.TrimSpace(m[1]))
					for _, prog := range idx.byName[target] {
						if string(prog.Kind) == "program" {
							add(prog.ID, 0.6, core.EvidenceSourceHeuristic, core.ReasonDispatch)
						}
					}
				}
			}
			continue
		}

		// Same-unit PERFORM targets first (narrowest scope wins).
		resolved := false
		for _, cand := range idx.byName[name] {
			k := string(cand.Kind)
			if cand.FilePath == symbol.FilePath && (k == "paragraph" || k == "section") {
				add(cand.ID, 1.0, core.EvidenceSourceASTKit, core.ReasonASTNarrowed)
				resolved = true
			}
		}
		if resolved {
			continue
		}
		// Estate-wide program / JCL-procedure resolution by declared name.
		for _, cand := range idx.byName[name] {
			k := string(cand.Kind)
			if k == "program" || k == "jcl-procedure" {
				add(cand.ID, 0.9, core.EvidenceSourceASTKit, core.ReasonASTNarrowed)
			}
		}
	}
	return edges
}

// mainframeDataKind: kinds that anchor lineage queries (declared storage).
func mainframeDataKind(kind core.SymbolKind) bool {
	switch string(kind) {
	case "data-item", "condition-name", "logical-file":
		return true
	}
	return false
}

// mainframeAnchorKind: every mainframe kind change-impact accepts as anchor.
func mainframeAnchorKind(kind core.SymbolKind) bool {
	return mainframeCallerKind(kind) || mainframeDataKind(kind)
}

// hasMainframeSymbols reports whether any symbol is cobol/jcl — used by the
// delta path to fall back to a full edge build until include-closure-aware
// invalidation (plan phase 0.4) lands. Estates are few-files corpora; a full
// rebuild is cheap there, and silence-losing data edges is not acceptable.
func hasMainframeSymbols(symbols []core.SymbolRecord) bool {
	for i := range symbols {
		if symbols[i].Language == "cobol" || symbols[i].Language == "jcl" {
			return true
		}
	}
	return false
}

var reFieldToken = regexp.MustCompile(`[A-Za-z0-9][A-Za-z0-9-]+`)

// buildMainframeDataEdges emits the lineage layer:
//
//  1. Resolved include edges: file:A -> file:B when A's COPY member name
//     matches B's basename (the member/file join the bare "import:MEMBER"
//     record cannot make). The unresolved import edge is kept — it is the
//     honest record that the reference exists even when the member is absent.
//  2. Field-reference edges (uses-type, 0.7, regex): paragraph/section body
//     tokens matched against data items VISIBLE to that file — declared in
//     it, or in the transitive closure of its resolved copybooks. Name-level
//     matching, no direction claim; read/write direction needs the
//     PROCEDURE DIVISION grammar phase.
func buildMainframeDataEdges(idx *edgeIndex, symbols []core.SymbolRecord) []core.Edge {
	// Member name -> file path, for cobol files only.
	memberFile := map[string]string{}
	cobolFiles := map[string][]string{} // filePath -> imports
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "cobol" {
			continue
		}
		base := s.FilePath
		if j := strings.LastIndexByte(base, '/'); j >= 0 {
			base = base[j+1:]
		}
		if j := strings.IndexByte(base, '.'); j >= 0 {
			base = base[:j]
		}
		memberFile[strings.ToUpper(base)] = s.FilePath
		if _, ok := cobolFiles[s.FilePath]; !ok {
			cobolFiles[s.FilePath] = s.Imports
		}
	}
	if len(cobolFiles) == 0 {
		return nil
	}

	var edges []core.Edge
	// 1. Resolved include edges + per-file include closure.
	closure := map[string]map[string]bool{} // filePath -> reachable member files
	var expand func(file string, seen map[string]bool)
	expand = func(file string, seen map[string]bool) {
		for _, imp := range cobolFiles[file] {
			target, ok := memberFile[strings.ToUpper(imp)]
			if !ok || seen[target] || target == file {
				continue
			}
			seen[target] = true
			expand(target, seen)
		}
	}
	seenInc := map[string]bool{}
	for file := range cobolFiles {
		seen := map[string]bool{}
		expand(file, seen)
		closure[file] = seen
		for _, imp := range cobolFiles[file] {
			if target, ok := memberFile[strings.ToUpper(imp)]; ok && target != file {
				key := file + "->" + target
				if seenInc[key] {
					continue
				}
				seenInc[key] = true
				edges = append(edges, core.Edge{
					From: "file:" + file, To: "file:" + target,
					Type: core.EdgeImports, Confidence: 1.0,
					Source: core.EvidenceSourceASTKit, Reason: core.ReasonASTNarrowed,
				})
			}
		}
	}

	// 2. Field references from paragraph/section/program bodies.
	fieldsByFile := map[string]map[string][]*core.SymbolRecord{}
	fieldMap := func(file string) map[string][]*core.SymbolRecord {
		if m, ok := fieldsByFile[file]; ok {
			return m
		}
		m := map[string][]*core.SymbolRecord{}
		addFrom := func(f string) {
			for _, s := range idx.byFile[f] {
				if mainframeDataKind(s.Kind) {
					m[strings.ToUpper(s.Name)] = append(m[strings.ToUpper(s.Name)], s)
				}
			}
		}
		addFrom(file)
		for inc := range closure[file] {
			addFrom(inc)
		}
		fieldsByFile[file] = m
		return m
	}

	seenRef := map[string]bool{}
	for i := range symbols {
		s := &symbols[i]
		if s.Language != "cobol" || !mainframeCallerKind(s.Kind) || s.RawText == "" {
			continue
		}
		visible := fieldMap(s.FilePath)
		for _, tok := range reFieldToken.FindAllString(s.RawText, -1) {
			for _, field := range visible[strings.ToUpper(tok)] {
				key := s.ID + "->" + field.ID
				if seenRef[key] {
					continue
				}
				seenRef[key] = true
				edges = append(edges, core.Edge{
					From: s.ID, To: field.ID,
					Type: core.EdgeUsesType, Confidence: 0.7,
					Source: core.EvidenceSourceRegex, Reason: core.ReasonRegexFallbck,
				})
			}
		}
	}
	return edges
}

// mainframeImpactLocked answers change-impact for mainframe anchors. The
// modern Type.member machinery cannot represent them: a data item's dotted
// qualified name is a HIERARCHY path (group.field), not a type family, and
// COBOL names are case-insensitive. Declarations match by name or qualified
// name; impact sites are inbound call edges plus — for data anchors —
// inbound field-reference (uses-type) edges. Completeness is "callers-only":
// reachability, not a closed change-set.
func (g *CodeGraph) mainframeImpactLocked(query string) *ChangeImpactResult {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	var decls []core.SymbolRecord
	declIDs := map[string]bool{}
	dataAnchor := false
	for id := range g.symbols {
		s := g.symbols[id]
		if !mainframeAnchorKind(s.Kind) {
			continue
		}
		if strings.ToLower(s.QualifiedName) != q && strings.ToLower(s.Name) != q {
			continue
		}
		decls = append(decls, s)
		declIDs[s.ID] = true
		if mainframeDataKind(s.Kind) {
			dataAnchor = true
		}
	}
	if len(decls) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var callers []core.SymbolRecord
	for id := range declIDs {
		for _, ei := range g.inbound[id] {
			edge := g.edges[ei]
			ok := edge.Type == core.EdgeCalls || (dataAnchor && edge.Type == core.EdgeUsesType)
			if !ok || declIDs[edge.From] || seen[edge.From] {
				continue
			}
			seen[edge.From] = true
			if s, found := g.symbols[edge.From]; found {
				callers = append(callers, s)
			}
		}
	}
	sortSymbols(decls)
	sortSymbols(callers)
	return &ChangeImpactResult{
		Query:        query,
		Declarations: decls,
		Callers:      callers,
		Completeness: "callers-only",
	}
}
