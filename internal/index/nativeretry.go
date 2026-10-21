package index

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/provasign/grove/internal/core"
	"github.com/provasign/grove/internal/native"
)

// Retrying analyzers without a completed pass.
//
// An analyzer that has never completed a full pass on this index (compiler
// not installed, dependencies missing, a timeout, invalidated by a compiler
// input change) used to retry only when some file changed: a no-change
// index returned "no file changes since last index" before reaching the
// retry logic, so installing the missing compiler and running a plain
// index changed nothing until --force (stack audit 2026-10-20 #5).
//
// Retries are bounded so that the no-change index every query runs stays
// cheap: each analyzer's attempts are recorded with the time the next one
// is due. An analyzer that never started (unavailable) is rechecked with
// its cheap availability probe, and re-run only once it reports available;
// one that ran and failed is re-run on an exponential schedule, since a
// failed compiler pass can cost minutes.

// metaNativeRetry: JSON object analyzer -> nativeRetry.
const metaNativeRetry = "native-retry"

type nativeRetry struct {
	Failures    int    `json:"failures"`
	Next        int64  `json:"next"` // unix nanoseconds; 0 = due now
	Unavailable bool   `json:"unavailable,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

const (
	unavailableRecheckBase = 15 * time.Second
	unavailableRecheckMax  = 10 * time.Minute
	failedRetryBase        = time.Minute
	failedRetryMax         = 6 * time.Hour
)

// retryDelay is the wait after the r.Failures-th failed attempt.
func retryDelay(r nativeRetry) time.Duration {
	base, max := failedRetryBase, failedRetryMax
	if r.Unavailable {
		base, max = unavailableRecheckBase, unavailableRecheckMax
	}
	d := base
	for n := 1; n < r.Failures && d < max; n++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

func (i *Indexer) clock() time.Time {
	if i.now != nil {
		return i.now()
	}
	return time.Now()
}

func (i *Indexer) loadNativeRetry(ctx context.Context) map[string]nativeRetry {
	out := map[string]nativeRetry{}
	if raw, ok, err := i.store.GetMeta(ctx, metaNativeRetry); err == nil && ok && raw != "" {
		if json.Unmarshal([]byte(raw), &out) != nil || out == nil {
			out = map[string]nativeRetry{}
		}
	}
	return out
}

func (i *Indexer) saveNativeRetry(ctx context.Context, state map[string]nativeRetry) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return i.store.SetMeta(ctx, metaNativeRetry, string(raw))
}

// loadNativeComplete returns the analyzers with a completed full pass.
func (i *Indexer) loadNativeComplete(ctx context.Context) map[string]bool {
	completed := map[string]bool{}
	if raw, ok, err := i.store.GetMeta(ctx, core.MetaNativeComplete); err == nil && ok && raw != "" {
		var names []string
		if json.Unmarshal([]byte(raw), &names) == nil {
			for _, n := range names {
				completed[n] = true
			}
		}
	}
	return completed
}

// noopRetries decides, for a run with no file changes, which incomplete
// analyzers to retry now. It returns them with notes for the analyzers
// still waiting, and persists any availability recheck it made. Analyzers
// with no recorded attempt are not retried here (no files of their
// languages, or an index from before attempts were recorded): the next run
// with changes attempts them as before.
func (i *Indexer) noopRetries(ctx context.Context, root string, completed map[string]bool, state map[string]nativeRetry) (map[string]bool, []string, error) {
	if !i.nativeConfig.Enabled || len(state) == 0 {
		return nil, nil, nil
	}
	now := i.clock()
	names := make([]string, 0, len(state))
	for n := range state {
		names = append(names, n)
	}
	sort.Strings(names)
	retry := map[string]bool{}
	var notes []string
	dirty := false
	for _, name := range names {
		r := state[name]
		if completed[name] {
			continue
		}
		if now.UnixNano() < r.Next {
			wait := time.Duration(r.Next - now.UnixNano()).Round(time.Second)
			notes = append(notes, fmt.Sprintf("%s has no completed pass (%s); next retry in %s, or use --force to retry now", name, r.Reason, wait))
			continue
		}
		if !r.Unavailable {
			retry[name] = true
			continue
		}
		avail, ok := native.AnalyzerAvailable(ctx, root, name, i.nativeConfig)
		if !ok {
			continue // disabled by config: nothing to retry
		}
		if avail.Available {
			retry[name] = true
			continue
		}
		r.Failures++
		r.Reason = avail.Reason
		r.Next = now.Add(retryDelay(r)).UnixNano()
		state[name] = r
		dirty = true
		notes = append(notes, fmt.Sprintf("%s still unavailable (%s); rechecked now, next check in %s", name, avail.Reason, retryDelay(r)))
	}
	if dirty {
		if err := i.saveNativeRetry(ctx, state); err != nil {
			return nil, nil, err
		}
	}
	return retry, notes, nil
}

// retryDue reports whether an incomplete analyzer's next full attempt is due
// on a run where none of its languages changed.
func (i *Indexer) retryDue(state map[string]nativeRetry, name string) bool {
	r, ok := state[name]
	return !ok || i.clock().UnixNano() >= r.Next
}

// recordNativeAttempts updates the retry schedule from a native run.
func (i *Indexer) recordNativeAttempts(ctx context.Context, state map[string]nativeRetry, res native.Result) error {
	if len(res.Completed) == 0 && len(res.Incomplete) == 0 {
		return nil
	}
	now := i.clock()
	for _, name := range res.Completed {
		delete(state, name)
	}
	for name, why := range res.Incomplete {
		r := state[name]
		if r.Unavailable != why.Unavailable {
			r.Failures = 0 // a different failure mode starts its own schedule
		}
		r.Failures++
		r.Unavailable = why.Unavailable
		r.Reason = why.Reason
		r.Next = now.Add(retryDelay(r)).UnixNano()
		state[name] = r
	}
	return i.saveNativeRetry(ctx, state)
}
