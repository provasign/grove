package graph

import (
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// Two files of one package each declare an anonymous class at line 4,
// column 12, so both are named `<anonymous@4:12>` (guava had 4,308 anonymous
// classes under 2,289 names).
func anonymousTwins(aBase, bBase string, aMethods, bMethods []string) []core.SymbolRecord {
	syms := []core.SymbolRecord{
		{ID: "p/Task.java::Task", FilePath: "p/Task.java", Language: "java", Kind: core.KindInterface,
			Name: "Task", QualifiedName: "Task", Signature: "interface Task",
			RawText: "interface Task {\n  void run();\n  void start();\n}"},
		{ID: "p/Task.java::Task.run", FilePath: "p/Task.java", Language: "java", Kind: core.KindMethod,
			Name: "run", QualifiedName: "Task.run", ParentSymbol: "Task", Signature: "void run();"},
		{ID: "p/Task.java::Task.start", FilePath: "p/Task.java", Language: "java", Kind: core.KindMethod,
			Name: "start", QualifiedName: "Task.start", ParentSymbol: "Task", Signature: "void start();"},
	}
	add := func(file, owner, base string, methods []string) {
		anon := "<anonymous@4:12>"
		syms = append(syms, core.SymbolRecord{
			ID: file + "::" + owner + "." + anon, FilePath: file, Language: "java", Kind: core.KindClass,
			Name: anon, QualifiedName: owner + "." + anon, ParentSymbol: owner,
			Signature: "class " + anon + " extends " + base, Span: core.LineRange{Start: 4, End: 6},
		})
		for _, m := range methods {
			syms = append(syms, core.SymbolRecord{
				ID: file + "::" + owner + "." + anon + "." + m, FilePath: file, Language: "java", Kind: core.KindMethod,
				Name: m, QualifiedName: owner + "." + anon + "." + m, ParentSymbol: anon,
				Signature: "public void " + m + "()", Span: core.LineRange{Start: 5, End: 5},
			})
		}
	}
	add("p/A.java", "A", aBase, aMethods)
	add("p/B.java", "B", bBase, bMethods)
	return syms
}

func ids(syms []*core.SymbolRecord) []string {
	var out []string
	for _, s := range syms {
		out = append(out, s.ID)
	}
	return out
}

func TestSubclassOverrides_AnonymousClassesStayInTheirFile(t *testing.T) {
	syms := anonymousTwins("Task", "Thread", []string{"run"}, []string{"run"})
	want := []string{"p/A.java::A.<anonymous@4:12>.run"}
	for _, order := range [][]core.SymbolRecord{syms, {syms[0], syms[1], syms[2], syms[5], syms[6], syms[3], syms[4]}} {
		got := ids(subclassOverrides(newEdgeIndex(order), "java", "Task", "run", ""))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Task.run overrides = %v, want %v", got, want)
		}
	}
}

func TestInterfaceSatisfaction_AnonymousMethodSetsStayInTheirFile(t *testing.T) {
	// Neither class has both of Task's methods; pooled by name they did.
	syms := anonymousTwins("Thread", "Thread", []string{"run"}, []string{"start"})
	sat, _ := buildInterfaceSatisfaction(newEdgeIndex(syms), syms)
	for _, m := range []string{"run", "start"} {
		if got := sat.implementorsFor(&syms[0], m); len(got) > 0 {
			t.Fatalf("Task.%s implementors = %v, want none", m, ids(got))
		}
	}
}

func TestJavaLocalTypes_AnonymousOwnerFieldsFromItsOwnFile(t *testing.T) {
	// A's anonymous class extends Task (field helper: Helper), B's extends
	// Thread (field helper: Wrong). A method of each sees its own base's.
	syms := anonymousTwins("Task", "Thread", nil, nil)
	syms = append(syms,
		core.SymbolRecord{ID: "p/Task.java::Task.helper", FilePath: "p/Task.java", Language: "java", Kind: core.KindField,
			Name: "helper", QualifiedName: "Task.helper", ParentSymbol: "Task", RawText: "  Helper helper = null;"},
		core.SymbolRecord{ID: "p/Thread.java::Thread", FilePath: "p/Thread.java", Language: "java", Kind: core.KindClass,
			Name: "Thread", QualifiedName: "Thread", Signature: "class Thread"},
		core.SymbolRecord{ID: "p/Thread.java::Thread.helper", FilePath: "p/Thread.java", Language: "java", Kind: core.KindField,
			Name: "helper", QualifiedName: "Thread.helper", ParentSymbol: "Thread", RawText: "  Wrong helper;"},
	)
	for file, want := range map[string]string{"p/A.java": "Helper", "p/B.java": "Wrong"} {
		owner := file[2:3]
		caller := core.SymbolRecord{
			ID: file + "::" + owner + ".<anonymous@4:12>.go", FilePath: file, Language: "java", Kind: core.KindMethod,
			Name: "go", QualifiedName: owner + ".<anonymous@4:12>.go", ParentSymbol: "<anonymous@4:12>",
			RawText: "public void go() {\n  helper.run();\n}", Span: core.LineRange{Start: 5, End: 7},
		}
		all := append(append([]core.SymbolRecord{}, syms...), caller)
		if got := javaLocalTypes(newEdgeIndex(all), &caller)["helper"]; got != want {
			t.Fatalf("%s: helper = %q, want %s (the field of its own anonymous class's base)", file, got, want)
		}
	}
}
