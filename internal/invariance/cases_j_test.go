package invariance

// Group J: Java differences the decoy invariant found on real repositories
// (round 2: guava, jackson-databind).

func casesJ() []twinCase {
	return []twinCase{
		{
			// jackson-databind BuilderCreatorSubtype4742Test: javaDeclSource
			// skipped leading annotations with its own quote tracking, so an
			// apostrophe in a comment inside @JsonSubTypes({...}) ran the
			// annotation to the end of the text, the abstract method lost
			// its parameter list, and the override edge went with it.
			id: "J1-java-comment-inside-annotation-args",
			clean: one("src/p/B.java", `package p;
abstract class Builder {
    @Sub({
            @Type(name = "bird")
    })
    public abstract Builder properties(String p);
}
class Impl extends Builder {
    @Override
    public Impl properties(String p) { return this; }
}
`),
			decoy: one("src/p/B.java", `package p;
abstract class Builder {
    @Sub({ // don't 'quote "unterminated (
            @Type(name = "bird") /* ) */
    })
    public abstract Builder properties(String p);
}
class Impl extends Builder {
    @Override
    public Impl properties(String p) { return this; }
}
`),
			want:           []string{"E Impl.properties overrides Builder"},
			rawAnnotations: true,
		},
		{
			// guava: anonymous classes are named by position, so two files of
			// one package each had a `<anonymous@4:12>`. The hierarchy and
			// method-set maps looked them up by name, gave one file's class
			// the other's base, and a comment line above either one moved
			// the collision (10k dispatch edges changed under the decoy).
			// The decoy keeps every line in place (names carry line numbers)
			// and only adds comments.
			id: "J2-java-anonymous-classes-same-position",
			clean: map[string]string{
				"src/p/Task.java": "package p;\ninterface Task { void run(); }\n",
				"src/p/A.java": `package p;
class A {
  Task make() {
    return new Task() {
      public void run() {}
    };
  }
}
`,
				"src/p/B.java": `package p;
class B {
  Thread make() {
    return new Thread() {
      public void run() {}
    };
  }
}
`,
				"src/p/C.java": `package p;
class C {
  void go(Task task) {
    task.run();
  }
}
`,
			},
			decoy: map[string]string{
				"src/p/Task.java": "package p;\ninterface Task { void run(); } // class Fake implements Task\n",
				"src/p/A.java": `package p;
class A { // new Thread() {
  Task make() { // new Task() {
    return new Task() { // public void run() {}
      public void run() {}
    };
  }
}
`,
				"src/p/B.java": `package p;
class B { /* extends Task */
  Thread make() {
    return new Thread() { // implements Task
      public void run() {}
    };
  }
}
`,
				"src/p/C.java": `package p;
class C {
  void go(Task task) { // Thread task;
    task.run();
  }
}
`,
			},
			// Dispatch edges are astkit-mode only (javac binds Task.run), so
			// the facts are the hierarchy; graph/java_anonymous_test.go
			// covers the by-name lookups.
			want:    []string{"E run@src/p/A.java overrides Task", "E C.go calls Task.run"},
			wantNot: []string{"E run@src/p/B.java overrides Task"},
		},
		{
			// A local declared without an initializer and assigned later
			// (`Map<K, V> map;` before a try). Its type used to arrive by
			// accident, from a class "field" regex run over every method
			// body; masking the class body to depth 1 lost it.
			id: "J3-java-local-declared-then-assigned",
			clean: one("src/p/U.java", `package p;
interface Store { int size(); }
class Mem implements Store { public int size() { return 0; } }
class U {
  Store make() { return new Mem(); }
  void m() {
    Store s;
    s = make();
    s.size();
  }
}
`),
			decoy: one("src/p/U.java", `package p;
interface Store { int size(); }
class Mem implements Store { public int size() { return 0; } }
class U {
  Store make() { return new Mem(); } // Mem s;
  void m() {
    Store s; /* Mem s; */
    s = make();
    s.size(); // Mem.size()
  }
}
`),
			want: []string{"E U.m calls Store.size"},
		},
		{
			// newtonsoft: astkit cut C# attribute sections out of signatures
			// (strings in them read as types), so typeof(T) and Enum.Member
			// arguments lost their uses-type edges with them. They are read
			// from the masked attribute text now; a string or a named
			// argument's name is not a type.
			id: "J4-csharp-attribute-type-references",
			clean: one("A.cs", `namespace P {
    public class AConverter { }
    public enum Mode { Fast, Slow }
    public class Name { }
    public class ConverterAttribute : System.Attribute { }
    public class Holder {
        [Converter(typeof(AConverter), Mode.Fast)]
        public int Value { get; set; }
        [Converter(Name = "x")]
        public int Other { get; set; }
    }
}
`),
			decoy: one("A.cs", `namespace P {
    public class AConverter { }
    public enum Mode { Fast, Slow }
    public class Name { }
    public class ConverterAttribute : System.Attribute { }
    public class Holder {
        [Converter(typeof(AConverter), /* typeof(Name) */ Mode.Fast)] // Name.Thing
        public int Value { get; set; }
        [Converter(Name = "x Name.Thing typeof(Mode)")]
        public int Other { get; set; }
    }
}
`),
			rawAnnotations: true,
			want: []string{"E Holder.Value uses-type AConverter", "E Holder.Value uses-type Mode",
				"E Holder.Value uses-type ConverterAttribute", "E Holder.Other uses-type ConverterAttribute"},
			wantNot: []string{"E Holder.Value uses-type Name", "E Holder.Other uses-type Name", "E Holder.Other uses-type Mode"},
		},
	}
}
