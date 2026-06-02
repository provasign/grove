package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// ─── DetectLanguage — plaintext extensions ────────────────────────────────────

func TestDetectLanguagePlaintext(t *testing.T) {
	cases := map[string]string{
		// Markdown
		"README.md":        "plaintext",
		"CHANGELOG.mdx":    "plaintext",
		"notes.markdown":   "plaintext",
		// YAML
		"config.yaml":      "plaintext",
		"policy.yml":       "plaintext",
		// JSON
		"package.json":     "plaintext",
		"mcp.json":         "plaintext",
		// XML
		"rules.xml":        "plaintext",
		"pom.xml":          "plaintext",
		// Shell
		"deploy.sh":        "plaintext",
		"setup.bash":       "plaintext",
		"rc.zsh":           "plaintext",
		// TOML
		"Cargo.toml":       "plaintext",
		"pyproject.toml":   "plaintext",
		// Config
		"app.ini":          "plaintext",
		"server.cfg":       "plaintext",
		"nginx.conf":       "plaintext",
		// Text
		"notes.txt":        "plaintext",
		// Proto / SQL / GraphQL
		"api.proto":        "plaintext",
		"schema.sql":       "plaintext",
		"query.graphql":    "plaintext",
		"query.gql":        "plaintext",
		// CSV
		"data.csv":         "plaintext",
		// Name-based (no extension)
		"Makefile":         "plaintext",
		"GNUmakefile":      "plaintext",
		"makefile":         "plaintext",
		"Dockerfile":       "plaintext",
		"Containerfile":    "plaintext",
		".gitignore":       "plaintext",
		".dockerignore":    "plaintext",
		".gitattributes":   "plaintext",
		".editorconfig":    "plaintext",
		".nvmrc":           "plaintext",
		".node-version":    "plaintext",
		".python-version":  "plaintext",
	}
	for path, want := range cases {
		got := DetectLanguage(path)
		if got != want {
			t.Errorf("DetectLanguage(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestDetectLanguageSecurityExclusions(t *testing.T) {
	excluded := []string{
		"id_rsa.key",
		"server.pem",
		"cert.crt",
		"cert.cer",
		"keystore.p12",
		"keystore.pfx",
		"app.jks",
		"trust.keystore",
		"app.pkcs12",
		".env", // bare .env — may contain real secrets
	}
	for _, path := range excluded {
		if got := DetectLanguage(path); got != "" {
			t.Errorf("DetectLanguage(%q) = %q, want \"\" (security exclusion)", path, got)
		}
	}
}

func TestDetectLanguageUnknownExtension(t *testing.T) {
	unknowns := []string{"file.bin", "image.png", "archive.zip", "video.mp4", "font.woff2"}
	for _, path := range unknowns {
		if got := DetectLanguage(path); got != "" {
			t.Errorf("DetectLanguage(%q) = %q, want \"\"", path, got)
		}
	}
}

func TestIsPlaintext(t *testing.T) {
	if !IsPlaintext("plaintext") {
		t.Error("IsPlaintext(\"plaintext\") should be true")
	}
	for _, lang := range []string{"go", "python", "typescript", ""} {
		if IsPlaintext(lang) {
			t.Errorf("IsPlaintext(%q) should be false", lang)
		}
	}
}

// ─── ExtractPlaintext — record structure ─────────────────────────────────────

func TestExtractPlaintext_BasicMarkdown(t *testing.T) {
	content := []byte("# My Document\n\nSome content here.\n")
	records := ExtractPlaintext("docs/readme.md", "abc123", content)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	if r.Name != "readme.md" {
		t.Errorf("Name = %q, want readme.md", r.Name)
	}
	if r.QualifiedName != "docs/readme.md" {
		t.Errorf("QualifiedName = %q", r.QualifiedName)
	}
	if r.Language != PlaintextLanguage {
		t.Errorf("Language = %q", r.Language)
	}
	if r.Kind != core.KindDocument {
		t.Errorf("Kind = %q", r.Kind)
	}
	if r.Signature != "My Document" {
		t.Errorf("Signature = %q, want \"My Document\"", r.Signature)
	}
	if !strings.Contains(r.Docstring, "Some content") {
		t.Errorf("Docstring missing file content: %q", r.Docstring)
	}
	if r.ID != "docs/readme.md::document@abc123" {
		t.Errorf("ID = %q", r.ID)
	}
	if r.BlobSHA != "abc123" {
		t.Errorf("BlobSHA = %q", r.BlobSHA)
	}
	if r.Span.Start != 1 {
		t.Errorf("Span.Start = %d, want 1", r.Span.Start)
	}
	if r.Span.End != 4 { // 4 lines: "# My Document", "", "Some content here.", ""
		t.Errorf("Span.End = %d, want 4", r.Span.End)
	}
	if r.TokenEstimate == 0 {
		t.Error("TokenEstimate should be non-zero for non-empty content")
	}
	if r.FilePath != "docs/readme.md" {
		t.Errorf("FilePath = %q", r.FilePath)
	}
}

func TestExtractPlaintext_YAML(t *testing.T) {
	content := []byte("name: my-policy\nrules:\n  - allow_all\n")
	records := ExtractPlaintext(".provasign/policies/default.yaml", "def456", content)
	if len(records) != 1 {
		t.Fatalf("expected 1 record")
	}
	r := records[0]
	if r.Signature != "name: my-policy" {
		t.Errorf("Signature = %q, want \"name: my-policy\"", r.Signature)
	}
	if !strings.Contains(r.Docstring, "allow_all") {
		t.Errorf("Docstring should contain YAML content")
	}
}

func TestExtractPlaintext_EmptyFile(t *testing.T) {
	records := ExtractPlaintext("empty.txt", "sha3", []byte{})
	if len(records) != 1 {
		t.Fatalf("expected 1 record even for empty file, got %d", len(records))
	}
	r := records[0]
	if r.Signature != "" {
		t.Errorf("empty file: Signature = %q, want \"\"", r.Signature)
	}
	if r.TokenEstimate != 0 {
		t.Errorf("empty file: TokenEstimate = %d, want 0", r.TokenEstimate)
	}
	if r.Span.Start != 1 {
		t.Errorf("Span.Start = %d", r.Span.Start)
	}
}

func TestExtractPlaintext_TokenEstimate(t *testing.T) {
	content := []byte(strings.Repeat("word ", 400)) // 2000 bytes → ~500 tokens
	records := ExtractPlaintext("a.txt", "sha", content)
	got := records[0].TokenEstimate
	if got < 400 || got > 600 {
		t.Errorf("TokenEstimate = %d, want ~500 (len/4)", got)
	}
}

func TestExtractPlaintext_LineCount(t *testing.T) {
	cases := []struct {
		content string
		wantEnd int
	}{
		{"line1\nline2\nline3", 3},
		{"single line", 1},
		{"a\nb\nc\nd\n", 5}, // trailing newline creates empty final element
		{"", 1},             // empty string splits to [""], giving 1 line
	}
	for _, c := range cases {
		records := ExtractPlaintext("test.txt", "sha", []byte(c.content))
		if records[0].Span.End != c.wantEnd {
			t.Errorf("content=%q: Span.End = %d, want %d", c.content, records[0].Span.End, c.wantEnd)
		}
	}
}

// ─── FTS limit / raw limit ────────────────────────────────────────────────────

func TestExtractPlaintext_DocstringCappedAtFTSLimit(t *testing.T) {
	big := strings.Repeat("a", PlaintextFTSLimit+1000)
	records := ExtractPlaintext("big.txt", "sha", []byte(big))
	r := records[0]
	if len(r.Docstring) > PlaintextFTSLimit {
		t.Errorf("Docstring len %d exceeds FTS limit %d", len(r.Docstring), PlaintextFTSLimit)
	}
	// raw_text should hold the full content (still under PlaintextRawLimit)
	if len(r.RawText) != PlaintextFTSLimit+1000 {
		t.Errorf("RawText len %d, want %d", len(r.RawText), PlaintextFTSLimit+1000)
	}
}

func TestExtractPlaintext_RawTextCappedAt1MB(t *testing.T) {
	huge := strings.Repeat("b", PlaintextRawLimit+1000)
	records := ExtractPlaintext("huge.txt", "sha", []byte(huge))
	r := records[0]
	if len(r.RawText) > PlaintextRawLimit {
		t.Errorf("RawText len %d exceeds raw limit %d", len(r.RawText), PlaintextRawLimit)
	}
	if len(r.Docstring) > PlaintextFTSLimit {
		t.Errorf("Docstring len %d exceeds FTS limit %d", len(r.Docstring), PlaintextFTSLimit)
	}
}

// ─── plaintextSignature ───────────────────────────────────────────────────────

func TestPlaintextSignature_VariousFormats(t *testing.T) {
	cases := []struct {
		content string
		want    string
	}{
		{"# H1 Title\n", "H1 Title"},
		{"## H2 Section\n", "H2 Section"},
		{"### Deep heading\nsome text", "Deep heading"},
		{"\n\n# After blanks\n", "After blanks"},
		{"---\nkey: value\n", "key: value"},         // skip YAML front-matter delimiter
		{"...\nother: stuff\n", "other: stuff"},      // skip YAML end marker
		{"name: my-policy\n", "name: my-policy"},
		{"", ""},                                     // empty
		{"\n\n\n", ""},                               // only blank lines
		{"---\n---\n", ""},                           // only delimiters
	}
	for _, c := range cases {
		lines := strings.Split(c.content, "\n")
		got := plaintextSignature(lines)
		if got != c.want {
			t.Errorf("content=%q: signature = %q, want %q", c.content, got, c.want)
		}
	}
}

func TestPlaintextSignature_CappedAt120Chars(t *testing.T) {
	long := strings.Repeat("x", 200)
	lines := []string{long}
	got := plaintextSignature(lines)
	if len(got) > 120 {
		t.Errorf("signature len %d, want ≤ 120", len(got))
	}
}

func TestPlaintextSignature_PureHeadingMarkerLine(t *testing.T) {
	// "### " with nothing after stripping should be skipped
	lines := []string{"###", "real content"}
	got := plaintextSignature(lines)
	if got != "real content" {
		t.Errorf("signature = %q, want \"real content\"", got)
	}
}

// ─── ExtractFile integration ──────────────────────────────────────────────────

func TestExtractFile_PlaintextMD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte("# Hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	symbols, err := engine.ExtractFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(symbols))
	}
	if symbols[0].Kind != core.KindDocument {
		t.Errorf("Kind = %q, want document", symbols[0].Kind)
	}
	if symbols[0].Name != "notes.md" {
		t.Errorf("Name = %q", symbols[0].Name)
	}
}

func TestExtractFile_PlaintextYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	if err := os.WriteFile(path, []byte("rules:\n  - block_public\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	symbols, err := engine.ExtractFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 1 || symbols[0].Language != PlaintextLanguage {
		t.Errorf("unexpected symbols: %+v", symbols)
	}
}

func TestExtractFile_SecurityExtensionReturnsNil(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.pem")
	if err := os.WriteFile(path, []byte("-----BEGIN CERTIFICATE-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	symbols, err := engine.ExtractFile(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(symbols) != 0 {
		t.Errorf("expected 0 symbols for .pem file, got %d", len(symbols))
	}
}

func TestWalkIndexesMarkdown(t *testing.T) {
	dir := t.TempDir()
	// Write a Go file and a markdown file.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Project\nDocs here.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine := NewEngine()
	symbols, filesIndexed, err := engine.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filesIndexed != 2 {
		t.Errorf("filesIndexed = %d, want 2", filesIndexed)
	}

	var hasDoc, hasCode bool
	for _, s := range symbols {
		if s.Kind == core.KindDocument {
			hasDoc = true
		}
		if s.Language == "go" {
			hasCode = true
		}
	}
	if !hasDoc {
		t.Error("Walk should produce a KindDocument record for README.md")
	}
	if !hasCode {
		t.Error("Walk should still produce code symbols for .go files")
	}
}

func TestWalkSkipsSecurityFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "key.pem"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DB_PASS=secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine()
	_, filesIndexed, err := engine.Walk(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filesIndexed != 0 {
		t.Errorf("filesIndexed = %d, want 0 (security files skipped)", filesIndexed)
	}
}
