package grove

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const goInterfaceImpactSource = `package coverage

import "net/http"

type Writer interface { http.CloseNotifier }
type writer struct { http.ResponseWriter }
func (w *writer) CloseNotify() <-chan bool { return nil }
type promoted struct { *writer }
type other struct{}
func (w *other) CloseNotify() <-chan bool { return nil }
type wrongReturn struct{}
func (w *wrongReturn) CloseNotify() chan bool { return nil }
type wrongParam struct{}
func (w *wrongParam) CloseNotify(int) <-chan bool { return nil }
type wrongCase struct{}
func (w *wrongCase) Closenotify() <-chan bool { return nil }
func Stream(w Writer) { <-w.CloseNotify() }
func Promoted(w *promoted) { <-w.CloseNotify() }
func External(w http.CloseNotifier) { <-w.CloseNotify() }
func Concrete(w *writer) { <-w.CloseNotify() }
`

func TestGoInterfaceImpactEmbeddedExternal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	for path, body := range map[string]string{
		"go.mod":    "module example.com/coverage\n\ngo 1.22\n",
		"writer.go": goInterfaceImpactSource,
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	enabled := true
	eng, err := Open(ctx, Config{RepoRoot: root, NativeAnalyzers: &enabled})
	if err != nil {
		t.Fatal(err)
	}
	defer eng.Close()
	indexed, err := eng.Index(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native: %v", indexed.Native)
	for _, tc := range []struct {
		query      string
		want       []string
		wantFamily []string
		wantSupers []string
	}{
		{"Writer.CloseNotify", []string{"Stream", "Promoted", "External", "Concrete"}, []string{"other.CloseNotify", "writer.CloseNotify"}, nil},
		{"writer.CloseNotify", []string{"Stream", "Promoted", "External", "Concrete"}, nil, []string{"Writer.CloseNotify"}},
		{"other.CloseNotify", []string{"Stream", "External"}, nil, []string{"Writer.CloseNotify"}},
		{"wrongReturn.CloseNotify", nil, nil, nil},
		{"wrongParam.CloseNotify", nil, nil, nil},
		{"wrongCase.Closenotify", nil, nil, nil},
	} {
		t.Run(tc.query, func(t *testing.T) {
			impact, err := eng.ChangeImpactScoped(ctx, tc.query, "writer.go")
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]bool{}
			for _, caller := range impact.Callers {
				got[caller.Name] = true
			}
			for _, name := range tc.want {
				if !got[name] {
					t.Errorf("missing caller %s: got %v", name, got)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("caller set: got %v, want %v", got, tc.want)
			}
			var family []string
			for _, member := range impact.Family {
				family = append(family, member.QualifiedName)
			}
			sort.Strings(family)
			if !reflect.DeepEqual(family, tc.wantFamily) {
				t.Errorf("family: got %v, want %v", family, tc.wantFamily)
			}
			var supers []string
			for _, member := range impact.Supers {
				supers = append(supers, member.QualifiedName)
			}
			sort.Strings(supers)
			if !reflect.DeepEqual(supers, tc.wantSupers) {
				t.Errorf("supers: got %v, want %v", supers, tc.wantSupers)
			}
		})
	}
}

func TestGoInterfaceImpactIncrementalRemovesStaleDispatch(t *testing.T) {
	var snapshots [][]string
	for _, incremental := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "incremental"}[incremental], func(t *testing.T) {
			if incremental {
				t.Setenv("GROVE_INCREMENTAL", "1")
			} else {
				t.Setenv("GROVE_INCREMENTAL", "")
			}
			root := t.TempDir()
			parts := strings.SplitN(goInterfaceImpactSource, "func Stream", 2)
			for path, body := range map[string]string{
				"go.mod":    "module example.com/coverage\n\ngo 1.22\n",
				"writer.go": parts[0],
				"calls.go":  "package coverage\nimport \"net/http\"\nfunc Stream" + parts[1],
			} {
				if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
			enabled := true
			eng, err := Open(ctx, Config{RepoRoot: root, NativeAnalyzers: &enabled})
			if err != nil {
				t.Fatal(err)
			}
			defer eng.Close()
			if _, err := eng.Index(ctx, ""); err != nil {
				t.Fatal(err)
			}
			before, err := eng.ChangeImpactScoped(ctx, "writer.CloseNotify", "writer.go")
			if err != nil || len(before.Callers) != 4 {
				t.Fatalf("before edit: %v, %v", before, err)
			}
			edited := strings.Replace(parts[0], "(w *writer) CloseNotify() <-chan bool", "(w *writer) CloseNotify() chan bool", 1)
			if err := os.WriteFile(filepath.Join(root, "writer.go"), []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := eng.Index(ctx, ""); err != nil {
				t.Fatal(err)
			}
			after, err := eng.ChangeImpactScoped(ctx, "writer.CloseNotify", "writer.go")
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, caller := range after.Callers {
				names = append(names, caller.Name)
			}
			sort.Strings(names)
			if !reflect.DeepEqual(names, []string{"Concrete", "Promoted"}) {
				t.Fatalf("stale interface dispatch after incompatible return type: %v", names)
			}
			snapshots = append(snapshots, storedEdgeDump(t, root))
		})
	}
	if len(snapshots) == 2 && !reflect.DeepEqual(snapshots[0], snapshots[1]) {
		t.Fatal("full and incremental edge sets differ")
	}
}
