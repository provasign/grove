package model2vec

import (
	"bytes"
	"strings"
	"testing"
)

// miniVocab provides a small deterministic vocabulary for tokenization tests:
// the special tokens at canonical IDs 0-4, then a handful of ASCII words and
// WordPiece continuations to exercise the greedy-longest-match algorithm.
var miniVocab = `[PAD]
[UNK]
[CLS]
[SEP]
[MASK]
the
quick
brown
fox
hello
world
rate
limit
limited
##ing
##s
##er
play
##ground
中
国
`

func newMiniTokenizer(t *testing.T) *Tokenizer {
	t.Helper()
	tok, err := LoadVocab(bytes.NewReader([]byte(miniVocab)))
	if err != nil {
		t.Fatalf("LoadVocab: %v", err)
	}
	return tok
}

// helper to convert a token-ID slice back to its strings via the embedded
// vocab map — useful for asserting against expected human-readable tokens.
func decode(t *testing.T, tok *Tokenizer, ids []int32) []string {
	t.Helper()
	rev := make(map[int32]string, len(tok.vocab))
	for s, id := range tok.vocab {
		rev[id] = s
	}
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = rev[id]
	}
	return out
}

func TestLoadVocab_BasicAndDuplicates(t *testing.T) {
	// Duplicates after the first occurrence should be ignored (vocab.txt
	// in real BERT models has no dups, but we shouldn't crash if one slips in).
	v := "[PAD]\n[UNK]\n[CLS]\n[SEP]\nfoo\nfoo\nbar\n"
	tok, err := LoadVocab(bytes.NewReader([]byte(v)))
	if err != nil {
		t.Fatal(err)
	}
	if tok.Size() != 6 {
		t.Errorf("Size = %d, want 6 (dups dropped)", tok.Size())
	}
	if tok.UnkID() != 1 {
		t.Errorf("UnkID = %d, want 1", tok.UnkID())
	}
}

func TestLoadVocab_MissingUNK(t *testing.T) {
	v := "[PAD]\n[CLS]\nfoo\n"
	_, err := LoadVocab(bytes.NewReader([]byte(v)))
	if err == nil || !strings.Contains(err.Error(), "[UNK]") {
		t.Errorf("err = %v, want missing [UNK]", err)
	}
}

func TestEncode_EmptyInput(t *testing.T) {
	tok := newMiniTokenizer(t)
	if got := tok.Encode(""); got != nil {
		t.Errorf("Encode(\"\") = %v, want nil", got)
	}
	// Whitespace-only also tokenizes to nothing.
	if got := tok.Encode("   \t\n"); got != nil {
		t.Errorf("Encode(whitespace) = %v, want nil", got)
	}
}

func TestEncode_PureASCIIWords(t *testing.T) {
	tok := newMiniTokenizer(t)
	ids := tok.Encode("the quick brown fox")
	got := decode(t, tok, ids)
	want := []string{"the", "quick", "brown", "fox"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEncode_LowercaseApplied(t *testing.T) {
	tok := newMiniTokenizer(t)
	ids := tok.Encode("THE Quick BROWN")
	got := decode(t, tok, ids)
	want := []string{"the", "quick", "brown"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEncode_PunctuationSplits(t *testing.T) {
	tok := newMiniTokenizer(t)
	// Punctuation should become its own token. "!" is not in miniVocab, so
	// it falls back to UNK — the assertion checks the SPLIT happened.
	ids := tok.Encode("hello, world!")
	got := decode(t, tok, ids)
	// We expect: hello / , (UNK) / world / ! (UNK)
	if len(got) != 4 {
		t.Fatalf("got %d tokens (%v), want 4", len(got), got)
	}
	if got[0] != "hello" || got[2] != "world" {
		t.Errorf("got %v, want hello at 0 and world at 2", got)
	}
	if got[1] != "[UNK]" || got[3] != "[UNK]" {
		t.Errorf("punct should be UNK: got %v", got)
	}
}

func TestEncode_WordPieceContinuation(t *testing.T) {
	tok := newMiniTokenizer(t)
	// "limiting" must split to limit + ##ing
	ids := tok.Encode("limiting")
	got := decode(t, tok, ids)
	want := []string{"limit", "##ing"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEncode_WordPieceLongestMatchFirst(t *testing.T) {
	tok := newMiniTokenizer(t)
	// "limited" must take "limited" whole (since it's in vocab) rather
	// than splitting to limit + ##ed.
	ids := tok.Encode("limited")
	got := decode(t, tok, ids)
	if len(got) != 1 || got[0] != "limited" {
		t.Errorf("got %v, want [limited] (longest match preferred)", got)
	}
}

func TestEncode_UnknownWordBecomesUNK(t *testing.T) {
	tok := newMiniTokenizer(t)
	// "xyzabc" has no prefix match → whole word UNK.
	ids := tok.Encode("xyzabc")
	if len(ids) != 1 || ids[0] != tok.UnkID() {
		t.Errorf("got %v, want single [UNK]", ids)
	}
}

func TestEncode_PartialMatchBacksOffToUNK(t *testing.T) {
	tok := newMiniTokenizer(t)
	// "playjunk" — "play" matches as a leading piece but "junk" has no
	// "##j..." continuation in vocab, so the whole word must become UNK
	// (the partial "play" piece is discarded).
	ids := tok.Encode("playjunk")
	if len(ids) != 1 || ids[0] != tok.UnkID() {
		t.Errorf("got %v, want single [UNK] (no continuation = whole-word UNK)", ids)
	}
}

func TestEncode_WordOverMaxCharsBecomesUNK(t *testing.T) {
	tok := newMiniTokenizer(t)
	longWord := strings.Repeat("a", 101)
	ids := tok.Encode(longWord)
	if len(ids) != 1 || ids[0] != tok.UnkID() {
		t.Errorf("got %v, want single [UNK] for >100 char word", ids)
	}
}

func TestEncode_CJKSplitsPerChar(t *testing.T) {
	tok := newMiniTokenizer(t)
	// 中国 → CJK chars each tokenized separately, then matched as whole pieces.
	ids := tok.Encode("中国")
	got := decode(t, tok, ids)
	if len(got) != 2 || got[0] != "中" || got[1] != "国" {
		t.Errorf("got %v, want [中, 国]", got)
	}
}

func TestEncode_AccentsStripped(t *testing.T) {
	v := miniVocab + "cafe\n"
	tok, err := LoadVocab(bytes.NewReader([]byte(v)))
	if err != nil {
		t.Fatal(err)
	}
	ids := tok.Encode("café") // 'é' must be stripped to 'e' → matches "cafe"
	got := decode(t, tok, ids)
	if len(got) != 1 || got[0] != "cafe" {
		t.Errorf("got %v, want [cafe] (accent stripped)", got)
	}
}

func TestEncode_ControlCharsDroppedAroundWord(t *testing.T) {
	tok := newMiniTokenizer(t)
	// Per HF BertNormalizer with clean_text=true, control chars are
	// REMOVED in place (not converted to spaces). Surrounding words must
	// survive the cleanup; an isolated word stays intact.
	ids := tok.Encode("hello\x00\x0b world\x07")
	got := decode(t, tok, ids)
	want := []string{"hello", "world"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestEncode_ReplacementCharDropped(t *testing.T) {
	tok := newMiniTokenizer(t)
	// Replacement char between two words separated by a real space — the
	// replacement char is removed without merging the words.
	ids := tok.Encode("hello� world")
	got := decode(t, tok, ids)
	want := []string{"hello", "world"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestNeedsNorm(t *testing.T) {
	if needsNorm("plain ascii only") {
		t.Error("pure ASCII should not need normalization")
	}
	if !needsNorm("café") {
		t.Error("non-ASCII input should need normalization")
	}
	if needsNorm("") {
		t.Error("empty string should not need normalization")
	}
}

func TestStripAccents_PassthroughForASCII(t *testing.T) {
	if got := stripAccents("plain"); got != "plain" {
		t.Errorf("got %q, want plain", got)
	}
}

func TestStripAccents_RemovesCombiningMarks(t *testing.T) {
	got := stripAccents("résumé naïve über")
	want := "resume naive uber"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSplitPunct_EmptyAndBoundaries(t *testing.T) {
	if got := splitPunct(""); got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}
	// Trailing + leading punctuation.
	got := splitPunct(",foo.bar.")
	want := []string{",", "foo", ".", "bar", "."}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestIsCJK_Boundaries(t *testing.T) {
	cases := map[rune]bool{
		'A':       false, // ASCII letter
		'1':       false, // digit
		'中':       true,  // CJK Unified Ideographs
		0x3400:    true,  // CJK Ext A start
		0x4DBF:    true,  // CJK Ext A end
		0x4DC0:    false, // just past Ext A
		0xF900:    true,  // CJK Compat Ideographs start
		0xFAFF:    true,  // CJK Compat Ideographs end
		0x20000:   true,  // CJK Ext B start
	}
	for r, want := range cases {
		if got := isCJK(r); got != want {
			t.Errorf("isCJK(%U) = %v, want %v", r, got, want)
		}
	}
}

func TestIsPunct_ASCIIAndUnicode(t *testing.T) {
	cases := map[rune]bool{
		'!':      true,
		'.':      true,
		',':      true,
		'@':      true,
		'[':      true,
		'`':      true,
		'{':      true,
		'~':      true,
		'a':      false,
		'0':      false,
		' ':      false,
		'¶':      true, // Unicode punctuation
	}
	for r, want := range cases {
		if got := isPunct(r); got != want {
			t.Errorf("isPunct(%q) = %v, want %v", r, got, want)
		}
	}
}

func TestIsControl_TabNewlineAreNotControl(t *testing.T) {
	if isControl('\t') || isControl('\n') || isControl('\r') {
		t.Error("tab/newline/cr should NOT be classified as control (they become spaces)")
	}
	if !isControl('\x00') {
		t.Error("NUL should be control")
	}
	if !isControl('\x07') {
		t.Error("BEL should be control")
	}
}
