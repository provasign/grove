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
	default:
		return ""
	}
}

func Supported(path string) bool {
	return DetectLanguage(path) != ""
}
