package server

import (
	"context"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

func TestDiagnosticsPublisherDebouncesLatestGeneration(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	defer publisher.Stop()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)

	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "old"}},
		Generation:  1,
		Debounce:    true,
	})
	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "new"}},
		Generation:  2,
		Debounce:    true,
	})

	var params publishDiagnosticsParams
	select {
	case params = <-notifier.messages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounced diagnostics")
	}
	if len(params.Diagnostics) != 1 || params.Diagnostics[0].Message != "new" {
		t.Fatalf("diagnostics = %+v, want latest generation", params.Diagnostics)
	}
}

func TestDiagnosticsPublisherFlushesOnStop(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)

	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "pending"}},
		Generation:  1,
		Debounce:    true,
	})
	publisher.Stop()

	select {
	case params := <-notifier.messages:
		if len(params.Diagnostics) != 1 || params.Diagnostics[0].Message != "pending" {
			t.Fatalf("diagnostics = %+v, want pending", params.Diagnostics)
		}
	default:
		t.Fatal("expected pending diagnostics to flush on stop")
	}
}

func TestDiagnosticsPublisherImmediateSend(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	defer publisher.Stop()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)

	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "now"}},
		Generation:  1,
		Debounce:    false,
	})

	select {
	case params := <-notifier.messages:
		if params.URI != "file:///test.nix" {
			t.Fatalf("uri = %q", params.URI)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for immediate diagnostics")
	}
}

// TestDiagnosticsPublisherDropsStaleVersion proves the version backstop holds
// INDEPENDENTLY of the generation layer. The stale update carries the HIGHEST
// generation (so the generation guard would happily let it through) but an OLDER
// document version than the handler's current version — the exact shape of a
// violated generation discipline or a compute that read a superseded snapshot.
// The publisher must drop it, and the wire must never see it.
func TestDiagnosticsPublisherDropsStaleVersion(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	defer publisher.Stop()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)
	// The document sits at version 5 by publish time.
	publisher.SetVersionLookup(func(string) (int32, bool) { return 5, true })

	// A fresh update matching the current version publishes normally.
	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "fresh"}},
		Generation:  1,
		Debounce:    false,
		Version:     5,
		Versioned:   true,
	})
	select {
	case params := <-notifier.messages:
		if len(params.Diagnostics) != 1 || params.Diagnostics[0].Message != "fresh" {
			t.Fatalf("first publish = %+v, want the fresh update", params.Diagnostics)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the fresh update")
	}

	// Now a LATER-generation update carrying an OLDER version — the generation
	// guard would pass it (gen 2 > gen 1), but the version backstop must not: it
	// was computed from a superseded buffer.
	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "stale"}},
		Generation:  2,
		Debounce:    false,
		Version:     2,
		Versioned:   true,
	})
	select {
	case params := <-notifier.messages:
		t.Fatalf("stale-versioned (but newer-generation) update reached the wire: %+v", params)
	case <-time.After(200 * time.Millisecond):
		// Correct: the version backstop dropped it despite the newer generation.
	}
}

// TestDiagnosticsPublisherStampsVersion checks the happy path: a fresh update
// (its version matches the current document version) reaches the notifier with
// the version stamped on the wire.
func TestDiagnosticsPublisherStampsVersion(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	defer publisher.Stop()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)
	publisher.SetVersionLookup(func(string) (int32, bool) { return 3, true })

	publisher.Publish(diagnosticUpdate{
		URI:         "file:///test.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "fresh"}},
		Generation:  1,
		Debounce:    false,
		Version:     3,
		Versioned:   true,
	})

	select {
	case params := <-notifier.messages:
		if params.Version == nil {
			t.Fatal("publishDiagnosticsParams.Version = nil, want stamped version 3")
		}
		if *params.Version != 3 {
			t.Fatalf("params.Version = %d, want 3", *params.Version)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for versioned diagnostics")
	}
}

// TestDiagnosticsPublisherUnversionedPassesThrough confirms updates with no
// version (disk-file publishes) bypass the backstop and carry no version on the
// wire, even when a version lookup is present.
func TestDiagnosticsPublisherUnversionedPassesThrough(t *testing.T) {
	publisher := newDiagnosticsPublisher()
	defer publisher.Stop()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	publisher.SetNotifier(notifier)
	publisher.SetVersionLookup(func(string) (int32, bool) { return 9, true })

	publisher.Publish(diagnosticUpdate{
		URI:         "file:///disk.nix",
		Diagnostics: []syntax.Diagnostic{{Message: "disk"}},
		Generation:  1,
		Debounce:    false,
		Versioned:   false,
	})

	select {
	case params := <-notifier.messages:
		if params.Version != nil {
			t.Fatalf("unversioned publish stamped a version: %d", *params.Version)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for unversioned diagnostics")
	}
}

type captureNotifier struct {
	messages chan publishDiagnosticsParams
}

func (n *captureNotifier) Notify(_ context.Context, method string, params any) error {
	if method != "textDocument/publishDiagnostics" {
		return nil
	}
	n.messages <- params.(publishDiagnosticsParams)
	return nil
}
