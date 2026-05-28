package parser

import "path/filepath"

func DetectLanguage(path string) string {
	switch filepath.Ext(path) {
	case ".go":
		return "go"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "tsx" // TSX uses a separate grammar that understands JSX syntax
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript" // tree-sitter-javascript already supports JSX natively
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
	default:
		return ""
	}
}

func Supported(path string) bool {
	return DetectLanguage(path) != ""
}
