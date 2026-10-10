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
	}
}
