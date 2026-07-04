package astkit

import (
	"context"
	"fmt"
	"sync"
	"time"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/c"
	"github.com/smacker/go-tree-sitter/cpp"
	"github.com/smacker/go-tree-sitter/csharp"
	"github.com/smacker/go-tree-sitter/golang"
	"github.com/smacker/go-tree-sitter/java"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/php"
	"github.com/smacker/go-tree-sitter/python"
	"github.com/smacker/go-tree-sitter/rust"
	tstsx "github.com/smacker/go-tree-sitter/typescript/tsx"
	tstype "github.com/smacker/go-tree-sitter/typescript/typescript"
)

// DefaultParseTimeout caps tree-sitter parse calls. Callers can override per
// Engine via WithTimeout.
const DefaultParseTimeout = 2 * time.Second

// Engine wraps tree-sitter parsing for every language astkit knows about.
// Engines are safe for concurrent use.
type Engine struct {
	timeout time.Duration
}

// NewEngine returns an Engine using DefaultParseTimeout.
func NewEngine() *Engine { return &Engine{timeout: DefaultParseTimeout} }

// WithTimeout returns a copy of e with the parse timeout overridden.
func (e *Engine) WithTimeout(d time.Duration) *Engine {
	cp := *e
	cp.timeout = d
	return &cp
}

// Parse parses src under lang and returns a tree-sitter tree. Caller is
// responsible for tree.Close().
//
// For non-AST languages (config formats, unknown) Parse returns (nil, nil)
// rather than an error — letting callers branch on `tree == nil` without
// inspecting error types.
func (e *Engine) Parse(ctx context.Context, lang LanguageKey, src []byte) (*sitter.Tree, error) {
	tsLang, ok := treeSitterLanguage(lang)
	if !ok {
		return nil, nil
	}
	p := sitter.NewParser()
	p.SetLanguage(tsLang)
	cctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	tree, err := p.ParseCtx(cctx, nil, src)
	if err != nil {
		return nil, fmt.Errorf("astkit: parse %s: %w", lang, err)
	}
	return tree, nil
}

// Validate parses src and immediately discards the tree — useful for input
// validation endpoints.
func (e *Engine) Validate(ctx context.Context, lang LanguageKey, src []byte) error {
	tree, err := e.Parse(ctx, lang, src)
	if tree != nil {
		tree.Close()
	}
	return err
}

// treeSitterLanguage maps a LanguageKey to its tree-sitter grammar.
func treeSitterLanguage(lang LanguageKey) (*sitter.Language, bool) {
	switch lang {
	case LangGo:
		return golang.GetLanguage(), true
	case LangTypeScript:
		return tstype.GetLanguage(), true
	case LangTSX:
		return tstsx.GetLanguage(), true
	case LangJavaScript:
		return javascript.GetLanguage(), true
	case LangPython:
		return python.GetLanguage(), true
	case LangJava:
		return java.GetLanguage(), true
	case LangRust:
		return rust.GetLanguage(), true
	case LangC:
		return c.GetLanguage(), true
	case LangCPP:
		return cpp.GetLanguage(), true
	case LangCSharp:
		return csharp.GetLanguage(), true
	case LangPHP:
		return php.GetLanguage(), true
	default:
		return nil, false
	}
}

// Strategy is implemented by every supported language. A Strategy converts a
// parsed tree-sitter tree into the canonical astkit types.
type Strategy interface {
	Language() LanguageKey
	Extensions() []string
	Extract(tree *sitter.Tree, src []byte) ([]Symbol, error)
	ExtractImports(tree *sitter.Tree, src []byte) ([]ImportStatement, error)
}

// Registry is a thread-safe map of LanguageKey → Strategy.
type Registry struct {
	mu sync.RWMutex
	m  map[LanguageKey]Strategy
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{m: make(map[LanguageKey]Strategy)} }

// Register adds s under its declared language.
func (r *Registry) Register(s Strategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[s.Language()] = s
}

// RegisterAs adds s under an additional key (used for JS/JSX, TS/TSX aliasing).
func (r *Registry) RegisterAs(key LanguageKey, s Strategy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[key] = s
}

// Get returns the strategy for lang, or nil if none registered.
func (r *Registry) Get(lang LanguageKey) Strategy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.m[lang]
}

// Extract is a convenience that looks up the strategy and runs Extract.
// Returns (nil, nil) when no strategy is registered.
func (r *Registry) Extract(lang LanguageKey, tree *sitter.Tree, src []byte) ([]Symbol, error) {
	s := r.Get(lang)
	if s == nil {
		return nil, nil
	}
	return s.Extract(tree, src)
}

// ExtractImports is a convenience analogous to Extract.
func (r *Registry) ExtractImports(lang LanguageKey, tree *sitter.Tree, src []byte) ([]ImportStatement, error) {
	s := r.Get(lang)
	if s == nil {
		return nil, nil
	}
	return s.ExtractImports(tree, src)
}
