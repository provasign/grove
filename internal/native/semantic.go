package native

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

func lexicalSemanticEdges(symbols []core.SymbolRecord, languages map[string]bool, callConfidence, typeConfidence float64) []core.Edge {
	byFile := map[string][]core.SymbolRecord{}
	for _, symbol := range symbols {
		if languages[symbol.Language] {
			byFile[symbol.FilePath] = append(byFile[symbol.FilePath], symbol)
		}
	}
	var edges []core.Edge
	seen := map[string]bool{}
	add := func(edge core.Edge) {
		key := edge.From + "\x00" + string(edge.Type) + "\x00" + edge.To
		if seen[key] {
			return
		}
		seen[key] = true
		edges = append(edges, edge)
	}
	for _, caller := range symbols {
		if !languages[caller.Language] || caller.RawText == "" || !callableKind(caller.Kind) {
			continue
		}
		text := caller.RawText
		for _, target := range byFile[caller.FilePath] {
			if target.ID == caller.ID {
				continue
			}
			if callableKind(target.Kind) && containsCall(text, target.Name) {
				add(symbolEdge(caller, target, core.EdgeCalls, callConfidence))
			}
			if typeKind(target.Kind) && containsTypeToken(text, target.Name) {
				add(symbolEdge(caller, target, core.EdgeUsesType, typeConfidence))
			}
		}
	}
	return edges
}

func callableKind(kind core.SymbolKind) bool {
	return kind == core.KindFunction || kind == core.KindMethod || kind == core.KindConstructor
}

func typeKind(kind core.SymbolKind) bool {
	switch kind {
	case core.KindClass, core.KindInterface, core.KindType, core.KindEnum, core.KindStruct, core.KindTrait:
		return true
	default:
		return false
	}
}

func containsCall(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*\(`)
	return pattern.MatchString(stripQuotedText(text))
}

func containsTypeToken(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\b`)
	return pattern.MatchString(stripQuotedText(text))
}

func stripQuotedText(text string) string {
	var out strings.Builder
	out.Grow(len(text))
	inString := false
	var quote rune
	escaped := false
	for _, r := range text {
		if inString {
			if escaped {
				escaped = false
				out.WriteRune(' ')
				continue
			}
			if r == '\\' {
				escaped = true
				out.WriteRune(' ')
				continue
			}
			if r == quote {
				inString = false
			}
			out.WriteRune(' ')
			continue
		}
		if r == '"' || r == '\'' || r == '`' {
			inString = true
			quote = r
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(r)
	}
	return out.String()
}
