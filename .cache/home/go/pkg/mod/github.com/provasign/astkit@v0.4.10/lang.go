package astkit

import (
	"path/filepath"
	"strings"
)

// LanguageKey identifies a programming language or data format known to
// astkit. The empty string is the sentinel for "unknown".
type LanguageKey string

const (
	LangUnknown    LanguageKey = ""
	LangGo         LanguageKey = "go"
	LangTypeScript LanguageKey = "typescript"
	LangTSX        LanguageKey = "tsx"
	LangJavaScript LanguageKey = "javascript"
	LangPython     LanguageKey = "python"
	LangJava       LanguageKey = "java"
	LangRust       LanguageKey = "rust"
	LangC          LanguageKey = "c"
	LangCPP        LanguageKey = "cpp"
	LangCSharp     LanguageKey = "csharp"
	LangPHP        LanguageKey = "php"

	// Non-AST data formats — recognized for detection only; astkit does not
	// extract symbols from them. Callers (e.g. Fuse) handle these via
	// structured-merge.
	LangJSON LanguageKey = "json"
	LangYAML LanguageKey = "yaml"
	LangTOML LanguageKey = "toml"
)

// DetectLanguage returns the language for a given file path. The content
// argument is currently unused but reserved for future shebang/heredoc
// disambiguation.
func DetectLanguage(path, _ string) LanguageKey {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return LangGo
	case ".ts":
		return LangTypeScript
	case ".tsx":
		return LangTSX
	case ".js", ".jsx", ".mjs", ".cjs":
		return LangJavaScript
	case ".py":
		return LangPython
	case ".java":
		return LangJava
	case ".rs":
		return LangRust
	case ".c", ".h":
		return LangC
	case ".cc", ".cpp", ".cxx", ".hpp", ".hh", ".hxx":
		return LangCPP
	case ".cs":
		return LangCSharp
	case ".php":
		return LangPHP
	case ".json":
		return LangJSON
	case ".yaml", ".yml":
		return LangYAML
	case ".toml":
		return LangTOML
	default:
		return LangUnknown
	}
}

// IsAST reports whether the language has a tree-sitter grammar registered in
// astkit (i.e. Parse will succeed).
func IsAST(lang LanguageKey) bool {
	_, ok := treeSitterLanguage(lang)
	return ok
}

// IsConfigData reports whether the language is a structured data format.
func IsConfigData(lang LanguageKey) bool {
	return lang == LangJSON || lang == LangYAML || lang == LangTOML
}
