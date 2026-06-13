package parser

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/provasign/astkit"
	"github.com/provasign/grove/internal/core"
	sitter "github.com/smacker/go-tree-sitter"
)

// Reference is one code occurrence of a symbol's name in reference position.
type Reference struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Enclosing string `json:"enclosing,omitempty"` // nearest enclosing symbol, if any
}

// ReferenceResult answers "where is NAME used?" by name across the repo — the
// resolution-free reference layer. It is near-complete by construction (every
// code occurrence of the name), and far more complete than the resolved call
// graph for types/classes/constants, which calls edges never capture (a class
// is referenced, not called). DefCount lets the caller tier the answer:
// exactly one defined symbol with the name => Unambiguous (the references are
// definitely to it); several => Ambiguous (references are to *some* of them).
type ReferenceResult struct {
	Name      string      `json:"name"`
	DefCount  int         `json:"defCount"`
	Ambiguous bool        `json:"ambiguous"`
	Refs      []Reference `json:"refs"`
}

// referenceNodeTypes are the tree-sitter node kinds that hold a name in
// reference position across the supported grammars.
var referenceNodeTypes = map[string]bool{
	"identifier": true, "type_identifier": true, "field_identifier": true,
	"name": true, // php
}

// References returns the code references to a symbol name across root. It parses
// each file (so it is grep-with-syntax: comments, strings and javadoc are
// excluded, unlike textual grep) and attributes each occurrence to its nearest
// enclosing symbol. It deliberately does NOT resolve which overload/definition
// a reference binds to — it reports completeness with an ambiguity flag.
func (e *Engine) References(root, name string) (ReferenceResult, error) {
	res := ReferenceResult{Name: name}
	eng := astkit.NewEngine()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		lang, ok := detectRefLang(p)
		if !ok {
			return nil
		}
		src, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		// Definitions in this file named `name`, and symbol spans for enclosing.
		syms, _ := e.ExtractFile(p, root)
		for i := range syms {
			if syms[i].Name == name {
				res.DefCount++
			}
		}
		tree, perr := eng.Parse(context.Background(), lang, src)
		if perr != nil || tree == nil {
			return nil
		}
		rel := relPath(root, p)
		walkRefs(tree.RootNode(), src, name, rel, syms, &res.Refs)
		return nil
	})
	res.Ambiguous = res.DefCount > 1
	return res, err
}

func walkRefs(n *sitter.Node, src []byte, name, file string, syms []core.SymbolRecord, out *[]Reference) {
	if n == nil {
		return
	}
	if referenceNodeTypes[n.Type()] && string(n.Content(src)) == name {
		line := int(n.StartPoint().Row) + 1
		*out = append(*out, Reference{File: file, Line: line, Enclosing: enclosingSymbol(syms, line)})
	}
	for i := 0; i < int(n.ChildCount()); i++ {
		walkRefs(n.Child(i), src, name, file, syms, out)
	}
}

// enclosingSymbol returns the tightest symbol whose span contains line.
func enclosingSymbol(syms []core.SymbolRecord, line int) string {
	best := ""
	bestSpan := 1 << 30
	for i := range syms {
		s := &syms[i]
		if s.Span.Start <= line && line <= s.Span.End {
			if span := s.Span.End - s.Span.Start; span < bestSpan {
				bestSpan, best = span, s.QualifiedName
			}
		}
	}
	return best
}

func detectRefLang(path string) (astkit.LanguageKey, bool) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return astkit.LangGo, true
	case ".java":
		return astkit.LangJava, true
	case ".ts":
		return astkit.LangTypeScript, true
	case ".tsx":
		return astkit.LangTSX, true
	case ".js", ".cjs", ".mjs", ".jsx":
		return astkit.LangJavaScript, true
	case ".py":
		return astkit.LangPython, true
	case ".rs":
		return astkit.LangRust, true
	case ".cs":
		return astkit.LangCSharp, true
	case ".php":
		return astkit.LangPHP, true
	case ".c", ".h":
		return astkit.LangC, true
	case ".cc", ".cpp", ".cxx", ".hpp":
		return astkit.LangCPP, true
	}
	return "", false
}

func relPath(root, p string) string {
	if r, err := filepath.Rel(root, p); err == nil {
		return r
	}
	return p
}
