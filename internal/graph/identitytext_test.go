package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A Java or TypeScript type's identity ignores its method bodies (they are
// symbols of their own) but not its fields: a body edit inside a big class
// sent every edit to a full edge rebuild (jackson ObjectMapper, 5-8s).
func TestIdentityTextIgnoresMemberBodiesNotFields(t *testing.T) {
	for _, lang := range []string{"java", "typescript"} {
		base := "class A {\n    int x;\n    void m() {\n        a();\n    }\n}\n"
		body := "class A {\n    int x;\n    void m() {\n        a();\n        b();\n        c();\n    }\n}\n"
		field := "class A {\n    int x;\n    int y;\n    void m() {\n        a();\n    }\n}\n"
		key := func(raw string) string {
			return identityText(&core.SymbolRecord{Language: lang, Kind: core.KindClass, RawText: raw})
		}
		if key(base) != key(body) {
			t.Errorf("%s: a method-body edit changed the class identity", lang)
		}
		if key(base) == key(field) {
			t.Errorf("%s: a new field did not change the class identity", lang)
		}
	}
	py := func(raw string) string {
		return identityText(&core.SymbolRecord{Language: "python", Kind: core.KindClass, RawText: raw})
	}
	if py("class A:\n    def m(self):\n        self.x = 1\n") == py("class A:\n    def m(self):\n        self.y = 1\n") {
		t.Error("python: class attributes come from method bodies; their edits must count")
	}
}
