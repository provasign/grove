// Command scip-check validates a SCIP index against the official scip.proto
// types and symbol grammar (github.com/scip-code/scip/bindings/go/scip): the
// checks `scip lint` makes that matter for an exporter. Used on Grove's SCIP
// export (SCIP-01) where the upstream CLI cannot be built.
//
//	scip-check index.scip [more.scip ...]
//
// Exit 1 when any index has an error; each error is printed.
package main

import (
	"fmt"
	"os"

	"github.com/scip-code/scip/bindings/go/scip"
	"google.golang.org/protobuf/proto"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scip-check index.scip [more.scip ...]")
		os.Exit(2)
	}
	bad := false
	for _, path := range os.Args[1:] {
		raw, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		errs, stats := Check(raw)
		for _, e := range errs {
			fmt.Printf("%s: %s\n", path, e)
		}
		fmt.Printf("%s: %d documents, %d occurrences, %d symbols, %d errors\n", path, stats.docs, stats.occs, stats.syms, len(errs))
		bad = bad || len(errs) > 0
	}
	if bad {
		os.Exit(1)
	}
}

type stats struct{ docs, occs, syms int }

// Check decodes raw as a scip.Index and returns every problem found.
func Check(raw []byte) ([]string, stats) {
	var idx scip.Index
	if err := proto.Unmarshal(raw, &idx); err != nil {
		return []string{"not a scip.Index: " + err.Error()}, stats{}
	}
	var errs []string
	add := func(format string, a ...any) {
		if len(errs) < 200 {
			errs = append(errs, fmt.Sprintf(format, a...))
		}
	}
	m := idx.GetMetadata()
	if m == nil || m.GetToolInfo().GetName() == "" || m.GetProjectRoot() == "" {
		add("metadata: missing tool name or project root")
	}
	validSymbol := func(where, sym string) bool {
		if sym == "" {
			add("%s: empty symbol", where)
			return false
		}
		if scip.IsLocalSymbol(sym) {
			return true
		}
		if _, err := scip.ParseSymbol(sym); err != nil {
			add("%s: symbol %q does not parse: %v", where, sym, err)
			return false
		}
		return true
	}
	defined := map[string]bool{}
	for _, d := range idx.GetDocuments() {
		for _, info := range d.GetSymbols() {
			defined[info.GetSymbol()] = true
		}
	}
	for _, s := range idx.GetExternalSymbols() {
		defined[s.GetSymbol()] = true
	}
	var st stats
	paths := map[string]bool{}
	for _, d := range idx.GetDocuments() {
		st.docs++
		p := d.GetRelativePath()
		if p == "" || paths[p] {
			add("document %q: empty or duplicate relative path", p)
		}
		paths[p] = true
		seenInfo := map[string]bool{}
		for _, info := range d.GetSymbols() {
			st.syms++
			sym := info.GetSymbol()
			if seenInfo[sym] {
				add("%s: duplicate SymbolInformation for %s", p, sym)
			}
			seenInfo[sym] = true
			validSymbol(p+": symbol information", sym)
			for _, rel := range info.GetRelationships() {
				if validSymbol(p+": relationship of "+sym, rel.GetSymbol()) && !defined[rel.GetSymbol()] {
					add("%s: relationship of %s names %s, which no document defines", p, sym, rel.GetSymbol())
				}
			}
			if e := info.GetEnclosingSymbol(); e != "" {
				validSymbol(p+": enclosing symbol of "+sym, e)
			}
		}
		for _, o := range d.GetOccurrences() {
			st.occs++
			r, err := scip.NewRange(o.GetRange())
			if err != nil {
				add("%s: occurrence %s: bad range %v: %v", p, o.GetSymbol(), o.GetRange(), err)
				continue
			}
			if !validSymbol(p+": occurrence", o.GetSymbol()) {
				continue
			}
			if !scip.IsLocalSymbol(o.GetSymbol()) && !defined[o.GetSymbol()] {
				add("%s: occurrence at %v names %s, which has no SymbolInformation", p, o.GetRange(), o.GetSymbol())
			}
			if len(o.GetEnclosingRange()) == 0 {
				continue
			}
			enc, err := scip.NewRange(o.GetEnclosingRange())
			if err != nil {
				add("%s: occurrence %s: bad enclosing range %v: %v", p, o.GetSymbol(), o.GetEnclosingRange(), err)
				continue
			}
			end := scip.Position{Line: r.End.Line, Character: r.End.Character}
			if !containsPos(enc, r.Start) || !containsOrEnds(enc, end) {
				add("%s: enclosing range %v does not contain occurrence %v of %s", p, o.GetEnclosingRange(), o.GetRange(), o.GetSymbol())
			}
		}
	}
	return errs, st
}

func containsPos(r scip.Range, p scip.Position) bool {
	return r.Contains(p)
}

// containsOrEnds is Contains with the range end allowed: an occurrence that
// ends exactly where its enclosing range ends is inside it.
func containsOrEnds(r scip.Range, p scip.Position) bool {
	return r.Contains(p) || (p.Line == r.End.Line && p.Character == r.End.Character)
}
