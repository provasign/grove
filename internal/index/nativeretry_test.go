package index

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/native"
	"github.com/provasign/grove/internal/parser"
	"github.com/provasign/grove/internal/store"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

// Stack audit 2026-10-20 #5: after a failed first pass (compiler
// unavailable), a plain index with no file changes returned "no file
// changes" and never retried. Now the analyzer is rechecked once its
// bounded wait has passed, and the no-change message says what is pending.
func TestNoopRechecksUnavailableNativePass(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	writeFiles(t, root, map[string]string{
		"a.ts":          "export function target() {}\nexport function caller() { target(); }\n",
		"tsconfig.json": `{"include":["*.ts"]}`,
	})
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	idx := NewWithNativeConfig(parser.NewEngine(), st, native.Config{Enabled: true})
	idx.now = clock.now
	_, initial, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(initial.Native, "\n"), "typescript not resolvable") {
		t.Skipf("fixture needs an unavailable TypeScript compiler: %v", initial.Native)
	}

	// Immediately again: within the recheck wait, a cheap no-op that says so.
	_, next, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(next.Native, "\n")
	if !strings.Contains(joined, "js-ts has no completed pass (typescript not resolvable in the project); next retry in 15s") ||
		strings.Contains(joined, "use --force to re-run analyzers") {
		t.Fatalf("pending retry not reported accurately: %v", next.Native)
	}

	// After the wait: rechecked (still unavailable), next wait doubles.
	clock.t = clock.t.Add(16 * time.Second)
	_, rechecked, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(rechecked.Native, "\n"); !strings.Contains(joined, "js-ts still unavailable (typescript not resolvable in the project); rechecked now, next check in 30s") {
		t.Fatalf("recheck not reported: %v", rechecked.Native)
	}
}

// The full recovery path with a real toolchain: the Go analyzer is
// unavailable (go not on PATH) on the first index; once go is back and the
// wait has passed, a plain index with no file changes runs and completes it.
func TestNoopRetryRecoversNativePass(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found")
	}
	root := t.TempDir()
	ctx := context.Background()
	writeFiles(t, root, map[string]string{
		"go.mod":  "module retry\n\ngo 1.22\n",
		"main.go": "package main\n\nfunc main() { run() }\n\nfunc run() {}\n",
	})
	st, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	clock := &fakeClock{t: time.Unix(1_800_000_000, 0)}
	idx := New(parser.NewEngine(), st)
	idx.now = clock.now

	origPath := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir())
	_, first, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(first.Native, "\n"), "go: skipped: go executable not found") {
		t.Fatalf("expected go unavailable: %v", first.Native)
	}

	t.Setenv("PATH", filepath.Dir(goBin)+string(os.PathListSeparator)+origPath)
	// Not yet due: still a no-op.
	_, early, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(early.Native, "\n"), "go has no completed pass") {
		t.Fatalf("expected a pending retry: %v", early.Native)
	}
	clock.t = clock.t.Add(time.Minute)
	_, retried, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(retried.Native, "\n")
	if strings.Contains(joined, "no file changes") || !strings.Contains(joined, "go: retrying its incomplete full pass") ||
		!strings.Contains(joined, "go: resolved ") {
		t.Fatalf("plain index did not recover the Go pass: %v", retried.Native)
	}
	raw, _, _ := st.GetMeta(ctx, core.MetaNativeComplete)
	if !strings.Contains(raw, `"go"`) {
		t.Fatalf("go not recorded complete after recovery: %s", raw)
	}
	// Recovered: back to a plain no-op.
	_, settled, err := idx.Index(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(settled.Native, "\n"), "no file changes since last index (use --force") {
		t.Fatalf("expected a settled no-op: %v", settled.Native)
	}
}

func TestRetryDelayIsBounded(t *testing.T) {
	cases := []struct {
		r    nativeRetry
		want time.Duration
	}{
		{nativeRetry{Failures: 1, Unavailable: true}, 15 * time.Second},
		{nativeRetry{Failures: 2, Unavailable: true}, 30 * time.Second},
		{nativeRetry{Failures: 50, Unavailable: true}, 10 * time.Minute},
		{nativeRetry{Failures: 1}, time.Minute},
		{nativeRetry{Failures: 4}, 8 * time.Minute},
		{nativeRetry{Failures: 50}, 6 * time.Hour},
	}
	for _, c := range cases {
		if got := retryDelay(c.r); got != c.want {
			t.Errorf("%+v: got %s want %s", c.r, got, c.want)
		}
	}
}
