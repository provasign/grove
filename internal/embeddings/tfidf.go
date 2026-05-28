// Package embeddings provides a pure-Go TF-IDF retrieval engine over symbol
// metadata (name + qualified name + signature + docstring). It is the
// in-process semantic-similarity signal consumed by graph.SemanticSearch.
//
// Why TF-IDF rather than a neural embedding model?
//  1. Zero runtime dependencies (Grove's non-negotiable constraint).
//  2. Predictable cost — Index() is O(N · L) and Query() is O(Q · K).
//  3. Strong baseline for code retrieval: identifiers, paths, and docstrings
//     have heavy lexical overlap with intent strings.
//
// The engine can be swapped for an external embedding service later by
// implementing the same Engine interface.
package embeddings

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

// Engine is the contract any embedding backend must satisfy.
type Engine interface {
	Index(symbols []core.SymbolRecord)
	Query(query string, limit int) []Scored
}

// Scored is one ranked result.
type Scored struct {
	Symbol *core.SymbolRecord
	Score  float64
}

// TFIDF is a small in-memory term-frequency / inverse-document-frequency index.
//
// Each symbol is treated as a "document" whose tokens come from
// {name, qualifiedName, signature, docstring, parentSymbol}.
type TFIDF struct {
	symbols []core.SymbolRecord
	// docVecs[i] = TF·IDF weighted vector for symbols[i], keyed by term.
	docVecs []map[string]float64
	// docNorms[i] = L2 norm of docVecs[i].
	docNorms []float64
	// idf[t] = log(N / df_t).
	idf map[string]float64
}

// NewTFIDF returns an empty engine. Call Index() before Query().
func NewTFIDF() *TFIDF {
	return &TFIDF{idf: map[string]float64{}}
}

// Index builds the index from the given symbols. Calling Index again replaces
// the previous index entirely.
func (t *TFIDF) Index(symbols []core.SymbolRecord) {
	t.symbols = symbols
	n := len(symbols)
	t.docVecs = make([]map[string]float64, n)
	t.docNorms = make([]float64, n)
	t.idf = make(map[string]float64)

	// Document frequencies.
	df := make(map[string]int)
	docTokens := make([][]string, n)
	for i := range symbols {
		toks := tokenize(documentText(&symbols[i]))
		docTokens[i] = toks
		seen := map[string]bool{}
		for _, tok := range toks {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			df[tok]++
		}
	}
	for term, dfreq := range df {
		// Add-one smoothing to keep idf positive even for ubiquitous terms.
		t.idf[term] = math.Log(float64(n+1) / float64(dfreq+1))
	}

	// Per-document TF·IDF vectors with L2 normalization.
	for i, toks := range docTokens {
		tf := map[string]int{}
		for _, tok := range toks {
			tf[tok]++
		}
		vec := make(map[string]float64, len(tf))
		var sumSq float64
		for term, count := range tf {
			w := (1.0 + math.Log(float64(count))) * t.idf[term]
			vec[term] = w
			sumSq += w * w
		}
		t.docVecs[i] = vec
		t.docNorms[i] = math.Sqrt(sumSq)
	}
}

// Query returns the top-`limit` symbols ranked by cosine similarity against
// the query text. Symbols are returned in descending Score order. Score is
// always in [0, 1].
func (t *TFIDF) Query(query string, limit int) []Scored {
	if limit <= 0 || len(t.symbols) == 0 {
		return nil
	}
	qToks := tokenize(query)
	if len(qToks) == 0 {
		return nil
	}
	qTF := map[string]int{}
	for _, tok := range qToks {
		qTF[tok]++
	}
	qVec := make(map[string]float64, len(qTF))
	var qSumSq float64
	for term, count := range qTF {
		idf, ok := t.idf[term]
		if !ok {
			// Skip terms absent from the corpus: they cannot appear in any
			// document vector, so they contribute nothing to dot products but
			// would inflate qNorm and suppress every real-term score.
			continue
		}
		w := (1.0 + math.Log(float64(count))) * idf
		qVec[term] = w
		qSumSq += w * w
	}
	qNorm := math.Sqrt(qSumSq)
	if qNorm == 0 {
		return nil
	}

	scored := make([]Scored, 0, len(t.symbols))
	for i := range t.symbols {
		dn := t.docNorms[i]
		if dn == 0 {
			continue
		}
		// Cosine similarity — iterate the smaller side.
		var dot float64
		dv := t.docVecs[i]
		if len(qVec) <= len(dv) {
			for term, w := range qVec {
				if dw, ok := dv[term]; ok {
					dot += w * dw
				}
			}
		} else {
			for term, dw := range dv {
				if w, ok := qVec[term]; ok {
					dot += w * dw
				}
			}
		}
		if dot == 0 {
			continue
		}
		scored = append(scored, Scored{
			Symbol: &t.symbols[i],
			Score:  dot / (qNorm * dn),
		})
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

// ─── Tokenization ────────────────────────────────────────────────────────────

func documentText(s *core.SymbolRecord) string {
	var sb strings.Builder
	sb.Grow(len(s.Name) + len(s.QualifiedName) + len(s.Signature) + len(s.Docstring) + len(s.ParentSymbol) + 8)
	sb.WriteString(s.Name)
	sb.WriteByte(' ')
	sb.WriteString(s.QualifiedName)
	sb.WriteByte(' ')
	sb.WriteString(s.Signature)
	sb.WriteByte(' ')
	sb.WriteString(s.Docstring)
	sb.WriteByte(' ')
	sb.WriteString(s.ParentSymbol)
	return sb.String()
}

// tokenize splits identifiers on camelCase and snake_case boundaries, returns
// lowercase tokens with stopwords filtered.
func tokenize(text string) []string {
	if text == "" {
		return nil
	}
	var raw []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		raw = append(raw, cur.String())
		cur.Reset()
	}
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	out := make([]string, 0, len(raw)*2)
	for _, w := range raw {
		// camelCase split: emit each Run of [a-z0-9]+ and [A-Z][a-z0-9]*.
		split := splitCamel(w)
		for _, s := range split {
			s = strings.ToLower(s)
			if len(s) < 2 || stopwords[s] {
				continue
			}
			out = append(out, s)
		}
	}
	return out
}

// splitCamel splits identifiers like "getUserByID" into ["get","User","By","ID"].
// Numeric runs and consecutive uppercase letters are preserved as single tokens.
func splitCamel(s string) []string {
	if s == "" {
		return nil
	}
	runes := []rune(s)
	var out []string
	start := 0
	for i := 1; i < len(runes); i++ {
		prev, cur := runes[i-1], runes[i]
		nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
		switch {
		case unicode.IsLower(prev) && unicode.IsUpper(cur):
			out = append(out, string(runes[start:i]))
			start = i
		case unicode.IsUpper(prev) && unicode.IsUpper(cur) && nextLower:
			out = append(out, string(runes[start:i]))
			start = i
		case (unicode.IsLetter(prev) && unicode.IsDigit(cur)) ||
			(unicode.IsDigit(prev) && unicode.IsLetter(cur)):
			out = append(out, string(runes[start:i]))
			start = i
		}
	}
	out = append(out, string(runes[start:]))
	return out
}

// stopwords are common English + programming filler tokens.
var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "is": true, "of": true, "for": true,
	"to": true, "in": true, "and": true, "or": true, "with": true, "this": true,
	"that": true, "be": true, "on": true, "as": true, "it": true,
	"func": true, "function": true, "method": true, "class": true, "struct": true,
	"return": true, "returns": true, "var": true, "let": true, "const": true,
	"void": true, "int": true, "str": true, "string": true, "bool": true,
	"true": true, "false": true, "null": true, "none": true, "self": true,
}
