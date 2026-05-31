package model2vec

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Tokenizer is a minimal BERT WordPiece tokenizer matching the configuration
// of baai/bge-base-en-v1.5 (the base of potion-base-8M):
//
//   - lowercase   = true
//   - strip_accents = true   (default when lowercase is true)
//   - handle_chinese_chars = true
//   - continuing_subword_prefix = "##"
//   - max_input_chars_per_word = 100
//   - unk_token = "[UNK]"
//
// Model2Vec inference does not need [CLS]/[SEP] wrapping — we tokenize the
// raw text into content WordPieces and average their embeddings. The Encode
// method therefore returns only content piece IDs.
type Tokenizer struct {
	vocab     map[string]int32
	unkID     int32
	maxChars  int // per-word safety cap (BERT default = 100)
}

// LoadVocab reads a BERT vocab.txt (one token per line, line N maps to ID N)
// and returns a Tokenizer ready for Encode.
func LoadVocab(r io.Reader) (*Tokenizer, error) {
	vocab := make(map[string]int32, 32000)
	scanner := bufio.NewScanner(r)
	// vocab.txt lines are short, but the buffer must accept the longest line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var id int32
	for scanner.Scan() {
		token := scanner.Text()
		if _, dup := vocab[token]; !dup {
			vocab[token] = id
		}
		id++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("vocab: scan: %w", err)
	}
	unkID, ok := vocab["[UNK]"]
	if !ok {
		return nil, fmt.Errorf("vocab: missing [UNK]")
	}
	return &Tokenizer{vocab: vocab, unkID: unkID, maxChars: 100}, nil
}

// Size returns the number of entries in the vocabulary.
func (t *Tokenizer) Size() int { return len(t.vocab) }

// UnkID returns the token ID used for unknown tokens.
func (t *Tokenizer) UnkID() int32 { return t.unkID }

// Encode tokenizes text into WordPiece content IDs. Empty input returns nil.
// Out-of-vocabulary words become a single UNK token.
func (t *Tokenizer) Encode(text string) []int32 {
	if text == "" {
		return nil
	}
	words := basicTokenize(text)
	if len(words) == 0 {
		return nil
	}
	out := make([]int32, 0, len(words)*2)
	for _, w := range words {
		out = t.wordPiece(out, w)
	}
	return out
}

// wordPiece appends the greedy-longest-match WordPiece IDs for a single
// word to dst. A word longer than maxChars or with no matching prefix
// becomes one UNK token.
func (t *Tokenizer) wordPiece(dst []int32, word string) []int32 {
	rs := []rune(word)
	if len(rs) > t.maxChars {
		return append(dst, t.unkID)
	}
	start := 0
	pieces := dst
	startLen := len(pieces)
	for start < len(rs) {
		end := len(rs)
		var pieceID int32 = -1
		for start < end {
			substr := string(rs[start:end])
			if start > 0 {
				substr = "##" + substr
			}
			if id, ok := t.vocab[substr]; ok {
				pieceID = id
				break
			}
			end--
		}
		if pieceID < 0 {
			// No matching prefix → whole word is UNK; discard any partial
			// pieces appended so far for this word.
			return append(pieces[:startLen], t.unkID)
		}
		pieces = append(pieces, pieceID)
		start = end
	}
	return pieces
}

// basicTokenize mirrors HuggingFace BertNormalizer + BertPreTokenizer:
// clean control chars, normalize whitespace, add spaces around CJK,
// lowercase, strip accents, then split on whitespace and punctuation.
func basicTokenize(text string) []string {
	// Phase 1: clean + CJK spacing into a single rune buffer.
	var cleaned strings.Builder
	cleaned.Grow(len(text))
	for _, r := range text {
		switch {
		case r == 0 || r == 0xFFFD || isControl(r):
			// Drop NULs, replacement char, and other control runes.
		case unicode.IsSpace(r):
			cleaned.WriteByte(' ')
		case isCJK(r):
			cleaned.WriteByte(' ')
			cleaned.WriteRune(r)
			cleaned.WriteByte(' ')
		default:
			cleaned.WriteRune(r)
		}
	}

	// Phase 2: lowercase + strip accents in NFD form, then drop combining marks.
	lowered := strings.ToLower(cleaned.String())
	stripped := stripAccents(lowered)

	// Phase 3: split on whitespace, then further split each chunk on punctuation.
	var out []string
	for _, chunk := range strings.Fields(stripped) {
		out = append(out, splitPunct(chunk)...)
	}
	return out
}

// stripAccents removes Unicode combining marks (the Mn category) after
// NFD-normalizing the input. This matches BertNormalizer's strip_accents
// behaviour when lowercase is true.
func stripAccents(s string) string {
	if !needsNorm(s) {
		return s
	}
	nfd := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(nfd))
	for _, r := range nfd {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// needsNorm is a fast-path probe: pure ASCII inputs never need NFD-stripping,
// so we can skip the allocation+walk for the common case.
func needsNorm(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return true
		}
	}
	return false
}

// splitPunct splits a whitespace-free chunk into runs separated by
// punctuation, with each punctuation rune becoming its own token. Matches
// BertPreTokenizer's punctuation-as-boundary behaviour.
func splitPunct(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	var run strings.Builder
	flush := func() {
		if run.Len() > 0 {
			out = append(out, run.String())
			run.Reset()
		}
	}
	for _, r := range s {
		if isPunct(r) {
			flush()
			out = append(out, string(r))
			continue
		}
		run.WriteRune(r)
	}
	flush()
	return out
}

// isPunct matches BERT's definition: ASCII !-/, :-@, [-`, {-~ plus the
// Unicode P* punctuation categories.
func isPunct(r rune) bool {
	if (r >= 33 && r <= 47) || (r >= 58 && r <= 64) ||
		(r >= 91 && r <= 96) || (r >= 123 && r <= 126) {
		return true
	}
	return unicode.IsPunct(r)
}

// isControl returns true for runes BERT treats as control characters
// (everything in the C* category except tab/newline/carriage-return, which
// the cleaner has already converted to spaces).
func isControl(r rune) bool {
	if r == '\t' || r == '\n' || r == '\r' {
		return false
	}
	return unicode.Is(unicode.C, r)
}

// isCJK returns true for the CJK Unified Ideographs blocks BERT inserts
// spaces around so each character becomes its own token.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) ||
		(r >= 0x3400 && r <= 0x4DBF) ||
		(r >= 0x20000 && r <= 0x2A6DF) ||
		(r >= 0x2A700 && r <= 0x2B73F) ||
		(r >= 0x2B740 && r <= 0x2B81F) ||
		(r >= 0x2B820 && r <= 0x2CEAF) ||
		(r >= 0xF900 && r <= 0xFAFF) ||
		(r >= 0x2F800 && r <= 0x2FA1F)
}
