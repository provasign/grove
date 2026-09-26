package parser

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Declaration coverage: every language the capability manifest calls
// "precise" must index each kind of declaration it has, under the qualified
// name an agent would type (Type.member). Go struct fields went unindexed for
// four months while the manifest said "precise" -- nothing checked the claim
// against code, and the call graph never needed the symbols (2026-09-25).
//
// A kind a language genuinely does not index is listed in gaps with a reason.
// A gap that starts being indexed fails the test, so the list cannot go stale.

type declCase struct {
	file string
	src  string
	want map[string]core.SymbolKind // qualified name -> kind
	gaps map[string]string          // qualified name -> why it is not indexed
}

var declCoverage = map[string]declCase{
	"go": {
		file: "p.go",
		src: `package p

const Limit = 10

var Default = New()

var (
	ErrClosed = errors.New("closed")
	C, D      = 1, 2
)

const E, F = 3, 4

type ID = string

type Store interface {
	Get(k string) string
}

type Cache struct {
	sync.Mutex
	// Size is the entry cap.
	Size  int ` + "`json:\"size\"`" + `
	A, b  string
}

func New() *Cache { return &Cache{} }

func (c *Cache) Get(k string) string { return k }
`,
		want: map[string]core.SymbolKind{
			"Limit": core.KindConst, "Default": core.KindVariable, "Store": core.KindInterface,
			"Cache": core.KindStruct, "Cache.Size": core.KindField, "Cache.A": core.KindField,
			"Cache.b": core.KindField, "New": core.KindFunction, "Cache.Get": core.KindMethod,
			"ErrClosed": core.KindVariable, "C": core.KindVariable, "D": core.KindVariable,
			"E": core.KindConst, "F": core.KindConst, "ID": core.KindType, "Store.Get": core.KindMethod,
		},
	},
	"python": {
		file: "p.py",
		src: `LIMIT: int = 10
MAX_RETRIES = 3
square = lambda x: x * x

class Store:
    size: int = 5
    name = "x"

    def __init__(self, cap):
        self.cap = cap

    def get(self, k):
        return k

    class Inner:
        def run(self):
            pass

def make():
    return Store(1)
`,
		want: map[string]core.SymbolKind{
			"LIMIT": core.KindVariable, "Store": core.KindClass, "Store.size": core.KindField,
			"Store.name": core.KindField, "Store.get": core.KindMethod, "make": core.KindFunction,
			"MAX_RETRIES": core.KindVariable, "square": core.KindFunction, "Store.cap": core.KindField,
		},
	},
	"javascript": {
		file: "p.js",
		src: `export const LIMIT = 10;

export class Store {
  size = 5;
  constructor(cap) { this.cap = cap; }
  get(k) { return k; }
}

export function make() { return new Store(1); }
`,
		want: map[string]core.SymbolKind{
			"LIMIT": core.KindVariable, "Store": core.KindClass, "Store.size": core.KindField,
			"Store.get": core.KindMethod, "make": core.KindFunction,
		},
	},
	"typescript": {
		file: "p.ts",
		src: `export const LIMIT = 10;

export interface Getter { get(k: string): string; }

export enum Mode { Fast, Slow }

export type Key = string;

export class Store implements Getter {
  size: number = 5;
  constructor(private cap: number) {}
  get(k: string): string { return k; }
}

export function make(): Store { return new Store(1); }
`,
		want: map[string]core.SymbolKind{
			"LIMIT": core.KindVariable, "Getter": core.KindInterface, "Mode": core.KindEnum,
			"Key": core.KindType, "Store": core.KindClass, "Store.size": core.KindField,
			"Store.get": core.KindMethod, "make": core.KindFunction,
		},
	},
	"tsx": {
		file: "p.tsx",
		src: `export const LIMIT = 10;

export class Store {
  size: number = 5;
  get(k: string): string { return k; }
}

export function View(): JSX.Element { return <div />; }
`,
		want: map[string]core.SymbolKind{
			"LIMIT": core.KindVariable, "Store": core.KindClass, "Store.size": core.KindField,
			"Store.get": core.KindMethod, "View": core.KindFunction,
		},
	},
	"java": {
		file: "p/Store.java",
		src: `package p;

public class Store implements Getter {
    public static final int LIMIT = 10;
    private int size;

    public Store(int size) { this.size = size; }

    public String get(String k) { return k; }

    public enum Mode { FAST, SLOW }

    static class Inner { void run() {} }
}

interface Getter { int MAX = 3; String get(String k); }

@interface Audited { String value(); }

record Point(int x, int y) {
    Point { check(x); }
}
`,
		want: map[string]core.SymbolKind{
			"Store": core.KindClass, "Store.LIMIT": core.KindField, "Store.size": core.KindField,
			"Store.get": core.KindMethod, "Getter": core.KindInterface, "Getter.MAX": core.KindField,
			"Audited": core.KindAnnotation, "Audited.value": core.KindMethod,
			"Point.x": core.KindMethod, "Point.Point": core.KindConstructor,
		},
	},
	"rust": {
		file: "p.rs",
		src: `pub const LIMIT: usize = 10;

pub trait Getter { const CAP: usize; type Key; fn get(&self, k: &str) -> String; }

pub struct Store { pub size: usize }

pub enum Mode { Fast, Slow }

pub union Bits { i: u32, f: f32 }

macro_rules! my_vec { () => {}; }

mod net {
    pub struct Conn { addr: String }
    impl Conn { pub fn open() {} }
}

impl Store {
    pub const UNIT: u8 = 1;
    pub fn new(size: usize) -> Self { Store { size } }
}

impl Getter for Store {
    fn get(&self, k: &str) -> String { k.to_string() }
}

pub fn make() -> Store { Store::new(1) }
`,
		want: map[string]core.SymbolKind{
			// `fn new` is a constructor by convention; enum variants are
			// fields of the enum; items in an inline module keep their owner
			// type under the module prefix.
			"LIMIT": core.KindConst, "Getter": core.KindTrait, "Store": core.KindStruct,
			"Store.size": core.KindField, "Mode": core.KindEnum, "Store.new": core.KindConstructor,
			"make": core.KindFunction, "Mode.Fast": core.KindField, "Bits": core.KindStruct,
			"Bits.i": core.KindField, "my_vec": core.KindMacro, "net.Conn.addr": core.KindField,
			"net.Conn.open": core.KindMethod, "Store.UNIT": core.KindConst,
			"Getter.CAP": core.KindConst, "Getter.Key": core.KindType,
		},
	},
	"c": {
		file: "p.c",
		src: `#define LIMIT 10

struct store {
    int size;
    char *name;
};

static int count = 0;

typedef struct {
    double x, y;
} point;

int store_get(struct store *s) { return s->size; }
`,
		want: map[string]core.SymbolKind{
			"store": core.KindStruct, "store.size": core.KindField, "store.name": core.KindField,
			"store_get": core.KindFunction, "point": core.KindStruct, "count": core.KindVariable,
			"point.x": core.KindField, "point.y": core.KindField,
		},
	},
	"cpp": {
		file: "p.cpp",
		src: `namespace app {

static int registry_size = 0;

class Store {
public:
    Store(int size) : size_(size) {}
    int get() const { return size_; }
private:
    int size_;
};

int make() { return Store(1).get(); }

struct Pair { int first; int (*cb)(int); };

}
`,
		want: map[string]core.SymbolKind{
			// C++ qualified names use the language's own :: separator.
			"app::Store": core.KindClass, "app::Store::size_": core.KindField,
			"app::Store::get": core.KindMethod, "app::make": core.KindFunction,
			"app::Pair::first": core.KindField, "app::Pair::cb": core.KindField,
			"app::registry_size": core.KindVariable,
		},
	},
	"csharp": {
		file: "P.cs",
		src: `namespace App {
    public interface IGetter { string Get(string k); }

    public enum Mode { Fast, Slow }

    public class Store : IGetter {
        public const int Limit = 10;
        private int size;
        public int Size { get; set; }
        public Store(int size) { this.size = size; }
        public string Get(string k) { return k; }
        public event EventHandler Changed;
        public static Store operator +(Store a, Store b) => a;
        ~Store() { }
    }

    public record Person(string First, string Last);

    public delegate void Notify(string msg);
}
`,
		want: map[string]core.SymbolKind{
			"IGetter": core.KindInterface, "Mode": core.KindEnum, "Store": core.KindClass,
			"Store.size": core.KindField, "Store.Size": core.KindField, "Store.Get": core.KindMethod,
			"Mode.Fast": core.KindConst, "Store.Changed": core.KindField, "Store.operator +": core.KindMethod,
			"Store.Store": core.KindConstructor, "Store.~Store": core.KindMethod,
			"Person.First": core.KindField, "Notify": core.KindType,
		},
	},
	"php": {
		file: "p.php",
		src: `<?php
namespace App;

const LIMIT = 10;

interface Getter { public function get(string $k): string; }

class Store implements Getter {
    public int $size = 5;
    const MODE = 'fast';
    public function __construct(int $size) { $this->size = $size; }
    public function get(string $k): string { return $k; }
}

function make(): Store { return new Store(1); }
`,
		want: map[string]core.SymbolKind{
			"LIMIT": core.KindConst, "Store.MODE": core.KindConst,
			"Getter": core.KindInterface, "Store": core.KindClass, "Store.size": core.KindField,
			"Store.get": core.KindMethod, "make": core.KindFunction,
		},
	},
	"swift": {
		file: "P.swift",
		src: `let limit = 10

protocol Getter { func get(_ k: String) -> String }

enum Mode { case fast, slow }

struct Store: Getter {
    var size: Int
    init(size: Int) { self.size = size }
    func get(_ k: String) -> String { return k }
}

protocol Repo { associatedtype Item }

func make() -> Store { return Store(size: 1) }
`,
		want: map[string]core.SymbolKind{
			"Getter": core.KindInterface, "Mode": core.KindEnum, "Store": core.KindStruct,
			"Store.size": core.KindField, "Store.get": core.KindMethod, "make": core.KindFunction,
			"limit": core.KindConst, "Mode.fast": core.KindConst, "Store.init": core.KindConstructor,
			"Repo.Item": core.KindType,
		},
	},
	"kotlin": {
		file: "P.kt",
		src: `package app

const val LIMIT = 10

interface Getter { fun get(k: String): String }

enum class Mode { FAST, SLOW }

class Store(val cap: Int) : Getter {
    var size: Int = 5
    override fun get(k: String): String = k
}

typealias Callback = (String) -> Unit

fun interface Handler { fun handle(x: Int): Int }

object Registry { val count = 1 }

val defaultStore = Store(1)

fun make(): Store = Store(1)
`,
		want: map[string]core.SymbolKind{
			"Getter": core.KindInterface, "Mode": core.KindEnum, "Store": core.KindClass,
			"Store.size": core.KindField, "Store.get": core.KindMethod, "make": core.KindFunction,
			"LIMIT": core.KindConst, "Store.cap": core.KindField, "Mode.FAST": core.KindConst,
			"Callback": core.KindType, "Handler": core.KindInterface, "Handler.handle": core.KindMethod,
			"Registry.count": core.KindField, "defaultStore": core.KindVariable,
		},
	},
	"objc": {
		file: "P.m",
		src: `typedef NS_ENUM(NSInteger, Mode) { ModeFast, ModeSlow };

typedef void (^Handler)(int);

@protocol Getter
@required
- (NSString *)get:(NSString *)k;
@property (nonatomic, readonly) int limit;
@end

@interface Store : NSObject <Getter> {
    int _size;
}
@property (nonatomic) int size;
- (NSString *)get:(NSString *)k;
@end

@implementation Store
- (NSString *)get:(NSString *)k { return k; }
@end
`,
		want: map[string]core.SymbolKind{
			"Store": core.KindClass, "Store._size": core.KindField, "Store.get:": core.KindMethod,
			"Store.size": core.KindField, "Mode": core.KindEnum, "Mode.ModeFast": core.KindConst,
			"Handler": core.KindType, "Getter": core.KindInterface, "Getter.get:": core.KindMethod,
			"Getter.limit": core.KindField,
		},
	},
}

func TestDeclarationCoverage(t *testing.T) {
	engine := NewEngine()
	for _, lang := range core.CurrentCapabilities().Languages {
		if lang.Indexing != "precise" {
			continue
		}
		c, ok := declCoverage[lang.Language]
		if !ok {
			t.Errorf("%s: manifest says indexing is precise but there is no declaration-coverage fixture", lang.Language)
			continue
		}
		t.Run(lang.Language, func(t *testing.T) {
			syms, err := engine.ExtractContent(c.file, []byte(c.src))
			if err != nil {
				t.Fatalf("extract: %v", err)
			}
			got := map[string]core.SymbolKind{}
			for _, s := range syms {
				got[s.QualifiedName] = s.Kind
			}
			if os.Getenv("DECL_DUMP") != "" {
				keys := make([]string, 0, len(got))
				for k := range got {
					keys = append(keys, fmt.Sprintf("%s=%s", k, got[k]))
				}
				sort.Strings(keys)
				t.Logf("%s extracted: %s", lang.Language, strings.Join(keys, " "))
			}
			for qn, kind := range c.want {
				if reason, isGap := c.gaps[qn]; isGap {
					if _, found := got[qn]; found {
						t.Errorf("%s is now indexed; remove its gap entry (%s)", qn, reason)
					}
					continue
				}
				if k, found := got[qn]; !found {
					t.Errorf("%s not indexed (want %s)", qn, kind)
				} else if k != kind {
					t.Errorf("%s indexed as %s, want %s", qn, k, kind)
				}
			}
		})
	}
}
