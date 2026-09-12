package graph

import (
	"strings"
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestBuildEdgesDoesNotCrossLanguageFamilies(t *testing.T) {
	symbols := []core.SymbolRecord{
		{ID: "contract.go::Runner@sha", FilePath: "contract.go", Language: "go", Kind: core.KindInterface, Name: "Runner", QualifiedName: "Runner", RawText: "type Runner interface { Run() }"},
		{ID: "worker.py::Worker@sha", FilePath: "worker.py", Language: "python", Kind: core.KindClass, Name: "Worker", QualifiedName: "Worker", RawText: "class Worker(Base): pass"},
		{ID: "worker.py::Worker.Run@sha", FilePath: "worker.py", Language: "python", Kind: core.KindMethod, Name: "Run", QualifiedName: "Worker.Run", ParentSymbol: "Worker"},
		{ID: "base.go::Base@sha", FilePath: "base.go", Language: "go", Kind: core.KindStruct, Name: "Base", QualifiedName: "Base"},
	}
	g := New()
	g.Replace(symbols, len(symbols))

	if hasEdge(g, core.EdgeImplements, "worker.py::Worker@sha", "contract.go::Runner@sha") ||
		hasEdge(g, core.EdgeOverrides, "worker.py::Worker.Run@sha", "contract.go::Runner@sha") {
		t.Fatal("Python method set must not satisfy a Go interface")
	}
	if hasEdge(g, core.EdgeExtends, "worker.py::Worker@sha", "base.go::Base@sha") {
		t.Fatal("Python class must not extend a same-named Go type")
	}
}

func TestGraphAPIsRejectCrossLanguageTypeAmbiguity(t *testing.T) {
	g := New()
	g.Replace([]core.SymbolRecord{
		{ID: "user.go::User@sha", FilePath: "user.go", Language: "go", Kind: core.KindStruct, Name: "User", QualifiedName: "User"},
		{ID: "user.go::User.Close@sha", FilePath: "user.go", Language: "go", Kind: core.KindMethod, Name: "Close", QualifiedName: "User.Close", ParentSymbol: "User"},
		{ID: "user.py::User@sha", FilePath: "user.py", Language: "python", Kind: core.KindClass, Name: "User", QualifiedName: "User"},
		{ID: "user.py::User.Close@sha", FilePath: "user.py", Language: "python", Kind: core.KindMethod, Name: "Close", QualifiedName: "User.Close", ParentSymbol: "User"},
	}, 2)

	for name, call := range map[string]func() error{
		"change-impact":           func() error { _, err := g.ChangeImpact("User.Close"); return err },
		"missing-implementations": func() error { _, err := g.MissingImplementations("User.Close"); return err },
		"rename-plan":             func() error { _, err := g.RenamePlan("User.Close", "Shutdown"); return err },
	} {
		if err := call(); err == nil || !strings.Contains(err.Error(), "ambiguous across language families") {
			t.Fatalf("%s error = %v, want cross-language ambiguity", name, err)
		}
	}

	if result, err := g.ChangeImpactScoped("User.Close", "user.go"); err != nil {
		t.Fatalf("file-scoped change-impact should resolve: %v", err)
	} else if len(result.Declarations) != 1 || result.Declarations[0].Language != "go" {
		t.Fatalf("file-scoped declarations = %+v, want only Go", result.Declarations)
	}
}

func TestSearchAndImpactPreferExactCaseAcrossLanguageFamilies(t *testing.T) {
	g := New()
	g.ReplaceWithEdges([]core.SymbolRecord{
		{ID: "user.go::User", FilePath: "user.go", Language: "go", Kind: core.KindStruct, Name: "User", QualifiedName: "User"},
		{ID: "use.go::run", FilePath: "use.go", Language: "go", Kind: core.KindFunction, Name: "run", QualifiedName: "run"},
		{ID: "job.cbl::USER", FilePath: "job.cbl", Language: "cobol", Kind: core.SymbolKind("program"), Name: "USER", QualifiedName: "USER"},
		{ID: "job.jcl::STEP", FilePath: "job.jcl", Language: "jcl", Kind: core.SymbolKind("step"), Name: "STEP", QualifiedName: "JOB.STEP"},
	}, []core.Edge{
		{From: "use.go::run", To: "user.go::User", Type: core.EdgeUsesType},
		{From: "job.jcl::STEP", To: "job.cbl::USER", Type: core.EdgeCalls},
	}, 4)

	results := g.Search("User", 50)
	if len(results) != 1 || results[0].ID != "user.go::User" {
		t.Fatalf("case-exact search = %+v", results)
	}
	impact := g.Impact("User", 1)
	if len(impact) != 1 || impact[0].ID != "use.go::run" {
		t.Fatalf("case-exact impact = %+v", impact)
	}
	upperImpact := g.Impact("USER", 1)
	if len(upperImpact) != 1 || upperImpact[0].ID != "job.jcl::STEP" {
		t.Fatalf("mainframe impact = %+v", upperImpact)
	}
}
