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
	for key, removedIdx := range removedByBody {
		addedIdx := addedByBody[key]
		if len(removedIdx) != 1 || len(addedIdx) != 1 {
			continue
		}
		before := diff.Removed[removedIdx[0]]
		after := diff.Added[addedIdx[0]]
		change := core.SymbolChange{
			Before:           &before,
			After:            &after,
			SignatureChanged: before.Signature != after.Signature,
		}
		diff.Renamed = append(diff.Renamed, change)
		usedRemoved[removedIdx[0]] = true
		usedAdded[addedIdx[0]] = true
		// A renamed exported symbol still breaks callers of the old name.
		// A pure file move (same qualified name, body untouched) does not.
		if before.Exports && before.QualifiedName != after.QualifiedName {
			diff.BreakingChanges = append(diff.BreakingChanges, change)
		}
	}
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

// renameKey returns a kind-scoped body fingerprint with the symbol's own
// name blanked out, so `func Login(...)` and `func SignIn(...)` with the
// same body collide on the same key.
func renameKey(s *core.SymbolRecord) (string, bool) {
	body := strings.TrimSpace(s.RawText)
	if len(body) < minRenameBodyLen || s.Name == "" {
		return "", false
	}
	normalized := strings.ReplaceAll(body, s.Name, "\x00")
	return string(s.Kind) + "\x00" + normalized, true
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
