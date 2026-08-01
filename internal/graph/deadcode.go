package graph

import (
	"regexp"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// DeadCodeResult reports production functions/methods that nothing reaches.
// Precision-first: a symbol appears in Dead only when it is unreachable from
// every root, is not exported, and its name occurs nowhere in live code text
// (so callback/value references that produce no call edge still keep a
// symbol alive). Anything static analysis cannot decide is bucketed
// separately or excluded, never reported as dead.
type DeadCodeResult struct {
	RootCount      int `json:"rootCount"`      // entry points seeded (mains, inits, tests, exported, extra)
	ReachableCount int `json:"reachableCount"` // symbols reached from the roots
	Considered     int `json:"considered"`     // production functions/methods examined

	// Dead: unreachable, non-exported, name unreferenced anywhere in live
	// code. The deletion-candidate list.
	Dead []core.SymbolRecord `json:"dead"`
	// ExportedUnreferenced: exported symbols with no in-project reference at
	// all (no inbound edge, name unreferenced). Dead only if nothing outside
	// this project links against it — a decision the graph cannot make.
	ExportedUnreferenced []core.SymbolRecord `json:"exportedUnreferenced"`

	// Caveats state what static reachability cannot see. They are part of
	// the result, not documentation: an agent relaying this answer must
	// relay them.
	Caveats []string `json:"caveats"`
}

var deadCodeCaveats = []string{
	"reflection, dependency injection, serialization hooks, and code generation call symbols invisibly — verify before deleting",
	"exported symbols are treated as live roots; for a library, exported liveness is not statically decidable in-project",
	"symbols referenced only from build tags, templates, or embedded assets are not seen by the graph",
}

// implicitNames are member names invoked by the runtime or standard library
// rather than by visible call sites; they are never dead-code candidates.
var implicitNames = map[string]bool{
	"main": true, "init": true,
	"String": true, "Error": true, "GoString": true, // Go fmt/error interfaces
	"MarshalJSON": true, "UnmarshalJSON": true, "MarshalText": true, "UnmarshalText": true,
	"equals": true, "hashCode": true, "toString": true, "clone": true, "finalize": true, // Java Object
	"compareTo": true, "close": true, "run": true, "call": true, // common external contracts
	"constructor": true, // TS/JS
}

var identTokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// DeadCode computes forward reachability from every entry point — main/init
// functions, test symbols, exported symbols, plus extraRoots by name — over
// call, uses-type, and override edges, and reports the production
// functions/methods nothing reaches.
func (g *CodeGraph) DeadCode(extraRoots []string) *DeadCodeResult {
	g.mu.RLock()
	defer g.mu.RUnlock()

	extra := make(map[string]bool, len(extraRoots))
	for _, r := range extraRoots {
		extra[r] = true
	}

	// 1. Roots.
	roots := make(map[string]bool)
	for id, s := range g.symbols {
		switch {
		case s.Name == "main" || s.Name == "init":
			roots[id] = true
		case isTestFilePath(s.FilePath):
			roots[id] = true
		case s.Exports:
			roots[id] = true
		case extra[s.Name] || extra[s.QualifiedName]:
			roots[id] = true
		case s.Language == "python" && strings.HasPrefix(s.Name, "__") && strings.HasSuffix(s.Name, "__"):
			roots[id] = true // dunder methods: runtime-invoked
		}
	}

	// 2. Forward reachability. Call edges already resolve dispatch to
	// concrete implementors; inbound override edges additionally keep
	// implementations of a live contract alive.
	reached := make(map[string]bool, len(roots))
	frontier := make([]string, 0, len(roots))
	for id := range roots {
		reached[id] = true
		frontier = append(frontier, id)
	}
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		for _, ei := range g.outbound[node] {
			edge := g.edges[ei]
			switch edge.Type {
			case core.EdgeCalls, core.EdgeUsesType, core.EdgeOverrides:
				// Test files are roots (isTestFilePath above) and reach the
				// production code they exercise over calls edges, so the
				// removed EdgeTests contributed no additional reachability.
				if !reached[edge.To] {
					reached[edge.To] = true
					frontier = append(frontier, edge.To)
				}
			}
		}
		for _, ei := range g.inbound[node] {
			edge := g.edges[ei]
			if edge.Type == core.EdgeOverrides && !reached[edge.From] {
				reached[edge.From] = true
				frontier = append(frontier, edge.From)
			}
		}
	}

	// 3. Token index over ALL code text (live and dead): how many distinct
	// symbols mention each identifier. A candidate whose name appears in any
	// symbol other than itself is not reported — this catches callbacks and
	// function values that produce no call edge, and it makes every Dead
	// entry safe to delete even when its only mentions are in other dead
	// code (a transitively-dead cluster surfaces top-down across re-runs,
	// never in an order that breaks compilation).
	tokenSymbols := make(map[string]int)
	for _, s := range g.symbols {
		if s.RawText == "" {
			continue
		}
		seen := make(map[string]bool)
		for _, tok := range identTokenRe.FindAllString(s.RawText, -1) {
			if !seen[tok] {
				seen[tok] = true
				tokenSymbols[tok]++
			}
		}
	}
	// A symbol's own RawText mentions its own name once (its declaration);
	// anything beyond that is an outside mention.
	selfMentions := func(s *core.SymbolRecord) int {
		if s.RawText == "" {
			return 0
		}
		return 1
	}

	res := &DeadCodeResult{RootCount: len(roots), ReachableCount: len(reached), Caveats: deadCodeCaveats}

	// 4. Candidates: production functions/methods.
	for id, s := range g.symbols {
		if s.Kind != core.KindFunction && s.Kind != core.KindMethod {
			continue
		}
		if isTestFilePath(s.FilePath) || implicitNames[s.Name] {
			continue
		}
		res.Considered++
		outsideMentions := tokenSymbols[s.Name] - selfMentions(&s)

		if s.Exports {
			// Exported symbols are roots; their own liveness is judged by
			// in-project evidence only.
			if !g.hasInboundReference(id) && outsideMentions <= 0 {
				res.ExportedUnreferenced = append(res.ExportedUnreferenced, s)
			}
			continue
		}
		if reached[id] {
			continue
		}
		if outsideMentions > 0 {
			continue // name-referenced from somewhere: value use, config, dynamic
		}
		res.Dead = append(res.Dead, s)
	}
	sortSymbols(res.Dead)
	sortSymbols(res.ExportedUnreferenced)
	return res
}

// hasInboundReference reports whether any structural edge other than
// contains/defines points at the symbol. Caller must hold g.mu.
func (g *CodeGraph) hasInboundReference(id string) bool {
	for _, ei := range g.inbound[id] {
		switch g.edges[ei].Type {
		case core.EdgeContains, core.EdgeDefines:
		default:
			return true
		}
	}
	return false
}

func isTestFilePath(p string) bool {
	lp := strings.ToLower(p)
	return strings.Contains(lp, "_test.") || strings.Contains(lp, ".test.") ||
		strings.Contains(lp, ".spec.") || strings.Contains(lp, "/__tests__/") ||
		strings.Contains(lp, "/tests/") || strings.Contains(lp, "/test/") ||
		strings.HasPrefix(lp, "tests/") || strings.HasPrefix(lp, "test/") ||
		strings.Contains(lp, "conftest.py") ||
		strings.HasPrefix(fileBase(lp), "test_")
}

func fileBase(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}
