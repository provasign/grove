// Package astkit is the shared code-intelligence layer used by every product
// in the Provasign toolchain.
//
// It owns:
//
//   - Language identification (LanguageKey + file-extension detection).
//   - Tree-sitter grammar dispatch (one place for every supported language).
//   - A rich, persistence-agnostic Symbol type that callers project down to
//     their own storage models.
//   - Per-language extraction strategies that emit Symbol + ImportStatement.
//
// Consumers (Grove, Fuse, Prism, Relay) import astkit and adapt to their own
// domain types — they do not depend on each other for parsing.
package astkit
