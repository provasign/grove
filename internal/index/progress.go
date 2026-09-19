package index

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/store"
)

// indexProgress persists an index run's phase, progress counter and native
// verdicts to the store's meta table so `grove status` can report them while
// the run is in flight and after it finishes. Writes are best-effort (a
// failed meta write never fails the index) and throttled to one per second
// per key, since the call resolver reports every few hundred symbols.
type indexProgress struct {
	store *store.Store

	mu        sync.Mutex
	lastWrite time.Time
	pending   string // latest progress text not yet written
	phaseName string
	started   bool // start ran (the index lock was held); finish is a no-op otherwise
}

func newIndexProgress(st *store.Store) *indexProgress {
	return &indexProgress{store: st}
}

// start stamps the run's start time and clears the previous run's end.
func (p *indexProgress) start(ctx context.Context) {
	if p == nil || p.store == nil {
		return
	}
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	_ = p.store.SetMeta(ctx, core.MetaIndexStarted, time.Now().UTC().Format(time.RFC3339))
	_ = p.store.SetMeta(ctx, core.MetaIndexFinished, "")
	_ = p.store.SetMeta(ctx, core.MetaIndexProgress, "")
}

// phase records a new phase and its opening progress text immediately.
func (p *indexProgress) phase(name, progress string) {
	if p == nil || p.store == nil {
		return
	}
	p.mu.Lock()
	p.phaseName = name
	p.pending = progress
	p.lastWrite = time.Now()
	p.mu.Unlock()
	ctx := context.Background()
	_ = p.store.SetMeta(ctx, core.MetaIndexPhase, name)
	_ = p.store.SetMeta(ctx, core.MetaIndexProgress, progress)
}

// progress records a progress text from the indexing goroutine, writing it
// through when at least a second has passed since the last write.
func (p *indexProgress) progress(text string) {
	if p == nil || p.store == nil {
		return
	}
	p.mu.Lock()
	p.pending = text
	due := time.Since(p.lastWrite) >= time.Second
	if due {
		p.lastWrite = time.Now()
	}
	p.mu.Unlock()
	if due {
		_ = p.store.SetMeta(context.Background(), core.MetaIndexProgress, text)
	}
}

// progressAsync records a progress text from a worker goroutine without
// touching the store (SQLite has one writer, and the indexing goroutine
// owns it); flush writes the latest value.
func (p *indexProgress) progressAsync(text string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.pending = text
	p.mu.Unlock()
}

// flush writes the latest pending progress text.
func (p *indexProgress) flush(ctx context.Context) {
	if p == nil || p.store == nil {
		return
	}
	p.mu.Lock()
	text := p.pending
	p.lastWrite = time.Now()
	p.mu.Unlock()
	if text != "" {
		_ = p.store.SetMeta(ctx, core.MetaIndexProgress, text)
	}
}

// native persists the analyzers' per-language verdicts for this run.
func (p *indexProgress) native(diagnostics []string) {
	if p == nil || p.store == nil {
		return
	}
	raw, err := json.Marshal(diagnostics)
	if err != nil {
		return
	}
	_ = p.store.SetMeta(context.Background(), core.MetaIndexNative, string(raw))
}

// finish stamps the run's end and final phase.
func (p *indexProgress) finish(runErr error) {
	if p == nil || p.store == nil {
		return
	}
	p.mu.Lock()
	started := p.started
	p.mu.Unlock()
	if !started {
		return // never held the lock (e.g. lock acquisition failed): not our run
	}
	ctx := context.Background()
	phase := "complete"
	if runErr != nil {
		phase = "failed: " + runErr.Error()
	}
	_ = p.store.SetMeta(ctx, core.MetaIndexPhase, phase)
	_ = p.store.SetMeta(ctx, core.MetaIndexProgress, "")
	_ = p.store.SetMeta(ctx, core.MetaIndexFinished, time.Now().UTC().Format(time.RFC3339))
}
