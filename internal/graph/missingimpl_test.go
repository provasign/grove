package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

// missingImplFixture: interface Codec { encode(Object) } with
//   - JsonCodec implements Codec, declares encode        -> implemented
//   - XmlCodec implements Codec, no encode               -> MISSING
//   - BaseCodec (abstract) implements Codec, no encode   -> abstract-missing
//   - YamlCodec extends BaseCodec, no encode             -> MISSING
//   - ZipCodec extends JsonCodec, no own encode          -> implemented (inherited)
//   - WireCodec implements Codec, extends external Frame -> unverifiable
//   - SubCodec (interface) extends Codec                 -> owes nothing
func missingImplFixture() *CodeGraph {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Codec.java::Codec@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindInterface,
			Name: "Codec", QualifiedName: "Codec", Signature: "public interface Codec"},
		{ID: "Codec.java::Codec.encode@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Codec.encode", ParentSymbol: "Codec",
			Signature: "byte[] encode(Object value)"},

		{ID: "JsonCodec.java::JsonCodec@sha", FilePath: "JsonCodec.java", Language: "java", Kind: core.KindClass,
			Name: "JsonCodec", QualifiedName: "JsonCodec", Signature: "public class JsonCodec implements Codec"},
		{ID: "JsonCodec.java::JsonCodec.encode@sha", FilePath: "JsonCodec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "JsonCodec.encode", ParentSymbol: "JsonCodec",
			Signature: "public byte[] encode(Object value)",
			RawText:   "public byte[] encode(Object value) { return null; }"},

		{ID: "XmlCodec.java::XmlCodec@sha", FilePath: "XmlCodec.java", Language: "java", Kind: core.KindClass,
			Name: "XmlCodec", QualifiedName: "XmlCodec", Signature: "public class XmlCodec implements Codec"},

		{ID: "BaseCodec.java::BaseCodec@sha", FilePath: "BaseCodec.java", Language: "java", Kind: core.KindClass,
			Name: "BaseCodec", QualifiedName: "BaseCodec", Modifiers: []string{"public", "abstract"},
			Signature: "public abstract class BaseCodec implements Codec"},
		{ID: "YamlCodec.java::YamlCodec@sha", FilePath: "YamlCodec.java", Language: "java", Kind: core.KindClass,
			Name: "YamlCodec", QualifiedName: "YamlCodec", Signature: "public class YamlCodec extends BaseCodec"},

		{ID: "ZipCodec.java::ZipCodec@sha", FilePath: "ZipCodec.java", Language: "java", Kind: core.KindClass,
			Name: "ZipCodec", QualifiedName: "ZipCodec", Signature: "public class ZipCodec extends JsonCodec"},

		{ID: "WireCodec.java::WireCodec@sha", FilePath: "WireCodec.java", Language: "java", Kind: core.KindClass,
			Name: "WireCodec", QualifiedName: "WireCodec",
			Signature: "public class WireCodec extends Frame implements Codec"},

		{ID: "SubCodec.java::SubCodec@sha", FilePath: "SubCodec.java", Language: "java", Kind: core.KindInterface,
			Name: "SubCodec", QualifiedName: "SubCodec", Signature: "public interface SubCodec extends Codec"},
	}, 3)
	return g
}

func names(recs []core.SymbolRecord) map[string]bool {
	out := map[string]bool{}
	for _, r := range recs {
		out[r.Name] = true
	}
	return out
}

func TestMissingImplementationsBuckets(t *testing.T) {
	g := missingImplFixture()
	r, err := g.MissingImplementations("Codec.encode")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	if len(r.Contract) != 1 || r.Contract[0].QualifiedName != "Codec.encode" {
		t.Fatalf("contract = %+v", r.Contract)
	}
	miss := names(r.Missing)
	if !miss["XmlCodec"] || !miss["YamlCodec"] || len(r.Missing) != 2 {
		t.Errorf("Missing = %v, want exactly {XmlCodec, YamlCodec}", miss)
	}
	if ab := names(r.AbstractMissing); !ab["BaseCodec"] || len(r.AbstractMissing) != 1 {
		t.Errorf("AbstractMissing = %v, want exactly {BaseCodec}", ab)
	}
	if uv := names(r.Unverifiable); !uv["WireCodec"] || len(r.Unverifiable) != 1 {
		t.Errorf("Unverifiable = %v, want exactly {WireCodec}", uv)
	}
	// JsonCodec (own impl) + ZipCodec (inherited via extends).
	if r.ImplementedCount != 2 {
		t.Errorf("ImplementedCount = %d, want 2", r.ImplementedCount)
	}
	if r.DefaultProvided {
		t.Error("DefaultProvided = true for a body-less interface member")
	}
	if r.Completeness != "closed" {
		t.Errorf("Completeness = %q, want closed", r.Completeness)
	}
	if miss["SubCodec"] || miss["JsonCodec"] || miss["ZipCodec"] {
		t.Errorf("Missing wrongly contains a covered type or interface: %v", miss)
	}
}

func TestMissingImplementationsDefaultMethod(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "Codec.java::Codec@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindInterface,
			Name: "Codec", QualifiedName: "Codec", Signature: "public interface Codec"},
		{ID: "Codec.java::Codec.encode@sha", FilePath: "Codec.java", Language: "java", Kind: core.KindMethod,
			Name: "encode", QualifiedName: "Codec.encode", ParentSymbol: "Codec",
			Signature: "default byte[] encode(Object value)",
			RawText:   "default byte[] encode(Object value) { return new byte[0]; }"},
		{ID: "XmlCodec.java::XmlCodec@sha", FilePath: "XmlCodec.java", Language: "java", Kind: core.KindClass,
			Name: "XmlCodec", QualifiedName: "XmlCodec", Signature: "public class XmlCodec implements Codec"},
	}, 3)
	r, err := g.MissingImplementations("Codec.encode")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	if !r.DefaultProvided {
		t.Fatal("DefaultProvided = false for a default method")
	}
	// Buckets are still computed under a default body: XmlCodec inherits the
	// default and is what breaks if the member becomes abstract/required.
	if miss := names(r.Missing); !miss["XmlCodec"] || len(r.Missing) != 1 {
		t.Errorf("Missing = %v, want exactly {XmlCodec} (inherits the default)", miss)
	}
}

func TestMissingImplementationsExternalContract(t *testing.T) {
	// Seed interface extends java.util.Iterator: completeness must degrade
	// to project-local, mirroring ChangeImpact's contract-boundary report.
	g := mapIteratorFixture()
	r, err := g.MissingImplementations("MapIterator.next")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	if r.Completeness != "project-local" {
		t.Errorf("Completeness = %q, want project-local (extends java.util.Iterator)", r.Completeness)
	}
	if len(r.OverridesExternal) == 0 {
		t.Error("OverridesExternal empty for a JDK-contract member")
	}
	// EntrySetMapIterator implements next -> nothing missing.
	if len(r.Missing) != 0 || r.ImplementedCount != 1 {
		t.Errorf("Missing=%v ImplementedCount=%d, want none missing / 1 implemented",
			names(r.Missing), r.ImplementedCount)
	}
}

func TestMissingImplementationsPythonABC(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "ops.py::BaseOps@sha", FilePath: "ops.py", Language: "python", Kind: core.KindClass,
			Name: "BaseOps", QualifiedName: "BaseOps", RawText: "class BaseOps(ABC):"},
		{ID: "ops.py::BaseOps.quote@sha", FilePath: "ops.py", Language: "python", Kind: core.KindMethod,
			Name: "quote", QualifiedName: "BaseOps.quote", ParentSymbol: "BaseOps",
			Signature: "def quote(self, name)",
			RawText:   "@abstractmethod\ndef quote(self, name):\n    raise NotImplementedError"},
		{ID: "mysql.py::MysqlOps@sha", FilePath: "mysql.py", Language: "python", Kind: core.KindClass,
			Name: "MysqlOps", QualifiedName: "MysqlOps", RawText: "class MysqlOps(BaseOps):"},
		{ID: "mysql.py::MysqlOps.quote@sha", FilePath: "mysql.py", Language: "python", Kind: core.KindMethod,
			Name: "quote", QualifiedName: "MysqlOps.quote", ParentSymbol: "MysqlOps",
			Signature: "def quote(self, name)", RawText: "def quote(self, name):\n    return name"},
		{ID: "sqlite.py::SqliteOps@sha", FilePath: "sqlite.py", Language: "python", Kind: core.KindClass,
			Name: "SqliteOps", QualifiedName: "SqliteOps", RawText: "class SqliteOps(BaseOps):"},
	}, 3)
	r, err := g.MissingImplementations("BaseOps.quote")
	if err != nil {
		t.Fatalf("MissingImplementations: %v", err)
	}
	if r.DefaultProvided {
		t.Fatal("DefaultProvided = true for an @abstractmethod member")
	}
	if miss := names(r.Missing); !miss["SqliteOps"] || len(r.Missing) != 1 {
		t.Errorf("Missing = %v, want exactly {SqliteOps}", miss)
	}
	if r.ImplementedCount != 1 {
		t.Errorf("ImplementedCount = %d, want 1 (MysqlOps)", r.ImplementedCount)
	}
}
