package parser

import (
	"os"
	"path/filepath"
	"testing"
)

func memberOccs(t *testing.T, files map[string]string, name string, langs ...string) map[string]string {
	t.Helper()
	root := t.TempDir()
	for p, body := range files {
		if err := os.WriteFile(filepath.Join(root, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	occs, skipped, err := NewEngine().MemberOccurrences(root, name, langs)
	if err != nil || skipped != 0 {
		t.Fatalf("scan: err=%v skipped=%d", err, skipped)
	}
	out := map[string]string{}
	for _, o := range occs {
		key := o.File + ":" + itoa(o.Line) + ":" + itoa(o.Col)
		desc := o.Form + "(" + o.Receiver + ")"
		if o.Write {
			desc += "w"
		}
		if o.Call {
			desc += "c"
		}
		out[key] = desc
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}

func expectOccs(t *testing.T, got, want map[string]string) {
	t.Helper()
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d occurrences, want %d: %v", len(got), len(want), got)
	}
}

// Shapes the classifier depends on: receiver text, literal types (including
// Go's elided inner literals), write/call flags, and positions that are NOT
// data-member references (method names, $locals, comments, strings).
func TestMemberOccurrenceShapes(t *testing.T) {
	expectOccs(t, memberOccs(t, map[string]string{"a.go": `package p
type T struct{ F int }
func (t *T) F2() { t.F = 1; _ = []T{{F: 2}}; _ = map[string]*T{"k": {F: 3}}; t.F++; g.F(); x := 1; _ = "F" }
// F in a comment
`}, "F", "go"), map[string]string{
		"a.go:2:15": "decl()",
		"a.go:3:21": "access(t)w",
		"a.go:3:37": "key(T)",
		"a.go:3:69": "key(T)",
		"a.go:3:79": "access(t)w",
		"a.go:3:86": "access(g)c",
	})
	expectOccs(t, memberOccs(t, map[string]string{"a.c": `typedef struct { int n; } T;
void f(T *t) { t->n = 1; T w = {.n = 2}; }
`}, "n", "c"), map[string]string{
		"a.c:1:21": "decl()",
		"a.c:2:18": "access(t)w",
		"a.c:2:33": "key(T)",
	})
	expectOccs(t, memberOccs(t, map[string]string{"a.php": `<?php
class A { const K = 1; protected $k; function m($k) { $this->k = self::K; $x->k(); return $k; } }
`}, "k", "php"), map[string]string{
		"a.php:2:34": "decl()",
		"a.php:2:61": "access($this)w",
	})
	expectOccs(t, memberOccs(t, map[string]string{"a.ts": `import { K } from "./k"
const o: Opts = { k: 1 } as any
function f(a: A) { return a.k + K }
`}, "k", "typescript"), map[string]string{
		"a.ts:2:18": "key(Opts)",
		"a.ts:3:28": "access(a)",
	})
	expectOccs(t, memberOccs(t, map[string]string{"a.py": `from mod import testing
class A:
    testing = False
    def m(self, testing):
        self.testing = testing
        A(testing=1)
`}, "testing", "python"), map[string]string{
		"a.py:1:16": "import(mod)",
		"a.py:3:4":  "decl()w",
		"a.py:4:16": "localdecl()",
		"a.py:5:13": "access(self)w",
		"a.py:5:23": "bare()",
		"a.py:6:10": "key(A)",
	})
}
