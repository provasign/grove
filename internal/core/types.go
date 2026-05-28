package core

type SymbolKind string

const (
	KindFunction    SymbolKind = "function"
	KindMethod      SymbolKind = "method"
	KindConstructor SymbolKind = "constructor"
	KindClass       SymbolKind = "class"
	KindInterface   SymbolKind = "interface"
	KindType        SymbolKind = "type"
	KindConst       SymbolKind = "const"
	KindEnum        SymbolKind = "enum"
	KindModule      SymbolKind = "module"
	KindNamespace   SymbolKind = "namespace"
	KindVariable    SymbolKind = "variable"
	KindField       SymbolKind = "field"
	KindStruct      SymbolKind = "struct"
	KindTrait       SymbolKind = "trait"
	KindDecorator   SymbolKind = "decorator"
	KindAnnotation  SymbolKind = "annotation"
	KindFile        SymbolKind = "file"
)

type LineRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// CallSite records one call expression observed inside a symbol's body.
// AST-extracted call sites enable high-confidence calls edges that do not
// rely on regex/string-stripping heuristics.
type CallSite struct {
	Callee string `json:"callee"` // bare name; receiver-qualified uses "Receiver.callee"
	Line   int    `json:"line"`
}

type SymbolRecord struct {
	ID             string     `json:"id"`
	FilePath       string     `json:"filePath"`
	BlobSHA        string     `json:"blobSha"`
	Language       string     `json:"language"`
	Kind           SymbolKind `json:"kind"`
	Name           string     `json:"name"`
	QualifiedName  string     `json:"qualifiedName"`
	Signature      string     `json:"signature"`
	Docstring      string     `json:"docstring,omitempty"`
	Span           LineRange  `json:"span"`
	Imports        []string   `json:"imports,omitempty"`
	Exports        bool       `json:"exports"`
	RawText        string     `json:"rawText,omitempty"`
	ParentSymbol   string     `json:"parentSymbol,omitempty"`
	TokenEstimate  int        `json:"tokenEstimate"`
	Modifiers      []string   `json:"modifiers,omitempty"`      // public/private/static/async/abstract/pub/...
	TypeParameters []string   `json:"typeParameters,omitempty"` // generics
	Annotations    []string   `json:"annotations,omitempty"`    // @Override, #[derive(...)], decorators
	CallSites      []CallSite `json:"callSites,omitempty"`      // AST-extracted call invocations
}

type EdgeType string

const (
	EdgeDefines    EdgeType = "defines"
	EdgeImports    EdgeType = "imports"
	EdgeCalls      EdgeType = "calls"
	EdgeExtends    EdgeType = "extends"
	EdgeImplements EdgeType = "implements"
	EdgeUsesType   EdgeType = "uses-type"
	EdgeTests      EdgeType = "tests"
	EdgeContains   EdgeType = "contains"
)

type Edge struct {
	From       string   `json:"from"`
	To         string   `json:"to"`
	Type       EdgeType `json:"type"`
	Confidence float64  `json:"confidence"`
}

type Status struct {
	FilesIndexed int `json:"filesIndexed"`
	SymbolCount  int `json:"symbolCount"`
	EdgeCount    int `json:"edgeCount"`
	SkippedFiles int `json:"skippedFiles,omitempty"`
	UpdatedFiles int `json:"updatedFiles,omitempty"`
}

type IndexResult struct {
	Root         string   `json:"root"`
	FilesSeen    int      `json:"filesSeen"`
	FilesUpdated int      `json:"filesUpdated"`
	FilesSkipped int      `json:"filesSkipped"`
	FilesPruned  int      `json:"filesPruned"`
	SymbolCount  int      `json:"symbolCount"`
	EdgeCount    int      `json:"edgeCount"`
	Errors       []string `json:"errors,omitempty"`
}

type IsolatedChangeRegion struct {
	IntentID       string   `json:"intentId"`
	Exclusive      []string `json:"exclusive"`
	SharedRead     []string `json:"sharedRead"`
	Boundary       []string `json:"boundary"`
	ExclusiveFiles []string `json:"exclusiveFiles"`
	ReadableFiles  []string `json:"readableFiles"`
	Confidence     float64  `json:"confidence"`
	LockKeys       []string `json:"lockKeys"`
}

type ConflictResult struct {
	Conflicts      bool     `json:"conflicts"`
	OverlapSymbols []string `json:"overlapSymbols"`
	OverlapFiles   []string `json:"overlapFiles"`
}

type LockRecord struct {
	LockKey    string `json:"lockKey"`
	IntentID   string `json:"intentId"`
	AcquiredAt string `json:"acquiredAt"`
	ExpiresAt  string `json:"expiresAt"`
}
