package native

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/provasign/grove/internal/core"
)

var (
	lexCallRe  = regexp.MustCompile(`([A-Za-z_$][A-Za-z0-9_$]*)\s*\(`)
	lexIdentRe = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)
)

// splitNominalTypeList splits a declaration's comma-separated base list while
// ignoring commas nested in generic arguments or constructor calls. It returns
// the source-level name without generic/argument suffixes.
func splitNominalTypeList(list string) []string {
	var out []string
	start, depth := 0, 0
	for i := 0; i <= len(list); i++ {
		if i < len(list) {
			switch list[i] {
			case '<', '(', '[':
				depth++
			case '>', ')', ']':
				if depth > 0 {
					depth--
				}
			}
		}
		if i < len(list) && (list[i] != ',' || depth != 0) {
			continue
		}
		part := strings.TrimSpace(list[start:i])
		start = i + 1
		if j := strings.IndexAny(part, "<("); j >= 0 {
			part = strings.TrimSpace(part[:j])
		}
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func balancedSuffixEnd(text string, open, close byte) int {
	if text == "" || text[0] != open {
		return 0
	}
	depth := 0
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return 0
}

func inheritanceClause(text, keyword string, stops ...string) []string {
	i := nominalKeyword(text, keyword)
	if i < 0 {
		return nil
	}
	rest := strings.TrimLeft(text[i+len(keyword):], " \t\r\n")
	end, depth := len(rest), 0
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '<', '(', '[':
			depth++
		case '>', ')', ']':
			if depth > 0 {
				depth--
			}
		case '{':
			if depth == 0 {
				end = i
				i = len(rest)
			}
		default:
			if depth == 0 {
				for _, stop := range stops {
					if nominalWordAt(rest, i, stop) {
						end = i
						i = len(rest)
						break
					}
				}
			}
		}
	}
	return splitNominalTypeList(rest[:end])
}

func nominalKeyword(text, keyword string) int {
	for i := 0; i+len(keyword) <= len(text); i++ {
		if nominalWordAt(text, i, keyword) {
			return i
		}
	}
	return -1
}

func nominalWordAt(text string, offset int, word string) bool {
	if offset < 0 || offset+len(word) > len(text) || text[offset:offset+len(word)] != word {
		return false
	}
	isIdent := func(b byte) bool {
		return b == '_' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
	}
	return (offset == 0 || !isIdent(text[offset-1])) &&
		(offset+len(word) == len(text) || !isIdent(text[offset+len(word)]))
}

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

// identTokenRe extracts identifier-shaped tokens for typeTokensIn.
var identTokenRe = regexp.MustCompile(`[A-Za-z_$][A-Za-z0-9_$]*`)

// typeTokensIn returns the distinct identifier tokens of text with quoted
// text stripped. Type-use passes scan the body ONCE and look tokens up,
// replacing the per-(symbol, type-name) regex scans that made the java/
// csharp/php analyzers quadratic (285s on a 1.2k-file repo — never noticed
// while the 5s timeout killed them before completion).
// asciiIdentRe: names the token pass can find. Anything else (unicode,
// dotted, generic) falls back to the per-name regex scan — the quadratic
// only bit when EVERY name took that path; the slow set is normally empty.
var asciiIdentRe = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)

// slowTypeNames returns the type names typeTokensIn cannot surface.
func slowTypeNames(typesByName map[string][]core.SymbolRecord) []string {
	var out []string
	for name := range typesByName {
		if !asciiIdentRe.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func typeTokensIn(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, t := range identTokenRe.FindAllString(stripQuotedText(text), -1) {
		tokens[t] = true
	}
	return tokens
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
