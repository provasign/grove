package invariance

// Group N: grove internal/native semantic passes read comments and strings
// as type references / clauses. The bugs live in the native layer, so the
// "native" mode is the one that proves them; "astkit" mode asserts the same
// facts hold without native (they must: comments are never code).

const phpComposer = `{"autoload":{"psr-4":{"App\\":"src/"}}}`

func casesN() []twinCase {
	return []twinCase{
		{
			id: "N1-go-comment-types",
			clean: map[string]string{"go.mod": goMod, "p.go": `package p

type Widget struct{}
type Gadget struct{}

func Use() int {
	return 1
}
`},
			decoy: map[string]string{"go.mod": goMod, "p.go": `package p

type Widget struct{}
type Gadget struct{}

func Use() int { // returns a Widget count; Gadget is unused here
	return 1
}
`},
			want:    []string{"S Use"},
			wantNot: []string{"E Use uses-type Widget", "E Use uses-type Gadget"},
		},
		{
			id: "N1-java-comment-types",
			clean: one("src/p/A.java", `package p;
class Fake {}
class Other {}
public class A {
  int m() {
    return 1;
  }
}
`),
			decoy: one("src/p/A.java", `package p;
class Fake {}
class Other {}
public class A {
  int m() {
    // keep Fake around; new Other() is created lazily
    return 1;
  }
}
`),
			want:    []string{"S A.m"},
			wantNot: []string{"E A.m uses-type Fake", "E A.m uses-type Other", "E A.m calls Other"},
		},
		{
			id: "N1-csharp-comment-types",
			clean: one("A.cs", `namespace P {
class Fake {}
public class A {
  int M() {
    return 1;
  }
}
}
`),
			decoy: one("A.cs", `namespace P {
class Fake {}
public class A {
  int M() {
    // TODO: new Fake() later
    return 1;
  }
}
}
`),
			want:    []string{"S A.M"},
			wantNot: []string{"E A.M uses-type Fake", "E A.M calls Fake"},
		},
		{
			id: "N1-php-comment-types",
			clean: map[string]string{"composer.json": phpComposer,
				"src/Loggable.php": "<?php\nnamespace App;\ninterface Loggable { public function log(); }\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        return 1;
    }
}
`},
			decoy: map[string]string{"composer.json": phpComposer,
				"src/Loggable.php": "<?php\nnamespace App;\ninterface Loggable { public function log(); }\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        // we no longer use Loggable;
        return 1;
    }
}
`},
			want:    []string{"S A.run"},
			wantNot: []string{"E A.run uses-type Loggable", "E A uses-type Loggable"},
		},
		{
			id: "N2-csharp-apostrophe",
			clean: one("A.cs", `namespace P {
class Real {}
public class A {
  void M() {
    // do not use Real yet
    var r = new Real(); var c = 'x';
  }
}
}
`),
			decoy: one("A.cs", `namespace P {
class Real {}
public class A {
  void M() {
    // don't use Real yet
    var r = new Real(); var c = 'x';
  }
}
}
`),
			want:       []string{"S A.M"},
			wantNative: []string{"E A.M uses-type Real"},
		},
		{
			id: "N2-java-apostrophe",
			clean: one("src/p/A.java", `package p;
class Base {}
public class A {
  void m() {
    // do not touch this
    Base b = null; char c = 'x';
  }
}
`),
			decoy: one("src/p/A.java", `package p;
class Base {}
public class A {
  void m() {
    // don't touch this
    Base b = null; char c = 'x';
  }
}
`),
			want:       []string{"S A.m"},
			wantNative: []string{"E A.m uses-type Base"},
		},
		{
			id: "N3-java-extends-comment",
			clean: one("src/p/A.java", `package p;
class Base {}
public class A {
  void m() {}
}
`),
			decoy: one("src/p/A.java", `package p;
class Base {}
public class A { // extends Base
  void m() {}
}
`),
			want:    []string{"S A"},
			wantNot: []string{"E A extends Base", "E A uses-type Base"},
		},
		{
			id:             "N4-csharp-attribute-string-bases",
			rawAnnotations: true,
			clean: one("A.cs", `using System.ComponentModel;
namespace P {
class Fake {}
interface IOther {}
[Description("example")] public class A {
  void M() {}
}
}
`),
			decoy: one("A.cs", `using System.ComponentModel;
namespace P {
class Fake {}
interface IOther {}
[Description("e.g. class Y : Fake, IOther")] public class A {
  void M() {}
}
}
`),
			want:    []string{"S A"},
			wantNot: []string{"E A extends Fake", "E A implements IOther", "E A uses-type Fake", "E A extends IOther"},
		},
		{
			id: "N5-php-implements-comment",
			clean: map[string]string{"composer.json": phpComposer,
				"src/Loggable.php": "<?php\nnamespace App;\ninterface Loggable { public function log(); }\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        return 1;
    }
}
`},
			decoy: map[string]string{"composer.json": phpComposer,
				"src/Loggable.php": "<?php\nnamespace App;\ninterface Loggable { public function log(); }\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        // we no longer use Loggable;
        return 1;
    }
}
`},
			want:    []string{"S A"},
			wantNot: []string{"E A implements Loggable", "E file:src/A.php imports file:src/Loggable.php"},
		},
		{
			id: "N6-php-string-class-ref",
			clean: map[string]string{"composer.json": phpComposer,
				"src/Fake.php": "<?php\nnamespace App;\nclass Fake {}\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        $s = "nothing is built here";
        return $s;
    }
}
`},
			decoy: map[string]string{"composer.json": phpComposer,
				"src/Fake.php": "<?php\nnamespace App;\nclass Fake {}\n",
				"src/A.php": `<?php
namespace App;
class A {
    public function run() {
        $s = "new \App\Fake() is not built here";
        return $s;
    }
}
`},
			want:    []string{"S A.run"},
			wantNot: []string{"E file:src/A.php imports file:src/Fake.php", "E A.run uses-type Fake", "E A.run calls Fake"},
		},
		{
			id: "N7-rust-string-and-comment",
			clean: map[string]string{
				"Cargo.toml": "[package]\nname = \"p\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"src/lib.rs": `pub struct Config;
pub trait Shape {}
pub struct Wrapper;

pub fn load() -> i32 {
    let msg = "error: missing";
    msg.len() as i32
}
`},
			decoy: map[string]string{
				"Cargo.toml": "[package]\nname = \"p\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"src/lib.rs": `pub struct Config;
pub trait Shape {}
pub struct Wrapper;

pub fn load() -> i32 {
    // e.g. impl Shape for Wrapper {}
    let msg = "error: Config missing";
    msg.len() as i32
}
`},
			want:    []string{"S load"},
			wantNot: []string{"E load uses-type Config", "E Wrapper implements Shape"},
		},
		{
			id: "N8-c-comment-include-and-type",
			clean: map[string]string{
				"dead.h":   "int dead(void);\n",
				"widget.h": "typedef struct Widget { int x; } Widget;\n",
				"a.c": `#include "widget.h"
int use(int arg) {
  return arg;
}
`},
			decoy: map[string]string{
				"dead.h":   "int dead(void);\n",
				"widget.h": "typedef struct Widget { int x; } Widget;\n",
				"a.c": `#include "widget.h"
/*
#include "dead.h"
*/
int use(int arg) {
  // Widget(arg) is legacy
  return arg;
}
`},
			want:       []string{"S use"},
			wantNative: []string{"E file:a.c imports file:widget.h"},
			wantNot:    []string{"E file:a.c imports file:dead.h", "E use uses-type Widget", "E use calls Widget"},
		},
	}
}
