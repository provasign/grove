package model2vec

import (
	"bytes"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/core"
)

// newTinyEngine constructs an Engine wired to a 6-entry vocab with a
// hand-picked embedding matrix. Used by the New/Embed/Index/Query tests so
// every numeric value is auditable instead of opaque float32 noise.
//
// vocab: [PAD], [UNK], [CLS], [SEP], [MASK], rate, limit, throttle
// dim:   2
// embeddings:
//   - special tokens point at the origin (no signal)
//   - rate     = (1, 0)
//   - limit    = (1, 0)
//   - throttle = (0.95, 0.31)   ~ small angle from "rate limit" centroid
//   - cake     = (-1, 0)        opposite direction
func newTinyEngine(t *testing.T) *Engine {
	t.Helper()
	v := "[PAD]\n[UNK]\n[CLS]\n[SEP]\n[MASK]\nrate\nlimit\nthrottle\ncake\n"
	tok, err := LoadVocab(bytes.NewReader([]byte(v)))
	if err != nil {
		t.Fatal(err)
	}
	dim := 2
	matrix := []float32{
		0, 0, // [PAD]
		0, 0, // [UNK]
		0, 0, // [CLS]
		0, 0, // [SEP]
		0, 0, // [MASK]
		1, 0, // rate
		1, 0, // limit
		0.95, 0.31, // throttle
		-1, 0, // cake
	}
	eng, err := New(tok, matrix, dim)
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestNew_NilTokenizerRejected(t *testing.T) {
	_, err := New(nil, []float32{1, 2}, 2)
	if err == nil || !strings.Contains(err.Error(), "nil tokenizer") {
		t.Errorf("err = %v, want nil tokenizer", err)
	}
}

func TestNew_BadDim(t *testing.T) {
	tok, _ := LoadVocab(bytes.NewReader([]byte("[PAD]\n[UNK]\n")))
	_, err := New(tok, []float32{1, 2}, 0)
	if err == nil || !strings.Contains(err.Error(), "invalid dim") {
		t.Errorf("err = %v, want invalid dim", err)
	}
}

func TestNew_MatrixLengthMismatch(t *testing.T) {
	tok, _ := LoadVocab(bytes.NewReader([]byte("[PAD]\n[UNK]\n")))
	// dim=3 but matrix has 5 elements — 5 % 3 != 0
	_, err := New(tok, []float32{1, 2, 3, 4, 5}, 3)
	if err == nil || !strings.Contains(err.Error(), "not divisible") {
		t.Errorf("err = %v, want not divisible", err)
	}
}

func TestNew_VocabMatrixSizeMismatch(t *testing.T) {
	tok, _ := LoadVocab(bytes.NewReader([]byte("[PAD]\n[UNK]\nfoo\n")))
	// tokenizer has 3 entries but matrix has 4 rows (dim=2, len=8)
	_, err := New(tok, []float32{1, 2, 3, 4, 5, 6, 7, 8}, 2)
	if err == nil || !strings.Contains(err.Error(), "vocab/matrix size mismatch") {
		t.Errorf("err = %v, want vocab/matrix size mismatch", err)
	}
}

func TestEngine_Dim(t *testing.T) {
	eng := newTinyEngine(t)
	if got := eng.Dim(); got != 2 {
		t.Errorf("Dim = %d, want 2", got)
	}
}

func TestEmbed_EmptyInputReturnsNil(t *testing.T) {
	eng := newTinyEngine(t)
	if got := eng.Embed(""); got != nil {
		t.Errorf("Embed(\"\") = %v, want nil", got)
	}
}

func TestEmbed_AllOOVReturnsNil(t *testing.T) {
	eng := newTinyEngine(t)
	// "xyzabc" tokenizes to single UNK; UNK is skipped from the mean.
	if got := eng.Embed("xyzabc"); got != nil {
		t.Errorf("Embed(all-OOV) = %v, want nil", got)
	}
}

func TestEmbed_SingleTokenIsNormalized(t *testing.T) {
	eng := newTinyEngine(t)
	v := eng.Embed("rate")
	if len(v) != 2 {
		t.Fatalf("dim = %d, want 2", len(v))
	}
	// rate = (1, 0), normalized is still (1, 0).
	if math.Abs(float64(v[0])-1) > 1e-6 || math.Abs(float64(v[1])) > 1e-6 {
		t.Errorf("got %v, want (1, 0)", v)
	}
}

func TestEmbed_MultiTokenMeanThenNormalize(t *testing.T) {
	eng := newTinyEngine(t)
	// "rate limit" → mean of (1,0) and (1,0) = (1,0); already unit norm.
	v := eng.Embed("rate limit")
	if math.Abs(float64(v[0])-1) > 1e-6 || math.Abs(float64(v[1])) > 1e-6 {
		t.Errorf("got %v, want (1, 0)", v)
	}
}

func TestEmbed_ProducesUnitVector(t *testing.T) {
	eng := newTinyEngine(t)
	v := eng.Embed("throttle")
	var sq float64
	for _, x := range v {
		sq += float64(x) * float64(x)
	}
	if math.Abs(sq-1) > 1e-6 {
		t.Errorf("|v|^2 = %f, want 1", sq)
	}
}

func TestEmbed_OOVTokensIgnoredFromMean(t *testing.T) {
	eng := newTinyEngine(t)
	// "rate xyzabc" → rate=(1,0), xyzabc=UNK (skipped) → mean = (1,0).
	v := eng.Embed("rate xyzabc")
	if math.Abs(float64(v[0])-1) > 1e-6 {
		t.Errorf("got %v, want UNK ignored, vector ≈ (1,0)", v)
	}
}

func TestIndex_EmptyCorpus(t *testing.T) {
	eng := newTinyEngine(t)
	eng.Index(nil)
	if got := eng.Query("rate limit", 5); got != nil {
		t.Errorf("Query on empty corpus = %v, want nil", got)
	}
}

func TestIndex_SymbolsWithoutSignalAreSkipped(t *testing.T) {
	eng := newTinyEngine(t)
	// Mix: one symbol with valid tokens, one whose tokens are all OOV.
	syms := []core.SymbolRecord{
		{ID: "good", Name: "throttle"},
		{ID: "bad", Name: "xyzabc"}, // → UNK → no signal
	}
	eng.Index(syms)
	results := eng.Query("rate limit", 5)
	if len(results) != 1 || results[0].Symbol.ID != "good" {
		t.Errorf("got %d results, want 1 (only the good symbol)", len(results))
	}
}

func TestQuery_EmptyQueryReturnsNil(t *testing.T) {
	eng := newTinyEngine(t)
	eng.Index([]core.SymbolRecord{{ID: "x", Name: "rate"}})
	if got := eng.Query("", 5); got != nil {
		t.Errorf("Query(\"\") = %v, want nil", got)
	}
}

func TestQuery_RanksSemanticallySimilarFirst(t *testing.T) {
	eng := newTinyEngine(t)
	corpus := []core.SymbolRecord{
		{ID: "throttle", Name: "throttle"},
		{ID: "cake", Name: "cake"},
	}
	eng.Index(corpus)
	results := eng.Query("rate limit", 5)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Symbol.ID != "throttle" {
		t.Errorf("top = %q, want throttle", results[0].Symbol.ID)
	}
	// "cake" is opposite direction → cosine ≤ 0 → filtered out entirely.
	for _, r := range results {
		if r.Symbol.ID == "cake" {
			t.Error("opposite-direction symbol should be filtered (cosine ≤ 0)")
		}
	}
}

func TestQuery_RespectsLimit(t *testing.T) {
	eng := newTinyEngine(t)
	corpus := []core.SymbolRecord{
		{ID: "a", Name: "rate"},
		{ID: "b", Name: "limit"},
		{ID: "c", Name: "throttle"},
	}
	eng.Index(corpus)
	results := eng.Query("rate limit", 2)
	if len(results) != 2 {
		t.Errorf("got %d results, want 2 (limit honoured)", len(results))
	}
}

func TestQuery_ZeroLimitReturnsAll(t *testing.T) {
	eng := newTinyEngine(t)
	corpus := []core.SymbolRecord{
		{ID: "a", Name: "rate"},
		{ID: "b", Name: "limit"},
		{ID: "c", Name: "throttle"},
	}
	eng.Index(corpus)
	results := eng.Query("rate limit", 0)
	if len(results) != 3 {
		t.Errorf("got %d results, want 3 (limit=0 means all)", len(results))
	}
}

func TestQuery_ScoresInValidRange(t *testing.T) {
	eng := newTinyEngine(t)
	eng.Index([]core.SymbolRecord{{ID: "x", Name: "rate"}})
	results := eng.Query("rate", 1)
	if len(results) != 1 {
		t.Fatal("expected one result")
	}
	if results[0].Score <= 0 || results[0].Score > 1+1e-9 {
		t.Errorf("score = %v, want in (0, 1]", results[0].Score)
	}
}

func TestDocumentText_FieldOrdering(t *testing.T) {
	// All fields populated — verify joined text contains each and the name
	// appears first (and twice, as the soft-boost design intends).
	s := &core.SymbolRecord{
		Name:          "Foo",
		QualifiedName: "pkg.Foo",
		Signature:     "func Foo() error",
		Docstring:     "Foo does the thing.",
		ParentSymbol:  "Module",
	}
	got := documentText(s)
	if !strings.HasPrefix(got, "Foo Foo ") {
		t.Errorf("expected name doubled at start, got %q", got)
	}
	for _, want := range []string{"pkg.Foo", "func Foo() error", "Foo does the thing.", "Module"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestDocumentText_EmptyFieldsOmitted(t *testing.T) {
	// Only Name set; the other separators should not appear as orphans.
	s := &core.SymbolRecord{Name: "Solo"}
	got := documentText(s)
	if got != "Solo Solo " {
		t.Errorf("got %q, want \"Solo Solo \"", got)
	}
}

func TestNormalize_ZeroVectorIsIdempotent(t *testing.T) {
	v := []float32{0, 0, 0}
	normalize(v)
	for i, x := range v {
		if x != 0 {
			t.Errorf("v[%d] = %v after normalize, want 0 (zero vector unchanged)", i, x)
		}
	}
}

func TestNormalize_ScalesToUnit(t *testing.T) {
	v := []float32{3, 4}
	normalize(v)
	if math.Abs(float64(v[0])-0.6) > 1e-6 || math.Abs(float64(v[1])-0.8) > 1e-6 {
		t.Errorf("got %v, want (0.6, 0.8)", v)
	}
}

// ─── Default()/embedded model tests ─────────────────────────────────────────

func TestDefault_ReturnsCachedEngine(t *testing.T) {
	eng1, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	eng2, _ := Default()
	if eng1 != eng2 {
		t.Error("Default() should return the same Engine on subsequent calls")
	}
}

func TestDefault_ConcurrentInitializationSafe(t *testing.T) {
	var wg sync.WaitGroup
	got := make([]*Engine, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			e, err := Default()
			if err != nil {
				t.Errorf("Default(): %v", err)
				return
			}
			got[idx] = e
		}(i)
	}
	wg.Wait()
	for i := 1; i < 4; i++ {
		if got[i] != got[0] {
			t.Errorf("goroutine %d got different engine instance", i)
		}
	}
}

func TestDefault_SemanticRankingOnRealModel(t *testing.T) {
	// The headline regression: a query whose words do NOT appear in the
	// relevant document must still rank that document first via semantic
	// understanding. This is the property TF-IDF cannot deliver.
	eng, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	corpus := []core.SymbolRecord{
		{ID: "throttle", Name: "throttleRequests",
			Docstring: "Limit how many requests per second a client may send before being slowed down."},
		{ID: "login", Name: "loginUser",
			Docstring: "Authenticate a user against the credentials store and issue a session token."},
		{ID: "csv", Name: "exportCSV",
			Docstring: "Write rows from a database query to a comma-separated values file on disk."},
	}
	eng.Index(corpus)

	results := eng.Query("rate limiting", 3)
	if len(results) == 0 {
		t.Fatal("no results")
	}
	if results[0].Symbol.ID != "throttle" {
		t.Errorf("top = %q, want throttle (semantic match for 'rate limiting')",
			results[0].Symbol.ID)
		for i, r := range results {
			t.Logf("  rank %d: %s  score=%.4f", i, r.Symbol.ID, r.Score)
		}
	}
}

func TestDefault_EmbeddingDimIs256(t *testing.T) {
	eng, err := Default()
	if err != nil {
		t.Fatalf("Default(): %v", err)
	}
	if eng.Dim() != 256 {
		t.Errorf("Dim = %d, want 256 (potion-base-8M)", eng.Dim())
	}
	vec := eng.Embed("anything")
	if len(vec) != 256 {
		t.Errorf("vec len = %d, want 256", len(vec))
	}
}
