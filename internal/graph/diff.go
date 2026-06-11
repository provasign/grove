package graph

import (
	"sort"

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

	sortSymbols(diff.Added)
	sortSymbols(diff.Removed)
	sortChanges(diff.Changed)
	sortChanges(diff.BreakingChanges)
	return diff
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
