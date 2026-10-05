package native

import (
	"bufio"
	"bytes"
	"context"

	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
)

// A resident TypeScript worker: one long-lived node process per repository
// root, running tsScript in GROVE_TS_WORKER mode. It keeps parsed source
// files and each project's previous program between index runs, so an edit
// re-parses only the edited files instead of the whole project (typeorm: the
// one-shot pass spent ~2s of 4.2s starting node and rebuilding programs).
//
// Only resident engines (Request.Resident) use it; one-shot CLI runs keep the
// per-run process. GROVE_TS_WORKER=0 turns it off. Any worker failure stops
// the worker and the run falls back to the one-shot process, so a broken
// worker costs one slow run, never a missing result.

type tsWorker struct {
	mu    sync.Mutex
	dir   string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *bufio.Reader
	errs  *bytes.Buffer
}

var tsWorkers = struct {
	sync.Mutex
	m map[string]*tsWorker
}{m: map[string]*tsWorker{}}

func tsWorkerEnabled() bool { return os.Getenv("GROVE_TS_WORKER") != "0" }

// tsWorkerFor returns the worker for a root, keyed by the node working
// directory too (untrusted mode runs node from a neutral directory).
func tsWorkerFor(root, dir string) *tsWorker {
	tsWorkers.Lock()
	defer tsWorkers.Unlock()
	key := root + "\x00" + dir
	w := tsWorkers.m[key]
	if w == nil {
		w = &tsWorker{dir: dir}
		tsWorkers.m[key] = w
	}
	return w
}

// StopTSWorkers stops the TypeScript workers of a repository root (every
// root when root is ""). Engines call it on Close.
func StopTSWorkers(root string) {
	tsWorkers.Lock()
	defer tsWorkers.Unlock()
	for key, w := range tsWorkers.m {
		if root == "" || key[:len(key)-len(w.dir)-1] == root {
			w.mu.Lock()
			w.stopLocked()
			w.mu.Unlock()
			delete(tsWorkers.m, key)
		}
	}
}

func (w *tsWorker) startLocked() error {
	cmd := exec.Command("node", "-e", tsScript)
	cmd.Dir = w.dir
	cmd.Env = appendEnv("GROVE_TS_WORKER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	w.errs = &bytes.Buffer{}
	cmd.Stderr = &limitedWriter{buf: w.errs, max: 64 << 10}
	if err := cmd.Start(); err != nil {
		return err
	}
	w.cmd, w.stdin, w.out = cmd, stdin, bufio.NewReaderSize(stdout, 1<<20)
	return nil
}

func (w *tsWorker) stopLocked() {
	if w.cmd == nil {
		return
	}
	_ = w.stdin.Close()
	_ = w.cmd.Process.Kill()
	_ = w.cmd.Wait()
	w.cmd, w.stdin, w.out = nil, nil, nil
}

// call sends one request and returns the payload line. On any error or
// cancellation the worker is stopped, dropping its caches.
func (w *tsWorker) call(ctx context.Context, request []byte) ([]byte, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err // never start a worker for a cancelled caller
	}
	if w.cmd == nil {
		if err := w.startLocked(); err != nil {
			return nil, err
		}
	}
	type reply struct {
		line []byte
		err  error
	}
	done := make(chan reply, 1)
	out := w.out
	go func() {
		if _, err := w.stdin.Write(append(request, '\n')); err != nil {
			done <- reply{err: err}
			return
		}
		for {
			line, err := out.ReadBytes('\n')
			if bytes.Contains(line, []byte(tsPayloadSentinel)) {
				done <- reply{line: line}
				return
			}
			if err != nil {
				done <- reply{err: err}
				return
			}
			// Anything else is output from the project's own tooling.
		}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			detail := clip(w.errs.String(), 400)
			w.stopLocked()
			if detail != "" {
				return nil, fmt.Errorf("%w: %s", r.err, detail)
			}
			return nil, r.err
		}
		return r.line, nil
	case <-ctx.Done():
		w.stopLocked() // unblocks the reader; the next run starts fresh
		return nil, ctx.Err()
	}
}

// limitedWriter keeps the first max bytes of a stream (worker stderr).
type limitedWriter struct {
	buf *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.buf.Len(); room > 0 {
		if len(p) > room {
			l.buf.Write(p[:room])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}
