package grove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Data-member change impact: fields, properties, class constants, and
// module/package variables. Before memberimpact.go every one of these
// queries returned the declaration alone (fields) or failed with a
// misleading "did you mean"/"declares no method" error (vars/consts).

func openMemberFixture(t *testing.T, files map[string]string) *Engine {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	for path, body := range files {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	disabled := false
	eng, err := Open(ctx, Config{RepoRoot: root, NativeAnalyzers: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { eng.Close() })
	if _, err := eng.Index(ctx, ""); err != nil {
		t.Fatal(err)
	}
	return eng
}

func accessLines(in []MemberAccess) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		out = append(out, fmt.Sprintf("%s:%d", a.FilePath, a.Line))
	}
	sort.Strings(out)
	return out
}

type memberCase struct {
	query     string
	confirmed []string
	ambiguous []string
	excluded  int // minimum
}

func checkMemberCases(t *testing.T, eng *Engine, cases []memberCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			r, err := eng.ChangeImpact(context.Background(), tc.query)
			if err != nil {
				t.Fatalf("change-impact %s: %v", tc.query, err)
			}
			if r.MemberKind == "" {
				t.Fatalf("%s: not answered as a data member: %+v", tc.query, r)
			}
			sort.Strings(tc.confirmed)
			sort.Strings(tc.ambiguous)
			if got := accessLines(r.Accesses); strings.Join(got, " ") != strings.Join(tc.confirmed, " ") {
				t.Errorf("confirmed = %v\nwant        %v\nambiguous = %v", got, tc.confirmed, accessLines(r.AmbiguousAccesses))
			}
			if got := accessLines(r.AmbiguousAccesses); strings.Join(got, " ") != strings.Join(tc.ambiguous, " ") {
				t.Errorf("ambiguous = %v, want %v", got, tc.ambiguous)
				for _, a := range r.AmbiguousAccesses {
					t.Logf("  %s:%d %s", a.FilePath, a.Line, a.Evidence)
				}
			}
			if r.ExcludedAccesses < tc.excluded {
				t.Errorf("excluded = %d, want >= %d", r.ExcludedAccesses, tc.excluded)
			}
			for _, a := range r.AmbiguousAccesses {
				if a.Evidence == "" {
					t.Errorf("ambiguous %s:%d carries no reason", a.FilePath, a.Line)
				}
			}
			wantCov := "receiver-typed"
			if len(tc.ambiguous) > 0 {
				wantCov = "partial"
			}
			if r.AccessCoverage != wantCov {
				t.Errorf("accessCoverage = %q, want %q", r.AccessCoverage, wantCov)
			}
		})
	}
}

const goMemberSource = `package p

type errorMsgs []string

func (a errorMsgs) Errors() []string { return a }

type Context struct {
	Errors errorMsgs
	Keys   map[string]any
}

type Params struct{ Keys []string }

type Engine struct {
	Context
	RedirectTrailingSlash bool
}

func New() *Engine {
	return &Engine{RedirectTrailingSlash: true}
}

func (c *Context) reset() { c.Errors = c.Errors[:0] }

func (c *Context) allocate() *Context { return c }

func use(e *Engine, p Params) []string {
	c := e.allocate()
	_ = c.Errors.Errors()
	_ = e.Errors
	_ = p.Keys
	cs := []Context{{Errors: nil}}
	return cs[0].Errors.Errors()
}

var DefaultWriter = 1

func w() int { DefaultWriter = 2; return DefaultWriter }

func shadow(DefaultWriter int) int { return DefaultWriter }
`

func TestMemberImpactGo(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{"go.mod": "module example.com/p\n\ngo 1.22\n", "p.go": goMemberSource})
	checkMemberCases(t, eng, []memberCase{
		// line 29 holds the field AND the errorMsgs.Errors() call: the
		// field occurrence confirms it, the method call never counts.
		{query: "Context.Errors", confirmed: []string{"p.go:8", "p.go:23", "p.go:29", "p.go:30", "p.go:32"}, ambiguous: []string{"p.go:33"}},
		{query: "Context.Keys", confirmed: []string{"p.go:9"}, excluded: 1},
		{query: "Engine.RedirectTrailingSlash", confirmed: []string{"p.go:16", "p.go:20"}},
		{query: "DefaultWriter", confirmed: []string{"p.go:36", "p.go:38"}, excluded: 1},
	})
}

func TestMemberImpactJava(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"Base.java": `class Base {
  protected boolean flag;
  static final int LIMIT = 3;
  Base(Base src) { this.flag = src.flag; }
  boolean get() { return flag; }
}
`,
		"Sub.java": `class Sub extends Base {
  Sub(Sub s) { super(s); }
  boolean f(Other o) { return flag && o.flag; }
  int g() { return Base.LIMIT + LIMIT; }
  void h() { boolean flag = true; use(flag); }
  void use(boolean b) {}
}
`,
		"Other.java": `class Other {
  boolean flag;
  static final int LIMIT = 9;
  boolean isOn() { return flag || Other.LIMIT > 0; }
}
`,
	})
	checkMemberCases(t, eng, []memberCase{
		// Sub.java:3 also holds o.flag (Other's field): the line is
		// confirmed by the bare flag; Other.java:4 and the shadowed
		// local in Sub.h are excluded.
		{query: "Base.flag", confirmed: []string{"Base.java:2", "Base.java:4", "Base.java:5", "Sub.java:3"}, excluded: 2},
		{query: "Base.LIMIT", confirmed: []string{"Base.java:3", "Sub.java:4"}, excluded: 1},
	})
}

func TestMemberImpactC(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"a.h": `typedef struct { int entries; } arr_t;
typedef struct { int entries; } other_t;
#define to_arr(x) container_of(x, arr_t, base)
`,
		"a.c": `#include "a.h"
static int counter = 0;
void f(arr_t *a, other_t *o, void *j) {
  a->entries = 1;
  counter++;
  o->entries = 2;
  arr_t v = {.entries = 3};
  int n = to_arr(j)->entries;
}
int g(int counter) { return counter; }
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "arr_t.entries", confirmed: []string{"a.h:1", "a.c:4", "a.c:7", "a.c:8"}, excluded: 1},
		{query: "counter", confirmed: []string{"a.c:2", "a.c:5"}, excluded: 1},
	})
}

func TestMemberImpactPython(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"app.py": `class App:
    testing = False

    def run(self):
        return self.testing


class Other:
    testing = True


def make() -> App:
    return App()


def check(o: Other, anything):
    x = make().testing
    y = anything.testing
    return o.testing
`,
		"tests/conftest.py": `import pytest
from app import App


@pytest.fixture
def app():
    a = App()
    return a
`,
		"tests/test_app.py": `def test_x(app):
    app.testing = True
`,
		"model.py": `class Model:
    def __init__(self):
        self.config = {}

    def get(self):
        return self.config
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "App.testing", confirmed: []string{"app.py:2", "app.py:5", "app.py:17", "tests/test_app.py:2"},
			ambiguous: []string{"app.py:18"}, excluded: 1},
		// An instance attribute assigned in __init__ has no symbol of its
		// own; it is still a data member, not "declares no method config".
		{query: "Model.config", confirmed: []string{"model.py:3", "model.py:6"}},
	})
}

func TestMemberImpactTypeScript(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"consts.ts": `export const LIMIT = 3
const HIDDEN = 1
export function f() { return HIDDEN + LIMIT }
`,
		"driver.ts": `export class Driver {
  isReplicated: boolean = false
  connect() { this.isReplicated = true }
}
export class Sub extends Driver {
  go() { return this.isReplicated }
}
export class Other {
  isReplicated = 1
}
`,
		"use.ts": `import { LIMIT } from "./consts"
import { Driver, Other } from "./driver"
export function g(d: Driver, o: Other) {
  const typed: Driver = { isReplicated: true } as any
  const loose = { isReplicated: false }
  return d.isReplicated || o.isReplicated || LIMIT
}
function h(LIMIT: number) { return LIMIT }
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "Driver.isReplicated", confirmed: []string{"driver.ts:2", "driver.ts:3", "driver.ts:6", "use.ts:4", "use.ts:6"}, excluded: 1},
		{query: "LIMIT", confirmed: []string{"consts.ts:1", "consts.ts:3", "use.ts:6"}, excluded: 1},
		{query: "HIDDEN", confirmed: []string{"consts.ts:2", "consts.ts:3"}},
	})
}

func TestMemberImpactPHP(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"src/Constraint.php": `<?php
class Constraint {
    const OP_EQ = 0;
    protected $operator;
    public function __construct($operator) { $this->operator = $operator; }
    public function m(Constraint $p) { return $p->operator === self::OP_EQ; }
}
`,
		"src/Other.php": `<?php
class Other { const OP_EQ = 1; public $operator; }
function g(Other $o) { return $o->operator . Constraint::OP_EQ . Other::OP_EQ; }
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "Constraint.OP_EQ", confirmed: []string{"src/Constraint.php:3", "src/Constraint.php:6", "src/Other.php:3"}},
		{query: "Constraint.operator", confirmed: []string{"src/Constraint.php:4", "src/Constraint.php:5", "src/Constraint.php:6"}, excluded: 1},
	})
}

// A same-named method keeps the method answer; a missing member still says
// so, now without claiming only methods were considered.
func TestMemberImpactKeepsMethodsAndReportsMissing(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{"go.mod": "module example.com/p\n\ngo 1.22\n", "p.go": goMemberSource})
	r, err := eng.ChangeImpact(context.Background(), "errorMsgs.Errors")
	if err != nil {
		t.Fatal(err)
	}
	if r.MemberKind != "" || len(r.Declarations) != 1 || r.Declarations[0].Kind != "method" {
		t.Fatalf("method query answered as data member: %+v", r)
	}
	_, err = eng.ChangeImpact(context.Background(), "Context.Nope")
	if err == nil || !strings.Contains(err.Error(), "declares no method or data member") {
		t.Fatalf("missing member error = %v", err)
	}
}

// Two package-level variables of one name in different scopes are two
// variables: the query must say so, not merge their readers.
func TestMemberImpactOwnerlessAmbiguity(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"a.c": "static int shared = 1;\nint fa(void) { return shared; }\n",
		"b.c": "static int shared = 2;\nint fb(void) { return shared; }\n",
	})
	_, err := eng.ChangeImpact(context.Background(), "shared")
	if err == nil || !strings.Contains(err.Error(), "is ambiguous") || !strings.Contains(err.Error(), "file=") {
		t.Fatalf("expected an ambiguity error naming file=, got %v", err)
	}
	r, err := eng.ChangeImpactScoped(context.Background(), "shared", "b.c")
	if err != nil {
		t.Fatal(err)
	}
	if got := accessLines(r.Accesses); strings.Join(got, " ") != "b.c:1 b.c:2" {
		t.Fatalf("file-scoped accesses = %v", got)
	}
}

// Rename plans for data members rewrite exactly the classified columns,
// include the declaration, and report counts that match their buckets.
func TestMemberRenamePlan(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{"go.mod": "module example.com/p\n\ngo 1.22\n", "p.go": goMemberSource})
	plan, err := eng.RenamePlan(context.Background(), "Context.Errors", "Errs")
	if err != nil {
		t.Fatal(err)
	}
	if plan.SitesTotal != len(plan.Edits)+len(plan.Ambiguous)+len(plan.Unresolved) {
		t.Fatalf("SitesTotal %d != %d edits + %d ambiguous + %d unresolved", plan.SitesTotal, len(plan.Edits), len(plan.Ambiguous), len(plan.Unresolved))
	}
	if len(plan.Unresolved) != 0 {
		t.Fatalf("unresolved = %v", plan.Unresolved)
	}
	after := map[int]string{}
	for _, e := range plan.Edits {
		after[e.Line] = strings.TrimSpace(e.After)
	}
	for line, want := range map[int]string{
		8:  "Errs errorMsgs",
		29: "_ = c.Errs.Errors()",
		32: "cs := []Context{{Errs: nil}}",
	} {
		if after[line] != want {
			t.Errorf("line %d after = %q, want %q", line, after[line], want)
		}
	}
	if len(plan.Ambiguous) != 1 || plan.Ambiguous[0].Line != 33 || plan.Ambiguous[0].Reason == "" {
		t.Fatalf("ambiguous = %+v", plan.Ambiguous)
	}
}

func TestMemberImpactCSharpRustCppKotlinSwift(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"Account.cs": `class Account {
    public int Balance;
    const int Limit = 5;
    public void Add(Account other) { this.Balance += other.Balance; Balance = Limit; }
}
class Ledger {
    public int Balance;
    void M(Account a, Ledger l) {
        a.Balance = 1;
        l.Balance = 2;
        var n = new Account { Balance = 1 };
    }
}
`,
		"lib.rs": `struct Point { x: i32 }
struct Other { x: i32 }
static ORIGIN: i32 = 0;
fn shift(p: &mut Point, o: &Other) -> i32 {
    p.x = ORIGIN;
    let y = o.x;
    let q = Point { x: 1 };
    q.x
}
`,
		"shape.cpp": `struct Shape {
    int sides;
    static const int MAX = 8;
    int get();
};
struct Box { int sides; };
int Shape::get() { return sides + this->sides + Shape::MAX; }
int use(Shape *s, Box *b) {
    int n = s->sides;
    return n + b->sides;
}
`,
		"Car.kt": `class Car(val wheels: Int) {
    var speed = 0
    fun go(other: Car) { this.speed = other.speed + 1; speed = 2 }
}
`,
		"Bird.swift": `class Bird {
    var altitude = 0
    func climb(other: Bird) { self.altitude = other.altitude + 1 }
}
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "Account.Balance", confirmed: []string{"Account.cs:2", "Account.cs:4", "Account.cs:9", "Account.cs:11"}, excluded: 1},
		{query: "Account.Limit", confirmed: []string{"Account.cs:3", "Account.cs:4"}},
		{query: "Point.x", confirmed: []string{"lib.rs:1", "lib.rs:5", "lib.rs:7", "lib.rs:8"}, excluded: 1},
		{query: "ORIGIN", confirmed: []string{"lib.rs:3", "lib.rs:5"}},
		{query: "Shape.sides", confirmed: []string{"shape.cpp:2", "shape.cpp:7", "shape.cpp:9"}, excluded: 1},
		{query: "Shape.MAX", confirmed: []string{"shape.cpp:3", "shape.cpp:7"}},
		{query: "Car.speed", confirmed: []string{"Car.kt:2", "Car.kt:3"}},
		{query: "Bird.altitude", confirmed: []string{"Bird.swift:2", "Bird.swift:3"}},
	})
}

// Receiver shapes found on held-out repos after the first pass: Go
// index-then-call (tree-sitter reads it as a generic conversion), Go tuple
// results, C chains through struct fields and variables named like struct
// tags, TS call results / for-of elements / inline-object return types.
func TestMemberImpactReceiverShapes(t *testing.T) {
	eng := openMemberFixture(t, map[string]string{
		"go.mod": "module example.com/q\n\ngo 1.22\n",
		"q.go": `package q

type Context struct {
	index    int8
	handlers []func(*Context)
}
type Engine struct{ trees []int }

func CreateTestContext() (c *Context, r *Engine) { return nil, nil }
func (c *Context) Next() { c.handlers[c.index](c) }
func T() {
	c, router := CreateTestContext()
	_ = router.trees
	_ = c
}
`,
		"h.c": `typedef struct hashtable { int size; } hashtable_t;
typedef struct { hashtable_t table; } obj_t;
typedef struct { int size; } buf_t;
int f(obj_t *object) { return object->table.size; }
int g(buf_t *buf) { return buf->size; }
int h(hashtable_t *hashtable) { return hashtable->size; }
`,
		"m.ts": `export class Meta { tableName: string = "" }
export class Svc {
  metas: Meta[] = []
  get(): Meta { return new Meta() }
  parse(): { tableName: string } { return { tableName: "" } }
  all() { for (const x of this.metas) { use(x.tableName) } }
}
export async function f(s: Svc) {
  const m = s.get()
  const p = s.parse()
  use(m.tableName)
  use(p.tableName)
}
function use(v: any) {}
`,
	})
	checkMemberCases(t, eng, []memberCase{
		{query: "Context.index", confirmed: []string{"q.go:4", "q.go:10"}},
		{query: "Engine.trees", confirmed: []string{"q.go:7", "q.go:13"}},
		{query: "hashtable_t.size", confirmed: []string{"h.c:1", "h.c:4", "h.c:6"}, excluded: 1},
		{query: "Meta.tableName", confirmed: []string{"m.ts:1", "m.ts:6", "m.ts:11"}, excluded: 1},
	})
}
