package invariance

// Group A: astkit extractors (call sites, enum/protocol/base clauses,
// modifiers, Lombok accessors, COBOL verbs) observed through the grove index.

func casesA() []twinCase {
	return []twinCase{
		{
			id: "A1-rust-macro-args",
			clean: one("lib.rs", `pub fn phantom_one(x: i32) -> i32 { x }
pub fn bar(x: i32) -> i32 { x }
pub fn f(c: char) {
    assert!(true,
    );
    assert_eq!(c, '"', "see");
}
`),
			decoy: one("lib.rs", `pub fn phantom_one(x: i32) -> i32 { x }
pub fn bar(x: i32) -> i32 { x }
pub fn f(c: char) {
    assert!(true, // note: phantom_one(2)
    );
    assert_eq!(c, '"', "see bar(1)");
}
`),
			want:    []string{"S f"},
			wantNot: []string{"E f calls phantom_one", "E f calls bar"},
		},
		{
			id: "A2-c-macro-body-strings",
			clean: one("a.c", `#include <stdio.h>
int value(int x) { return x; }
int helper_fn(int x) { return x; }
int other_fn(int x) { return x; }
#define LOG(x) printf("v=%d", x)
#define LOG2(x) do { \
    printf("%d", (x)); \
  } while (0)
`),
			decoy: one("a.c", `#include <stdio.h>
int value(int x) { return x; }
int helper_fn(int x) { return x; }
int other_fn(int x) { return x; }
#define LOG(x) printf("value(%d) helper_fn(x)", x)
#define LOG2(x) do { \
    printf("%d", (x)); /* other_fn(x) */ \
  } while (0) // other_fn(x)
`),
			want:    []string{"S LOG", "S LOG2"},
			wantNot: []string{"E LOG calls value", "E LOG calls helper_fn", "E LOG2 calls other_fn"},
		},
		{
			id: "A3-objc-enum-string-and-if0",
			clean: one("m.m", `#import <Foundation/Foundation.h>
static NSString *k = @"none";
#define X 1
typedef NS_ENUM(NSInteger, Mode) {
  ModeA,
#if X
  ModeB,
#endif
  ModeC
};
`),
			decoy: one("m.m", `#import <Foundation/Foundation.h>
static NSString *k = @"typedef NS_ENUM(NSInteger, Ghost) { GhostA };";
#if 0
typedef NS_ENUM(NSInteger, Dead) { DeadA };
#endif
#define X 1
typedef NS_ENUM(NSInteger, Mode) {
  ModeA,
#if X
  ModeB,
#endif
  ModeC
};
`),
			want:    []string{"S Mode", "S ModeA", "S ModeB", "S ModeC"},
			wantNot: []string{"S Ghost", "S GhostA", "S Dead", "S DeadA"},
		},
		{
			id: "A4-objc-protocol-comment",
			clean: one("m.h", `@interface Sup
@end
@protocol FakeProto
@end
@protocol RealProto
@end
@interface M : Sup <RealProto>
@end
`),
			decoy: one("m.h", `@interface Sup
@end
@protocol FakeProto
@end
@protocol RealProto
@end
@interface M : Sup /* <FakeProto> */ <RealProto>
@end
`),
			want:    []string{"E M implements RealProto", "E M extends Sup"},
			wantNot: []string{"E M implements FakeProto"},
		},
		{
			id: "A4-kotlin-ctor-param-comment",
			clean: one("Foo.kt", `open class Base
class Thing
class Foo(
    val a: Int,
) : Base()
`),
			decoy: one("Foo.kt", `open class Base
class Thing
class Foo(
    val a: Int, // : Thing
) : Base()
`),
			want:    []string{"S Foo"},
			wantNot: []string{"E Foo uses-type Thing", "E Foo extends Thing", "E Foo implements Thing"},
		},
		{
			id: "A4-python-base-and-param-comments",
			clean: one("a.py", `class Base:
    pass


class Fake:
    pass


class A(Base):
    pass


def g(a,
      b):
    return a
`),
			decoy: one("a.py", `class Base:
    pass


class Fake:
    pass


class A(Base):  # was Fake
    pass


def g(a,  # first, really (Fake)
      b):
    return a
`),
			sig:     true,
			want:    []string{"E A extends Base"},
			wantNot: []string{"E A uses-type Fake", "E A extends Fake", "E g uses-type Fake"},
		},
		{
			id: "A4-python-multiline-def-return",
			clean: one("a.py", `class Gadget:
    pass


def build(size,) -> Gadget:
    return Gadget()
`),
			decoy: one("a.py", `class Gadget:
    pass


def build(
    size,
) -> Gadget:
    return Gadget()
`),
			sig:  true,
			want: []string{"E build uses-type Gadget"},
		},
		{
			id:             "A4-csharp-attribute-string",
			rawAnnotations: true,
			clean: one("A.cs", `using System.ComponentModel;
namespace P {
class Fake {}
public class A {
  [Description("plain")] public void M() {}
}
}
`),
			decoy: one("A.cs", `using System.ComponentModel;
namespace P {
class Fake {}
public class A {
  [Description("see Fake and new Fake()")] public void M() {}
}
}
`),
			want:    []string{"S A.M"},
			wantNot: []string{"E A.M uses-type Fake", "E A uses-type Fake", "E A.M calls Fake"},
		},
		{
			id: "A5-kotlin-fun-in-comment",
			clean: one("p.kt", `// just for you
interface Plain {
    fun x(): Int
}
`),
			decoy: one("p.kt", `// just for fun
interface Plain {
    fun x(): Int
}
`),
			want:    []string{"S Plain"},
			wantNot: []string{"S Plain mod=fun"},
		},
		{
			id: "A6-java-public-in-name-and-string",
			clean: one("src/p/A.java", `package p;
class A {
  void publication() {}
  String mode = "x";
}
`),
			decoy: one("src/p/A.java", `package p;
class A {
  void publication() {}
  String mode = "public";
}
`),
			want:    []string{"S A.publication unexported", "S A.mode unexported"},
			wantNot: []string{"S A.publication exported", "S A.mode exported", "S A.mode mod=public"},
		},
		{
			id: "A7-java-lombok-string-type",
			clean: one("src/p/A.java", `package p;
import lombok.Getter;
class A {
  @Getter String kind = "x";
}
@lombok.Getter
class B {
  String name;
}
`),
			decoy: one("src/p/A.java", `package p;
import lombok.Getter;
class A {
  @Getter String kind = "boolean x";
}
@lombok.Getter
class B {
  String name;
}
`),
			want:    []string{"S A.getKind", "S B.getName"},
			wantNot: []string{"S A.isKind"},
		},
		{
			id: "A8-cobol-literals-and-inline-comment",
			clean: one("PROG1.cbl", `       IDENTIFICATION DIVISION.
       PROGRAM-ID. PROG1.
       DATA DIVISION.
       WORKING-STORAGE SECTION.
       01 X PIC 9.
       PROCEDURE DIVISION.
       MAIN-PARA.
           DISPLAY 'PLEASE WAIT'.
           MOVE 1 TO X
           DISPLAY 'HELLO'.
           STOP RUN.
       BACKUP.
           DISPLAY 'B'.
       OLD-PARA.
           DISPLAY 'O'.
`),
			decoy: one("PROG1.cbl", `       IDENTIFICATION DIVISION.
       PROGRAM-ID. PROG1.
       DATA DIVISION.
       WORKING-STORAGE SECTION.
       01 X PIC 9.
       PROCEDURE DIVISION.
       MAIN-PARA.
           DISPLAY 'PLEASE PERFORM BACKUP FIRST'.
           MOVE 1 TO X *> PERFORM OLD-PARA
           DISPLAY 'CALL SUPPORT'.
           STOP RUN.
       BACKUP.
           DISPLAY 'B'.
       OLD-PARA.
           DISPLAY 'O'.
`),
			want:    []string{"S BACKUP", "S OLD-PARA"},
			wantNot: []string{"E MAIN-PARA calls BACKUP", "E MAIN-PARA calls OLD-PARA", "E MAIN-PARA calls SUPPORT", "E * calls SUPPORT"},
		},
		{
			id: "A9-c-header-class-comment",
			clean: one("a.h", `/*
 * Helpers for the C API.
 */
int real(void);
`),
			decoy: one("a.h", `/*
 class handling for the C API
 */
int real(void);
`),
			want:    []string{"S real lang=c"},
			wantNot: []string{"S real lang=cpp", "S handling"},
		},
	}
}
