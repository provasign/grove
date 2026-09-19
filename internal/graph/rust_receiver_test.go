package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Rust receiver typing measured on ripgrep (R 0.8947 → 0.9047): each
// fixture is the minimal shape of a site that was a miss before the rule.

func rustSym(id, file string, kind core.SymbolKind, name, parent, sig, raw string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "rust", Kind: kind,
		Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig,
		RawText: raw, CallSites: sites,
	}
}

func TestRustCalls_BareCallIsNeverAMethod(t *testing.T) {
	// `let stats = stats(&low)` inside `impl HiArgs` names the free fn,
	// not HiArgs::stats — Rust has no implicit self.
	g := New()
	syms := []core.SymbolRecord{
		rustSym("h.rs::HiArgs@sha", "h.rs", core.KindStruct, "HiArgs", "", "pub struct HiArgs", "pub struct HiArgs { stats: Option<Stats> }"),
		rustSym("h.rs::HiArgs.stats@sha", "h.rs", core.KindMethod, "stats", "HiArgs", "pub fn stats(&self) -> Option<&Stats>", ""),
		rustSym("h.rs::stats@sha", "h.rs", core.KindFunction, "stats", "", "fn stats(low: &LowArgs) -> Option<Stats>", ""),
		rustSym("h.rs::HiArgs.from_low_args@sha", "h.rs", core.KindMethod, "from_low_args", "HiArgs",
			"pub fn from_low_args(low: LowArgs) -> anyhow::Result<HiArgs>",
			"pub fn from_low_args(low: LowArgs) -> anyhow::Result<HiArgs> { let stats = stats(&low); Ok(HiArgs { stats }) }",
			core.CallSite{Callee: "stats", Line: 1, Argc: 1}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "h.rs::HiArgs.from_low_args@sha", "h.rs::stats@sha") {
		t.Fatalf("bare stats(&low) must resolve to the free function")
	}
	if hasEdge(g, core.EdgeCalls, "h.rs::HiArgs.from_low_args@sha", "h.rs::HiArgs.stats@sha") {
		t.Fatalf("bare stats(&low) must not resolve to the method HiArgs::stats")
	}
}

func TestRustReceivers_ConstSliceEnumVariantAndTupleField(t *testing.T) {
	g := New()
	syms := []core.SymbolRecord{
		// for flag in FLAGS.iter() — FLAGS: &[&dyn Flag] → the trait's declaration.
		rustSym("m.rs::Flag@sha", "m.rs", core.KindTrait, "Flag", "", "pub(crate) trait Flag", ""),
		rustSym("m.rs::Flag.name_long@sha", "m.rs", core.KindMethod, "name_long", "Flag", "fn name_long(&self) -> &'static str;", ""),
		rustSym("d.rs::AfterContext.name_long@sha", "d.rs", core.KindMethod, "name_long", "AfterContext", "fn name_long(&self) -> &'static str", ""),
		rustSym("d.rs::FLAGS@sha", "d.rs", core.KindVariable, "FLAGS", "", "pub(super) const FLAGS: &[&dyn Flag] = &[", ""),
		rustSym("b.rs::generate@sha", "b.rs", core.KindFunction, "generate", "", "pub(crate) fn generate() -> String",
			"pub(crate) fn generate() -> String { for flag in FLAGS.iter() { opts.push_str(flag.name_long()); } }",
			core.CallSite{Callee: "flag.name_long", Line: 1}),
		// match *printer { Printer::Standard(ref mut p) => p.sink_with_path(..) }
		rustSym("s.rs::Printer@sha", "s.rs", core.KindEnum, "Printer", "", "pub(crate) enum Printer<W> {",
			"pub(crate) enum Printer<W> {\n    Standard(grep::printer::Standard<W>),\n    Summary(grep::printer::Summary<W>),\n}"),
		rustSym("p1.rs::Standard.sink_with_path@sha", "p1.rs", core.KindMethod, "sink_with_path", "Standard", "pub fn sink_with_path(&mut self, path: &Path)", ""),
		rustSym("p2.rs::Summary.sink_with_path@sha", "p2.rs", core.KindMethod, "sink_with_path", "Summary", "pub fn sink_with_path(&mut self, path: &Path)", ""),
		rustSym("s.rs::search_path@sha", "s.rs", core.KindFunction, "search_path", "", "fn search_path(printer: &mut Printer<W>, path: &Path)",
			"fn search_path(printer: &mut Printer<W>, path: &Path) { match *printer { Printer::Standard(ref mut p) => { p.sink_with_path(path); } } }",
			core.CallSite{Callee: "p.sink_with_path", Line: 1, Argc: 1, Args: []string{"path"}}),
		// struct Override(Gitignore); self.0.is_empty()
		rustSym("o.rs::Override@sha", "o.rs", core.KindStruct, "Override", "", "pub struct Override(Gitignore);", "pub struct Override(Gitignore);"),
		rustSym("o.rs::Override.0@sha", "o.rs", core.KindField, "0", "Override", "Gitignore", "Gitignore"),
		rustSym("g.rs::Gitignore.is_empty@sha", "g.rs", core.KindMethod, "is_empty", "Gitignore", "pub fn is_empty(&self) -> bool", ""),
		rustSym("t.rs::Types.is_empty@sha", "t.rs", core.KindMethod, "is_empty", "Types", "pub fn is_empty(&self) -> bool", ""),
		rustSym("o.rs::Override.is_empty@sha", "o.rs", core.KindMethod, "is_empty", "Override", "pub fn is_empty(&self) -> bool",
			"pub fn is_empty(&self) -> bool { self.0.is_empty() }",
			core.CallSite{Callee: "0.is_empty", Line: 1}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "b.rs::generate@sha", "m.rs::Flag.name_long@sha") {
		t.Fatalf("flag from FLAGS: &[&dyn Flag] must dispatch through the Flag trait declaration")
	}
	if hasEdge(g, core.EdgeCalls, "b.rs::generate@sha", "d.rs::AfterContext.name_long@sha") {
		t.Fatalf("a trait-typed receiver must not fan out to a concrete impl")
	}
	if !hasEdge(g, core.EdgeCalls, "s.rs::search_path@sha", "p1.rs::Standard.sink_with_path@sha") {
		t.Fatalf("Printer::Standard(ref mut p) must type p as Standard")
	}
	if hasEdge(g, core.EdgeCalls, "s.rs::search_path@sha", "p2.rs::Summary.sink_with_path@sha") {
		t.Fatalf("Printer::Standard(ref mut p) must not reach Summary's method")
	}
	if !hasEdge(g, core.EdgeCalls, "o.rs::Override.is_empty@sha", "g.rs::Gitignore.is_empty@sha") {
		t.Fatalf("self.0 must type as the tuple struct's first field")
	}
	if hasEdge(g, core.EdgeCalls, "o.rs::Override.is_empty@sha", "t.rs::Types.is_empty@sha") {
		t.Fatalf("self.0 must not reach an unrelated is_empty")
	}
}
