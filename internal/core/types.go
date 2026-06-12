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
	KindDocument    SymbolKind = "document"
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
	From       string         `json:"from"`
	To         string         `json:"to"`
	Type       EdgeType       `json:"type"`
	Confidence float64        `json:"confidence"`
	Source     EvidenceSource `json:"source,omitempty"`
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
	Native       []string `json:"native,omitempty"`
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

// SymbolChange pairs the before/after versions of one logical symbol in a
// graph diff. After is nil for removals; Before is nil for additions when a
// change surfaces in BreakingChanges context.
type SymbolChange struct {
	Before           *SymbolRecord `json:"before,omitempty"`
	After            *SymbolRecord `json:"after,omitempty"`
	SignatureChanged bool          `json:"signatureChanged"`
	BodyChanged      bool          `json:"bodyChanged"`
}

// GraphDiff is the structural delta between two symbol snapshots, keyed by
// stable identity (file path + qualified name + kind) rather than symbol ID,
// so a one-line edit — which changes every ID in the file via the content
// SHA — reports only the symbols whose signature or body actually changed.
type GraphDiff struct {
	Added   []SymbolRecord `json:"added"`
	Removed []SymbolRecord `json:"removed"`
	Changed []SymbolChange `json:"changed"`
	// Renamed pairs a removed symbol with an added one whose body is
	// identical modulo its own name — a rename or a move, not churn. Only
	// unambiguous 1:1 body matches are paired; everything else stays in
	// Added/Removed.
	Renamed []SymbolChange `json:"renamed,omitempty"`
	// BreakingChanges are exported symbols that were removed, renamed, or
	// whose signature changed — the contract surface consumers depend on.
	BreakingChanges []SymbolChange `json:"breakingChanges"`
}

// Empty reports whether the diff carries no structural change.
func (d GraphDiff) Empty() bool {
	return len(d.Added) == 0 && len(d.Removed) == 0 && len(d.Changed) == 0 && len(d.Renamed) == 0
}

type Verdict string

const (
	VerdictAllow        Verdict = "allow"
	VerdictManualReview Verdict = "manual_review"
	VerdictBlock        Verdict = "block"
)

type EvidenceSource string

const (
	EvidenceSourceASTKit     EvidenceSource = "astkit"
	EvidenceSourceTreeSitter EvidenceSource = "tree_sitter"
	EvidenceSourceNative     EvidenceSource = "native"
	EvidenceSourceHeuristic  EvidenceSource = "heuristic"
	EvidenceSourceRegex      EvidenceSource = "regex"
	EvidenceSourceUnknown    EvidenceSource = "unknown"
)

type EvidenceRef struct {
	FilePath   string         `json:"filePath,omitempty"`
	BlobSHA    string         `json:"blobSha,omitempty"`
	Span       LineRange      `json:"span,omitempty"`
	SymbolID   string         `json:"symbolId,omitempty"`
	EdgeID     string         `json:"edgeId,omitempty"`
	Source     EvidenceSource `json:"source"`
	Confidence float64        `json:"confidence"`
	Reason     string         `json:"reason,omitempty"`
}

type FindingSeverity string

const (
	FindingInfo    FindingSeverity = "info"
	FindingWarning FindingSeverity = "warning"
	FindingError   FindingSeverity = "error"
)

type CertificationFinding struct {
	Severity FindingSeverity `json:"severity"`
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Evidence []EvidenceRef   `json:"evidence,omitempty"`
}

type CertificationPolicy struct {
	RequireTestsForCode bool `json:"requireTestsForCode"`
}

type DiffInput struct {
	UnifiedDiff string              `json:"unifiedDiff"`
	BaseRef     string              `json:"baseRef,omitempty"`
	HeadRef     string              `json:"headRef,omitempty"`
	Policy      CertificationPolicy `json:"policy,omitempty"`
}

type CertificationReport struct {
	Version         int                    `json:"version"`
	BaseRef         string                 `json:"baseRef,omitempty"`
	HeadRef         string                 `json:"headRef,omitempty"`
	ChangedFiles    []string               `json:"changedFiles"`
	ChangedSymbols  []SymbolRecord         `json:"changedSymbols"`
	ImpactedSymbols []SymbolRecord         `json:"impactedSymbols"`
	Tests           []SymbolRecord         `json:"tests"`
	Unknowns        []CertificationFinding `json:"unknowns,omitempty"`
	Findings        []CertificationFinding `json:"findings,omitempty"`
	Verdict         Verdict                `json:"verdict"`
}
