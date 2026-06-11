package parser

import (
	"path/filepath"
	"strings"

	"github.com/provasign/grove/internal/core"
)

const (
	// PlaintextFTSLimit is the maximum bytes stored in the docstring column,
	// which feeds the search indexes. 64 KB covers nearly all real-world
	// docs and configs while bounding per-document index cost.
	PlaintextFTSLimit = 64 * 1024

	// PlaintextRawLimit is the maximum bytes stored in raw_text.
	// Prism uses raw_text for progressive disclosure; 1 MB is sufficient
	// for the largest policy/swagger/OpenAPI files teams encounter in practice.
	PlaintextRawLimit = 1024 * 1024
)

// ExtractPlaintext produces a single whole-file SymbolRecord for non-code
// documents. The record kind is KindDocument and the language is "plaintext".
//
// Content layout:
//   - docstring  — full text, capped at PlaintextFTSLimit (searchable)
//   - raw_text   — full text, capped at PlaintextRawLimit (Prism disclosure)
//   - signature  — first meaningful line (title / heading / top-level key)
//   - name       — base filename
//   - qualified_name — repo-relative path (used for path-based queries)
func ExtractPlaintext(relPath, blobSHA string, content []byte) []core.SymbolRecord {
	name := filepath.Base(relPath)
	text := string(content)
	lines := strings.Split(text, "\n")

	docstring := text
	if len(docstring) > PlaintextFTSLimit {
		docstring = docstring[:PlaintextFTSLimit]
	}
	rawText := text
	if len(rawText) > PlaintextRawLimit {
		rawText = rawText[:PlaintextRawLimit]
	}

	return []core.SymbolRecord{{
		ID:            relPath + "::document@" + blobSHA,
		FilePath:      relPath,
		BlobSHA:       blobSHA,
		Language:      PlaintextLanguage,
		Kind:          core.KindDocument,
		Name:          name,
		QualifiedName: relPath,
		Signature:     plaintextSignature(lines),
		Docstring:     docstring,
		RawText:       rawText,
		Span:          core.LineRange{Start: 1, End: len(lines)},
		TokenEstimate: len(content) / 4,
	}}
}

// plaintextSignature returns the first meaningful content line, stripped of
// markdown heading markers and leading whitespace, capped at 120 characters.
// Lines that are blank or consist solely of YAML front-matter delimiters
// ("---", "...") are skipped.
func plaintextSignature(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "---" || line == "..." {
			continue
		}
		// Strip markdown heading markers (# ## ### etc.)
		trimmed := strings.TrimLeft(line, "#")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		if len(trimmed) > 120 {
			trimmed = trimmed[:120]
		}
		return trimmed
	}
	return ""
}
