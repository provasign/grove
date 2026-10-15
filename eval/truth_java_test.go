package eval

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParseJavapSupersStripsTypeArguments(t *testing.T) {
	out := "Classfile /x/B.class\n  Compiled from \"B.java\"\n" +
		"public class p.B extends p.A<java.lang.String, p.C<p.D>> implements p.I, p.J<p.K>\n" +
		"  minor version: 0\n"
	if got, want := parseJavapSupers(out), []string{"p.A", "p.I", "p.J"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("supers = %v, want %v", got, want)
	}
	iface := "public interface p.I extends p.J, p.K<T>\n"
	if got, want := parseJavapSupers(iface), []string{"p.J", "p.K"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("interface supers = %v, want %v", got, want)
	}
}

// An invoke names the receiver's static type; a method inherited from a
// superclass or superinterface must still resolve to its declaration.
func TestJavaCallTruthResolvesInheritedMethods(t *testing.T) {
	if _, err := exec.LookPath(jdkTool("javac")); err != nil {
		t.Skip("no JDK")
	}
	if err := exec.Command(jdkTool("javac"), "-version").Run(); err != nil {
		t.Skip("no working javac")
	}
	root := t.TempDir()
	src := filepath.Join(root, "src", "main", "java", "p")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"Base.java":  "package p;\npublic abstract class Base<T> {\n  public T setMax(int n) { return null; }\n}\n",
		"Named.java": "package p;\npublic interface Named {\n  default String name() { return \"x\"; }\n}\n",
		"Builder.java": "package p;\npublic class Builder extends Base<Builder> implements Named {\n" +
			"  public Builder() {}\n}\n",
		"User.java": "package p;\npublic class User {\n  void use() {\n    Builder b = new Builder();\n" +
			"    b.setMax(3);\n    b.name();\n  }\n}\n",
	}
	for n, body := range files {
		if err := os.WriteFile(filepath.Join(src, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, edges, err := JavaCallTruth(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, e := range edges {
		got[e.Caller.Name+"→"+e.Callee.Name] = true
	}
	for _, want := range []string{"User.use→Builder.Builder", "User.use→Base.setMax", "User.use→Named.name"} {
		if !got[want] {
			t.Errorf("missing %s; edges=%v", want, got)
		}
	}
}

// javap prints a class initializer as `static {};` and generic bounds with
// `?` before the method name; both start a new method, so their invokes are
// not attributed to the method printed before them.
func TestParseJavapStaticInitializerAndWildcardHeaders(t *testing.T) {
	out := `Compiled from "C.java"
public class p.C {
  public java.lang.String toString();
    descriptor: ()Ljava/lang/String;
    Code:
      LineNumberTable:
        line 10: 0
  public static <T extends java.lang.Comparable<? super T>> T max(T...);
    descriptor: ([Ljava/lang/Comparable;)Ljava/lang/Comparable;
    Code:
         0: invokestatic  #2                  // Method of:(I)V
      LineNumberTable:
        line 20: 0
  static {};
    descriptor: ()V
    Code:
         0: invokespecial #3                  // Method "<init>":()V
      LineNumberTable:
        line 5: 0
}
`
	methods := parseJavap(out, "p.C")
	byName := map[string]*javaMethod{}
	for _, m := range methods {
		byName[m.name] = m
	}
	if byName["max"] == nil || len(byName["max"].invokes) != 1 {
		t.Fatalf("wildcard header not parsed as its own method: %+v", methods)
	}
	if byName["<clinit>"] == nil || len(byName["<clinit>"].invokes) != 1 || len(byName["toString"].invokes) != 0 {
		t.Fatalf("static initializer invokes misattributed: %+v", methods)
	}
}
