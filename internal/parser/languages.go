package parser

import (
	"path/filepath"
	"strings"
)

// PlaintextLanguage is the language tag assigned to non-code documents
// (markdown, YAML, JSON, XML, shell scripts, etc.) that Grove indexes as
// whole-file FTS5 records rather than extracted symbol trees.
const PlaintextLanguage = "plaintext"

// securityExtensions are file types that may contain credentials or key
// material. Grove never indexes these, even if they appear inside a tracked
// repository, to prevent credential content leaking into the FTS5 index.
var securityExtensions = map[string]bool{
	".key":      true,
	".pem":      true,
	".crt":      true,
	".cer":      true,
	".p12":      true,
	".pfx":      true,
	".jks":      true,
	".keystore": true,
	".pkcs12":   true,
}

// DetectLanguage returns the Grove language tag for a file path.
// Returns "" for unsupported or security-excluded files.
func DetectLanguage(path string) string {
	ext := filepath.Ext(path)

	// Security: never index credential/key material.
	if securityExtensions[strings.ToLower(ext)] {
		return ""
	}

	// Name-based detection for extensionless files or dotfiles whose full
	// name is the meaningful identifier (e.g. "Makefile", ".env").
	switch filepath.Base(path) {
	case ".env":
		// The bare .env file frequently contains real secrets; skip it.
		// .env.example, .env.sample etc. are caught by extension below.
		return ""
	case "Makefile", "GNUmakefile", "makefile",
		"Dockerfile", "Containerfile",
		".gitignore", ".dockerignore", ".gitattributes",
		".editorconfig", ".eslintignore", ".prettierignore",
		".nvmrc", ".node-version", ".python-version":
		return PlaintextLanguage
	}

	switch ext {
	// ── Code languages (AST-parsed via Tree-sitter or regex fallback) ──
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".py":
		return "python"
	case ".java":
		return "java"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".cxx", ".c++", ".hh", ".hpp", ".hxx":
		return "cpp"
	case ".cs":
		return "csharp"
	case ".php", ".php3", ".php4", ".php5", ".phtml":
		return "php"

	// ── Plaintext documents (FTS5-indexed as whole-file records) ──
	case ".md", ".mdx", ".markdown":
		return PlaintextLanguage
	case ".yaml", ".yml":
		return PlaintextLanguage
	case ".json":
		return PlaintextLanguage
	case ".xml":
		return PlaintextLanguage
	case ".sh", ".bash", ".zsh", ".fish":
		return PlaintextLanguage
	case ".toml":
		return PlaintextLanguage
	case ".ini", ".cfg", ".conf":
		return PlaintextLanguage
	case ".txt":
		return PlaintextLanguage
	case ".proto":
		return PlaintextLanguage
	case ".sql":
		return PlaintextLanguage
	case ".graphql", ".gql":
		return PlaintextLanguage
	case ".csv":
		return PlaintextLanguage

	default:
		return ""
	}
}

// Supported reports whether Grove can index the file at path.
func Supported(path string) bool {
	return DetectLanguage(path) != ""
}

// IsPlaintext reports whether the language tag belongs to the plaintext family.
func IsPlaintext(lang string) bool {
	return lang == PlaintextLanguage
}
