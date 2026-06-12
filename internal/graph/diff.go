package graph

import (
	"sort"
	"strings"

	"github.com/provasign/grove/internal/core"
)

// DiffSymbols computes the structural delta between two symbol snapshots
// (typically: the graph before and after a merge or reindex).
//
// Symbols are matched by stable identity — file path + qualified name +
// kind — not by symbol ID: IDs embed the file content SHA, so any edit
// changes every ID in the file and an ID-based diff would report whole-file
// churn for a one-line change. A symbol whose span moved but whose
// signature and body are unchanged is not reported at all; that is what
// makes the diff usable as a drift signal ("the ground shifted under you")
// rather than a line-number echo.
//
// Same-key collisions (e.g. C++ overloads sharing a qualified name) are
// paired positionally in document order; surplus entries on either side
// surface as added/removed.
func DiffSymbols(before, after []core.SymbolRecord) core.GraphDiff {
	beforeByKey := bucketByIdentity(before)
	afterByKey := bucketByIdentity(after)

	var diff core.GraphDiff

	for key, beforeBucket := range beforeByKey {
		afterBucket := afterByKey[key]
		paired := len(beforeBucket)
		if len(afterBucket) < paired {
			paired = len(afterBucket)
		}
		for i := 0; i < paired; i++ {
			b, a := beforeBucket[i], afterBucket[i]
			change := core.SymbolChange{
				Before:           b,
				After:            a,
				SignatureChanged: b.Signature != a.Signature,
				BodyChanged:      b.RawText != a.RawText,
			}
			if !change.SignatureChanged && !change.BodyChanged {
				continue
			}
			diff.Changed = append(diff.Changed, change)
			if b.Exports && change.SignatureChanged {
				diff.BreakingChanges = append(diff.BreakingChanges, change)
			}
		}
		for _, b := range beforeBucket[paired:] {
			diff.Removed = append(diff.Removed, *b)
			if b.Exports {
				diff.BreakingChanges = append(diff.BreakingChanges, core.SymbolChange{Before: b, SignatureChanged: true})
			}
		}
	}
	for key, afterBucket := range afterByKey {
		paired := len(beforeByKey[key])
		if paired >= len(afterBucket) {
			continue
		}
		for _, a := range afterBucket[paired:] {
			diff.Added = append(diff.Added, *a)
		}
	}

	detectRenames(&diff)

	sortSymbols(diff.Added)
	sortSymbols(diff.Removed)
	sortChanges(diff.Changed)
	sortChanges(diff.Renamed)
	sortChanges(diff.BreakingChanges)
	return diff
}

// minRenameBodyLen guards against pairing trivially identical bodies
// (`{ return nil }` boilerplate) as renames. Short-bodied renames stay in
// Added/Removed — a false "removed" is conservative, a false rename is not.
const minRenameBodyLen = 24

// detectRenames pairs removed and added symbols whose bodies are identical
// after substituting their own name. A rename otherwise reports as a
// removal (breaking) plus an unrelated addition, which both overstates the
// break and hides the continuity a merge tool or drift consumer needs.
// Only unambiguous 1:1 matches per (kind, normalized body) pair; ambiguous
// groups are left as-is.
func detectRenames(diff *core.GraphDiff) {
	if len(diff.Added) == 0 || len(diff.Removed) == 0 {
		return
	}
	removedByBody := map[string][]int{}
	for i := range diff.Removed {
		if key, ok := renameKey(&diff.Removed[i]); ok {
			removedByBody[key] = append(removedByBody[key], i)
		}
	}
	addedByBody := map[string][]int{}
	for i := range diff.Added {
		if key, ok := renameKey(&diff.Added[i]); ok {
			addedByBody[key] = append(addedByBody[key], i)
		}
	}

	usedRemoved := map[int]bool{}
	usedAdded := map[int]bool{}
	pair := func(ri, ai int) {
		before := diff.Removed[ri]
		after := diff.Added[ai]
		change := core.SymbolChange{
			Before:           &before,
			After:            &after,
			SignatureChanged: before.Signature != after.Signature,
		}
		diff.Renamed = append(diff.Renamed, change)
		usedRemoved[ri] = true
		usedAdded[ai] = true
		// A renamed exported symbol still breaks callers of the old name.
		// A pure file move (same qualified name, body untouched) does not.
		if before.Exports && before.QualifiedName != after.QualifiedName {
			diff.BreakingChanges = append(diff.BreakingChanges, change)
		}
	}
	for key, removedIdx := range removedByBody {
		addedIdx := addedByBody[key]
		if len(removedIdx) != 1 || len(addedIdx) != 1 {
			continue
		}
		pair(removedIdx[0], addedIdx[0])
	}

	// Second pass for partial renames: the declaration was renamed but the
	// old name survives elsewhere in the body — typically the doc comment
	// ("// Get an item...") after a mechanical symbol rename. Blanking only
	// each side's own name leaves those occurrences asymmetric, so compare
	// with BOTH names blanked on both sides. Pairwise and bounded; still
	// 1:1-unambiguous only.
	detectPartialRenames(diff, usedRemoved, usedAdded, pair)

	if len(usedRemoved) == 0 {
		return
	}
	diff.Removed = withoutIndices(diff.Removed, usedRemoved)
	diff.Added = withoutIndices(diff.Added, usedAdded)
	// Pull the paired removals back out of BreakingChanges: they were
	// recorded as breaking removals before rename pairing ran, and the
	// rename entry above already carries the breaking signal when due.
	kept := diff.BreakingChanges[:0]
	for _, change := range diff.BreakingChanges {
		if change.After == nil && isRenamedBefore(diff.Renamed, change.Before) {
			continue
		}
		kept = append(kept, change)
	}
	diff.BreakingChanges = kept
}

// maxPairwiseRename bounds the quadratic partial-rename pass; diffs churning
// more symbols than this per file get keyed matching only.
const maxPairwiseRename = 12

// detectPartialRenames pairs remaining removed/added symbols whose bodies
// match once both the old and the new name are blanked on both sides.
func detectPartialRenames(diff *core.GraphDiff, usedRemoved, usedAdded map[int]bool, pair func(ri, ai int)) {
	if len(diff.Removed) > maxPairwiseRename || len(diff.Added) > maxPairwiseRename {
		return
	}
	dualKey := func(s *core.SymbolRecord, oldName, newName string) (string, bool) {
		body := strings.TrimSpace(s.RawText)
		if len(body) < minRenameBodyLen {
			return "", false
		}
		body = replaceWholeIdent(body, oldName)
		body = replaceWholeIdent(body, newName)
		return string(s.Kind) + "\x00" + body, true
	}
	type match struct{ ri, ai int }
	matchesByRemoved := map[int][]int{}
	matchesByAdded := map[int][]int{}
	var candidates []match
	for ri := range diff.Removed {
		if usedRemoved[ri] || diff.Removed[ri].Name == "" {
			continue
		}
		for ai := range diff.Added {
			if usedAdded[ai] || diff.Added[ai].Name == "" {
				continue
			}
			r, a := &diff.Removed[ri], &diff.Added[ai]
			if r.Kind != a.Kind {
				continue
			}
			rk, ok1 := dualKey(r, r.Name, a.Name)
			ak, ok2 := dualKey(a, r.Name, a.Name)
			if !ok1 || !ok2 || rk != ak {
				continue
			}
			candidates = append(candidates, match{ri, ai})
			matchesByRemoved[ri] = append(matchesByRemoved[ri], ai)
			matchesByAdded[ai] = append(matchesByAdded[ai], ri)
		}
	}
	for _, m := range candidates {
		if len(matchesByRemoved[m.ri]) == 1 && len(matchesByAdded[m.ai]) == 1 {
			pair(m.ri, m.ai)
		}
	}
}

// renameKey returns a kind-scoped body fingerprint with the symbol's own
// name blanked out, so `func Login(...)` and `func SignIn(...)` with the
// same body collide on the same key.
func renameKey(s *core.SymbolRecord) (string, bool) {
	body := strings.TrimSpace(s.RawText)
	if len(body) < minRenameBodyLen || s.Name == "" {
		return "", false
	}
	normalized := replaceWholeIdent(body, s.Name)
	return string(s.Kind) + "\x00" + normalized, true
}

// replaceWholeIdent blanks standalone identifier occurrences of name in
// body — occurrences not flanked by identifier characters. A plain
// ReplaceAll also mangles identifiers that merely contain the name ("Get"
// inside "GetKeys"), which made normalization asymmetric between the
// removed and the added body, so renames of common short names never
// paired and fell back to removed+added.
func replaceWholeIdent(body, name string) string {
	var sb strings.Builder
	sb.Grow(len(body))
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], name)
		if j < 0 {
			sb.WriteString(body[i:])
			break
		}
		j += i
		end := j + len(name)
		sb.WriteString(body[i:j])
		leftOK := j == 0 || !isIdentChar(body[j-1])
		rightOK := end == len(body) || !isIdentChar(body[end])
		if leftOK && rightOK {
			sb.WriteByte('\x00')
		} else {
			sb.WriteString(name)
		}
		i = end
	}
	return sb.String()
}

func isIdentChar(c byte) bool {
	return c == '_' ||
		('0' <= c && c <= '9') ||
		('a' <= c && c <= 'z') ||
		('A' <= c && c <= 'Z')
}

func isRenamedBefore(renamed []core.SymbolChange, before *core.SymbolRecord) bool {
	if before == nil {
		return false
	}
	for _, r := range renamed {
		if r.Before != nil && r.Before.ID == before.ID {
			return true
		}
	}
	return false
}

func withoutIndices(symbols []core.SymbolRecord, drop map[int]bool) []core.SymbolRecord {
	out := symbols[:0]
	for i := range symbols {
		if !drop[i] {
			out = append(out, symbols[i])
		}
	}
	return out
}

// identityKey is the stable cross-snapshot identity of a symbol.
func identityKey(s *core.SymbolRecord) string {
	return s.FilePath + "\x00" + s.QualifiedName + "\x00" + string(s.Kind)
}

func bucketByIdentity(symbols []core.SymbolRecord) map[string][]*core.SymbolRecord {
	out := make(map[string][]*core.SymbolRecord, len(symbols))
	for i := range symbols {
		key := identityKey(&symbols[i])
		out[key] = append(out[key], &symbols[i])
	}
	// Positional pairing requires deterministic bucket order.
	for _, bucket := range out {
		sort.Slice(bucket, func(i, j int) bool { return bucket[i].Span.Start < bucket[j].Span.Start })
	}
	return out
}

func sortSymbols(symbols []core.SymbolRecord) {
	sort.Slice(symbols, func(i, j int) bool {
		if symbols[i].FilePath == symbols[j].FilePath {
			return symbols[i].Span.Start < symbols[j].Span.Start
		}
		return symbols[i].FilePath < symbols[j].FilePath
	})
}

func sortChanges(changes []core.SymbolChange) {
	at := func(c core.SymbolChange) *core.SymbolRecord {
		if c.Before != nil {
			return c.Before
		}
		return c.After
	}
	sort.Slice(changes, func(i, j int) bool {
		a, b := at(changes[i]), at(changes[j])
		if a.FilePath == b.FilePath {
			return a.Span.Start < b.Span.Start
		}
		return a.FilePath < b.FilePath
	})
}
