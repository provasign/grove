package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	goversion "go/version"

	"golang.org/x/mod/modfile"
)

// The Go pass runs with GOTOOLCHAIN=local, so it never downloads a toolchain.
// When the installed go is older than the repository requires, `go list`
// fails ("go.mod requires go >= 1.27 (running go 1.26.3; GOTOOLCHAIN=local)")
// and the whole Go pass is name-based, while the user's own `go build` works
// because it switched to a newer toolchain. That toolchain is then in the
// module cache (golang.org/toolchain@v0.0.1-go1.27.4.darwin-arm64). Running
// it from there is what the go command itself would do, minus the download.

var goLocalEnv struct {
	once     sync.Once
	version  string // go env GOVERSION of the go on PATH, e.g. "go1.26.3"
	modCache string // go env GOMODCACHE
}

func loadGoLocalEnv() {
	goLocalEnv.once.Do(func() {
		cmd := exec.Command("go", "env", "GOVERSION", "GOMODCACHE")
		cmd.Env = scrubbedEnv("GOTOOLCHAIN=local", "GOWORK=off")
		out, err := cmd.Output()
		if err != nil {
			return
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 2 {
			goLocalEnv.version = strings.TrimSpace(lines[0])
			goLocalEnv.modCache = strings.TrimSpace(lines[1])
		}
	})
}

// goRequiredVersion is the newest Go the repository's go.mod (and the go.work
// analysis follows, if any) asks for through its go or toolchain line,
// as "go1.X[.Y]"; "" when nothing is declared.
func goRequiredVersion(root string) string {
	need := ""
	bump := func(v string) {
		if v == "" {
			return
		}
		if !strings.HasPrefix(v, "go") {
			v = "go" + v
		}
		if goversion.IsValid(v) && (need == "" || goversion.Compare(v, need) > 0) {
			need = v
		}
	}
	if data, err := os.ReadFile(filepath.Join(root, "go.mod")); err == nil {
		// Parse, not ParseLax: ParseLax drops the toolchain line.
		f, err := modfile.Parse("go.mod", data, nil)
		if err != nil {
			f, err = modfile.ParseLax("go.mod", data, nil)
		}
		if err == nil {
			if f.Go != nil {
				bump(f.Go.Version)
			}
			if f.Toolchain != nil {
				bump(f.Toolchain.Name)
			}
		}
	}
	if ws, ok := goWorkspaceFor(root); ok {
		if data, err := os.ReadFile(ws.file); err == nil {
			if w, err := modfile.ParseWork(ws.file, data, nil); err == nil {
				if w.Go != nil {
					bump(w.Go.Version)
				}
				if w.Toolchain != nil {
					bump(w.Toolchain.Name)
				}
			}
		}
	}
	return need
}

// goToolchain is a toolchain the Go pass runs instead of the go on PATH.
type goToolchain struct {
	version string // "go1.27.4"
	goroot  string
}

// goToolchainFor returns a toolchain from the module cache that satisfies
// the repository's go requirement when the installed go does not: the
// oldest cached one that does. ok=false keeps the go on PATH (it is new
// enough, nothing suitable is cached, or the repository is untrusted).
func goToolchainFor(root string) (goToolchain, bool) {
	if untrustedMode() {
		return goToolchain{}, false
	}
	loadGoLocalEnv()
	local := goLocalEnv.version
	need := goRequiredVersion(root)
	if local == "" || need == "" || !goversion.IsValid(local) || goversion.Compare(local, need) >= 0 {
		return goToolchain{}, false
	}
	return cachedGoToolchain(goLocalEnv.modCache, need)
}

func cachedGoToolchain(modCache, need string) (goToolchain, bool) {
	if modCache == "" {
		return goToolchain{}, false
	}
	suffix := "." + runtime.GOOS + "-" + runtime.GOARCH
	dirs, _ := filepath.Glob(filepath.Join(modCache, "golang.org", "toolchain@v0.0.1-go*"+suffix))
	var found []goToolchain
	for _, dir := range dirs {
		v := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(dir), "toolchain@v0.0.1-"), suffix)
		if !goversion.IsValid(v) || goversion.Compare(v, need) < 0 {
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, "bin", "go")); err != nil || info.IsDir() {
			continue
		}
		found = append(found, goToolchain{version: v, goroot: dir})
	}
	if len(found) == 0 {
		return goToolchain{}, false
	}
	sort.Slice(found, func(i, j int) bool { return goversion.Compare(found[i].version, found[j].version) < 0 })
	return found[0], true
}

// goCommand runs the go command for the Go pass over root: the cached
// toolchain goToolchainFor picks, else the go on PATH, in goAnalyzerEnv.
func goCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	env := goAnalyzerEnv(root)
	name := "go"
	if tc, ok := goToolchainFor(root); ok {
		name = filepath.Join(tc.goroot, "bin", "go")
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				env[i] = "PATH=" + filepath.Join(tc.goroot, "bin") + string(os.PathListSeparator) + kv[len("PATH="):]
			}
		}
		env = append(withoutEnv(env, "GOROOT"), "GOROOT="+tc.goroot)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = root
	cmd.Env = env
	return cmd
}

func withoutEnv(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

// goToolchainNote says which toolchain ran when it is not the installed one,
// or why none could, for the Go pass diagnostics; "" when the go on PATH
// satisfies the repository.
func goToolchainNote(root string) string {
	if tc, ok := goToolchainFor(root); ok {
		return "go list ran with cached toolchain " + tc.version + " (repository requires " + goRequiredVersion(root) +
			"; installed " + goLocalEnv.version + ")"
	}
	return ""
}
