package grove

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Comment/string-as-code audit (2026-10-10), tool-output findings: the
// answers of missing-implementations, rename-plan, and change-impact must
// not change when text inside comments, docstrings, or string literals
// looks like code. The graph-shaped findings are in
// internal/invariance/twin_test.go. Each test names its audit ID.

func editLines(es []RenameEdit) []string {
	var out []string
	for _, e := range es {
		out = append(out, fmt.Sprintf("%s:%d %s", e.FilePath, e.Line, strings.TrimSpace(e.Before)))
	}
	return out
}

func hasEditLine(es []RenameEdit, file string, line int) bool {
	for _, e := range es {
		if e.FilePath == file && e.Line == line {
			return true
		}
	}
	return false
}

func symNames(ss []Symbol) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s.FilePath+"::"+s.QualifiedName)
	}
	return out
}

// E5: a docstring that mentions NotImplementedError must not make an
// implementation look abstract.
func TestCommentString_E5_MissingImplPythonDocstring(t *testing.T) {
	for _, form := range []struct{ name, doc string }{
		{"clean", `"""Saves the record and returns 1."""`},
		{"decoy", `"""Saves the record; never raises NotImplementedError."""`},
	} {
		t.Run(form.name, func(t *testing.T) {
			eng := openMemberFixture(t, map[string]string{"models.py": `class Contract:
    def save(self):
        raise NotImplementedError


class BaseImpl(Contract):
    def save(self):
        ` + form.doc + `
        return 1


class Child(BaseImpl):
    pass
`})
			r, err := eng.MissingImplementations(context.Background(), "Contract.save")
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Missing)+len(r.AbstractMissing) > 0 {
				t.Errorf("missing=%v abstractMissing=%v (implemented=%d), want none: BaseImpl.save returns 1 and Child inherits it",
					symNames(r.Missing), symNames(r.AbstractMissing), r.ImplementedCount)
			}
		})
	}
}

// E7/E8: rename-plan must find edit sites on lines that also carry
// attributes, private fields, or string interpolation.
func TestCommentString_E7E8_RenamePlanSites(t *testing.T) {
	cases := []struct {
		id, file, src, query, newName string
		wantLines                     []int
	}{
		{
			// E7: the attribute's string on the declaration line hid the
			// declaration from the plan.
			id: "E7-php-attribute-decl-line", file: "Svc.php", query: "Svc.act", newName: "perform",
			src: `<?php
class Svc {
    #[Route('/x')] public function act($n) {
        if ($n <= 0) { return 0; }
        return $this->act($n - 1);
    }
}
`,
			wantLines: []int{3, 5},
		},
		{
			// E8: "#" of a private field read as a comment start.
			id: "E8-js-private-field-line", file: "store.js", query: "Store.render", newName: "draw",
			src: `export class Store {
  #cache;
  render(n) { return n; }
  load() {
    this.#cache = this.render(1);
    return this.#cache;
  }
}
`,
			wantLines: []int{3, 5},
		},
		{
			id: "E8-kotlin-string-template", file: "V.kt", query: "V.label", newName: "tag",
			src: `class V {
    fun label(n: Int): String = "x" + n
    fun show(): String = "value=${label(3)}"
}
`,
			wantLines: []int{2, 3},
		},
		{
			id: "E8-swift-string-interpolation", file: "V.swift", query: "V.label", newName: "tag",
			src: `class V {
    func label(_ n: Int) -> String { return "x" }
    func show() -> String { return "value=\(label(3))" }
}
`,
			wantLines: []int{2, 3},
		},
		{
			id: "E8-php-string-interpolation", file: "Svc.php", query: "Svc.act", newName: "perform",
			src: `<?php
class Svc {
    public function act($n) { return $n; }
    public function show() { return "r={$this->act(2)}"; }
}
`,
			wantLines: []int{3, 4},
		},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			eng := openMemberFixture(t, map[string]string{tc.file: tc.src})
			r, err := eng.RenamePlan(context.Background(), tc.query, tc.newName)
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range tc.wantLines {
				if !hasEditLine(r.Edits, tc.file, line) {
					t.Errorf("rename %s: no confirmed edit on %s:%d\nedits=%v\nambiguous=%v\nunresolved=%v",
						tc.query, tc.file, line, editLines(r.Edits), editLines(r.Ambiguous), r.Unresolved)
				}
			}
		})
	}
}

// E9: a Go raw string holding a backslash (`\`) must not hide the receiver
// type of a later member access.
func TestCommentString_E9_ChangeImpactGoRawString(t *testing.T) {
	for _, form := range []struct{ name, lit string }{
		{"clean", `"\\"`},
		{"decoy", "`\\`"},
	} {
		t.Run(form.name, func(t *testing.T) {
			eng := openMemberFixture(t, map[string]string{
				"go.mod": "module example.com/p\n\ngo 1.21\n",
				"p.go": `package p

import "strings"

type Cfg struct{ Name string }

type Other struct{ Name string }

func mk() (*Cfg, error) { return &Cfg{}, nil }

func Path(p string) string {
	p = strings.ReplaceAll(p, ` + form.lit + `, "/")
	c, _ := mk()
	return c.Name + p
}
`})
			r, err := eng.ChangeImpact(context.Background(), "Cfg.Name")
			if err != nil {
				t.Fatal(err)
			}
			const site = "p.go:14"
			if got := accessLines(r.Accesses); !contains(got, site) {
				t.Errorf("%s (return c.Name + p) not receiver-typed: confirmed=%v ambiguous=%v",
					site, got, accessLines(r.AmbiguousAccesses))
				for _, a := range r.AmbiguousAccesses {
					t.Logf("  ambiguous %s:%d %s", a.FilePath, a.Line, a.Evidence)
				}
			}
		})
	}
}

// E4 (change-impact half): a multi-line base list must still put the
// subclass override in Base.method's family.
func TestCommentString_E4_ChangeImpactMultilineBases(t *testing.T) {
	for _, form := range []struct{ name, header string }{
		{"clean", "class Multi(Base,):"},
		{"decoy", "class Multi(\n    Base,  # the (old) Mixin\n):"},
	} {
		t.Run(form.name, func(t *testing.T) {
			eng := openMemberFixture(t, map[string]string{"a.py": `class Base:
    def method(self):
        return 0


` + form.header + `
    def method(self):
        return 1
`})
			r, err := eng.ChangeImpact(context.Background(), "Base.method")
			if err != nil {
				t.Fatal(err)
			}
			if !contains(symNames(r.Family), "a.py::Multi.method") {
				t.Errorf("family=%v, want a.py::Multi.method", symNames(r.Family))
			}
		})
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
