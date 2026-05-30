// Package parser bridges Grove's storage-aware projection to the shared
// astkit tree-sitter extraction layer. The actual per-language extraction
// logic lives in astkit/strategies; this file only:
//
//  1. Maps Grove's language strings (e.g. "javascript") → astkit.LanguageKey.
//  2. Drives the shared parser/registry.
//  3. Projects each astkit.Symbol → core.SymbolRecord, attaching Grove-only
//     fields (ID, FilePath, BlobSHA, Language, Imports, TokenEstimate) and
//     renaming Body→RawText, Exported→Exports, ParentName→ParentSymbol.
package parser

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tabladrum/grove-suite/astkit"
	"github.com/tabladrum/grove-suite/astkit/strategies"
	"github.com/tabladrum/grove-suite/grove/internal/core"
)

const parseTimeout = 5 * time.Second

var (
	sharedEngineOnce sync.Once
	sharedEngine     *astkit.Engine
	sharedRegistry   *astkit.Registry
)

func bridge() (*astkit.Engine, *astkit.Registry) {
	sharedEngineOnce.Do(func() {
		sharedEngine = astkit.NewEngine()
		sharedRegistry = strategies.Default()
	})
	return sharedEngine, sharedRegistry
}

// languageToKey maps Grove's language string convention to astkit.LanguageKey.
// Unknown languages return ("", false).
func languageToKey(language string) (astkit.LanguageKey, bool) {
	switch language {
	case "go":
		return astkit.LangGo, true
	case "python":
		return astkit.LangPython, true
	case "javascript":
		return astkit.LangJavaScript, true
	case "typescript":
		return astkit.LangTypeScript, true
	case "tsx":
		return astkit.LangTSX, true
	case "java":
		return astkit.LangJava, true
	case "rust":
		return astkit.LangRust, true
	case "c":
		return astkit.LangC, true
	case "cpp", "c++":
		return astkit.LangCPP, true
	case "csharp", "c#":
		return astkit.LangCSharp, true
	case "php":
		return astkit.LangPHP, true
	}
	return "", false
}

// ParseTree validates that src is syntactically valid for the given language.
// Returns nil on success, a wrapped error if the language is unsupported or
// the parser reported syntax errors.
func (e *Engine) ParseTree(language string, src []byte) error {
	key, ok := languageToKey(language)
	if !ok {
		return fmt.Errorf("unsupported language: %s", language)
	}
	eng, _ := bridge()
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	tree, err := eng.Parse(ctx, key, src)
	if err != nil {
		return err
	}
	defer tree.Close()
	if tree.RootNode().HasError() {
		return fmt.Errorf("tree-sitter reported syntax errors for %s", language)
	}
	return nil
}

// extractSymbolsFromAST parses src via astkit and projects the resulting
// astkit.Symbol values onto core.SymbolRecord, attaching Grove-only fields.
//
// Returns (nil, false, false) when the language is unsupported (caller falls
// back to regex extraction). When tree-sitter produces a partial parse with
// syntax errors, the extracted symbols are returned with hasErrors=true so the
// caller may merge them with regex-extracted ones.
func extractSymbolsFromAST(language, filePath, blobSHA string, src []byte, fileImports []string) (syms []core.SymbolRecord, ok bool, hasErrors bool) {
	key, supported := languageToKey(language)
	if !supported {
		return nil, false, false
	}
	eng, reg := bridge()
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	tree, err := eng.Parse(ctx, key, src)
	if err != nil {
		return nil, false, false
	}
	defer tree.Close()
	hasErrors = tree.RootNode().HasError()
	akSyms, err := reg.Extract(key, tree, src)
	if err != nil {
		return nil, false, false
	}
	syms = make([]core.SymbolRecord, 0, len(akSyms))
	for _, s := range akSyms {
		syms = append(syms, projectSymbol(s, filePath, blobSHA, language, fileImports))
	}
	return syms, true, hasErrors
}

// projectSymbol converts an astkit.Symbol into a Grove SymbolRecord by adding
// storage-aware identifiers and per-file import context.
func projectSymbol(s astkit.Symbol, filePath, blobSHA, language string, fileImports []string) core.SymbolRecord {
	return core.SymbolRecord{
		ID:             symID(filePath, s.QualifiedName, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       language,
		Kind:           core.SymbolKind(s.Kind),
		Name:           s.Name,
		QualifiedName:  s.QualifiedName,
		ParentSymbol:   s.ParentName,
		Signature:      s.Signature,
		Docstring:      s.Docstring,
		RawText:        s.Body,
		Span:           core.LineRange{Start: s.Span.Start, End: s.Span.End},
		Exports:        s.Exported,
		Imports:        append([]string(nil), fileImports...),
		Modifiers:      append([]string(nil), s.Modifiers...),
		TypeParameters: append([]string(nil), s.TypeParameters...),
		Annotations:    append([]string(nil), s.Annotations...),
		CallSites:      projectCallSites(s.CallSites),
		TokenEstimate:  estimateTokens(s.Body),
	}
}

func projectCallSites(in []astkit.CallSite) []core.CallSite {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.CallSite, len(in))
	for i, c := range in {
		out[i] = core.CallSite{Callee: c.Callee, Line: c.Line}
	}
	return out
}

func symID(filePath, qualifiedName, blobSHA string) string {
	return fmt.Sprintf("%s::%s@%s", filePath, qualifiedName, blobSHA)
}
