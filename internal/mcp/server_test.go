package mcp

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tabladrum/grove-suite/grove/internal/graph"
	"github.com/tabladrum/grove-suite/grove/internal/index"
	"github.com/tabladrum/grove-suite/grove/internal/parser"
	"github.com/tabladrum/grove-suite/grove/internal/store"
)

func TestMCPToolsList(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	request := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	input := bytes.NewBufferString(fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(request), request))
	var output bytes.Buffer
	if err := NewServer(root, graph.New(), parser.NewEngine(), st).Serve(input, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "grove_query") {
		t.Fatalf("unexpected output: %s", output.String())
	}
}

func TestMCPIndexTool(t *testing.T) {
	root := t.TempDir()
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, _, err := index.New(parser.NewEngine(), st).Index(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}
