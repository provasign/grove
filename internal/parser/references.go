package parser

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

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
//
// `name` may be a bare name ("ValidateToken") or a qualified one
// ("auth.Service.ValidateToken", "pkg::Type::method") — references position is
// always the bare leaf identifier, so the qualifier is stripped to the last
// segment before matching. A byte-level substring pre-filter skips files that
// cannot contain the name without parsing them, which is what keeps the query
// fast on large trees (only the handful of files mentioning the name are
// parsed, not the whole repo).
func (e *Engine) References(root, name string) (ReferenceResult, error) {
	res := ReferenceResult{Name: name}
	leaf := leafName(name)
	leafBytes := []byte(leaf)

	// Phase 1 (serial walk): collect candidate files, skipping dependency/
	// cache/VCS dirs so references report the user's code, not vendored or
	// downloaded sources (matches the indexer's ignore policy).
	type task struct {
		abs, rel string
		lang     astkit.LanguageKey
	}
	var tasks []task
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if p != root && refSkipDir(info.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if lang, ok := detectRefLang(p); ok {
			tasks = append(tasks, task{abs: p, rel: relPath(root, p), lang: lang})
		}
		return nil
	})

	// Phase 2 (parallel): read + parse the candidate files. Most files don't
	// mention the name, so a byte-level substring pre-filter skips parsing them
	// entirely; the remainder are parsed concurrently (astkit engines are safe
	// for concurrent use, as the indexer relies on). Outcomes land by index so
	// the merge below stays in deterministic walk order.
	type outcome struct {
		refs []Reference
		defs int
	}
	outcomes := make([]outcome, len(tasks))
	if len(tasks) > 0 {
		workers := runtime.GOMAXPROCS(0)
		if workers > 8 {
			workers = 8
		}
		if workers > len(tasks) {
			workers = len(tasks)
		}
		taskCh := make(chan int)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				eng := astkit.NewEngine()
				for idx := range taskCh {
					t := tasks[idx]
					src, rerr := os.ReadFile(t.abs)
					if rerr != nil || !bytes.Contains(src, leafBytes) {
						continue
					}
					// Definitions named `leaf` here, and spans for enclosing.
					syms, _ := e.ExtractFile(t.abs, root)
					defs := 0
					for i := range syms {
						if syms[i].Name == leaf {
							defs++
						}
					}
					tree, perr := eng.Parse(context.Background(), t.lang, src)
					if perr != nil || tree == nil {
						outcomes[idx] = outcome{defs: defs}
						continue
					}
					var refs []Reference
					walkRefs(tree.RootNode(), src, leaf, t.rel, syms, &refs)
					outcomes[idx] = outcome{refs: refs, defs: defs}
				}
			}()
		}
		for idx := range tasks {
			taskCh <- idx
		}
		close(taskCh)
		wg.Wait()
	}

	for _, o := range outcomes {
		res.DefCount += o.defs
		res.Refs = append(res.Refs, o.refs...)
	}
	res.Ambiguous = res.DefCount > 1
	return res, err
}

// leafName strips a qualifier to the bare identifier that appears in reference
// position: "auth.Service.ValidateToken" -> "ValidateToken",
// "pkg::Type::method" -> "method". A trailing separator or an already-bare name
// is returned unchanged.
func leafName(name string) string {
	if i := strings.LastIndexAny(name, ".:#\\/"); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
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

// refSkipDir mirrors the indexer's shouldSkipDirName policy (internal/index/
// ignore.go) so References walks the same file set the index does. Kept as a
// local copy rather than an import to avoid an internal/index ↔ internal/parser
// dependency.
func refSkipDir(name string) bool {
	switch name {
	case ".git", ".grove", ".cache", ".venv", ".tox", ".next", ".idea",
		"node_modules", "vendor", "dist", "bin", "__pycache__", "target",
		"coverage", ".pytest_cache", ".mypy_cache", ".ruff_cache":
		return true
	default:
		return false
	}
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
