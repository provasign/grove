// Package internalast contains helpers shared by every per-language strategy
// implementation. Kept in a sub-package so that experimentation in one
// strategy can not accidentally widen the helper API.
package internalast

import (
	sitter "github.com/smacker/go-tree-sitter"

	"github.com/provasign/astkit"
)

// WalkChildren invokes fn on every immediate child of n.
func WalkChildren(n *sitter.Node, fn func(*sitter.Node)) {
	if n == nil {
		return
	}
	count := int(n.ChildCount())
	for i := 0; i < count; i++ {
		c := n.Child(i)
		if c != nil {
			fn(c)
		}
	}
}

// FindChildByType returns the first immediate child of n whose Type() equals kind.
func FindChildByType(n *sitter.Node, kind string) *sitter.Node {
	if n == nil {
		return nil
	}
	count := int(n.ChildCount())
	for i := 0; i < count; i++ {
		c := n.Child(i)
		if c != nil && c.Type() == kind {
			return c
		}
	}
	return nil
}

// NodeText returns the source text covered by n, clamped to src bounds.
func NodeText(n *sitter.Node, src []byte) string {
	if n == nil {
		return ""
	}
	start, end := n.StartByte(), n.EndByte()
	if int(end) > len(src) {
		end = uint32(len(src))
	}
	if start > end {
		return ""
	}
	return string(src[start:end])
}

// NodeSpan returns the 1-indexed inclusive line range covered by n.
func NodeSpan(n *sitter.Node) astkit.LineRange {
	if n == nil {
		return astkit.LineRange{}
	}
	return astkit.LineRange{
		Start: int(n.StartPoint().Row) + 1,
		End:   int(n.EndPoint().Row) + 1,
	}
}

// IsCapitalized reports whether name begins with an uppercase ASCII letter.
// Useful for Go's export rule.
func IsCapitalized(name string) bool {
	if name == "" {
		return false
	}
	c := name[0]
	return c >= 'A' && c <= 'Z'
}

// HasModifier reports whether modifiers contains m.
func HasModifier(modifiers []string, m string) bool {
	for _, x := range modifiers {
		if x == m {
			return true
		}
	}
	return false
}

// FirstLine returns the first line of s, useful as a signature fallback.
func FirstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
