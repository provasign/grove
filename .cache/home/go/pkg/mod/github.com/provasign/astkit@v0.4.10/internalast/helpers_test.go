package internalast_test

import (
	"context"
	"testing"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/golang"

	"github.com/provasign/astkit/internalast"
)

func parseGo(t *testing.T, src string) (*sitter.Tree, []byte) {
	t.Helper()
	p := sitter.NewParser()
	p.SetLanguage(golang.GetLanguage())
	tree, err := p.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return tree, []byte(src)
}

func TestWalkChildren(t *testing.T) {
	tree, _ := parseGo(t, "package main\nfunc a(){}\nfunc b(){}\n")
	defer tree.Close()
	count := 0
	internalast.WalkChildren(tree.RootNode(), func(*sitter.Node) { count++ })
	if count < 3 { // package + 2 funcs
		t.Errorf("WalkChildren count=%d, want >=3", count)
	}
	internalast.WalkChildren(nil, func(*sitter.Node) { t.Fatal("nil should noop") })
}

func TestFindChildByType(t *testing.T) {
	tree, _ := parseGo(t, "package main\nfunc a(){}\n")
	defer tree.Close()
	root := tree.RootNode()
	if got := internalast.FindChildByType(root, "package_clause"); got == nil {
		t.Error("expected package_clause")
	}
	if got := internalast.FindChildByType(root, "nonsense"); got != nil {
		t.Error("expected nil for missing type")
	}
	if got := internalast.FindChildByType(nil, "x"); got != nil {
		t.Error("nil node should return nil")
	}
}

func TestNodeText_NodeSpan(t *testing.T) {
	tree, src := parseGo(t, "package main\nfunc abc(){}\n")
	defer tree.Close()
	fn := internalast.FindChildByType(tree.RootNode(), "function_declaration")
	if fn == nil {
		t.Fatal("no func")
	}
	txt := internalast.NodeText(fn, src)
	if txt != "func abc(){}" {
		t.Errorf("NodeText=%q", txt)
	}
	span := internalast.NodeSpan(fn)
	if span.Start != 2 || span.End != 2 {
		t.Errorf("span=%+v want {2,2}", span)
	}
	if internalast.NodeText(nil, src) != "" {
		t.Error("nil NodeText must be empty")
	}
	if internalast.NodeSpan(nil) != internalast.NodeSpan(nil) {
		t.Error("zero span comparison broken")
	}
}

func TestIsCapitalized(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{{"", false}, {"abc", false}, {"Abc", true}, {"_x", false}, {"Z", true}} {
		if got := internalast.IsCapitalized(c.in); got != c.want {
			t.Errorf("IsCapitalized(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestHasModifier(t *testing.T) {
	if !internalast.HasModifier([]string{"pub", "static"}, "static") {
		t.Error("expected true")
	}
	if internalast.HasModifier(nil, "x") {
		t.Error("nil slice must return false")
	}
}

func TestFirstLine(t *testing.T) {
	if internalast.FirstLine("abc\ndef") != "abc" {
		t.Error("multi-line broken")
	}
	if internalast.FirstLine("oneline") != "oneline" {
		t.Error("single line broken")
	}
	if internalast.FirstLine("") != "" {
		t.Error("empty broken")
	}
}
