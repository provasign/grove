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
