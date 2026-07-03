package server

import (
	"context"
	"sync"
)

// diagScheduler coalesces per-URI diagnostics recomputes.
//
// Diagnostics only ever need the LATEST buffer content for a URI, so enqueuing
// one scheduler task per keystroke is wasteful and — on a full background queue —
// dangerous: before this coalescer, a burst of didChange notifications could fill
// the queue and, because the notification path submitted with a blocking Submit,
// freeze the LSP read loop entirely (the reported stuck-diagnostics bug).
//
// Instead each URI keeps at most one in-flight recompute plus a dirty mark. A
// recompute that finishes with dirty set re-runs once more against the newest
// buffer, so the published diagnostics always converge to the final content under
// any burst, while the number of queued background tasks stays bounded by the
// count of distinct dirty URIs (a handful of open files) rather than by the
// keystroke rate. The generation guard in computeFileDiagnostics still orders
// publishes so an older-generation compute can never clobber a newer one.
type diagScheduler struct {
	// enqueue submits run on the background lane without blocking, reporting
	// whether it was accepted (false when the queue is full or the scheduler is
	// stopped, in which case no worker will execute run).
	enqueue func(run func(context.Context)) bool
	// exec performs one diagnostics recompute-and-publish for a URI.
	exec func(ctx context.Context, uri, path string, debounce bool)

	mu      sync.Mutex
	pending map[string]*diagEntry
}

// diagEntry is the coalescer's per-URI state. path/debounce hold the latest
// requested recompute; running is true while a task is queued or executing for
// the URI; dirty records that newer content arrived while running, arming exactly
// one more recompute.
//
// cancel aborts the currently executing compute iteration. It is set (under mu)
// while an iteration runs and cleared to nil between iterations. When schedule
// marks the entry dirty because a newer buffer superseded the running compute,
// it invokes cancel so the stale multi-second recompute stops immediately
// instead of running to completion and delaying the fresh one. Cancellation is
// pure work-saving: the dirty mark still drives the follow-up recompute, and the
// generation guard plus version backstop remain the correctness layers.
type diagEntry struct {
	path     string
	debounce bool
	running  bool
	dirty    bool
	cancel   context.CancelFunc
}

func newDiagScheduler(enqueue func(run func(context.Context)) bool, exec func(ctx context.Context, uri, path string, debounce bool)) *diagScheduler {
	return &diagScheduler{
		enqueue: enqueue,
		exec:    exec,
		pending: make(map[string]*diagEntry),
	}
}

// schedule requests a diagnostics recompute for uri against its latest buffer. It
// never blocks: if a recompute is already in flight it just marks the entry dirty
// so the running task re-runs once more; otherwise it launches one. Safe to call
// from the LSP read loop.
func (d *diagScheduler) schedule(uri, path string, debounce bool) {
	d.mu.Lock()
	entry := d.pending[uri]
	if entry == nil {
		entry = &diagEntry{}
		d.pending[uri] = entry
	}
	entry.path = path
	entry.debounce = debounce
	if entry.running {
		entry.dirty = true
		// Grab the in-flight iteration's cancel while holding the lock, but invoke
		// it after unlocking: the newer buffer supersedes the running compute, so
		// abandoning it now saves the wasted CPU. The dirty mark already guarantees
		// runLoop recomputes the newest content next. cancel is safe to call late
		// or twice (a CancelFunc is idempotent and never re-enters the coalescer).
		cancel := entry.cancel
		d.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return
	}
	entry.running = true
	d.mu.Unlock()

	d.launch(uri)
}

// launch enqueues the run loop for uri. On queue overflow (no worker will run it)
// it releases the running claim and leaves the entry dirty so a later schedule
// re-arms the newest content. Overflow is only reachable under sustained
// background saturation; because background tasks are coarse and diagnostics are
// coalesced to at most one task per open URI, the queue does not fill in practice,
// and continued typing (each didChange re-arms) closes the gap regardless.
func (d *diagScheduler) launch(uri string) {
	if d.enqueue(func(ctx context.Context) { d.runLoop(ctx, uri) }) {
		return
	}
	d.mu.Lock()
	if entry := d.pending[uri]; entry != nil {
		entry.running = false
		entry.dirty = true
	}
	d.mu.Unlock()
}

// runLoop executes recomputes for uri until it observes no pending dirty content,
// re-reading the latest path/debounce (and, in exec, a fresh VFS snapshot and
// generation) on every iteration so the final publish reflects the newest buffer.
//
// Each iteration runs under a cancellable child of ctx recorded in the entry, so
// a concurrent schedule can cancel the in-flight compute the moment a newer
// buffer supersedes it. The child is always cancelled before the iteration ends
// (the CancelFunc never leaks), and the entry's cancel is cleared so a late
// schedule cannot fire a stale cancel at a compute that already finished.
func (d *diagScheduler) runLoop(ctx context.Context, uri string) {
	for {
		d.mu.Lock()
		entry := d.pending[uri]
		if entry == nil {
			d.mu.Unlock()
			return
		}
		path, debounce := entry.path, entry.debounce
		entry.dirty = false
		iterCtx, cancel := context.WithCancel(ctx)
		entry.cancel = cancel
		d.mu.Unlock()

		d.exec(iterCtx, uri, path, debounce)

		d.mu.Lock()
		entry = d.pending[uri]
		if entry != nil {
			entry.cancel = nil
		}
		cancel()
		if entry == nil {
			d.mu.Unlock()
			return
		}
		if entry.dirty {
			d.mu.Unlock()
			continue
		}
		delete(d.pending, uri)
		d.mu.Unlock()
		return
	}
}
