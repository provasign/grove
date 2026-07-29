package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/rand"
	"sort"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// ─── The incremental-vs-full equivalence harness ─────────────────────────────
//
// The oracle is BuildEdges (a pure, deterministic function of the symbol set —
// pinned by determinism_test.go). Any incremental rebuild strategy must
// produce a byte-identical edge set after every edit in any edit sequence.
// The harness applies seeded random edit sequences to a synthetic multi-file
// corpus and checks the candidate builder against the oracle after each step.

// edgeSetHash canonicalizes an edge set (sorted by every field) and hashes it,
// so two edge slices compare equal iff they contain the same edges regardless
// of order.
func edgeSetHash(edges []core.Edge) string {
	lines := make([]string, 0, len(edges))
	for _, e := range edges {
		lines = append(lines, fmt.Sprintf("%s|%s|%s|%.6f|%s|%s", e.From, e.To, e.Type, e.Confidence, e.Source, e.Reason))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// deltaBuilder is the contract an incremental edge-construction strategy must
// satisfy: given the previous state and the new full symbol set plus the
// changed-file delta, produce the new edge set.
type deltaBuilder func(prevEdges []core.Edge, prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool) []core.Edge

// fullRebuild is the trivial (always-correct) strategy the harness validates
// itself with; incremental strategies are checked against the same oracle.
func fullRebuild(_ []core.Edge, _, symbols []core.SymbolRecord, _ map[string]bool) []core.Edge {
	return BuildEdges(symbols)
}

// ─── Synthetic corpus ────────────────────────────────────────────────────────
//
// A deterministic multi-file Go-flavored corpus: types with methods,
// interfaces, cross-file callers resolved by name, and test files whose
// closure exercises the tests BFS. Symbols are regenerated from the model on
// every step, so an edit is a model mutation + regeneration — exactly what a
// reindex produces.

type synthFile struct {
	path  string
	funcs []synthFunc
	// rev models the file blob SHA: every mutation of the file bumps it,
	// which turns over the IDs of ALL its symbols — exactly what a real
	// reindex does. A pure comment edit bumps rev and nothing else.
	rev int
}

type synthFunc struct {
	name    string
	recv    string   // receiver type; "" for free function
	calls   []string // callee names (bare or recv.method)
	imports []string
}

type synthCorpus struct {
	files map[string]*synthFile
	rng   *rand.Rand
	seq   int
}

// Corpus dimensions. Large enough that a single-file edit (plus its package
// and name-matched callers) stays well under maxAffectedFraction — mirroring
// the real-monorepo geometry the incremental path targets — while common-name
// edits still legitimately trip the fallback.
const (
	synthProdFiles = 30
	synthDirs      = 10
	synthFuncsPer  = 4
	synthTestFiles = 6
)

func newSynthCorpus(seed int64) *synthCorpus {
	c := &synthCorpus{files: map[string]*synthFile{}, rng: rand.New(rand.NewSource(seed))}
	for i := 0; i < synthProdFiles; i++ {
		p := fmt.Sprintf("pkg%d/file%d.go", i%synthDirs, i)
		c.files[p] = &synthFile{path: p}
		for j := 0; j < synthFuncsPer; j++ {
			c.files[p].funcs = append(c.files[p].funcs, synthFunc{
				name:  fmt.Sprintf("Fn%d_%d", i, j),
				calls: []string{fmt.Sprintf("Fn%d_%d", (i+1)%synthProdFiles, j)},
			})
		}
	}
	for i := 0; i < synthTestFiles; i++ {
		p := fmt.Sprintf("pkg%d/file%d_test.go", i%synthDirs, i)
		c.files[p] = &synthFile{path: p}
		c.files[p].funcs = append(c.files[p].funcs, synthFunc{
			name:  fmt.Sprintf("TestFn%d_0", i),
			calls: []string{fmt.Sprintf("Fn%d_0", i)},
		})
	}
	return c
}

// symbols regenerates the full symbol slice from the model, deterministically.
func (c *synthCorpus) symbols() []core.SymbolRecord {
	paths := make([]string, 0, len(c.files))
	for p := range c.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []core.SymbolRecord
	for _, p := range paths {
		f := c.files[p]
		for i, fn := range f.funcs {
			var sites []core.CallSite
			raw := "func " + fn.name + "() {"
			for _, callee := range fn.calls {
				sites = append(sites, core.CallSite{Callee: callee, Line: i + 1, Argc: 0})
				raw += " " + callee + "()"
			}
			raw += " }"
			out = append(out, core.SymbolRecord{
				ID:            fmt.Sprintf("%s::%s@r%d_%d", f.path, fn.name, f.rev, i),
				FilePath:      f.path,
				Language:      "go",
				Kind:          core.KindFunction,
				Name:          fn.name,
				QualifiedName: fn.name,
				ParentSymbol:  fn.recv,
				RawText:       raw,
				CallSites:     sites,
				Imports:       fn.imports,
				Span:          core.LineRange{Start: i * 10, End: i*10 + 5},
			})
		}
	}
	return out
}

// mutate applies one random edit and returns the set of changed file paths.
func (c *synthCorpus) mutate() map[string]bool {
	c.seq++
	paths := make([]string, 0, len(c.files))
	for p := range c.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	p := paths[c.rng.Intn(len(paths))]
	f := c.files[p]
	f.rev++ // every touch turns over the file's symbol IDs
	changed := map[string]bool{p: true}

	switch c.rng.Intn(6) {
	case 0: // add a function that calls an existing name
		target := paths[c.rng.Intn(len(paths))]
		tf := c.files[target]
		callee := ""
		if len(tf.funcs) > 0 {
			callee = tf.funcs[c.rng.Intn(len(tf.funcs))].name
		}
		f.funcs = append(f.funcs, synthFunc{
			name:  fmt.Sprintf("Added%d", c.seq),
			calls: []string{callee},
		})
	case 1: // remove a function (leave at least one per file)
		if len(f.funcs) > 1 {
			f.funcs = f.funcs[:len(f.funcs)-1]
		}
	case 2: // rename a function — cross-file name-resolution invalidation
		if len(f.funcs) > 0 {
			i := c.rng.Intn(len(f.funcs))
			f.funcs[i].name = fmt.Sprintf("Renamed%d_%d", c.seq, i)
		}
	case 3: // rewire a call site to a random existing name
		if len(f.funcs) > 0 {
			i := c.rng.Intn(len(f.funcs))
			target := paths[c.rng.Intn(len(paths))]
			tf := c.files[target]
			if len(tf.funcs) > 0 {
				f.funcs[i].calls = []string{tf.funcs[c.rng.Intn(len(tf.funcs))].name}
			}
		}
	case 4: // add a new test that calls a random production function
		tp := fmt.Sprintf("pkg%d/gen%d_test.go", c.rng.Intn(3), c.seq)
		target := paths[c.rng.Intn(len(paths))]
		tf := c.files[target]
		callee := ""
		if len(tf.funcs) > 0 {
			callee = tf.funcs[c.rng.Intn(len(tf.funcs))].name
		}
		c.files[tp] = &synthFile{path: tp, funcs: []synthFunc{{
			name:  fmt.Sprintf("TestGen%d", c.seq),
			calls: []string{callee},
		}}}
		changed[tp] = true
	case 5: // pure comment edit: rev bump only — the common agent edit; a
		// correct incremental builder should do (almost) no re-resolution
	}
	return changed
}

// runEquivalence drives a candidate builder through nSteps random edits per
// seed and asserts hash equality with the oracle after every step.
func runEquivalence(t *testing.T, build deltaBuilder, seeds []int64, nSteps int) {
	t.Helper()
	for _, seed := range seeds {
		c := newSynthCorpus(seed)
		prevSymbols := c.symbols()
		prevEdges := BuildEdges(prevSymbols)
		for step := 0; step < nSteps; step++ {
			changed := c.mutate()
			symbols := c.symbols()
			got := build(prevEdges, prevSymbols, symbols, changed)
			want := BuildEdges(symbols)
			if gh, wh := edgeSetHash(got), edgeSetHash(want); gh != wh {
				t.Fatalf("seed %d step %d (changed %v): incremental edge set diverges from full rebuild\nincremental %d edges (%s)\nfull        %d edges (%s)",
					seed, step, changed, len(got), gh[:12], len(want), wh[:12])
			}
			prevSymbols, prevEdges = symbols, want
		}
	}
}

// TestEquivalenceHarnessSelfCheck validates the harness itself: the trivial
// full-rebuild strategy must pass, and the oracle must be pure (two calls on
// identical input hash identically) at every step.
func TestEquivalenceHarnessSelfCheck(t *testing.T) {
	c := newSynthCorpus(1)
	s := c.symbols()
	if edgeSetHash(BuildEdges(s)) != edgeSetHash(BuildEdges(s)) {
		t.Fatal("oracle is not pure: two BuildEdges calls on identical input differ")
	}
	runEquivalence(t, fullRebuild, []int64{1, 2, 3}, 15)
}

// TestEquivalenceHarnessCatchesUnsoundBuilder proves the harness has teeth:
// a builder that never recomputes anything must be caught within a short
// edit sequence.
func TestEquivalenceHarnessCatchesUnsoundBuilder(t *testing.T) {
	lazy := func(prevEdges []core.Edge, _, _ []core.SymbolRecord, _ map[string]bool) []core.Edge {
		return prevEdges
	}
	caught := false
	for _, seed := range []int64{1, 2, 3} {
		c := newSynthCorpus(seed)
		prevSymbols := c.symbols()
		prevEdges := BuildEdges(prevSymbols)
		for step := 0; step < 10 && !caught; step++ {
			changed := c.mutate()
			symbols := c.symbols()
			got := lazy(prevEdges, prevSymbols, symbols, changed)
			want := BuildEdges(symbols)
			if edgeSetHash(got) != edgeSetHash(want) {
				caught = true
			}
			prevSymbols, prevEdges = symbols, want
		}
	}
	if !caught {
		t.Fatal("harness failed to catch a builder that never recomputes — edit generator too weak")
	}
}

// TestBuildEdgesDeltaEquivalence is the core correctness gate of the
// incremental re-architecture: across randomized edit sequences,
// BuildEdgesDelta must produce the exact BuildEdges multiset every step —
// and the incremental path must actually execute (a builder that always
// falls back to full rebuild would pass equivalence vacuously).
func TestBuildEdgesDeltaEquivalence(t *testing.T) {
	deltaStats.incremental, deltaStats.fallback = 0, 0
	delta := func(prevEdges []core.Edge, prevSymbols, symbols []core.SymbolRecord, changedFiles map[string]bool) []core.Edge {
		return BuildEdgesDelta(prevEdges, prevSymbols, symbols, changedFiles, nil)
	}
	runEquivalence(t, delta, []int64{1, 2, 3, 4, 5, 6, 7, 8}, 30)
	t.Logf("delta paths: incremental=%d fallback=%d", deltaStats.incremental, deltaStats.fallback)
	if deltaStats.incremental == 0 {
		t.Fatal("BuildEdgesDelta never took the incremental path — equivalence gate is vacuous")
	}
	if deltaStats.incremental < deltaStats.fallback {
		t.Fatalf("incremental path is the minority (%d vs %d fallbacks) — affected-set rules too loose for the synthetic corpus",
			deltaStats.incremental, deltaStats.fallback)
	}
}

// TestAffectedCallersIsScoped pins that the affected set is a strict subset
// on a localized edit: a caller in another package that neither imports the
// changed file's package nor references any changed name must NOT be
// re-resolved.
func TestAffectedCallersIsScoped(t *testing.T) {
	c := newSynthCorpus(42)
	symbols := c.symbols()
	changed := map[string]bool{"pkg0/file0.go": true}
	nameDelta := map[string]bool{}
	for i := range symbols {
		if changed[symbols[i].FilePath] {
			nameDelta[toLowerName(symbols[i].Name)] = true
		}
	}
	idx := newEdgeIndex(symbols)
	affected, ok := affectedCallers(idx, symbols, changed, nameDelta)
	if !ok {
		t.Fatal("affectedCallers refused to scope")
	}
	// pkg1/file1.go defines Fn1_* and calls Fn2_* — different package dir,
	// no imports, no reference to any Fn0_* name.
	for i := range symbols {
		s := &symbols[i]
		if s.FilePath == "pkg1/file1.go" && affected[s.ID] {
			t.Fatalf("unrelated caller %s wrongly affected by a pkg0/file0.go edit", s.ID)
		}
		if s.FilePath == "pkg0/file0.go" && !affected[s.ID] {
			t.Fatalf("changed-file symbol %s not affected", s.ID)
		}
	}
}

func toLowerName(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= 'A' && r <= 'Z' {
			out[i] = r + 32
		}
	}
	return string(out)
}
