package native

import (
	"bufio"
	"bytes"
	"context"
	"strconv"
	"time"

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

// residentWorker is one long-lived analyzer process (a node TypeScript
// worker, a JVM javac worker) serving one request per stdin line and
// answering with one line carrying its payload marker.
type residentWorker struct {
	mu     sync.Mutex
	root   string
	marker string
	start  func() *exec.Cmd // builds the command; Dir and Env set
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader
	errs   *bytes.Buffer
	// idle stops the process after workerIdle without a request, releasing
	// its memory; the next request starts it again.
	idle *time.Timer
}

// workerIdle is how long a resident worker may sit unused before it exits
// (GROVE_WORKER_IDLE_MIN minutes, default 20).
func workerIdle() time.Duration {
	if v, err := strconv.Atoi(os.Getenv("GROVE_WORKER_IDLE_MIN")); err == nil && v > 0 {
		return time.Duration(v) * time.Minute
	}
	return 20 * time.Minute
}

var residentWorkers = struct {
	sync.Mutex
	m map[string]*residentWorker
}{m: map[string]*residentWorker{}}

func tsWorkerEnabled() bool { return os.Getenv("GROVE_TS_WORKER") != "0" }

// residentWorkerFor returns the worker registered under key for root,
// creating it (not yet started) on first use.
func residentWorkerFor(key, root, marker string, start func() *exec.Cmd) *residentWorker {
	residentWorkers.Lock()
	defer residentWorkers.Unlock()
	w := residentWorkers.m[key]
	if w == nil {
		w = &residentWorker{root: root, marker: marker, start: start}
		residentWorkers.m[key] = w
	}
	return w
}

// tsWorkerFor returns the TypeScript worker for a root, keyed by the node
// working directory too (untrusted mode runs node from a neutral directory).
func tsWorkerFor(root, dir string) *residentWorker {
	return residentWorkerFor("ts\x00"+root+"\x00"+dir, root, tsPayloadSentinel, func() *exec.Cmd {
		cmd := exec.Command("node", "-e", tsScript)
		cmd.Dir = dir
		cmd.Env = appendEnv("GROVE_TS_WORKER=1")
		return cmd
	})
}

// StopTSWorkers stops every resident worker (TypeScript and Java) of a
// repository root, or of every root when root is "". Engines call it on
// Close.
func StopTSWorkers(root string) {
	residentWorkers.Lock()
	defer residentWorkers.Unlock()
	for key, w := range residentWorkers.m {
		if root == "" || w.root == root {
			w.mu.Lock()
			w.stopLocked()
			w.mu.Unlock()
			delete(residentWorkers.m, key)
		}
	}
}

func (w *residentWorker) startLocked() error {
	cmd := w.start()
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

func (w *residentWorker) stopLocked() {
	if w.idle != nil {
		w.idle.Stop()
		w.idle = nil
	}
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
func (w *residentWorker) call(ctx context.Context, request []byte) ([]byte, error) {
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
	// The goroutine uses only these locals: stopLocked clears the fields
	// when the caller is cancelled, and reading them here raced with that
	// (a nil stdin made the goroutine panic and take the process down).
	in, out, marker := w.stdin, w.out, []byte(w.marker)
	go func() {
		if _, err := in.Write(append(request, '\n')); err != nil {
			done <- reply{err: err}
			return
		}
		for {
			line, err := out.ReadBytes('\n')
			if bytes.Contains(line, marker) {
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
	defer w.armIdleLocked()
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

// stop stops the worker process (it restarts on the next call).
func (w *residentWorker) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopLocked()
}

// A TypeScript worker whose process grows past tsWorkerMaxBytes is stopped
// and its root uses one-shot processes for the rest of this process: on a
// very large monorepo a resident program set can hold several GB.
var tsOverCap sync.Map // root -> true

func tsWorkerMaxBytes() int64 {
	if v, err := strconv.ParseInt(os.Getenv("GROVE_TS_WORKER_MAX_MB"), 10, 64); err == nil && v > 0 {
		return v << 20
	}
	return 3072 << 20
}

func tsWorkerOverCap(root string) bool { _, over := tsOverCap.Load(root); return over }
func markTSWorkerOverCap(root string)  { tsOverCap.Store(root, true) }

// armIdleLocked (re)starts the idle timer of a running worker.
func (w *residentWorker) armIdleLocked() {
	if w.cmd == nil {
		return
	}
	if w.idle != nil {
		w.idle.Reset(workerIdle())
		return
	}
	w.idle = time.AfterFunc(workerIdle(), func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		w.stopLocked()
	})
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
