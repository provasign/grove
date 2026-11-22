package graph

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// selectSeedsLocked narrows the same-named types a Type.member query seeded to
// the ones the query can mean, and refuses to merge types that are not one
// declaration unit or one inheritance line.
//
// Before this, every type with the queried simple name seeded the change set.
// Same-named types in different packages became one "closed" family: on the
// prism repo, Server.Handler merged graphui.Server and httpapi.Server into 7
// sites marked safe to claim complete (2026-11-22), and a Rust Client with no
// Status method blocked every Go Client.Status query as a cross-language
// ambiguity. The order of the steps below is what fixes both:
//
//  1. A qualifier in the query (graphui.Server.Handler, com.x.Foo.bar,
//     Outer.Inner.m) keeps the seeds it names. A qualifier naming none of
//     them is ignored, so the steps below still refuse a silent merge.
//  2. Seeds that declare the member win over seeds that do not. A method is
//     declared in one place, so this alone drops the halves of a split type
//     (C# partial, Swift extension) and unrelated same-named types.
//  3. Language families are compared only after 2.
//  4. The survivors are grouped: one declaration unit (same package or module
//     and qualified name: Go build-tag twins, foo.pyi beside foo.py,
//     foo.d.ts beside foo.ts, one C++ namespace) or one inheritance line.
//     More than one group is an ambiguity, reported as candidates the caller
//     can re-run; nothing is merged and nothing is guessed.
func (g *CodeGraph) selectSeedsLocked(operation, query, typeName, methodName, file string, typeIDs []string) ([]string, error) {
	if len(typeIDs) < 2 {
		return typeIDs, nil
	}
	if qualifier := impactQualifier(query); qualifier != "" && file == "" {
		var named []string
		for _, id := range typeIDs {
			s := g.symbols[id]
			if seedMatchesQualifier(&s, qualifier, typeName) {
				named = append(named, id)
			}
		}
		if len(named) > 0 {
			typeIDs = named
		}
	}
	if len(typeIDs) < 2 {
		return typeIDs, nil
	}
	var declaring []string
	for _, id := range typeIDs {
		if g.seedDeclaresMember(id, methodName) {
			declaring = append(declaring, id)
		}
	}
	if len(declaring) > 0 {
		typeIDs = declaring
	}
	if err := g.rejectCrossLanguageSeedsLocked(operation, query, typeIDs); err != nil {
		return nil, err
	}
	groups := g.seedGroupsLocked(typeIDs)
	if len(groups) < 2 {
		return typeIDs, nil
	}
	return nil, g.seedAmbiguityError(operation, query, typeName, methodName, groups)
}

// seedDeclaresMember reports whether the type declares methodName itself, as
// an indexed member or as interface body text.
func (g *CodeGraph) seedDeclaresMember(id, methodName string) bool {
	if len(g.containedMethods([]string{id}, methodName)) > 0 {
		return true
	}
	t, ok := g.symbols[id]
	return ok && g.typeDeclaresMember(&t, methodName)
}

// seedGroupsLocked partitions seeds into declaration units joined by
// inheritance. Groups and their members are sorted, so the answer is stable.
func (g *CodeGraph) seedGroupsLocked(typeIDs []string) [][]string {
	parent := make(map[string]string, len(typeIDs))
	var find func(string) string
	find = func(x string) string {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b string) {
		ra, rb := find(a), find(b)
		if ra == rb {
			return
		}
		if ra < rb {
			parent[rb] = ra
		} else {
			parent[ra] = rb
		}
	}
	for _, id := range typeIDs {
		parent[id] = id
	}
	byUnit := map[string]string{}
	for _, id := range typeIDs {
		s := g.symbols[id]
		unit := s.Language + "\x00" + seedScopePath(&s) + "\x00" + s.QualifiedName
		if first, ok := byUnit[unit]; ok {
			union(first, id)
		} else {
			byUnit[unit] = id
		}
	}
	for _, id := range typeIDs {
		up := g.supertypeClosureLocked(id)
		for _, other := range typeIDs {
			if other != id && up[other] {
				union(id, other)
			}
		}
	}
	members := map[string][]string{}
	for _, id := range typeIDs {
		root := find(id)
		members[root] = append(members[root], id)
	}
	groups := make([][]string, 0, len(members))
	for _, ids := range members {
		sort.Strings(ids)
		groups = append(groups, ids)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i][0] < groups[j][0] })
	return groups
}

// supertypeClosureLocked is every type reachable from id over extends and
// implements edges, id excluded.
func (g *CodeGraph) supertypeClosureLocked(id string) map[string]bool {
	seen := map[string]bool{}
	frontier := []string{id}
	for len(frontier) > 0 {
		node := frontier[0]
		frontier = frontier[1:]
		for _, ei := range g.outbound[node] {
			edge := g.edges[ei]
			if edge.Type != core.EdgeExtends && edge.Type != core.EdgeImplements {
				continue
			}
			if edge.To != id && !seen[edge.To] {
				seen[edge.To] = true
				frontier = append(frontier, edge.To)
			}
		}
	}
	return seen
}

// seedAmbiguityError lists one re-runnable candidate per group, in the
// "is ambiguous — N candidates" form prism parses: name, then (file:line) of
// the member's declaration so a scoped re-run lands on it.
func (g *CodeGraph) seedAmbiguityError(operation, query, typeName, methodName string, groups [][]string) error {
	reps := make([]core.SymbolRecord, len(groups))
	for i, ids := range groups {
		reps[i] = g.symbols[ids[0]]
	}
	qualifiers := distinguishingQualifiers(reps)
	var b strings.Builder
	for i, ids := range groups {
		rep := reps[i]
		file, line := rep.FilePath, rep.Span.Start
		if decls := g.containedMethods(ids, methodName); len(decls) > 0 {
			sortSymbols(decls)
			file, line = decls[0].FilePath, decls[0].Span.Start
		}
		name := rep.QualifiedName
		if name == "" {
			name = typeName
		}
		if qualifiers[i] != "" {
			name = qualifiers[i] + "." + name
		}
		fmt.Fprintf(&b, "\n  %s.%s  (%s:%d)", name, methodName, file, line)
	}
	return fmt.Errorf("%s: %q is ambiguous — %d candidates:%s\nThese are same-named types in different packages or modules; no change sets were merged. re-run with one of these",
		operation, query, len(groups), b.String())
}

// impactQualifier is the part of a Type.member query before the type:
// "graphui" for graphui.Server.Handler, "" for Server.Handler.
func impactQualifier(query string) string {
	q := strings.TrimSpace(query)
	if i := strings.IndexByte(q, '('); i >= 0 {
		q = q[:i]
	}
	dot := strings.LastIndexByte(q, '.')
	if dot <= 0 {
		return ""
	}
	head := q[:dot]
	j := strings.LastIndexByte(head, '.')
	if j <= 0 {
		return ""
	}
	return head[:j]
}

// seedMatchesQualifier: the qualifier names the seed's enclosing types
// (Cart.Line for Cart.Line.total) or a suffix of its package or module path
// (graphui, com.x, pkg.models).
func seedMatchesQualifier(s *core.SymbolRecord, qualifier, typeName string) bool {
	if s.QualifiedName != "" && s.QualifiedName != typeName &&
		strings.HasSuffix(qualifier+"."+typeName, s.QualifiedName) {
		return true
	}
	q := strings.ReplaceAll(qualifier, ".", "/")
	p := seedScopePath(s)
	return p == q || strings.HasSuffix(p, "/"+q)
}

// seedScopePath is the package or module a type belongs to, as a slash path:
// the module file without extensions for Python and JS/TS (so foo.pyi and
// foo.py, foo.d.ts and foo.ts are one module), the package directory for
// Java (source root stripped) and the other directory-scoped languages.
func seedScopePath(s *core.SymbolRecord) string {
	p := strings.ReplaceAll(s.FilePath, "\\", "/")
	switch s.Language {
	case "python", "typescript", "tsx", "javascript", "jsx":
		p = strings.TrimSuffix(p, path.Ext(p))
		return strings.TrimSuffix(p, ".d")
	case "java":
		if pkg, ok := javaPackageSuffix(path.Dir(p)); ok {
			return pkg
		}
	case "c", "cpp", "objc":
		// Headers and sources live in different directories; the namespace
		// in the qualified name is the scope (geo::Shape), the file stem
		// otherwise (include/shape.h beside src/shape.cpp).
		if strings.Contains(s.QualifiedName, "::") {
			return ""
		}
		stem := path.Base(p)
		return strings.TrimSuffix(stem, path.Ext(stem))
	}
	return path.Dir(p)
}

// distinguishingQualifiers gives each representative the shortest trailing
// run of its scope path's segments that no other representative shares, as a
// dotted prefix. A representative whose qualified name already differs gets
// none.
func distinguishingQualifiers(reps []core.SymbolRecord) []string {
	out := make([]string, len(reps))
	names := map[string]int{}
	for _, r := range reps {
		names[r.QualifiedName]++
	}
	for i, r := range reps {
		if names[r.QualifiedName] < 2 {
			continue
		}
		segs := strings.Split(strings.Trim(seedScopePath(&r), "/"), "/")
		for n := 1; n <= len(segs); n++ {
			cand := strings.Join(segs[len(segs)-n:], ".")
			unique := true
			for j, o := range reps {
				if j == i || o.QualifiedName != r.QualifiedName {
					continue
				}
				osegs := strings.Split(strings.Trim(seedScopePath(&o), "/"), "/")
				if len(osegs) >= n && strings.Join(osegs[len(osegs)-n:], ".") == cand {
					unique = false
					break
				}
			}
			if unique {
				out[i] = cand
				break
			}
		}
		if out[i] == "" && seedScopePath(&r) != "" {
			out[i] = strings.Join(segs, ".")
		}
	}
	return out
}
