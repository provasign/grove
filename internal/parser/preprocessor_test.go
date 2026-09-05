package parser

import (
	"strings"
	"testing"
)

// `#if A ... else ... #else ... else ... #endif` hands the C# grammar two
// else branches; the blanked variant keeps the #if branch, drops the
// #else branch and every directive, and preserves line numbers.
func TestBlankPreprocessorBranches(t *testing.T) {
	src := "a();\n#if HAVE_X\n  b();\n#else\n  c();\n#endif\nd();\n"
	got := string(blankPreprocessorBranches([]byte(src)))
	want := "a();\n\n  b();\n\n\n\nd();\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.Count(got, "\n") != strings.Count(src, "\n") {
		t.Fatalf("line count changed")
	}
	if blankPreprocessorBranches([]byte("no directives\n")) != nil {
		t.Fatalf("no #if must return nil")
	}
	// Nested: an #else inside a kept branch is blanked; the outer branch stays.
	nested := "#if A\nx();\n#if B\ny();\n#else\nz();\n#endif\nw();\n#endif\n"
	got = string(blankPreprocessorBranches([]byte(nested)))
	if !strings.Contains(got, "x();") || !strings.Contains(got, "y();") || strings.Contains(got, "z();") || !strings.Contains(got, "w();") {
		t.Fatalf("nested handling wrong: %q", got)
	}
}

// A C# file split by #if/#else parses cleaner on the blanked variant and
// keeps the call sites the ERROR recovery swallowed.
func TestPreprocessorReparseKeepsCallSites(t *testing.T) {
	src := `class A {
    void F(object value) {
        if (value is int)
        {
            One();
        }
#if HAVE_X
        else if (value is long)
        {
            Two();
        }
#else
        else
        {
            Three();
        }
#endif
        else
        {
            Four();
        }
    }
}`
	syms, ok, hasErrors := extractSymbolsFromAST("csharp", "a.cs", "sha", []byte(src), nil)
	if !ok {
		t.Fatal("csharp not supported")
	}
	if hasErrors {
		t.Fatalf("expected the blanked re-parse to be clean")
	}
	var callees []string
	for _, s := range syms {
		if s.Name == "F" {
			for _, cs := range s.CallSites {
				callees = append(callees, cs.Callee)
			}
		}
	}
	for _, want := range []string{"One", "Two", "Four"} {
		found := false
		for _, c := range callees {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("call to %s missing from %v", want, callees)
		}
	}
}
