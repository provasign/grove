package invariance

// Group L: grove internal/graph local-type inference reads comments and
// strings as declarations/assignments.

const goMod = "module example.com/p\n\ngo 1.21\n"

func one(path, body string) map[string]string { return map[string]string{path: body} }

// with returns a copy of base with files replaced/added.
func with(base map[string]string, kv ...string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i]] = kv[i+1]
	}
	return out
}

func casesL() []twinCase {
	return []twinCase{
		{
			id: "L1-python",
			clean: one("app.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


def work():
    print("it was removed")
    client = Foo()
    return client.go()
`),
			decoy: one("app.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


def work():
    print('client = Bar() was removed')
    client = Foo()
    return client.go()
`),
			want:    []string{"E work calls Foo.go"},
			wantNot: []string{"E work calls Bar.go"},
		},
		{
			id: "L1-ts",
			clean: one("app.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export function work() {
  const note = "it was removed";
  const client = new Foo();
  return client.go();
}
`),
			decoy: one("app.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export function work() {
  const note = 'client = new Bar() was removed';
  const client = new Foo();
  return client.go();
}
`),
			want:    []string{"E work calls Foo.go"},
			wantNot: []string{"E work calls Bar.go"},
		},
		{
			id: "L1-php",
			clean: one("app.php", `<?php
class Foo { public function go() { return 1; } }
class Bar { public function go() { return 2; } }
class Svc {
    public function work() {
        echo 1;
        $client = new Foo();
        return $client->go();
    }
}
`),
			decoy: one("app.php", `<?php
class Foo { public function go() { return 1; } }
class Bar { public function go() { return 2; } }
class Svc {
    public function work() {
        echo 'client = new Bar() was removed';
        $client = new Foo();
        return $client->go();
    }
}
`),
			want:    []string{"E Svc.work calls Foo.go"},
			wantNot: []string{"E Svc.work calls Bar.go"},
		},
		{
			id: "L2-rust-rawstring",
			clean: map[string]string{
				"Cargo.toml": "[package]\nname = \"p\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"src/lib.rs": `pub struct Foo;
impl Foo {
    pub fn new() -> Foo { Foo }
    pub fn go(&self) -> i32 { 1 }
}
pub struct Bar;
impl Bar {
    pub fn new() -> Bar { Bar }
    pub fn go(&self) -> i32 { 2 }
}
pub fn work() -> i32 {
    let client = Foo::new();
    let _fixture = "";
    client.go()
}
`},
			decoy: map[string]string{
				"Cargo.toml": "[package]\nname = \"p\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"src/lib.rs": `pub struct Foo;
impl Foo {
    pub fn new() -> Foo { Foo }
    pub fn go(&self) -> i32 { 1 }
}
pub struct Bar;
impl Bar {
    pub fn new() -> Bar { Bar }
    pub fn go(&self) -> i32 { 2 }
}
pub fn work() -> i32 {
    let client = Foo::new();
    let _fixture = r#"
let client = Bar::new();
"#;
    client.go()
}
`},
			want:    []string{"E work calls Foo.go"},
			wantNot: []string{"E work calls Bar.go"},
		},
		{
			id: "L2-python-docstring",
			clean: one("svc.py", `class Real:
    def go(self):
        return 0


class Foo:
    def go(self):
        return 1


class Redis:
    def go(self):
        return 2


class Svc:
    def __init__(self):
        """Set up the service."""
        self.x = Real()
        self.cache = Real()

    def run(self):
        return self.x.go()

    def run2(self):
        return self.cache.go()
`),
			decoy: one("svc.py", `class Real:
    def go(self):
        return 0


class Foo:
    def go(self):
        return 1


class Redis:
    def go(self):
        return 2


class Svc:
    def __init__(self):
        """He said "hi.
        self.x = Foo()
        """
        '''Don't do this: self.cache = Redis()'''
        self.x = Real()
        self.cache = Real()

    def run(self):
        return self.x.go()

    def run2(self):
        return self.cache.go()
`),
			want:    []string{"E Svc.run calls Real.go", "E Svc.run2 calls Real.go"},
			wantNot: []string{"E Svc.run calls Foo.go", "E Svc.run2 calls Redis.go"},
		},
		{
			// Shape 1 (a local in another method leaks into b) is checked by
			// want/wantNot; shape 2 (a commented-out declaration) by the twin.
			id: "L3-java",
			clean: one("src/p/Svc.java", `package p;
class Foo { void run() {} }
class Bar { void run() {} }
class Svc {
  void a() { Foo helper = new Foo(); helper.run(); }
  private Bar helper;
  void b() { helper.run(); }
}
`),
			decoy: one("src/p/Svc.java", `package p;
class Foo { void run() {} }
class Bar { void run() {} }
class Svc {
  void a() { Foo helper = new Foo(); helper.run(); }
  /*
    Foo helper = null;
  */
  private Bar helper;
  void b() { helper.run(); }
}
`),
			want:    []string{"E Svc.b calls Bar.run", "E Svc.a calls Foo.run"},
			wantNot: []string{"E Svc.b calls Foo.run"},
		},
		{
			id: "L4-csharp",
			clean: one("Svc.cs", `namespace P {
class Foo { public void Go() {} }
class Bar { public void Go() {} }
class Svc {
  void A() { Foo helper = new Foo(); helper.Go(); }
  private Bar helper;
  void B() { helper.Go(); }
}
}
`),
			decoy: one("Svc.cs", `namespace P {
class Foo { public void Go() {} }
class Bar { public void Go() {} }
class Svc {
  void A() { Foo helper = new Foo(); helper.Go(); }
  /*
    Foo helper = null;
  */
  private Bar helper;
  void B() { helper.Go(); }
}
}
`),
			want:    []string{"E Svc.B calls Bar.Go", "E Svc.A calls Foo.Go"},
			wantNot: []string{"E Svc.B calls Foo.Go"},
		},
		{
			id: "L5-cpp-header",
			clean: one("svc.hpp", `struct Foo { int go() { return 1; } };
struct Bar { int go() { return 2; } };
struct Svc {
  int a() { Foo helper; return helper.go(); }
  int b() { return helper.go(); }
private:
  Bar helper;
};
`),
			decoy: one("svc.hpp", `struct Foo { int go() { return 1; } };
struct Bar { int go() { return 2; } };
struct Svc {
  int a() { Foo helper; return helper.go(); }
  int b() { return helper.go(); }
private:
  /*
    Foo helper;
  */
  Bar helper;
};
`),
			want:    []string{"E Svc.b calls Bar.go", "E Svc.a calls Foo.go"},
			wantNot: []string{"E Svc.b calls Foo.go"},
		},
		{
			id: "L6-python-numpy-docstring",
			clean: one("svc.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


class Svc:
    """Service."""

    def __init__(self):
        self.client = Foo()

    def run(self):
        return self.client.go()
`),
			decoy: one("svc.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


class Svc:
    """Service.

    Attributes
    ----------
    client : Bar
        The old client.
    """

    def __init__(self):
        self.client = Foo()

    def run(self):
        return self.client.go()
`),
			want:    []string{"E Svc.run calls Foo.go"},
			wantNot: []string{"E Svc.run calls Bar.go"},
		},
		{
			// A method-local annotated variable must not type self.tmp.
			id: "L6-python-method-local",
			clean: one("svc.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


class Svc:
    def __init__(self):
        self.tmp = Foo()

    def helper(self):
        tmp: Bar = Bar()
        return tmp

    def run(self):
        return self.tmp.go()
`),
			decoy: one("svc.py", `class Foo:
    def go(self):
        return 1


class Bar:
    def go(self):
        return 2


class Svc:
    def __init__(self):
        # self.tmp: Bar
        self.tmp = Foo()

    def helper(self):
        tmp: Bar = Bar()
        return tmp

    def run(self):
        return self.tmp.go()
`),
			want:    []string{"E Svc.run calls Foo.go"},
			wantNot: []string{"E Svc.run calls Bar.go"},
		},
		{
			id: "L7-ts",
			clean: one("svc.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export class Svc {
  items: Set<Foo> = new Set();
  run() {
    this.items.forEach((x) => x.go());
  }
}
`),
			decoy: one("svc.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export class Svc {
  items: Set<Foo> = new Set();
  run() {
    // items: Set<Bar> was the old shape
    this.items.forEach((x) => x.go());
  }
}
`),
			want:    []string{"E Svc.run calls Foo.go"},
			wantNot: []string{"E Svc.run calls Bar.go"},
		},
		{
			id: "L8-ts",
			clean: one("svc.ts", `export class Cache { fetch(k: string) { return k; } }
export class Store { fetch(k: string) { return k; } }
export class Inner { cache: Cache = new Cache(); store: Store = new Store(); }
export class Svc {
  inner: Inner = new Inner();
  get(k: string) {
    return this.inner.store.fetch("k");
  }
}
`),
			decoy: one("svc.ts", `export class Cache { fetch(k: string) { return k; } }
export class Store { fetch(k: string) { return k; } }
export class Inner { cache: Cache = new Cache(); store: Store = new Store(); }
export class Svc {
  inner: Inner = new Inner();
  get(k: string) {
    // prefer this.inner.cache.fetch(k) once warmed
    return this.inner.store.fetch("k");
  }
}
`),
			want:    []string{"E Svc.get calls Store.fetch"},
			wantNot: []string{"E Svc.get calls Cache.fetch"},
		},
		{
			id: "L9-php-extends-comment",
			clean: one("dog.php", `<?php
interface Loud { public function speak(); }
class Base { public function speak() { return 1; } }
class Dog extends Base {
    public function run() { return $this->speak(); }
}
`),
			decoy: one("dog.php", `<?php
interface Loud { public function speak(); }
class Base { public function speak() { return 1; } }
class Dog extends Base { // We no longer use Loud; it was too noisy.
    public function run() { return $this->speak(); }
}
`),
			want:    []string{"E Dog.run calls Base.speak", "E Dog extends Base"},
			wantNot: []string{"E Dog implements Loud", "E Dog.run calls Loud.speak"},
		},
		{
			id: "L9-php-property-comment",
			clean: one("svc.php", `<?php
class Foo { public function go() { return 1; } }
class Bar { public function go() { return 2; } }
class Svc {
    private Foo $client;
    public function run() { return $this->client->go(); }
}
`),
			decoy: one("svc.php", `<?php
class Foo { public function go() { return 1; } }
class Bar { public function go() { return 2; } }
class Svc {
    // private Bar $client;  (old)
    private Foo $client;
    public function run() { return $this->client->go(); }
}
`),
			want:    []string{"E Svc.run calls Foo.go"},
			wantNot: []string{"E Svc.run calls Bar.go"},
		},
		{
			id: "L10-ts-default-string",
			clean: one("p.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export function angle(open: string = "x", client: Foo) { return client.go(); }
export function paren(close: string = "x", client: Foo) { return client.go(); }
`),
			decoy: one("p.ts", `export class Foo { go() { return 1; } }
export class Bar { go() { return 2; } }
export function angle(open: string = "<", client: Foo) { return client.go(); }
export function paren(close: string = ")", client: Foo) { return client.go(); }
`),
			want:    []string{"E angle calls Foo.go", "E paren calls Foo.go"},
			wantNot: []string{"E angle calls Bar.go", "E paren calls Bar.go"},
		},
		{
			id: "L11-go-struct-comment",
			clean: map[string]string{"go.mod": goMod, "p.go": `package p

type Foo struct{}

func (Foo) Go() int { return 1 }

type Bar struct{}

func (Bar) Go() int { return 2 }

type Svc struct {
	client *Foo
}

func (s *Svc) Run() int { return s.client.Go() }
`},
			decoy: map[string]string{"go.mod": goMod, "p.go": `package p

type Foo struct{}

func (Foo) Go() int { return 1 }

type Bar struct{}

func (Bar) Go() int { return 2 }

type Svc struct {
	client *Foo
	/*
		client Bar
	*/
}

func (s *Svc) Run() int { return s.client.Go() }
`},
			want:    []string{"E Svc.Run calls Foo.Go"},
			wantNot: []string{"E Svc.Run calls Bar.Go"},
		},
	}
}
