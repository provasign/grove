package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Java overload fan-out, measured on commons-io (P 0.876 → 0.894) and
// commons-lang (unaffected, held within baseline).

func javaSym2(id, file string, kind core.SymbolKind, name, parent, sig, raw string, sites ...core.CallSite) core.SymbolRecord {
	return core.SymbolRecord{
		ID: id, FilePath: file, BlobSHA: "sha", Language: "java", Kind: kind,
		Name: name, QualifiedName: name, ParentSymbol: parent, Signature: sig,
		RawText: raw, CallSites: sites,
	}
}

func TestJavaBareCall_InheritedFromUnresolvedExternalBaseDoesNotFanOut(t *testing.T) {
	// IORandomAccessFile extends java.io.RandomAccessFile (external, not
	// indexed) and calls bare write(buf, 0, n): that is
	// RandomAccessFile.write, not any of the unrelated same-named,
	// same-arity IOUtils/FileUtils/FilesUncheck overloads that happen to
	// share the package.
	g := New()
	syms := []core.SymbolRecord{
		javaSym2("f.java::FileUtils@sha", "f.java", core.KindClass, "FileUtils", "", "public class FileUtils", ""),
		javaSym2("f.java::FileUtils.write@sha", "f.java", core.KindMethod, "write", "FileUtils", "public static void write(byte[] data, OutputStream output, int off, int len)", ""),
		javaSym2("i.java::IOUtils@sha", "i.java", core.KindClass, "IOUtils", "", "public class IOUtils", ""),
		javaSym2("i.java::IOUtils.write@sha", "i.java", core.KindMethod, "write", "IOUtils", "public static void write(byte[] data, OutputStream output, int off, int len)", ""),
		javaSym2("r.java::IORandomAccessFile@sha", "r.java", core.KindClass, "IORandomAccessFile", "",
			"public class IORandomAccessFile extends RandomAccessFile", ""),
		javaSym2("r.java::IORandomAccessFile.clear@sha", "r.java", core.KindMethod, "clear", "IORandomAccessFile",
			"public IORandomAccessFile clear() throws IOException",
			"public IORandomAccessFile clear() throws IOException { write(zeroBuffer, 0, toWrite); }",
			core.CallSite{Callee: "write", Line: 1, Argc: 3, Args: []string{"zeroBuffer", "", "toWrite"}}),
	}
	g.Replace(syms, 2)

	if hasEdge(g, core.EdgeCalls, "r.java::IORandomAccessFile.clear@sha", "f.java::FileUtils.write@sha") {
		t.Fatalf("bare write() must not fan out to an unrelated same-package class's overload")
	}
	if hasEdge(g, core.EdgeCalls, "r.java::IORandomAccessFile.clear@sha", "i.java::IOUtils.write@sha") {
		t.Fatalf("bare write() must not fan out to an unrelated same-package class's overload")
	}
}

func TestJavaBareCall_ChainedOnConstructorStillResolvesByImportScope(t *testing.T) {
	// StrSubstitutor (no superclass) does `new StrBuilder(n).append(x)`:
	// astkit cannot name the chained receiver, so the call arrives bare,
	// but StrSubstitutor extends nothing unresolved — the import-scoped
	// fallback must still bind it to the imported StrBuilder.append, not
	// be dropped by the same-package external-base rule above.
	g := New()
	syms := []core.SymbolRecord{
		javaSym2("u.java::FormattableUtils@sha", "u.java", core.KindClass, "FormattableUtils", "", "class FormattableUtils", ""),
		javaSym2("u.java::FormattableUtils.append@sha", "u.java", core.KindMethod, "append", "FormattableUtils", "static void append(char[] chars, Appendable appendable)", ""),
		javaSym2("b.java::StrBuilder@sha", "b.java", core.KindClass, "StrBuilder", "", "public class StrBuilder implements Appendable", ""),
		javaSym2("b.java::StrBuilder.append@sha", "b.java", core.KindMethod, "append", "StrBuilder", "public StrBuilder append(char[] chars)", ""),
		javaSym2("s.java::StrSubstitutor@sha", "s.java", core.KindClass, "StrSubstitutor", "",
			"public class StrSubstitutor", ""),
		javaSym2("s.java::StrSubstitutor.replace@sha", "s.java", core.KindMethod, "replace", "StrSubstitutor",
			"public String replace(char[] source)",
			"import org.apache.commons.lang3.text.StrBuilder;\npublic String replace(char[] source) { StrBuilder buf = new StrBuilder(source.length).append(source); return buf.toString(); }",
			core.CallSite{Callee: "append", Line: 1, Argc: 1, Args: []string{"source"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "s.java::StrSubstitutor.replace@sha", "b.java::StrBuilder.append@sha") {
		t.Fatalf("chained append() on an imported constructor result must still resolve")
	}
}

func TestJavaArgTypes_ThisTypesTheEnclosingClass(t *testing.T) {
	// Builder.get() does `new WildcardFileFilter(this)`: the bare "this"
	// argument types as the enclosing Builder class, picking the one
	// constructor that takes a Builder over five unrelated overloads.
	g := New()
	syms := []core.SymbolRecord{
		javaSym2("w.java::WildcardFileFilter@sha", "w.java", core.KindClass, "WildcardFileFilter", "", "public class WildcardFileFilter", ""),
		javaSym2("w.java::WildcardFileFilter.ctor1@sha", "w.java", core.KindConstructor, "WildcardFileFilter", "WildcardFileFilter", "private WildcardFileFilter(Builder builder)", ""),
		javaSym2("w.java::WildcardFileFilter.ctor2@sha", "w.java", core.KindConstructor, "WildcardFileFilter", "WildcardFileFilter", "public WildcardFileFilter(String wildcard)", ""),
		javaSym2("w.java::WildcardFileFilter.ctor3@sha", "w.java", core.KindConstructor, "WildcardFileFilter", "WildcardFileFilter", "public WildcardFileFilter(List wildcards)", ""),
		javaSym2("w.java::Builder@sha", "w.java", core.KindClass, "Builder", "WildcardFileFilter", "public static class Builder", ""),
		javaSym2("w.java::Builder.get@sha", "w.java", core.KindMethod, "get", "Builder",
			"public WildcardFileFilter get()",
			"public WildcardFileFilter get() { return new WildcardFileFilter(this); }",
			core.CallSite{Callee: "WildcardFileFilter", Line: 1, Argc: 1, Args: []string{"this"}}),
	}
	g.Replace(syms, 2)

	if !hasEdge(g, core.EdgeCalls, "w.java::Builder.get@sha", "w.java::WildcardFileFilter.ctor1@sha") {
		t.Fatalf("new WildcardFileFilter(this) must bind the Builder-typed constructor")
	}
	if hasEdge(g, core.EdgeCalls, "w.java::Builder.get@sha", "w.java::WildcardFileFilter.ctor2@sha") {
		t.Fatalf("new WildcardFileFilter(this) must not also bind an unrelated overload")
	}
	if hasEdge(g, core.EdgeCalls, "w.java::Builder.get@sha", "w.java::WildcardFileFilter.ctor3@sha") {
		t.Fatalf("new WildcardFileFilter(this) must not also bind an unrelated overload")
	}
}
