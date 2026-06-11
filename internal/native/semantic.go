package native

import (
	"regexp"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
)

var (
	lexCallRe  = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
	lexIdentRe = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
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
		// Strip and tokenize once per caller instead of compiling a regex
		// and re-stripping the body for every (caller, target) pair.
		stripped := stripQuotedText(caller.RawText)
		callNames := map[string]bool{}
		for _, m := range lexCallRe.FindAllStringSubmatch(stripped, -1) {
			callNames[m[1]] = true
		}
		identNames := map[string]bool{}
		for _, token := range lexIdentRe.FindAllString(stripped, -1) {
			identNames[token] = true
		}
		for _, target := range byFile[caller.FilePath] {
			if target.ID == caller.ID {
				continue
			}
			if callableKind(target.Kind) && callNames[target.Name] {
				add(symbolEdge(caller, target, core.EdgeCalls, callConfidence))
			}
			if typeKind(target.Kind) && identNames[target.Name] {
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

// Pattern caches: the per-language analyzers call containsCall /
// containsTypeToken inside (symbol × candidate) loops; compiling a fresh
// regex per probe dominated their cost.
var (
	patternCacheMu    sync.Mutex
	callPatternCache  = map[string]*regexp.Regexp{}
	tokenPatternCache = map[string]*regexp.Regexp{}
)

func cachedPattern(cache map[string]*regexp.Regexp, name, prefix, suffix string) *regexp.Regexp {
	patternCacheMu.Lock()
	defer patternCacheMu.Unlock()
	if p, ok := cache[name]; ok {
		return p
	}
	p := regexp.MustCompile(prefix + regexp.QuoteMeta(name) + suffix)
	cache[name] = p
	return p
}

func containsCall(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := cachedPattern(callPatternCache, name, `\b`, `\s*\(`)
	return pattern.MatchString(stripQuotedText(text))
}

func containsTypeToken(text, name string) bool {
	if name == "" {
		return false
	}
	pattern := cachedPattern(tokenPatternCache, name, `\b`, `\b`)
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
