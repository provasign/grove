package store

import (
	"context"
	"reflect"
	"testing"

	"github.com/provasign/grove/internal/core"
)

// A file's import list is stored once; every symbol still reads it back, a
// symbol whose list differs keeps its own, and an empty list stays empty.
func TestFileImportsStoredOnceAndReadBack(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	shared := []string{"os", "django.db"}
	sym := func(name string, imports []string, line int) core.SymbolRecord {
		return core.SymbolRecord{ID: "m.py::" + name, FilePath: "m.py", BlobSHA: "s", Language: "python",
			Kind: core.KindFunction, Name: name, QualifiedName: name,
			Span: core.LineRange{Start: line, End: line}, Imports: imports}
	}
	must(t, st.UpsertFile(ctx, "m.py", "s", "python", -1, -1, []core.SymbolRecord{
		sym("a", shared, 1), sym("b", shared, 2), sym("odd", []string{"sys"}, 3), sym("bare", nil, 4),
	}))

	var markers int
	must(t, st.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM symbols WHERE imports = ?`, fileImportsMarker).Scan(&markers))
	if markers != 2 {
		t.Fatalf("symbols storing the marker = %d, want 2", markers)
	}

	want := map[string][]string{"a": shared, "b": shared, "odd": {"sys"}, "bare": nil}
	check := func(label string, got []core.SymbolRecord) {
		t.Helper()
		for _, s := range got {
			if w := want[s.Name]; !(len(w) == 0 && len(s.Imports) == 0) && !reflect.DeepEqual(s.Imports, w) {
				t.Fatalf("%s: %s imports = %#v, want %#v", label, s.Name, s.Imports, want[s.Name])
			}
		}
	}
	all, err := st.AllSymbols(ctx)
	must(t, err)
	if len(all) != 4 {
		t.Fatalf("AllSymbols = %d symbols, want 4", len(all))
	}
	check("AllSymbols", all)
	some, err := st.SymbolsForFiles(ctx, []string{"m.py"})
	must(t, err)
	check("SymbolsForFiles", some)

	// Re-upserting with a different list replaces the file row too.
	must(t, st.UpsertFile(ctx, "m.py", "s2", "python", -1, -1, []core.SymbolRecord{sym("a", []string{"json"}, 1)}))
	all, err = st.AllSymbols(ctx)
	must(t, err)
	if len(all) != 1 || !reflect.DeepEqual(all[0].Imports, []string{"json"}) {
		t.Fatalf("after re-upsert: %+v", all)
	}
}
