// Package eval is Grove's edge-accuracy harness. It scores Grove's graph
// edges against ground truth derived from typed language toolchains
// (go/ssa + VTA for Go), producing precision/recall scorecards per repo.
//
// The harness lives in a nested module so golang.org/x/tools stays out of
// Grove's runtime dependency set.
package eval

// FuncRef identifies one function or method declaration in a repo,
// toolchain-independently: file path relative to the repo root, the
// declaration's starting line, and a display name ("Func" or "Recv.Method").
type FuncRef struct {
	File string `json:"file"`
	Line int    `json:"line"`
	Name string `json:"name"`
}

// TruthEdge is one caller→callee relation asserted by the ground-truth
// toolchain between two in-repo declarations.
type TruthEdge struct {
	Caller FuncRef `json:"caller"`
	Callee FuncRef `json:"callee"`
}

// TruthFile is the JSONL header line. Subsequent lines are TruthEdge values.
type TruthFile struct {
	Schema    string `json:"schema"` // "grove-eval/calls/v1"
	Repo      string `json:"repo"`
	Commit    string `json:"commit,omitempty"`
	Generator string `json:"generator"` // e.g. "go-ssa-vta"
	Functions int    `json:"functions"`
	Edges     int    `json:"edges"`
}

// Scorecard is the result of scoring one repo's Grove edges against truth.
type Scorecard struct {
	Repo            string        `json:"repo"`
	Commit          string        `json:"commit,omitempty"`
	Generator       string        `json:"generator"`
	EdgeType        string        `json:"edgeType"`
	TruthFunctions  int           `json:"truthFunctions"`
	GroveFunctions  int           `json:"groveFunctions"`
	MatchedUniverse int           `json:"matchedUniverse"`
	SymbolMatchRate float64       `json:"symbolMatchRate"`
	TruthEdges      int           `json:"truthEdges"`
	GroveEdges      int           `json:"groveEdges"`
	TruePositives   int           `json:"truePositives"`
	Precision       float64       `json:"precision"`
	Recall          float64       `json:"recall"`
	F1              float64       `json:"f1"`
	FalsePositives  []EdgeExample `json:"falsePositives,omitempty"`
	FalseNegatives  []EdgeExample `json:"falseNegatives,omitempty"`

	// Tests-edge scoring only: function-level coverage signal quality.
	FunctionsCovered int     `json:"functionsCovered,omitempty"`
	FunctionsHit     int     `json:"functionsHit,omitempty"`
	FunctionHitRate  float64 `json:"functionHitRate,omitempty"`
}

// EdgeExample is a human-readable sample of a mismatch, for debugging.
type EdgeExample struct {
	Caller string `json:"caller"`
	Callee string `json:"callee"`
}
