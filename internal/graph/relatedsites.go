package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// RelatedSite is a bounded, informational pointer that is NOT part of a
// change set: a place that probably needs the same edit, found by a
// different relation than "calls the changed member". Sites() never
// includes these, so completeness and relay semantics are unchanged.
//
// Why it exists (2026-09-26 review of failed benchmark tasks):
//   - jackson-databind pr6061: the fix changed writeBinary, which reaches a
//     POJO-node producer (TreeWriteContext.writePOJO). Two other gold sites
//     were the OTHER callers of that producer in the same file
//     (writePOJO's byte[] branch, writeEmbeddedObject). change_impact on
//     writeBinary listed only its upstream callers, so no agent saw them.
//   - jackson-databind pr5977: three of the container serializers'
//     createContextual overrides call the shared helper
//     _hasDynamicTypingOverride; ObjectArraySerializer's does not, and that
//     gap was the whole bug.
type RelatedSite struct {
	Symbol core.SymbolRecord
	// Relation: "co-caller" (also calls something the target calls),
	// "peer-lacks" (a same-named override in the same hierarchy that does
	// NOT call a helper the target and other peers call), or
	// "target-lacks" (the target does not call a helper its peers call).
	Relation string
	// Via is the qualified name of the shared callee or helper.
	Via string
	// Detail is a one-line explanation.
	Detail string
}

const (
	maxRelatedSites     = 8
	maxRelatedGapSites  = 3
	maxRelatedCoCallers = 5
	// A callee with more callers than this is a general utility; its other
	// callers are not "the same effect" in any useful sense.
	maxCoCalleeFanIn = 12
	// Peer groups larger than this are framework-wide contracts
	// (ValueSerializer.createContextual has ~40 overrides): a helper most
	// of them skip is normal, not a gap.
	maxPeerGroup     = 30
	maxAncestorDepth = 4
	maxDescendants   = 400
)

// RelatedSites computes the bounded related-but-not-affected group for a
// method change-impact result. It returns nil for data-member and type-level
// results. Deterministic: every map walk is sorted.
func (g *CodeGraph) RelatedSites(r *ChangeImpactResult) []RelatedSite {
	if r == nil || r.MemberKind != "" || r.Completeness == "type-level" {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()

	inSet := map[string]bool{}
	for _, group := range [][]core.SymbolRecord{r.Declarations, r.Family, r.Supers, r.Callers} {
		for _, s := range group {
			inSet[s.ID] = true
		}
	}
	var targets []core.SymbolRecord
	for _, d := range r.Declarations {
		s, ok := g.symbols[d.ID]
		if !ok || !relatedCallable(s.Kind) {
			continue // synthesized interface member or non-callable anchor
		}
		targets = append(targets, s)
	}
	if len(targets) == 0 {
		return nil
	}
	sortSymbols(targets)

	seen := map[string]bool{}
	var out []RelatedSite
	add := func(site RelatedSite) bool {
		if seen[site.Symbol.ID] || len(out) >= maxRelatedSites {
			return false
		}
		seen[site.Symbol.ID] = true
		out = append(out, site)
		return true
	}
	for _, gap := range g.siblingGapsLocked(targets, inSet) {
		add(gap)
	}
	coCallers := 0
	for _, co := range g.coCallersLocked(targets, inSet) {
		if coCallers >= maxRelatedCoCallers {
			break
		}
		if add(co) {
			coCallers++
		}
	}
	return out
}

func relatedCallable(k core.SymbolKind) bool {
	return k == core.KindMethod || k == core.KindFunction || k == core.KindConstructor
}

// callEdgeTargets returns the distinct callable symbols id calls, sorted.
func (g *CodeGraph) callEdgeTargets(id string) []string {
	set := map[string]bool{}
	for _, ei := range g.outbound[id] {
		e := g.edges[ei]
		if e.Type != core.EdgeCalls || e.To == id {
			continue
		}
		if s, ok := g.symbols[e.To]; ok && relatedCallable(s.Kind) {
			set[e.To] = true
		}
	}
	return mapKeys(set)
}

// callEdgeSources returns the distinct symbols calling id, sorted.
func (g *CodeGraph) callEdgeSources(id string) []string {
	set := map[string]bool{}
	for _, ei := range g.inbound[id] {
		e := g.edges[ei]
		if e.Type != core.EdgeCalls || e.From == id {
			continue
		}
		if _, ok := g.symbols[e.From]; ok {
			set[e.From] = true
		}
	}
	return mapKeys(set)
}

// ownerTypeID is the type whose contains edge holds member id ("" if none).
func (g *CodeGraph) ownerTypeID(id string) string {
	best := ""
	for _, ei := range g.inbound[id] {
		e := g.edges[ei]
		if e.Type != core.EdgeContains {
			continue
		}
		if s, ok := g.symbols[e.From]; ok && isTypeKind(s.Kind) && (best == "" || e.From < best) {
			best = e.From
		}
	}
	return best
}

// ancestorTypeIDs walks extends/implements edges upward, nearest first.
func (g *CodeGraph) ancestorTypeIDs(typeID string, depth int) []string {
	var out []string
	visited := map[string]bool{typeID: true}
	frontier := []string{typeID}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []string
		for _, id := range frontier {
			var ups []string
			for _, ei := range g.outbound[id] {
				e := g.edges[ei]
				if (e.Type == core.EdgeExtends || e.Type == core.EdgeImplements) && !visited[e.To] {
					if _, ok := g.symbols[e.To]; ok {
						visited[e.To] = true
						ups = append(ups, e.To)
					}
				}
			}
			sort.Strings(ups)
			next = append(next, ups...)
		}
		out = append(out, next...)
		frontier = next
	}
	return out
}

// descendantTypeIDs is typeID plus every indexed subtype, capped.
func (g *CodeGraph) descendantTypeIDs(typeID string) []string {
	set := map[string]bool{typeID: true}
	frontier := []string{typeID}
	for len(frontier) > 0 && len(set) < maxDescendants {
		node := frontier[0]
		frontier = frontier[1:]
		for _, ei := range g.inbound[node] {
			e := g.edges[ei]
			if (e.Type == core.EdgeExtends || e.Type == core.EdgeImplements) && !set[e.From] {
				set[e.From] = true
				frontier = append(frontier, e.From)
			}
		}
	}
	return mapKeys(set)
}

func relatedQN(s core.SymbolRecord) string {
	if s.QualifiedName != "" {
		return s.QualifiedName
	}
	return s.Name
}

// relatedOwnerName is the declaring-type label used in details
// ("ObjectArraySerializer" for ObjectArraySerializer.createContextual).
func relatedOwnerName(s core.SymbolRecord) string {
	qn := relatedQN(s)
	if i := strings.LastIndexByte(qn, '.'); i > 0 {
		return qn[:i]
	}
	return qn
}

// peerGroup is a set of same-named methods declared across one hierarchy.
type peerGroup struct {
	root  string // the type every peer descends from (or is)
	name  string
	peers []core.SymbolRecord // sorted, production only
}

func (g *CodeGraph) peersNamed(rootType, name string) []core.SymbolRecord {
	var out []core.SymbolRecord
	for _, m := range g.containedMethods(g.descendantTypeIDs(rootType), name) {
		if !isTestFile(m.FilePath) {
			out = append(out, m)
		}
	}
	sortSymbols(out)
	return out
}

// similarity scores how much a peer's callees overlap a reference callee
// set (Jaccard). Ties in "who lacks the helper" are broken by it, so the
// peer that otherwise looks like the helper's callers ranks first.
func (g *CodeGraph) similarity(id string, ref map[string]bool) float64 {
	callees := g.callEdgeTargets(id)
	if len(callees) == 0 || len(ref) == 0 {
		return 0
	}
	inter := 0
	for _, c := range callees {
		if ref[c] {
			inter++
		}
	}
	union := len(ref) + len(callees) - inter
	return float64(inter) / float64(union)
}

// lackingPeers ranks the peers that do not call helper by similarity to the
// peers that do. A peer that calls a same-named super (super.m()) which
// itself calls the helper already reaches it and is not lacking.
func (g *CodeGraph) lackingPeers(peers []core.SymbolRecord, helper string, callers map[string]bool) []core.SymbolRecord {
	ref := map[string]bool{}
	for id := range callers {
		for _, c := range g.callEdgeTargets(id) {
			if c != helper {
				ref[c] = true
			}
		}
	}
	type scored struct {
		s     core.SymbolRecord
		score float64
	}
	var lacking []scored
	for _, p := range peers {
		if callers[p.ID] {
			continue
		}
		reaches := false
		for _, c := range g.callEdgeTargets(p.ID) {
			if callers[c] {
				reaches = true // super.m() into a peer that calls the helper
				break
			}
		}
		if reaches {
			continue
		}
		lacking = append(lacking, scored{p, g.similarity(p.ID, ref)})
	}
	sort.SliceStable(lacking, func(i, j int) bool {
		if lacking[i].score != lacking[j].score {
			return lacking[i].score > lacking[j].score
		}
		return lessSymbols(&lacking[i].s, &lacking[j].s)
	})
	out := make([]core.SymbolRecord, 0, len(lacking))
	for _, l := range lacking {
		out = append(out, l.s)
	}
	return out
}

func relatedOwnerList(syms []core.SymbolRecord, limit int) string {
	names := make([]string, 0, len(syms))
	for _, s := range syms {
		names = append(names, relatedOwnerName(s))
	}
	sort.Strings(names)
	if len(names) > limit {
		return strings.Join(names[:limit], ", ") + fmt.Sprintf(" +%d more", len(names)-limit)
	}
	return strings.Join(names, ", ")
}

// siblingGapsLocked finds same-named methods in one hierarchy where most
// members share a call to a helper and some do not.
//
// Two entry points reach the same question:
//   - the target IS the helper (change_impact _hasDynamicTypingOverride):
//     its callers share a method name and live under the helper's type, so
//     the other same-named methods under that type are the peers;
//   - the target is one of the peers (change_impact X.createContextual):
//     for each ancestor type, helpers declared on it are checked against
//     the same-named methods under it.
func (g *CodeGraph) siblingGapsLocked(targets []core.SymbolRecord, inSet map[string]bool) []RelatedSite {
	var out []RelatedSite
	emitted := map[string]bool{}
	emit := func(site RelatedSite) {
		if len(out) >= maxRelatedGapSites || emitted[site.Symbol.ID] || inSet[site.Symbol.ID] && site.Relation != "target-lacks" {
			return
		}
		emitted[site.Symbol.ID] = true
		out = append(out, site)
	}
	for _, t := range targets {
		owner := g.ownerTypeID(t.ID)
		if owner == "" || t.Kind == core.KindConstructor {
			continue
		}
		// (a) The target is a helper shared by same-named callers.
		byName := map[string]map[string]bool{}
		for _, c := range g.callEdgeSources(t.ID) {
			cs := g.symbols[c]
			if isTestFile(cs.FilePath) || !relatedCallable(cs.Kind) {
				continue
			}
			if byName[cs.Name] == nil {
				byName[cs.Name] = map[string]bool{}
			}
			byName[cs.Name][c] = true
		}
		for _, name := range mapKeysOfSets(byName) {
			callers := byName[name]
			if len(callers) < 2 {
				continue
			}
			peers := g.peersNamed(owner, name)
			if len(peers) > maxPeerGroup {
				continue
			}
			var inGroup []core.SymbolRecord
			groupCallers := map[string]bool{}
			for _, p := range peers {
				if callers[p.ID] {
					inGroup = append(inGroup, p)
					groupCallers[p.ID] = true
				}
			}
			if len(inGroup) < 2 {
				continue
			}
			lacking := g.lackingPeers(peers, t.ID, groupCallers)
			for i, p := range lacking {
				if i >= maxRelatedGapSites {
					break
				}
				emit(RelatedSite{Symbol: p, Relation: "peer-lacks", Via: relatedQN(t),
					Detail: fmt.Sprintf("does not call %s; %d of %d %s methods under %s do (%s)",
						t.Name, len(inGroup), len(peers), name, relatedOwnerName(t), relatedOwnerList(inGroup, 4))})
			}
		}
		// (b) The target is one of the peers: check helpers on ancestors,
		// nearest first, and stop at the first ancestor with a finding —
		// a far ancestor's group (StdSerializer: every serializer) turns
		// trivial helpers (handledType) into noise.
		for _, anc := range g.ancestorTypeIDs(owner, maxAncestorDepth) {
			if len(out) > 0 {
				break
			}
			peers := g.peersNamed(anc, t.Name)
			if len(peers) < 3 || len(peers) > maxPeerGroup {
				continue
			}
			targetCalls := map[string]bool{}
			for _, c := range g.callEdgeTargets(t.ID) {
				targetCalls[c] = true
			}
			var helpers []core.SymbolRecord
			for _, ei := range g.outbound[anc] {
				e := g.edges[ei]
				if e.Type != core.EdgeContains {
					continue
				}
				if h, ok := g.symbols[e.To]; ok && relatedCallable(h.Kind) && h.Kind != core.KindConstructor && h.Name != t.Name {
					helpers = append(helpers, h)
				}
			}
			sortSymbols(helpers)
			for _, h := range helpers {
				callers := map[string]bool{}
				var calling []core.SymbolRecord
				for _, p := range peers {
					for _, c := range g.callEdgeTargets(p.ID) {
						if c == h.ID {
							callers[p.ID] = true
							if p.ID != t.ID {
								calling = append(calling, p)
							}
							break
						}
					}
				}
				if len(calling) < 2 {
					continue
				}
				if targetCalls[h.ID] {
					for i, p := range g.lackingPeers(peers, h.ID, callers) {
						if i >= maxRelatedGapSites {
							break
						}
						emit(RelatedSite{Symbol: p, Relation: "peer-lacks", Via: relatedQN(h),
							Detail: fmt.Sprintf("does not call %s; %s and %d other %s methods under %s do (%s)",
								h.Name, relatedOwnerName(t), len(calling), t.Name, relatedOwnerName(g.symbols[anc]), relatedOwnerList(calling, 4))})
					}
					continue
				}
				// The target itself lacks a helper its peers share, unless it
				// reaches it through a same-named super call.
				reaches := false
				for c := range targetCalls {
					if callers[c] {
						reaches = true
						break
					}
				}
				if !reaches {
					emit(RelatedSite{Symbol: t, Relation: "target-lacks", Via: relatedQN(h),
						Detail: fmt.Sprintf("%s does not call %s; %d of %d %s methods under %s do (%s)",
							relatedQN(t), relatedQN(h), len(calling), len(peers), t.Name, relatedOwnerName(g.symbols[anc]), relatedOwnerList(calling, 4))})
				}
			}
		}
	}
	return out
}

func mapKeysOfSets(m map[string]map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// coCallersLocked lists other callers of what the targets call ("sites that
// reach the same effect"): direct callees, plus — for a callee in the
// target's own file — that callee's same-file callees (writeBinary ->
// writePOJO -> TreeWriteContext.writePOJO). Same file first, then same
// directory, then elsewhere; production callers only unless the target is a
// test itself.
func (g *CodeGraph) coCallersLocked(targets []core.SymbolRecord, inSet map[string]bool) []RelatedSite {
	type effect struct {
		id  string
		via string // intermediate callee, for depth-2 effects
		src core.SymbolRecord
	}
	var effects []effect
	isEffect := map[string]bool{}
	targetIDs := map[string]bool{}
	for _, t := range targets {
		targetIDs[t.ID] = true
	}
	addEffect := func(id, via string, src core.SymbolRecord) {
		if isEffect[id] || targetIDs[id] || inSet[id] {
			return
		}
		s := g.symbols[id]
		// Constructors are skipped: a type's other constructors and
		// factories all "reach" them, which says nothing about the edit.
		if isTestFile(s.FilePath) || s.Kind == core.KindConstructor {
			return
		}
		isEffect[id] = true
		effects = append(effects, effect{id, via, src})
	}
	for _, t := range targets {
		for _, x := range g.callEdgeTargets(t.ID) {
			addEffect(x, "", t)
		}
	}
	depth1 := len(effects)
	for i := 0; i < depth1; i++ {
		x := g.symbols[effects[i].id]
		src := effects[i].src
		if x.FilePath != src.FilePath {
			continue
		}
		for _, y := range g.callEdgeTargets(x.ID) {
			if g.symbols[y].FilePath == src.FilePath {
				addEffect(y, fmt.Sprintf("%s:%d", x.Name, x.Span.Start), src)
			}
		}
	}
	type cand struct {
		site RelatedSite
		rank int
		ord  int
	}
	var cands []cand
	seen := map[string]bool{}
	for ord, e := range effects {
		callers := g.callEdgeSources(e.id)
		if len(callers) > maxCoCalleeFanIn {
			continue
		}
		es := g.symbols[e.id]
		for _, c := range callers {
			if targetIDs[c] || inSet[c] || isEffect[c] || seen[c] {
				continue
			}
			cs := g.symbols[c]
			if isTestFile(cs.FilePath) && !isTestFile(e.src.FilePath) {
				continue
			}
			// Another file's caller of another file's callee is only
			// loosely related (every serializer calls findContentSerializer);
			// keep it when the shared callee lives in the target's own file
			// (TokenBuffer.serialize -> TreeBuildingGenerator.writePOJO).
			if cs.FilePath != e.src.FilePath && es.FilePath != e.src.FilePath {
				continue
			}
			seen[c] = true
			rank := 1
			if cs.FilePath == e.src.FilePath {
				rank = 0
			}
			path := e.src.Name
			if e.via != "" {
				path += " via " + e.via
			}
			cands = append(cands, cand{RelatedSite{Symbol: cs, Relation: "co-caller", Via: relatedQN(es),
				Detail: fmt.Sprintf("also calls %s (reached from %s)", relatedQN(es), path)}, rank, ord})
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].rank != cands[j].rank {
			return cands[i].rank < cands[j].rank
		}
		if cands[i].ord != cands[j].ord {
			return cands[i].ord < cands[j].ord
		}
		return lessSymbols(&cands[i].site.Symbol, &cands[j].site.Symbol)
	})
	out := make([]RelatedSite, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.site)
	}
	return out
}
