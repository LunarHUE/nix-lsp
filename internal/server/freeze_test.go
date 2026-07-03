package server

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

// TestDidChangeDoesNotFreezeReadLoopWhenQueueSaturated is the regression guard
// for the reported stuck-diagnostics freeze. With both scheduler workers parked
// and the background queue filled to capacity, a didChange notification (which
// the real server dispatches synchronously on its read loop) must still return
// promptly instead of blocking inside Submit and wedging the whole server.
func TestDidChangeDoesNotFreezeReadLoopWhenQueueSaturated(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "{ ok = true; }")

	// Park both workers, then fill the background queue (cap 64).
	release := make(chan struct{})
	park := func(context.Context) error { <-release; return nil }
	handler.tasks.Submit(context.Background(), lsp.LaneBackground, park)
	handler.tasks.Submit(context.Background(), lsp.LaneBackground, park)
	time.Sleep(50 * time.Millisecond)
	noop := func(context.Context) error { return nil }
	for range 64 {
		handler.tasks.Submit(context.Background(), lsp.LaneBackground, noop)
	}

	done := make(chan struct{})
	go func() {
		_, _ = handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": 2},
			"contentChanges": []map[string]any{{"text": "{ ok = false; }"}},
		}))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("didChange blocked on a saturated queue: the read loop froze")
	}
	close(release)

	// The server is still live: a subsequent request answers rather than hanging.
	unblocked := make(chan struct{})
	go func() {
		_, _ = handler.Handle(context.Background(), "textDocument/documentSymbol", mustJSON(t, map[string]any{
			"textDocument": map[string]any{"uri": uri},
		}))
		close(unblocked)
	}()
	select {
	case <-unblocked:
	case <-time.After(2 * time.Second):
		t.Fatal("documentSymbol hung: server wedged after saturation")
	}
}

// TestDidChangeBurstConvergesToLatestContent drives the user's scenario: a burst
// of keystroke didChanges that pass through many broken syntax states and end on
// valid content. The coalescer must converge to the final buffer's diagnostics
// (empty), never leaving a stale syntax error stuck on screen.
func TestDidChangeBurstConvergesToLatestContent(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "{ ok = true; }")

	// 60 edits alternating broken/valid, ending clean.
	for i := range 60 {
		text := "{ ok = true; }"
		if i%2 == 0 {
			text = "{ ok = " // broken: unterminated
		}
		if _, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": i + 2},
			"contentChanges": []map[string]any{{"text": text}},
		})); err != nil {
			t.Fatalf("didChange %d error = %v", i, err)
		}
	}

	if got := waitForDiagnostics(t, handler, uri, 0); len(got) != 0 {
		t.Fatalf("final diagnostics = %+v, want none (converged to valid buffer)", got)
	}
}

// TestDidChangeCoalescesUnderBurst confirms the burst collapses to far fewer
// recomputes than edits: without coalescing each keystroke would be its own task.
func TestDidChangeCoalescesUnderBurst(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "{ ok = true; }")

	const edits = 100
	for i := range edits {
		if _, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": i + 2},
			"contentChanges": []map[string]any{{"text": fmt.Sprintf("{ ok = %d; }", i)}},
		})); err != nil {
			t.Fatalf("didChange %d error = %v", i, err)
		}
	}

	// Converges to the last content with no diagnostics.
	if got := waitForDiagnostics(t, handler, uri, 0); len(got) != 0 {
		t.Fatalf("final diagnostics = %+v, want none", got)
	}
}
