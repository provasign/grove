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
	"bytes"
	"context"
	"fmt"
	sitter "github.com/smacker/go-tree-sitter"
	"sync"
	"time"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/strategies"
	"github.com/provasign/grove/internal/core"
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
	case "cobol":
		return astkit.LangCOBOL, true
	case "jcl":
		return astkit.LangJCL, true
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
	if tree == nil {
		// No grammar (text-strategy language): nothing to validate.
		return nil
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
	if tree == nil {
		// No grammar for this language. Text-capable strategies (astkit
		// TextStrategy) extract from src alone; everything else has no
		// AST path. Unreachable for grammar-backed languages.
		if !reg.TextCapable(key) {
			return nil, false, false
		}
	} else {
		defer tree.Close()
		hasErrors = tree.RootNode().HasError()
		if hasErrors && preprocessedLanguage(language) {
			// `#if A ... else ... #else ... else ... #endif` hands the
			// grammar two else branches; tree-sitter recovers with ERROR
			// nodes that swallow call sites. Re-parse with the directives
			// blanked and every #else/#elif branch blanked (the #if branch
			// is kept, line numbers untouched); keep it only when it
			// parses cleaner.
			if alt := blankPreprocessorBranches(src); alt != nil {
				if altTree, err := eng.Parse(ctx, key, alt); err == nil && altTree != nil {
					if altErrs := countErrorNodes(altTree.RootNode()); altErrs < countErrorNodes(tree.RootNode()) {
						tree.Close()
						tree, src = altTree, alt
						hasErrors = altErrs > 0
					} else {
						altTree.Close()
					}
				}
			}
		}
	}
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

// extractImportsFromAST parses imports through astkit when a strategy exists.
// Regex extraction remains the fallback for unsupported languages or parse
// failures.
func extractImportsFromAST(language string, src []byte) ([]string, bool) {
	key, supported := languageToKey(language)
	if !supported {
		return nil, false
	}
	eng, reg := bridge()
	ctx, cancel := context.WithTimeout(context.Background(), parseTimeout)
	defer cancel()
	tree, err := eng.Parse(ctx, key, src)
	if err != nil || (tree == nil && !reg.TextCapable(key)) {
		return nil, false
	}
	if tree != nil {
		defer tree.Close()
	}
	akImports, err := reg.ExtractImports(key, tree, src)
	if err != nil {
		return nil, false
	}
	seen := make(map[string]bool, len(akImports))
	imports := make([]string, 0, len(akImports))
	for _, imp := range akImports {
		if imp.Path == "" || seen[imp.Path] {
			continue
		}
		seen[imp.Path] = true
		imports = append(imports, imp.Path)
	}
	if key == astkit.LangPython {
		// From-import members as "module#name" candidates: a member that
		// resolves to a file is a submodule import (`from . import cli`),
		// which the edge index rewrites into a plain module path; the rest
		// are dropped there. The "#" form never reaches consumers.
		for _, imp := range akImports {
			for _, name := range imp.Names {
				cand := imp.Path + "#" + name
				if imp.Path == "" || seen[cand] {
					continue
				}
				seen[cand] = true
				imports = append(imports, cand)
			}
		}
	}
	return imports, true
}

// projectSymbol converts an astkit.Symbol into a Grove SymbolRecord by adding
// storage-aware identifiers and per-file import context.
//
// astkit reports QualifiedName as the bare member name; Grove qualifies it
// with the parent (receiver/class) so that two same-named members in one file
// (e.g. `(*A).Close` and `(*B).Close`, or `__init__` on two classes) produce
// distinct symbol IDs instead of silently collapsing into one record.
func projectSymbol(s astkit.Symbol, filePath, blobSHA, language string, fileImports []string) core.SymbolRecord {
	qualifiedName := s.QualifiedName
	if s.ParentName != "" && qualifiedName == s.Name {
		qualifiedName = s.ParentName + "." + s.Name
	}
	return core.SymbolRecord{
		ID:             symID(filePath, qualifiedName, blobSHA),
		FilePath:       filePath,
		BlobSHA:        blobSHA,
		Language:       language,
		Kind:           core.SymbolKind(s.Kind),
		Name:           s.Name,
		QualifiedName:  qualifiedName,
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
		AttrSites:      projectCallSites(s.AttrSites),
		TokenEstimate:  estimateTokens(s.Body),
	}
}

func projectCallSites(in []astkit.CallSite) []core.CallSite {
	if len(in) == 0 {
		return nil
	}
	out := make([]core.CallSite, len(in))
	for i, c := range in {
		out[i] = core.CallSite{Callee: c.Callee, Line: c.Line, Argc: c.Argc, Args: c.Args, Generic: c.Generic}
	}
	return out
}

func symID(filePath, qualifiedName, blobSHA string) string {
	return fmt.Sprintf("%s::%s@%s", filePath, qualifiedName, blobSHA)
}

func preprocessedLanguage(language string) bool {
	return language == "csharp" || language == "c" || language == "cpp"
}

// blankPreprocessorBranches returns src with every preprocessor directive
// line and every #else/#elif branch replaced by blank lines (byte offsets
// change, line numbers do not), or nil when src has no #if.
func blankPreprocessorBranches(src []byte) []byte {
	if !bytes.Contains(src, []byte("#if")) {
		return nil
	}
	lines := bytes.Split(src, []byte("\n"))
	out := make([][]byte, len(lines))
	var skipping []bool // per nesting level: are we inside a skipped branch
	for i, line := range lines {
		t := bytes.TrimSpace(line)
		switch {
		case bytes.HasPrefix(t, []byte("#if")):
			skipping = append(skipping, false)
			out[i] = nil
			continue
		case bytes.HasPrefix(t, []byte("#elif")), bytes.HasPrefix(t, []byte("#else")):
			if len(skipping) > 0 {
				skipping[len(skipping)-1] = true
			}
			out[i] = nil
			continue
		case bytes.HasPrefix(t, []byte("#endif")):
			if len(skipping) > 0 {
				skipping = skipping[:len(skipping)-1]
			}
			out[i] = nil
			continue
		}
		skip := false
		for _, s := range skipping {
			if s {
				skip = true
				break
			}
		}
		if skip {
			out[i] = nil
		} else {
			out[i] = line
		}
	}
	return bytes.Join(out, []byte("\n"))
}

func countErrorNodes(n *sitter.Node) int {
	if n == nil {
		return 0
	}
	c := 0
	if n.Type() == "ERROR" || n.IsMissing() {
		c++
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		c += countErrorNodes(n.Child(i))
	}
	return c
}
