package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

// Python has no overloading: an override whose annotations narrow the base's
// (value: uuid.UUID over value: t.Any) is still an override and belongs to
// the rename family (werkzeug UUIDConverter.to_url was dropped).
func TestPythonOverrideWithNarrowedAnnotationsJoinsFamily(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"conv\"\n",
		"conv.py": `import typing as t
import uuid


class BaseConverter:
    def to_url(self, value: t.Any) -> str:
        return str(value)


class AnyConverter(BaseConverter):
    def to_url(self, value: t.Any) -> str:
        return str(value)


class UUIDConverter(BaseConverter):
    def to_url(self, value: uuid.UUID) -> str:
        return str(value)
`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g, _, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	r, err := g.ChangeImpact("BaseConverter.to_url")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, s := range r.Family {
		got[s.QualifiedName] = true
	}
	for _, want := range []string{"AnyConverter.to_url", "UUIDConverter.to_url"} {
		if !got[want] {
			t.Errorf("family missing %s; got %v", want, got)
		}
	}
}

// A call on a container element dispatches through the container's
// annotation: self._converters: dict[str, BaseConverter] makes
// self._converters[k].to_url() a caller of BaseConverter.to_url and its
// overrides (it used to read as a bare to_url() and bind nothing).
func TestPythonContainerElementReceiverBindsMethod(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"pyproject.toml": "[project]\nname = \"conv\"\n",
		"conv.py": `class BaseConverter:
    def to_url(self, value) -> str:
        return str(value)


class UUIDConverter(BaseConverter):
    def to_url(self, value) -> str:
        return str(value)


class Unrelated:
    def to_url(self, value) -> str:
        return ""


class Rule:
    def __init__(self) -> None:
        self._converters: dict[str, BaseConverter] = {}

    def build(self, name, value):
        return self._converters[name].to_url(value)


def handle(converters: list[BaseConverter], value):
    return converters[0].to_url(value)
`,
	}
	for rel, body := range files {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	g, _, err := New(parser.NewEngine(), st).Index(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	syms, edges := g.Snapshot()
	qn := map[string]string{}
	for _, s := range syms {
		qn[s.ID] = s.QualifiedName
	}
	calls := map[string]bool{}
	for _, e := range edges {
		if e.Type == "calls" {
			calls[qn[e.From]+"→"+qn[e.To]] = true
		}
	}
	for _, want := range []string{"Rule.build→BaseConverter.to_url", "Rule.build→UUIDConverter.to_url", "handle→BaseConverter.to_url"} {
		if !calls[want] {
			t.Errorf("missing %s", want)
		}
	}
	for _, bad := range []string{"Rule.build→Unrelated.to_url", "handle→Unrelated.to_url"} {
		if calls[bad] {
			t.Errorf("unexpected %s (element type is BaseConverter)", bad)
		}
	}
}
