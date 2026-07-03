package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// TestDiagSchedulerCancelsSupersededCompute proves the liveness fix: when a newer
// buffer supersedes an in-flight recompute, the coalescer cancels the running
// compute's context immediately (so a stale multi-second compute stops burning
// CPU) and the superseded result is never published, while the fresh recompute
// runs against the newest content.
//
// Before the fix (diagEntry carried no cancel; schedule only set dirty), the
// first exec's context was never cancelled, so this test's first exec — which
// blocks until it observes cancellation — would hang until the bounded wait
// below failed.
func TestDiagSchedulerCancelsSupersededCompute(t *testing.T) {
	const uri = "file:///super.nix"

	enqueue := func(run func(context.Context)) bool {
		go run(context.Background())
		return true
	}

	firstStarted := make(chan struct{})
	firstCtxErr := make(chan error, 1)
	freshDone := make(chan struct{})

	var (
		mu        sync.Mutex
		calls     int
		published []string
	)

	exec := func(ctx context.Context, u, path string, _ bool) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()

		if n == 1 {
			// The superseded compute: announce it started, then block until its
			// context is cancelled by the superseding schedule. A cancelled compute
			// returns WITHOUT publishing, matching computeFileDiagnostics's early-out
			// on a context error.
			close(firstStarted)
			<-ctx.Done()
			firstCtxErr <- ctx.Err()
			return
		}

		// The fresh compute runs to completion and publishes the newest content.
		mu.Lock()
		published = append(published, path)
		mu.Unlock()
		close(freshDone)
	}

	d := newDiagScheduler(enqueue, exec)

	d.schedule(uri, "v1", false)

	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("first compute never started")
	}

	// A newer edit supersedes the in-flight compute.
	d.schedule(uri, "v2", false)

	select {
	case err := <-firstCtxErr:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("superseded compute context error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("superseded compute context was never cancelled")
	}

	select {
	case <-freshDone:
	case <-time.After(2 * time.Second):
		t.Fatal("fresh compute never ran")
	}

	mu.Lock()
	got := append([]string(nil), published...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "v2" {
		t.Fatalf("published = %v, want exactly [v2] (superseded v1 must never publish)", got)
	}
}
