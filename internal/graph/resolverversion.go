package graph

import "github.com/provasign/grove/internal/core"

// Bump when edge resolution changes for unchanged source. Stored call edges
// cannot be reused across this boundary merely because blob hashes match.
const ResolverVersion = "2026-09-11-language-graph-v8"

// CurrentNativeEdges keeps supplemental evidence that remains valid during
// a resolver upgrade. Legacy Python native calls were name-only bindings.
func CurrentNativeEdges(symbols []core.SymbolRecord, edges []core.Edge) []core.Edge {
	language := make(map[string]string, len(symbols))
	for _, s := range symbols {
		language[s.ID] = s.Language
	}
	var out []core.Edge
	for _, e := range edges {
		if e.Source != core.EvidenceSourceNative {
			continue
		}
		if e.Type == core.EdgeCalls && language[e.From] == "python" {
			continue
		}
		out = append(out, e)
	}
	return out
}
