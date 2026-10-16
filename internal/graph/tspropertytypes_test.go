package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// zod's shape: `inst._zod.parse(...)` inside a `$constructor` callback whose
// `inst` is typed only through a generic. Every declaration of `_zod` that can
// have a `parse` reaches _$ZodTypeInternals.parse; the check family's `_zod`
// has no parse and drops out.
func tsPropFixture(extra ...core.SymbolRecord) []core.SymbolRecord {
	mk := func(id string, kind core.SymbolKind, name, parent, sig, raw string, start, end int, calls ...core.CallSite) core.SymbolRecord {
		qn := name
		if parent != "" {
			qn = parent + "." + name
		}
		return core.SymbolRecord{ID: id, FilePath: "s.ts", BlobSHA: "1", Language: "typescript", Kind: kind,
			Name: name, QualifiedName: qn, ParentSymbol: parent, Signature: sig, RawText: raw, CallSites: calls,
			Span: core.LineRange{Start: start, End: end}}
	}
	syms := []core.SymbolRecord{
		mk("s::Internals", core.KindInterface, "_$Internals", "", "interface _$Internals",
			"interface _$Internals {\n  parse(p: number): number;\n}", 1, 3),
		{ID: "s::Internals.parse", FilePath: "s.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindMethod,
			Name: "parse", QualifiedName: "_$Internals.parse", ParentSymbol: "_$Internals",
			Signature: "parse(p: number): number", Annotations: []string{"declaration"}, Span: core.LineRange{Start: 2, End: 2}},
		mk("s::Sub", core.KindInterface, "$SubInternals", "", "interface $SubInternals<out O = unknown> extends _$Internals",
			"interface $SubInternals<out O = unknown> extends _$Internals {\n  output: O;\n}", 4, 6),
		// Generic parameter: `_zod: I` resolves to I's constraint.
		mk("s::Type", core.KindInterface, "$Type", "", "interface $Type<\n  O = unknown,\n  I extends $SubInternals<O> = $SubInternals<O>,\n>",
			"interface $Type<\n  O = unknown,\n  I extends $SubInternals<O> = $SubInternals<O>,\n> {\n  _zod: I;\n}", 7, 12),
		mk("s::Type._zod", core.KindField, "_zod", "$Type", "_zod: I", "_zod: I;", 11, 11),
		mk("s::String", core.KindInterface, "$String", "", "interface $String extends $Type",
			"interface $String extends $Type {\n  _zod: core.$SubInternals<string>;\n}", 13, 15),
		mk("s::String._zod", core.KindField, "_zod", "$String", "_zod: core.$SubInternals<string>", "_zod: core.$SubInternals<string>;", 14, 14),
		// Unrelated family without a parse member: not a receiver of .parse.
		mk("s::CheckInternals", core.KindInterface, "$CheckInternals", "", "interface $CheckInternals",
			"interface $CheckInternals {\n  check(p: number): void;\n}", 16, 18),
		mk("s::CheckInternals.check", core.KindMethod, "check", "$CheckInternals", "check(p: number): void", "", 17, 17),
		mk("s::Check", core.KindInterface, "$Check", "", "interface $Check",
			"interface $Check {\n  _zod: $CheckInternals;\n}", 19, 21),
		mk("s::Check._zod", core.KindField, "_zod", "$Check", "_zod: $CheckInternals", "_zod: $CheckInternals;", 20, 20),
		// An inline object type without parse, and a type alias.
		mk("s::Err", core.KindInterface, "$Err", "", "interface $Err",
			"interface $Err {\n  _zod: {\n    def: string[];\n  };\n}", 22, 26),
		mk("s::Err._zod", core.KindField, "_zod", "$Err", "_zod: {", "_zod: {\n    def: string[];\n  }", 23, 25),
		mk("s::Some", core.KindType, "SomeType", "", "type SomeType = { _zod: _$Internals };",
			"type SomeType = { _zod: _$Internals };", 27, 27),
		// Decoy: a callable parse on an unrelated class.
		mk("s::Parser", core.KindClass, "Parser", "", "class Parser", "class Parser {\n  parse(s: string) { return s }\n}", 28, 30),
		mk("s::Parser.parse", core.KindMethod, "parse", "Parser", "parse(s: string)", "parse(s: string) { return s }", 29, 29),
		// The caller: inst is an untyped callback parameter.
		mk("s::top", core.KindFunction, "<top-level>", "", "",
			"const $Thing = $constructor(\"$Thing\", (inst, def) => {\n"+
				"  inst._zod.parse = (p, ctx) => p;\n"+
				"  const base = inst._zod.parse;\n"+
				"  return inst._zod.parse(1, def);\n"+
				"});", 31, 35,
			core.CallSite{Callee: "$constructor", Line: 31, Argc: 2},
			core.CallSite{Callee: "inst._zod.parse", Line: 34, Argc: 2}),
	}
	return append(syms, extra...)
}

func edgesOf(syms []core.SymbolRecord, from, to string) map[core.EdgeType]string {
	out := map[core.EdgeType]string{}
	for _, e := range BuildEdges(syms) {
		if e.From == from && e.To == to {
			out[e.Type] = e.Lines
		}
	}
	return out
}

func TestTSPropertyRule_UniqueFamilyTypesUntypedReceiver(t *testing.T) {
	syms := tsPropFixture()
	got := edgesOf(syms, "s::top", "s::Internals.parse")
	if _, ok := got[core.EdgeCalls]; !ok {
		t.Errorf("inst._zod.parse(...) must call _$Internals.parse; got %v", got)
	}
	if got[core.EdgeWrites] != "32:13" {
		t.Errorf("inst._zod.parse = ... must be a write at 32:13; got %q", got[core.EdgeWrites])
	}
	if got[core.EdgeReads] != "33:26" {
		t.Errorf("const base = inst._zod.parse must be a read at 33:26; got %q", got[core.EdgeReads])
	}
	if d := edgesOf(syms, "s::top", "s::Parser.parse"); len(d) > 0 {
		t.Errorf("an unrelated Parser.parse must not be reached: %v", d)
	}
	for _, e := range BuildEdges(syms) {
		if e.Reason == core.ReasonMemberRef && e.To != "s::Internals.parse" {
			t.Errorf("unexpected member-ref edge %+v", e)
		}
	}
}

// The property declared with two unrelated types that both have the member:
// the name does not decide, nothing binds.
func TestTSPropertyRule_UnrelatedDeclarationsDecideNothing(t *testing.T) {
	syms := tsPropFixture(
		core.SymbolRecord{ID: "s::Alien", FilePath: "a.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindInterface,
			Name: "Alien", QualifiedName: "Alien", Signature: "interface Alien",
			RawText: "interface Alien {\n  parse(s: string): string;\n  _zod: AlienInternals;\n}", Span: core.LineRange{Start: 1, End: 4}},
		core.SymbolRecord{ID: "s::Alien._zod", FilePath: "a.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindField,
			Name: "_zod", QualifiedName: "Alien._zod", ParentSymbol: "Alien", Signature: "_zod: AlienInternals",
			RawText: "_zod: AlienInternals;", Span: core.LineRange{Start: 3, End: 3}},
		core.SymbolRecord{ID: "s::AlienInternals", FilePath: "a.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindInterface,
			Name: "AlienInternals", QualifiedName: "AlienInternals", Signature: "interface AlienInternals",
			RawText: "interface AlienInternals {\n  parse(s: string): string;\n}", Span: core.LineRange{Start: 5, End: 7}},
		core.SymbolRecord{ID: "s::AlienInternals.parse", FilePath: "a.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindMethod,
			Name: "parse", QualifiedName: "AlienInternals.parse", ParentSymbol: "AlienInternals",
			Signature: "parse(s: string): string", Annotations: []string{"declaration"}, Span: core.LineRange{Start: 6, End: 6}},
	)
	// (The name-based fallback that ran before this rule existed still
	// applies to the call; only the rule's own targets are asserted.)
	for _, to := range []string{"s::Internals.parse", "s::AlienInternals.parse"} {
		if got := edgesOf(syms, "s::top", to); len(got) > 0 {
			t.Errorf("two families declare _zod with a parse: no edge to %s, got %v", to, got)
		}
	}
}

// An unconstrained generic parameter (`_zod: T`) or an untyped declaration
// (`_zod = {}`) could be anything: the rule stays out.
func TestTSPropertyRule_UnconstrainedOrUntypedDeclarationDecidesNothing(t *testing.T) {
	cases := map[string][]core.SymbolRecord{
		"unconstrained generic": {
			{ID: "s::Box", FilePath: "b.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindInterface,
				Name: "Box", QualifiedName: "Box", Signature: "interface Box<T>",
				RawText: "interface Box<T> {\n  _zod: T;\n}", Span: core.LineRange{Start: 1, End: 3}},
			{ID: "s::Box._zod", FilePath: "b.ts", BlobSHA: "1", Language: "typescript", Kind: core.KindField,
				Name: "_zod", QualifiedName: "Box._zod", ParentSymbol: "Box", Signature: "_zod: T",
				RawText: "_zod: T;", Span: core.LineRange{Start: 2, End: 2}},
		},
		"untyped js field": {
			{ID: "s::Js", FilePath: "j.js", BlobSHA: "1", Language: "javascript", Kind: core.KindClass,
				Name: "Js", QualifiedName: "Js", Signature: "class Js",
				RawText: "class Js {\n  _zod = {};\n}", Span: core.LineRange{Start: 1, End: 3}},
			{ID: "s::Js._zod", FilePath: "j.js", BlobSHA: "1", Language: "javascript", Kind: core.KindField,
				Name: "_zod", QualifiedName: "Js._zod", ParentSymbol: "Js", Signature: "_zod = {}",
				RawText: "_zod = {};", Span: core.LineRange{Start: 2, End: 2}},
		},
	}
	for name, extra := range cases {
		syms := tsPropFixture(extra...)
		if got := edgesOf(syms, "s::top", "s::Internals.parse"); len(got) > 0 {
			t.Errorf("%s: no edge expected, got %v", name, got)
		}
	}
}

// The generic-constraint declaration alone pins the family.
func TestTSPropertyRule_GenericConstraintAlone(t *testing.T) {
	var syms []core.SymbolRecord
	for _, s := range tsPropFixture() {
		switch s.ID {
		case "s::String", "s::String._zod", "s::Some", "s::Err", "s::Err._zod":
			continue
		}
		syms = append(syms, s)
	}
	if _, ok := edgesOf(syms, "s::top", "s::Internals.parse")[core.EdgeCalls]; !ok {
		t.Errorf("`_zod: I` with I extends $SubInternals must reach _$Internals.parse")
	}
}

// The member reads/writes come from the caller's text, not its sites, so the
// incremental name rule must look at the text: a new file declaring another
// `_zod` (whose type has its own parse) makes the binding ambiguous, and a
// caller in an untouched file must drop its write edge as a full build does.
func TestTSPropertyRule_DeltaRebindsMemberRefsOnNewDeclaration(t *testing.T) {
	prev := tsPropFixture()
	for i := range prev {
		if prev[i].ID == "s::top" {
			prev[i].ID, prev[i].FilePath = "w::top", "w.ts"
			prev[i].CallSites = prev[i].CallSites[:1] // reads/writes only: no `.parse(` call site
		}
	}
	add := func(id string, kind core.SymbolKind, name, parent, sig, raw string, line int) core.SymbolRecord {
		qn := name
		if parent != "" {
			qn = parent + "." + name
		}
		return core.SymbolRecord{ID: id, FilePath: "fz.ts", BlobSHA: "2", Language: "typescript", Kind: kind,
			Name: name, QualifiedName: qn, ParentSymbol: parent, Signature: sig, RawText: raw,
			Span: core.LineRange{Start: line, End: line}}
	}
	next := append(append([]core.SymbolRecord{}, prev...),
		add("fz::Fz", core.KindInterface, "Fz", "", "interface Fz", "interface Fz { _zod: Fz2 }", 1),
		add("fz::Fz._zod", core.KindField, "_zod", "Fz", "_zod: Fz2", "_zod: Fz2", 1),
		add("fz::Fz2", core.KindInterface, "Fz2", "", "interface Fz2", "interface Fz2 { parse(x: number): void }", 2),
		add("fz::Fz2.parse", core.KindMethod, "parse", "Fz2", "parse(x: number): void", "parse(x: number): void", 2),
	)
	memberRefs := func(edges []core.Edge) map[string]bool {
		out := map[string]bool{}
		for _, e := range edges {
			if e.From == "w::top" && e.Reason == core.ReasonMemberRef {
				out[string(e.Type)+" "+e.To+" "+e.Lines] = true
			}
		}
		return out
	}
	prevEdges := BuildEdges(prev)
	if len(memberRefs(prevEdges)) == 0 {
		t.Fatal("fixture: the caller should have member-ref edges before the edit")
	}
	full := memberRefs(BuildEdges(next))
	delta := memberRefs(BuildEdgesDelta(prevEdges, prev, next, map[string]bool{"fz.ts": true}, nil))
	if len(full) != len(delta) {
		t.Fatalf("delta member-ref edges %v, full build %v", delta, full)
	}
	for k := range full {
		if !delta[k] {
			t.Fatalf("delta member-ref edges %v, full build %v", delta, full)
		}
	}
}
