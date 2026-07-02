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
