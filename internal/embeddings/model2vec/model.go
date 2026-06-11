// Package model2vec is a pure-Go inference engine for MinishLab's Model2Vec
// static embeddings, bundled with the potion-base-8M model.
//
// Model2Vec is a distilled sentence-transformer represented as a lookup
// table: a single matrix of shape [vocab_size, dim]. Inference is just
// tokenize → look up each token's row → mean-pool → L2-normalize. There is
// no neural network at runtime, no GPU, no CGO.
//
// The bundled model (potion-base-8M, 29 MB) is embedded via go:embed and
// loaded once at startup. The same Engine instance handles all queries.
package model2vec

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"sync"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/embeddings"
)

// Engine is a Model2Vec semantic-similarity backend. It implements the
// embeddings.Engine interface so it can be swapped for TFIDF in the graph.
//
// Construction is split from initialization: Default() loads the bundled
// model lazily on first use, so importing the package costs only memory
// for the embedded byte slices, not a 30 MB matrix allocation.
type Engine struct {
	tokenizer *Tokenizer
	matrix    []float32 // flat row-major [vocab_size * dim]
	dim       int

	mu       sync.RWMutex
	docVecs  []normalizedVec
	symbols  []core.SymbolRecord
}

type normalizedVec struct {
	v    []float32
	norm float32 // L2 norm before normalization; zero ⇒ empty vector
}

// New constructs an Engine from an already-loaded vocabulary and embedding
// matrix. Most callers want Default() instead.
func New(tok *Tokenizer, matrix []float32, dim int) (*Engine, error) {
	if tok == nil {
		return nil, fmt.Errorf("model2vec: nil tokenizer")
	}
	if dim <= 0 {
		return nil, fmt.Errorf("model2vec: invalid dim %d", dim)
	}
	if len(matrix)%dim != 0 {
		return nil, fmt.Errorf("model2vec: matrix length %d not divisible by dim %d", len(matrix), dim)
	}
	vocab := len(matrix) / dim
	if vocab != tok.Size() {
		return nil, fmt.Errorf("model2vec: vocab/matrix size mismatch: %d vs %d", tok.Size(), vocab)
	}
	return &Engine{tokenizer: tok, matrix: matrix, dim: dim}, nil
}

// Default returns an Engine initialized from the embedded potion-base-8M
// model. Subsequent calls return the same Engine via package-level cache,
// so the 30 MB matrix is decoded exactly once per process.
func Default() (*Engine, error) {
	return defaultOnce()
}

var (
	defaultEngine *Engine
	defaultErr    error
	defaultMu     sync.Mutex
)

// defaultOnce decodes the embedded model on first call and caches the result.
// Errors are cached too — once decoding fails, we don't retry.
func defaultOnce() (*Engine, error) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultEngine != nil || defaultErr != nil {
		return defaultEngine, defaultErr
	}
	tok, err := LoadVocab(bytes.NewReader(embeddedVocab))
	if err != nil {
		defaultErr = fmt.Errorf("model2vec: load vocab: %w", err)
		return nil, defaultErr
	}
	matrix, _, dim, err := LoadEmbeddings(bytes.NewReader(embeddedModel))
	if err != nil {
		defaultErr = fmt.Errorf("model2vec: load embeddings: %w", err)
		return nil, defaultErr
	}
	eng, err := New(tok, matrix, dim)
	if err != nil {
		defaultErr = err
		return nil, err
	}
	defaultEngine = eng
	return eng, nil
}

// Dim returns the embedding dimension.
func (e *Engine) Dim() int { return e.dim }

// Embed produces a single L2-normalized embedding vector for free-form text.
// Empty input or all-OOV input returns a nil vector — callers should treat
// nil as "no signal" and fall back accordingly.
func (e *Engine) Embed(text string) []float32 {
	ids := e.tokenizer.Encode(text)
	if len(ids) == 0 {
		return nil
	}
	// Exclude UNK tokens from the mean: averaging in a random/zero vector
	// for unknown words would dilute the meaningful signal.
	vec := make([]float32, e.dim)
	count := 0
	for _, id := range ids {
		if id == e.tokenizer.unkID {
			continue
		}
		base := int(id) * e.dim
		row := e.matrix[base : base+e.dim]
		for i, v := range row {
			vec[i] += v
		}
		count++
	}
	if count == 0 {
		return nil
	}
	inv := 1.0 / float32(count)
	for i := range vec {
		vec[i] *= inv
	}
	normalize(vec)
	return vec
}

// Index pre-computes a normalized vector for every symbol's document text.
// Safe to call multiple times — each call replaces the previous index.
func (e *Engine) Index(symbols []core.SymbolRecord) {
	e.IndexWithCache(symbols, nil)
}

// IndexWithCache is Index with vector reuse across rebuilds. A symbol's
// vector is a pure function of its document text, and symbol IDs embed the
// file content SHA — so a cache hit by ID is always valid. After a delta
// reindex of a large repo this turns the first query's full-corpus
// re-embed into embedding only the symbols of changed files. Entries whose
// IDs are gone are pruned so the cache tracks the live symbol set.
func (e *Engine) IndexWithCache(symbols []core.SymbolRecord, cache map[string][]float32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.symbols = symbols
	e.docVecs = make([]normalizedVec, len(symbols))
	used := make(map[string]bool, len(symbols))
	for i := range symbols {
		id := symbols[i].ID
		if cache != nil {
			used[id] = true
			if vec, ok := cache[id]; ok {
				if vec != nil {
					e.docVecs[i] = normalizedVec{v: vec, norm: 1}
				}
				continue
			}
		}
		text := documentText(&symbols[i])
		vec := e.Embed(text)
		if cache != nil {
			cache[id] = vec // nil is cached too: "no signal" is a valid result
		}
		if vec == nil {
			e.docVecs[i] = normalizedVec{}
			continue
		}
		// Embed() already normalizes, so norm=1. Track "has signal" via
		// the v != nil check rather than a separate flag.
		e.docVecs[i] = normalizedVec{v: vec, norm: 1}
	}
	if cache != nil {
		for id := range cache {
			if !used[id] {
				delete(cache, id)
			}
		}
	}
}

// Query returns the top-`limit` symbols ranked by cosine similarity. Returns
// nil if the engine has not been indexed or the query produces no signal.
func (e *Engine) Query(query string, limit int) []embeddings.Scored {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if len(e.symbols) == 0 {
		return nil
	}
	qvec := e.Embed(query)
	if qvec == nil {
		return nil
	}
	out := make([]embeddings.Scored, 0, len(e.symbols))
	for i := range e.symbols {
		dv := e.docVecs[i]
		if dv.norm == 0 {
			continue
		}
		score := dot(qvec, dv.v)
		if score <= 0 {
			continue
		}
		// Take an address-stable copy of the symbol so callers can hold
		// pointers without aliasing the slice's backing array.
		sym := e.symbols[i]
		out = append(out, embeddings.Scored{Symbol: &sym, Score: float64(score)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ─── math helpers ─────────────────────────────────────────────────────────────

// normalize L2-normalizes vec in place. Zero vectors are left unchanged.
func normalize(vec []float32) {
	var sq float32
	for _, v := range vec {
		sq += v * v
	}
	if sq == 0 {
		return
	}
	inv := float32(1.0 / math.Sqrt(float64(sq)))
	for i := range vec {
		vec[i] *= inv
	}
}

// dot computes the dot product of two equal-length vectors.
func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

// documentText joins the symbol fields the embedding should "see". Matches
// the field set used by TFIDF for consistency, but pre-pends name twice as
// a soft boost — names tend to be the most retrievable signal.
func documentText(s *core.SymbolRecord) string {
	// Pre-size buffer to avoid growth allocations in the common case.
	n := len(s.Name)*2 + len(s.QualifiedName) + len(s.Signature) +
		len(s.Docstring) + len(s.ParentSymbol) + 5
	buf := make([]byte, 0, n)
	if s.Name != "" {
		buf = append(buf, s.Name...)
		buf = append(buf, ' ')
		buf = append(buf, s.Name...)
		buf = append(buf, ' ')
	}
	if s.QualifiedName != "" {
		buf = append(buf, s.QualifiedName...)
		buf = append(buf, ' ')
	}
	if s.Signature != "" {
		buf = append(buf, s.Signature...)
		buf = append(buf, ' ')
	}
	if s.Docstring != "" {
		buf = append(buf, s.Docstring...)
		buf = append(buf, ' ')
	}
	if s.ParentSymbol != "" {
		buf = append(buf, s.ParentSymbol...)
	}
	return string(buf)
}
