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
