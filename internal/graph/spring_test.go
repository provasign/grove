package graph

import (
	"testing"

	"github.com/provasign/grove/internal/core"
)

func TestSplitDerivedProps(t *testing.T) {
	cases := map[string][]string{
		"LoanId":            {"LoanId"},
		"LoanIdAndStatus":   {"LoanId", "Status"},
		"NameOrEmail":       {"Name", "Email"},
		"AndroidId":         {"AndroidId"}, // "And" inside a word must not split
		"OrderDateAndTotal": {"OrderDate", "Total"},
	}
	for in, want := range cases {
		got := splitDerivedProps(in)
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", in, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", in, got, want)
				break
			}
		}
	}
}

func TestBuildFrameworkEdges(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "e1", Language: "java", Kind: core.KindClass, Name: "Loan", QualifiedName: "Loan", FilePath: "Loan.java"},
		{ID: "g1", Language: "java", Kind: core.KindMethod, Name: "getLoanId", QualifiedName: "Loan.getLoanId", ParentSymbol: "Loan", FilePath: "Loan.java"},
		{ID: "g2", Language: "java", Kind: core.KindMethod, Name: "getStatus", QualifiedName: "Loan.getStatus", ParentSymbol: "Loan", FilePath: "Loan.java"},
		{ID: "gx", Language: "java", Kind: core.KindMethod, Name: "getLoanId", QualifiedName: "Other.getLoanId", ParentSymbol: "Other", FilePath: "Other.java"},
		{ID: "r1", Language: "java", Kind: core.KindInterface, Name: "LoanRepo", QualifiedName: "LoanRepo",
			Signature: "public interface LoanRepo extends JpaRepository<Loan, Long>", FilePath: "LoanRepo.java"},
		{ID: "m1", Language: "java", Kind: core.KindMethod, Name: "findByLoanIdAndStatus", QualifiedName: "LoanRepo.findByLoanIdAndStatus", ParentSymbol: "LoanRepo", FilePath: "LoanRepo.java"},
		{ID: "t1", Language: "plaintext", Kind: core.KindFile, Name: "view", FilePath: "templates/view.html",
			RawText: `<td th:text="${loan.loanId}"></td><input th:field="*{status}"/>`},
	}
	idx := newEdgeIndex(syms)
	edges := buildFrameworkEdges(idx, syms)
	got := map[string]float64{}
	for _, e := range edges {
		got[e.From+"->"+e.To] = e.Confidence
	}
	// Derived query: both properties, scoped to the repo's OWN entity —
	// Other.getLoanId must not receive a derived-query edge.
	if got["m1->g1"] != 0.7 || got["m1->g2"] != 0.7 {
		t.Errorf("derived-query edges missing: %v", got)
	}
	if _, ok := got["m1->gx"]; ok {
		t.Error("derived query leaked to another entity's accessor")
	}
	// Template EL: both identifiers, estate-wide (gx legitimately included).
	if got["t1->g1"] != 0.6 || got["t1->g2"] != 0.6 || got["t1->gx"] != 0.6 {
		t.Errorf("template EL edges missing: %v", got)
	}
}

func TestBuildFrameworkEdges_AngularFields(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "c1", Language: "typescript", Kind: core.KindClass, Name: "HeaderComponent", QualifiedName: "HeaderComponent", FilePath: "header.component.ts"},
		{ID: "f1", Language: "typescript", Kind: core.KindField, Name: "currentUser", QualifiedName: "HeaderComponent.currentUser", ParentSymbol: "HeaderComponent", FilePath: "header.component.ts"},
		{ID: "f2", Language: "typescript", Kind: core.KindField, Name: "isLoggedIn", QualifiedName: "HeaderComponent.isLoggedIn", ParentSymbol: "HeaderComponent", FilePath: "header.component.ts"},
		{ID: "t1", Language: "plaintext", Kind: core.KindFile, Name: "tmpl", FilePath: "header.component.html",
			RawText: `<span>{{ currentUser.username }}</span><div [hidden]="isLoggedIn" (click)="logout()"></div>`},
	}
	idx := newEdgeIndex(syms)
	edges := buildFrameworkEdges(idx, syms)
	got := map[string]bool{}
	for _, e := range edges {
		got[e.From+"->"+e.To] = true
	}
	if !got["t1->f1"] {
		t.Errorf("interpolation {{ currentUser... }} missed field edge: %v", got)
	}
	if !got["t1->f2"] {
		t.Errorf("[hidden]=\"isLoggedIn\" missed property-binding edge: %v", got)
	}
}

func TestBuildFrameworkEdges_PythonJinja(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "c1", Language: "python", Kind: core.KindClass, Name: "User", QualifiedName: "User", FilePath: "models.py"},
		{ID: "f1", Language: "python", Kind: core.KindField, Name: "username", QualifiedName: "User.username", ParentSymbol: "User", FilePath: "models.py"},
		{ID: "m1", Language: "python", Kind: core.KindMethod, Name: "full_name", QualifiedName: "User.full_name", ParentSymbol: "User", FilePath: "models.py",
			Annotations: []string{"property"}},
		{ID: "m2", Language: "python", Kind: core.KindMethod, Name: "full_name", QualifiedName: "User.full_name.setter", ParentSymbol: "User", FilePath: "models.py",
			Annotations: []string{"full_name.setter"}},
		{ID: "t1", Language: "plaintext", Kind: core.KindFile, Name: "tmpl", FilePath: "user.html",
			RawText: `<h1>{{ user.username }}</h1><p>{{ user.full_name }}</p>`},
	}
	idx := newEdgeIndex(syms)
	edges := buildFrameworkEdges(idx, syms)
	got := map[string]bool{}
	for _, e := range edges {
		got[e.From+"->"+e.To] = true
	}
	if !got["t1->f1"] {
		t.Errorf("Jinja {{ user.username }} missed field edge: %v", got)
	}
	if !got["t1->m1"] {
		t.Errorf("Jinja {{ user.full_name }} missed @property edge: %v", got)
	}
	if got["t1->m2"] {
		t.Error("setter method should not be an accessor target")
	}
}

func TestFieldImpactLocked(t *testing.T) {
	syms := []core.SymbolRecord{
		{ID: "c1", Language: "typescript", Kind: core.KindClass, Name: "HeaderComponent", QualifiedName: "HeaderComponent", FilePath: "header.component.ts"},
		{ID: "f1", Language: "typescript", Kind: core.KindField, Name: "currentUser", QualifiedName: "HeaderComponent.currentUser", ParentSymbol: "HeaderComponent", FilePath: "header.component.ts"},
		{ID: "t1", Language: "plaintext", Kind: core.KindFile, Name: "tmpl", FilePath: "header.component.html",
			RawText: `<span>{{ currentUser.username }}</span>`},
		// A same-named METHOD on a different type must not confuse the "does
		// a method exist" guard — it's scoped per query's own typeIDs.
		{ID: "c2", Language: "typescript", Kind: core.KindClass, Name: "Other", QualifiedName: "Other", FilePath: "other.ts"},
		{ID: "m1", Language: "typescript", Kind: core.KindMethod, Name: "currentUser", QualifiedName: "Other.currentUser", ParentSymbol: "Other", FilePath: "other.ts"},
	}
	// EdgeContains for the field: buildFrameworkEdges only emits CALLS
	// edges, so fieldImpactLocked's own contains-scan needs its own edge —
	// same as any real index (buildContains emits it from ParentSymbol).
	extra := []core.Edge{{From: "c1", To: "f1", Type: core.EdgeContains}}
	g := New()
	g.ReplaceWithEdges(syms, extra, len(syms))

	r := g.fieldImpactLocked("HeaderComponent.currentUser")
	if r == nil {
		t.Fatal("fieldImpactLocked returned nil for a real field")
	}
	if len(r.Declarations) != 1 || r.Declarations[0].ID != "f1" {
		t.Fatalf("declarations = %+v", r.Declarations)
	}
	if len(r.Callers) != 1 || r.Callers[0].ID != "t1" {
		t.Fatalf("callers = %+v", r.Callers)
	}
	if r.Completeness != "callers-only" || !r.HasHeuristicRefs {
		t.Fatalf("completeness=%s heuristic=%v", r.Completeness, r.HasHeuristicRefs)
	}

	// A method of the same name on THIS type must win — nil defers to the
	// normal method path.
	if r2 := g.fieldImpactLocked("Other.currentUser"); r2 != nil {
		t.Fatalf("field path fired despite a same-name method: %+v", r2)
	}
}
