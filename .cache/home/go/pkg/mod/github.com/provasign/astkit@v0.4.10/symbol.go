package astkit

// SymbolKind enumerates the kinds of declarations astkit recognizes.
// New kinds may be added; consumers should treat unknown kinds as "other".
type SymbolKind string

const (
	KindFunction    SymbolKind = "function"
	KindMethod      SymbolKind = "method"
	KindConstructor SymbolKind = "constructor"
	KindClass       SymbolKind = "class"
	KindInterface   SymbolKind = "interface"
	KindStruct      SymbolKind = "struct"
	KindEnum        SymbolKind = "enum"
	KindTrait       SymbolKind = "trait"
	KindType        SymbolKind = "type"
	KindConst       SymbolKind = "const"
	KindVariable    SymbolKind = "variable"
	KindField       SymbolKind = "field"
	KindModule      SymbolKind = "module"
	KindNamespace   SymbolKind = "namespace"
	KindDecorator   SymbolKind = "decorator"
	KindAnnotation  SymbolKind = "annotation"
	KindFile        SymbolKind = "file"
	KindOther       SymbolKind = "other"
)

// KindVar is a historical alias for KindVariable. New code should prefer KindVariable.
const KindVar = KindVariable

// LineRange is a 1-indexed inclusive line span within a source file.
type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CallSite records one call expression observed inside a symbol's body.
// Receiver-qualified calls use "Receiver.callee"; macros end in "!".
type CallSite struct {
	Callee string `json:"callee"`
	Line   int    `json:"line"`
	// Argc is the number of argument expressions at the call site (0 may
	// mean "no arguments" or "unknown" for older indexes; consumers treat
	// it as advisory). Lets graph consumers disambiguate overloads.
	Argc int `json:"argc,omitempty"`
	// Args holds each argument's identifier when the argument is a bare
	// identifier, "" otherwise. With local type inference downstream, this
	// disambiguates same-arity overloads (isEmpty(char[]) vs isEmpty(long[])).
	Args []string `json:"args,omitempty"`
}

// Symbol is the canonical, persistence-agnostic representation of a single
// declaration extracted from source. It is intentionally a superset of what
// any single consumer needs:
//
//   - Grove projects Symbol → SymbolRecord (adding ID/BlobSHA/FilePath/Language
//     and the file's import list; usually retaining Body as RawText).
//   - Fuse projects Symbol → SymbolData (deriving a per-file Key from
//     QualifiedName, preserving Body for merge reconstruction).
//
// Producers must at minimum set Kind and Name.
type Symbol struct {
	// Kind classifies the declaration (function, class, etc.).
	Kind SymbolKind `json:"kind"`

	// Name is the bare identifier (e.g. "Bar" for method Foo.Bar).
	Name string `json:"name"`

	// QualifiedName disambiguates within a file (e.g. "Foo.Bar" for methods
	// of class Foo). For top-level symbols it equals Name.
	QualifiedName string `json:"qualifiedName"`

	// ParentName is the immediate enclosing type/class name, if any.
	ParentName string `json:"parentName,omitempty"`

	// Signature is the declaration text without a body (best-effort).
	Signature string `json:"signature"`

	// Docstring is the language-idiomatic documentation comment immediately
	// preceding the declaration, if any. Producers MAY leave this empty and
	// let downstream consumers attach it from raw source.
	Docstring string `json:"docstring,omitempty"`

	// Body is the full source text covering the declaration including its
	// body. Required by consumers that need to reconstruct merged source
	// (Fuse). Grove may drop this before persisting.
	Body string `json:"body,omitempty"`

	// Span is the 1-indexed inclusive line range covered by Body.
	Span LineRange `json:"span"`

	// Exported reports whether the declaration is visible outside its
	// defining unit (file/module/package), per language semantics.
	Exported bool `json:"exported"`

	// Modifiers lists language-idiomatic modifier keywords (public, static,
	// async, pub, etc.).
	Modifiers []string `json:"modifiers,omitempty"`

	// TypeParameters lists generic type parameters declared on the symbol
	// (e.g. ["T", "U"] for `func F[T any, U Comparable]`).
	TypeParameters []string `json:"typeParameters,omitempty"`

	// Annotations lists raw annotation/decorator/attribute text attached to
	// the symbol (Java @Override, Rust #[derive(...)], Python @cache, …).
	Annotations []string `json:"annotations,omitempty"`

	// CallSites records every call expression observed inside Body, when the
	// strategy supports call-site extraction.
	CallSites []CallSite `json:"callSites,omitempty"`

	// AttrSites records attribute accesses that are NOT call expressions
	// ("x.blueprints" with no parens). Python-only today: these are how
	// @property code executes, so graph consumers need them to model
	// property calls. Same shape as CallSites ("qualifier.name").
	AttrSites []CallSite `json:"attrSites,omitempty"`
}

// ImportStatement is one parsed import / use / require clause.
type ImportStatement struct {
	// Raw is the original source text of the clause.
	Raw string `json:"raw"`

	// Path is the imported module/package path.
	Path string `json:"path"`

	// Alias is the local binding name, if the import was aliased.
	Alias string `json:"alias,omitempty"`

	// Group classifies the import as stdlib | external | relative when known.
	Group string `json:"group,omitempty"`

	// Line is the 1-indexed source line of the clause.
	Line int `json:"line"`
}

// Export is a declared public surface entry. Some consumers compute this from
// Symbol.Exported; others need it explicit (e.g. `export {x}` re-exports in
// TS/JS).
type Export struct {
	Name string     `json:"name"`
	Kind SymbolKind `json:"kind"`
}
