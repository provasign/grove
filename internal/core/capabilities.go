package core

// CapabilityManifest is the release-level contract for Grove's semantic
// operations. A tier is a quality statement, not merely a parser-availability
// statement.
type CapabilityManifest struct {
	SchemaVersion string                `json:"schemaVersion"`
	Tiers         map[string]string     `json:"tiers"`
	Languages     []LanguageCapability  `json:"languages"`
	Operations    []OperationCapability `json:"operations"`
}

type LanguageCapability struct {
	Language      string `json:"language"`
	Indexing      string `json:"indexing"`
	Resolution    string `json:"resolution"`
	NativeCeiling string `json:"nativeCeiling,omitempty"`
	Limitations   string `json:"limitations,omitempty"`
}

type OperationCapability struct {
	Operation   string `json:"operation"`
	DefaultTier string `json:"defaultTier"`
	Caveat      string `json:"caveat,omitempty"`
}

// CurrentCapabilities returns a new manifest value so callers can serialize or
// extend it without mutating process-global state.
func CurrentCapabilities() CapabilityManifest {
	return CapabilityManifest{
		SchemaVersion: "1",
		Tiers: map[string]string{
			"precise":     "compiler-resolved or exact evidence with strict fixtures",
			"measured":    "evaluated semantic evidence with published precision and recall",
			"structural":  "syntax and project structure without complete call resolution",
			"heuristic":   "best-effort evidence that authoritative consumers must treat as degraded",
			"unsupported": "no quality claim",
		},
		Languages: []LanguageCapability{
			{Language: "go", Indexing: "precise", Resolution: "measured", NativeCeiling: "precise", Limitations: "precise call resolution requires the native go/types analyzer to complete"},
			{Language: "python", Indexing: "precise", Resolution: "measured", Limitations: "dynamic dispatch and runtime imports remain bounded heuristics"},
			{Language: "javascript", Indexing: "precise", Resolution: "measured", NativeCeiling: "precise", Limitations: "native ceiling requires a project-local TypeScript compiler"},
			{Language: "typescript", Indexing: "precise", Resolution: "measured", NativeCeiling: "precise", Limitations: "native ceiling requires a project-local TypeScript compiler"},
			{Language: "tsx", Indexing: "precise", Resolution: "measured", Limitations: "JSX component usage and path aliases are incomplete"},
			{Language: "java", Indexing: "precise", Resolution: "measured", Limitations: "native analysis is structural, not compiler call resolution"},
			{Language: "rust", Indexing: "precise", Resolution: "measured", Limitations: "cargo metadata is structural, not compiler call resolution"},
			{Language: "c", Indexing: "precise", Resolution: "measured", Limitations: "native analysis is structural unless compiler evidence is available"},
			{Language: "cpp", Indexing: "precise", Resolution: "measured", Limitations: "templates, overloads, and macros can degrade resolution"},
			{Language: "csharp", Indexing: "precise", Resolution: "measured", Limitations: "native analysis is structural without Roslyn-backed resolution"},
			{Language: "php", Indexing: "precise", Resolution: "measured", Limitations: "namespace and Composer scope are not complete"},
			{Language: "plaintext", Indexing: "structural", Resolution: "unsupported", Limitations: "whole-document indexing only"},
		},
		Operations: []OperationCapability{
			{Operation: "symbols", DefaultTier: "precise"},
			{Operation: "dependencies", DefaultTier: "measured"},
			{Operation: "change-impact", DefaultTier: "measured", Caveat: "closed only under the reported graph policy and completed analyzers"},
			{Operation: "rename-plan", DefaultTier: "measured", Caveat: "review ambiguous textual or dynamic sites"},
			{Operation: "test-selection", DefaultTier: "measured", Caveat: "framework and dynamic-dispatch gaps can require broader test execution"},
			{Operation: "dead-code", DefaultTier: "heuristic", Caveat: "reflection, runtime registration, and external entry points require review"},
			{Operation: "certification", DefaultTier: "measured", Caveat: "unsupported or unmapped changes return manual_review"},
		},
	}
}
