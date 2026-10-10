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
	}
}
