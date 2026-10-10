package parser

import (
	"bytes"
	"context"
	"strings"

	"github.com/provasign/astkit"
	"github.com/provasign/astkit/textmask"
	sitter "github.com/smacker/go-tree-sitter"
)

// brokenParse is the tree parseBrokenCFamily chose, with the copies of the
// source it addresses.
type brokenParse struct {
	tree      *sitter.Tree
	src       []byte // what was parsed
	text      []byte // what symbol text is cut from (src's lines, unmasked)
	cuts      *commentCuts
	uncutText []byte // text with its comments, on the source's lines
}

// parseBrokenCFamily re-parses a C-family file whose first parse has
// errors from its code alone: comments cut out, comment-only lines
// dropped (cFamilyCommentCuts). Tree-sitter's recovery is sensitive to
// every byte and line it skips, so only an input that is the same for any
// commenting of the program gives the same tree. With branches, the copy
// with every #else/#elif branch and directive blanked is parsed too
// (blankPreprocessorBranches: two else branches of one `if` throw the
// grammar) and kept when it has fewer ERROR nodes. src, text and live are
// extractASTSymbols' offset-aligned copies. It returns nil when nothing
// parses.
func parseBrokenCFamily(ctx context.Context, eng *astkit.Engine, key astkit.LanguageKey, language string, src, text, live []byte, branches bool) *brokenParse {
	cuts := cFamilyCommentCuts(language, live)
	cutSrc, cutLive := cuts.apply(src), cuts.apply(live)
	tree, err := eng.Parse(ctx, key, cutSrc)
	if err != nil || tree == nil {
		return nil
	}
	best := &brokenParse{tree: tree, src: cutSrc, text: cuts.apply(text), cuts: cuts, uncutText: text}
	if !branches || !tree.RootNode().HasError() {
		return best
	}
	alt := blankPreprocessorBranches(cutSrc)
	if alt == nil {
		return best
	}
	altTree, err := eng.Parse(ctx, key, alt)
	if err != nil || altTree == nil {
		return best
	}
	if countErrorNodes(altTree.RootNode()) >= countErrorNodes(tree.RootNode()) {
		altTree.Close()
		return best
	}
	tree.Close()
	// Same line lengths as alt: macro blanking never touches a directive
	// line, and live has src's directive lines.
	return &brokenParse{tree: altTree, src: alt, text: blankPreprocessorBranches(cutLive), cuts: cuts,
		uncutText: blankPreprocessorBranches(live)}
}

// commentCut removes columns [from, to) of one source line and leaves a
// single space in their place, so the code on either side stays two
// tokens.
type commentCut struct{ from, to int }

// commentCuts takes the comments out of a C-family source: every comment
// becomes one space and a line holding nothing but comments is dropped.
// It maps the cut copy's lines and columns back to the source.
type commentCuts struct {
	lines [][]commentCut // per source line
	drop  []bool         // per source line: dropped from the cut copy
	rows  []int          // per cut line: its source line (0-based)
}

// cFamilyCommentCuts returns the cuts for src. Comments are the bytes
// textmask.MaskComments changes, joined across the blanks between them. A
// comment-only line that ends a backslash-continued line is kept (blank),
// so no line joins a #define.
func cFamilyCommentCuts(language string, src []byte) *commentCuts {
	masked := textmask.MaskComments(language, string(src))
	lines := bytes.Split(src, []byte("\n"))
	c := &commentCuts{lines: make([][]commentCut, len(lines)), drop: make([]bool, len(lines))}
	off := 0
	for i, line := range lines {
		mline := masked[off : off+len(line)]
		off += len(line) + 1
		for k := 0; k < len(line); k++ {
			if mline[k] == line[k] {
				continue
			}
			from, to := k, k+1
			for j := to; j < len(line); j++ {
				if mline[j] != line[j] {
					to = j + 1
				} else if line[j] != ' ' && line[j] != '\t' {
					break
				}
			}
			c.lines[i] = append(c.lines[i], commentCut{from: from, to: to})
			k = to - 1
		}
		if c.lines[i] != nil && strings.TrimSpace(mline) == "" {
			if continued := i > 0 && bytes.HasSuffix(bytes.TrimRight(lines[i-1], " \t\r"), []byte("\\")); continued {
				c.lines[i] = []commentCut{{from: 0, to: len(line)}}
			} else {
				c.drop[i] = true
			}
		}
	}
	for i := range lines {
		if !c.drop[i] {
			c.rows = append(c.rows, i)
		}
	}
	return c
}

// apply returns src with the cuts applied. src must have the lines the
// cuts were computed from (any offset-preserving copy does).
func (c *commentCuts) apply(src []byte) []byte {
	lines := bytes.Split(src, []byte("\n"))
	out := make([]byte, 0, len(src))
	first := true
	for i, line := range lines {
		if i < len(c.drop) && c.drop[i] {
			continue
		}
		if !first {
			out = append(out, '\n')
		}
		first = false
		prev := 0
		if i < len(c.lines) {
			for _, cut := range c.lines[i] {
				if cut.to > len(line) {
					break
				}
				out = append(out, line[prev:cut.from]...)
				out = append(out, ' ')
				prev = cut.to
			}
		}
		out = append(out, line[prev:]...)
	}
	return out
}

// row maps a 1-based line of the cut copy to the source.
func (c *commentCuts) row(line int) int {
	if line >= 1 && line <= len(c.rows) {
		return c.rows[line-1] + 1
	}
	return line
}

// column maps column col of cut line row (0-based) back to the source.
func (c *commentCuts) column(row, col int) int {
	if row < 0 || row >= len(c.rows) {
		return col
	}
	orig, k := 0, 0
	for _, cut := range c.lines[c.rows[row]] {
		keep := cut.from - orig
		if col < k+keep {
			return orig + col - k
		}
		k += keep
		if col == k {
			return cut.from
		}
		k++
		orig = cut.to
	}
	return orig + col - k
}

// restore maps every line of syms (extracted from text, the cut copy)
// back to the source and replaces each body with the same stretch of
// uncut (the source as text was before the cut), comments included. A
// body that is not a slice of text keeps its text.
func (c *commentCuts) restore(syms []astkit.Symbol, text, uncut []byte) {
	starts := func(b []byte) []int {
		out := []int{0}
		for i, ch := range b {
			if ch == '\n' {
				out = append(out, i+1)
			}
		}
		return out
	}
	cutStarts, uncutStarts := starts(text), starts(uncut)
	if len(cutStarts) != len(c.rows) || len(uncutStarts) != len(c.drop) {
		return
	}
	// pos is the source offset of column col of cut line row (0-based).
	pos := func(row, col int) int {
		orig := c.rows[row]
		end := len(uncut)
		if orig+1 < len(uncutStarts) {
			end = uncutStarts[orig+1] - 1
		}
		return min(uncutStarts[orig]+c.column(row, col), end)
	}
	for i := range syms {
		s := &syms[i]
		if row := s.Span.Start - 1; s.Body != "" && row >= 0 && row < len(cutStarts) {
			if at := bytes.Index(text[cutStarts[row]:], []byte(s.Body)); at >= 0 {
				from := cutStarts[row] + at
				endRow := row + strings.Count(s.Body, "\n")
				to := from + len(s.Body) - 1 // last byte
				if endRow < len(cutStarts) {
					a := pos(row, from-cutStarts[row])
					b := pos(endRow, to-cutStarts[endRow]) + 1
					if a < b && b <= len(uncut) {
						s.Body = string(uncut[a:b])
					}
				}
			}
		}
		s.Span.Start, s.Span.End = c.row(s.Span.Start), c.row(s.Span.End)
		for k := range s.CallSites {
			s.CallSites[k].Line = c.row(s.CallSites[k].Line)
		}
		for k := range s.AttrSites {
			s.AttrSites[k].Line = c.row(s.AttrSites[k].Line)
		}
	}
}
