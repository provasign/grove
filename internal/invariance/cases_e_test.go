package invariance

// Group E: grove internal/graph edge builders. Tool-output findings
// (E4 change-impact, E5 missing-implementations, E7/E8 rename-plan,
// E9 change-impact) are in pkg/grove/comment_string_tools_test.go.

func casesE() []twinCase {
	return []twinCase{
		{
			id: "E1-python-docstring-local-def",
			clean: one("a.py", `def helper(x):
    return x


def caller():
    """Example."""
    return helper(1)
`),
			decoy: one("a.py", `def helper(x):
    return x


def caller():
    """Example:

        def helper(x):
            return 2
    """
    return helper(1)
`),
			want: []string{"E caller calls helper"},
		},
		{
			id: "E1-js-comment-and-template-local-def",
			clean: one("a.js", `function helper(x) { return x; }
function caller() {
  return helper(1);
}
function caller2() {
  const t = "";
  return helper(2);
}
`),
			decoy: one("a.js", "function helper(x) { return x; }\nfunction caller() {\n  /* function helper(x) { return 0; } */\n  return helper(1);\n}\nfunction caller2() {\n  const t = `\nfunction helper(y) {}\n`;\n  return helper(2);\n}\n"),
			want:  []string{"E caller calls helper", "E caller2 calls helper"},
		},
		{
			id: "E2-go-embedded-field-comment",
			clean: map[string]string{"go.mod": goMod, "p.go": `package p

type Reader struct{}

type Holder struct {
	x int
}
`},
			decoy: map[string]string{"go.mod": goMod, "p.go": `package p

type Reader struct{}

type Holder struct {
	/*
	Reader
	*/
	x int
}
`},
			want:    []string{"S Holder"},
			wantNot: []string{"E Holder extends Reader", "E Holder uses-type Reader"},
		},
		{
			id: "E3-rust-workspace-comment-path",
			clean: map[string]string{
				"Cargo.toml":        "[workspace]\nmembers = [\"alpha\", \"beta\"]\nresolver = \"2\"\n",
				"alpha/Cargo.toml":  "[package]\nname = \"alpha\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"beta/Cargo.toml":   "[package]\nname = \"beta\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"beta/src/lib.rs":   "pub fn normalize(x: i32) -> i32 { x }\n",
				"alpha/src/util.rs": "pub fn normalize(x: i32) -> i32 { x + 1 }\n",
				"alpha/src/b.rs":    "use super::util::*;\n\npub fn other(x: i32) -> i32 { normalize(x) }\n",
				"alpha/src/lib.rs": `pub mod b;
pub mod util;

pub fn run() -> i32 {
    0
}
`},
			decoy: map[string]string{
				"Cargo.toml":        "[workspace]\nmembers = [\"alpha\", \"beta\"]\nresolver = \"2\"\n",
				"alpha/Cargo.toml":  "[package]\nname = \"alpha\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"beta/Cargo.toml":   "[package]\nname = \"beta\"\nversion = \"0.1.0\"\nedition = \"2021\"\n",
				"beta/src/lib.rs":   "pub fn normalize(x: i32) -> i32 { x }\n",
				"alpha/src/util.rs": "pub fn normalize(x: i32) -> i32 { x + 1 }\n",
				"alpha/src/b.rs":    "use super::util::*;\n\npub fn other(x: i32) -> i32 { normalize(x) }\n",
				"alpha/src/lib.rs": `pub mod b;
pub mod util;

pub fn run() -> i32 {
    // Same contract as beta::normalize
    0
}
`},
			want:    []string{"E other calls normalize@alpha/src/util.rs"},
			wantNot: []string{"E other calls normalize@beta/src/lib.rs"},
		},
		{
			id: "E4-python-multiline-bases",
			clean: one("a.py", `class Base:
    def method(self):
        return 0


class Mixin:
    pass


class Multi(Base,):
    def method(self):
        return 1


class Commented(Base, Mixin):
    pass
`),
			decoy: one("a.py", `class Base:
    def method(self):
        return 0


class Mixin:
    pass


class Multi(
    Base,
):
    def method(self):
        return 1


class Commented(Base,  # the (old) Mixin
                Mixin):
    pass
`),
			want: []string{"E Multi extends Base", "E Commented extends Base", "E Commented extends Mixin"},
		},
		{
			id: "E6-kotlin-supertype-comments",
			clean: one("k.kt", `open class KBase
interface KIface
class KOther
class Widget
class Gadget
class KTrailing : KBase(), KIface {}
class KMultiComment : KBase(), KIface {
}
fun build(size: Int): Gadget? = null
`),
			decoy: one("k.kt", `open class KBase
interface KIface
class KOther
class Widget
class Gadget
class KTrailing : KBase(), KIface /* see KOther */ {}
class KMultiComment : KBase(), // the base
    KIface {
}
fun build(
    size: Int, // not a Widget
): Gadget? = null
`),
			want: []string{"E KTrailing implements KIface", "E KMultiComment implements KIface",
				"E KTrailing extends KBase", "E KMultiComment extends KBase", "E build uses-type Gadget"},
			wantNot: []string{"E KTrailing uses-type KOther", "E KTrailing implements KOther", "E build uses-type Widget"},
		},
		{
			id: "E10-cobol-move-comment-and-continuation",
			clean: one("PROG2.cbl", `       IDENTIFICATION DIVISION.
       PROGRAM-ID. PROG2.
       DATA DIVISION.
       WORKING-STORAGE SECTION.
       01 WS-TOTAL PIC 9(4).
       01 WS-OUT PIC 9(4).
       01 WS-OLD PIC 9(4).
       PROCEDURE DIVISION.
       PARA-ONE.
           MOVE WS-TOTAL TO WS-OUT.
       PARA-TWO.
           MOVE WS-TOTAL TO WS-OUT.
           STOP RUN.
`),
			decoy: one("PROG2.cbl", `       IDENTIFICATION DIVISION.
       PROGRAM-ID. PROG2.
       DATA DIVISION.
       WORKING-STORAGE SECTION.
       01 WS-TOTAL PIC 9(4).
       01 WS-OUT PIC 9(4).
       01 WS-OLD PIC 9(4).
       PROCEDURE DIVISION.
       PARA-ONE.
           MOVE WS-TOTAL TO WS-OUT. *> WAS WS-OLD
       PARA-TWO.
           MOVE WS-TOTAL
               TO WS-OUT.
           STOP RUN.
`),
			want: []string{"E PARA-ONE writes WS-OUT", "E PARA-ONE reads WS-TOTAL",
				"E PARA-TWO writes WS-OUT", "E PARA-TWO reads WS-TOTAL"},
			wantNot: []string{"E PARA-ONE writes WS-OLD", "E PARA-ONE reads WS-OLD", "E PARA-TWO reads WS-OUT"},
		},
		{
			// Suspected (not in the confirmed 53): javaQualifiedCallRe reads
			// a trailing comment on a call line.
			id: "E-suspect-java-qualified-call-comment",
			clean: one("src/p/U.java", `package p;
class Other { static void build() {} }
class Real { static void build() {} }
class U {
  void m() {
    Real.build();
  }
}
`),
			decoy: one("src/p/U.java", `package p;
class Other { static void build() {} }
class Real { static void build() {} }
class U {
  void m() {
    Real.build(); // Other.build()
  }
}
`),
			want:    []string{"E U.m calls Real.build"},
			wantNot: []string{"E U.m calls Other.build", "E U.m uses-type Other"},
		},
		{
			// Suspected: graphCallableSymbol takes a JS class field whose
			// string initializer contains "=>" for a callable.
			id: "E-suspect-js-field-arrow-in-string",
			clean: one("c.js", `export class C {
  label = "a to b";
  run() { return this.label; }
}
export function use(c) { return c.label; }
`),
			decoy: one("c.js", `export class C {
  label = "a => b";
  run() { return this.label; }
}
export function use(c) { return c.label; }
`),
			want:    []string{"S C.run"},
			wantNot: []string{"S C.label kind=method", "S C.label kind=function", "E use calls C.label", "E C.run calls C.label"},
		},
	}
}
