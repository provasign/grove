// Tests for per-language metadata extraction:
//   - Modifiers (public/private/static/async/abstract/pub/...)
//   - TypeParameters (generics)
//   - Annotations / decorators / attribute macros
//   - CallSites (AST-extracted call invocations inside a body)
package parser

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func bySymbol(t *testing.T, syms []core.SymbolRecord, name string) core.SymbolRecord {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not extracted; got %d symbols", name, len(syms))
	return core.SymbolRecord{}
}

func containsStr(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// ─── TypeScript ──────────────────────────────────────────────────────────────

func TestTSMetadata_ModifiersTypeParamsDecoratorsCallSites(t *testing.T) {
	src := `
@Injectable()
export class UserService<T extends Base, U> {
    private readonly cache: Map<string, T> = new Map();

    constructor(private readonly repo: Repository) {}

    @log
    public async save(user: T): Promise<void> {
        this.cache.set(user.id, user);
        await this.repo.persist(user);
    }
}
`
	syms, err := extractSymbolsFromString("typescript", "src/user.ts", src)
	if err != nil {
		t.Fatal(err)
	}

	cls := bySymbol(t, syms, "UserService")
	if cls.Kind != core.KindClass {
		t.Fatalf("UserService.Kind = %q, want class", cls.Kind)
	}
	if !containsStr(cls.TypeParameters, "T") || !containsStr(cls.TypeParameters, "U") {
		t.Errorf("UserService.TypeParameters = %v, want [T U]", cls.TypeParameters)
	}
	if !containsStr(cls.Annotations, "Injectable()") {
		t.Errorf("UserService.Annotations = %v, want Injectable()", cls.Annotations)
	}

	ctor := bySymbol(t, syms, "constructor")
	if ctor.Kind != core.KindConstructor {
		t.Errorf("constructor.Kind = %q, want constructor", ctor.Kind)
	}

	save := bySymbol(t, syms, "save")
	if !containsStr(save.Modifiers, "public") {
		t.Errorf("save.Modifiers = %v, want contains public", save.Modifiers)
	}
	if !containsStr(save.Modifiers, "async") {
		t.Errorf("save.Modifiers = %v, want contains async", save.Modifiers)
	}
	if !containsStr(save.Annotations, "log") {
		t.Errorf("save.Annotations = %v, want contains log", save.Annotations)
	}
	// CallSites: this.cache.set(...), this.repo.persist(...)
	gotCalls := map[string]bool{}
	for _, cs := range save.CallSites {
		gotCalls[cs.Callee] = true
	}
	if !gotCalls["set"] || !gotCalls["persist"] {
		t.Errorf("save.CallSites = %#v, want at least {set, persist}", save.CallSites)
	}
}

// ─── Python ──────────────────────────────────────────────────────────────────

func TestPythonMetadata_DecoratorsInitConstructorPrivate(t *testing.T) {
	src := `
class Cache:
    @staticmethod
    def make() -> "Cache":
        return Cache()

    def __init__(self, capacity):
        self.capacity = capacity
        self._evictions = 0

    def _bump(self):
        self._evictions += 1
`
	syms, err := extractSymbolsFromString("python", "cache.py", src)
	if err != nil {
		t.Fatal(err)
	}

	init := bySymbol(t, syms, "__init__")
	if init.Kind != core.KindConstructor {
		t.Errorf("__init__.Kind = %q, want constructor", init.Kind)
	}

	make := bySymbol(t, syms, "make")
	if !containsStr(make.Annotations, "staticmethod") {
		t.Errorf("make.Annotations = %v, want contains staticmethod", make.Annotations)
	}

	bump := bySymbol(t, syms, "_bump")
	if !containsStr(bump.Modifiers, "protected") {
		t.Errorf("_bump.Modifiers = %v, want protected", bump.Modifiers)
	}
	if len(bump.CallSites) == 0 {
		// _bump increments via assignment; no calls — that's OK
		t.Logf("note: _bump has no call sites (expected)")
	}
}

// ─── Java ────────────────────────────────────────────────────────────────────

func TestJavaMetadata_ModifiersGenericsAnnotationsFieldConstructor(t *testing.T) {
	src := `
package com.example;

public class Repo<T extends Entity> {

    @Inject
    private final Database db;

    public Repo(Database db) {
        this.db = db;
    }

    @Override
    public T find(String id) {
        return db.lookup(id);
    }
}
`
	syms, err := extractSymbolsFromString("java", "Repo.java", src)
	if err != nil {
		t.Fatal(err)
	}

	cls := bySymbol(t, syms, "Repo")
	if !containsStr(cls.Modifiers, "public") {
		t.Errorf("Repo.Modifiers = %v, want public", cls.Modifiers)
	}
	if !containsStr(cls.TypeParameters, "T") {
		t.Errorf("Repo.TypeParameters = %v, want T", cls.TypeParameters)
	}

	db := bySymbol(t, syms, "db")
	if db.Kind != core.KindField {
		t.Errorf("db.Kind = %q, want field", db.Kind)
	}
	if !containsStr(db.Modifiers, "private") || !containsStr(db.Modifiers, "final") {
		t.Errorf("db.Modifiers = %v, want private+final", db.Modifiers)
	}
	if !containsStr(db.Annotations, "Inject") {
		t.Errorf("db.Annotations = %v, want Inject", db.Annotations)
	}

	// Constructor: there are two symbols named "Repo" (class and ctor); find the constructor one.
	var ctor core.SymbolRecord
	for _, s := range syms {
		if s.Name == "Repo" && s.Kind == core.KindConstructor {
			ctor = s
			break
		}
	}
	if ctor.Kind != core.KindConstructor {
		t.Fatalf("constructor not extracted")
	}

	find := bySymbol(t, syms, "find")
	if !containsStr(find.Annotations, "Override") {
		t.Errorf("find.Annotations = %v, want Override", find.Annotations)
	}
	// CallSite: db.lookup(id) → lookup
	gotCallees := map[string]bool{}
	for _, cs := range find.CallSites {
		gotCallees[cs.Callee] = true
	}
	if !gotCallees["lookup"] {
		t.Errorf("find.CallSites = %#v, want contains lookup", find.CallSites)
	}
}

// ─── Rust ────────────────────────────────────────────────────────────────────

func TestRustMetadata_AttributesVisibilityNewIsConstructorFields(t *testing.T) {
	src := `
#[derive(Clone, Debug)]
pub struct Service<T> {
    pub name: String,
    cache: T,
}

impl<T> Service<T> {
    pub fn new(name: String, cache: T) -> Self {
        let cleaned = name.trim().to_string();
        Service { name: cleaned, cache }
    }
}
`
	syms, err := extractSymbolsFromString("rust", "lib.rs", src)
	if err != nil {
		t.Fatal(err)
	}

	st := bySymbol(t, syms, "Service")
	if !containsStr(st.Modifiers, "pub") {
		t.Errorf("Service.Modifiers = %v, want pub", st.Modifiers)
	}
	if !strings.Contains(strings.Join(st.Annotations, " "), "derive") {
		t.Errorf("Service.Annotations = %v, want derive(...)", st.Annotations)
	}

	name := bySymbol(t, syms, "name")
	if name.Kind != core.KindField {
		t.Errorf("name.Kind = %q, want field", name.Kind)
	}

	newFn := bySymbol(t, syms, "new")
	if newFn.Kind != core.KindConstructor {
		t.Errorf("new.Kind = %q, want constructor", newFn.Kind)
	}
	gotCallees := map[string]bool{}
	for _, cs := range newFn.CallSites {
		gotCallees[cs.Callee] = true
	}
	if !gotCallees["trim"] && !gotCallees["to_string"] {
		t.Errorf("new.CallSites = %#v, want at least one of {trim, to_string}", newFn.CallSites)
	}
}

// ─── Go ──────────────────────────────────────────────────────────────────────

func TestGoMetadata_CallSitesAndGenerics(t *testing.T) {
	src := `
package svc

func Map[T any, U any](xs []T, f func(T) U) []U {
    out := make([]U, 0, len(xs))
    for _, x := range xs {
        out = append(out, f(x))
    }
    return out
}
`
	syms, err := extractSymbolsFromString("go", "svc/svc.go", src)
	if err != nil {
		t.Fatal(err)
	}
	mp := bySymbol(t, syms, "Map")
	if !containsStr(mp.TypeParameters, "T") || !containsStr(mp.TypeParameters, "U") {
		t.Errorf("Map.TypeParameters = %v, want [T U]", mp.TypeParameters)
	}
	gotCallees := map[string]bool{}
	for _, cs := range mp.CallSites {
		gotCallees[cs.Callee] = true
	}
	if !gotCallees["make"] || !gotCallees["append"] || !gotCallees["len"] {
		t.Errorf("Map.CallSites = %#v, want {make, append, len}", mp.CallSites)
	}
}
