package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// captureCaller records window/workDoneProgress/create requests and answers
// them with a configurable outcome.
type captureCaller struct {
	mu       sync.Mutex
	tokens   []string
	failCall bool
}

func (c *captureCaller) Call(_ context.Context, method string, params any, _ any) error {
	if method != "window/workDoneProgress/create" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	create, ok := params.(workDoneProgressCreateParams)
	if ok {
		c.tokens = append(c.tokens, create.Token)
	}
	if c.failCall {
		return &responseErrorStub{}
	}
	return nil
}

func (c *captureCaller) createTokens() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.tokens...)
}

type responseErrorStub struct{}

func (responseErrorStub) Error() string { return "client rejected create" }

// progressEvent is one captured $/progress notification.
type progressEvent struct {
	token string
	value map[string]any
}

// progressNotifier records both publishDiagnostics and $/progress so tests can
// assert the progress sequence without disturbing diagnostics flow.
type progressNotifier struct {
	mu        sync.Mutex
	progress  []progressEvent
	published int
}

func (n *progressNotifier) Notify(_ context.Context, method string, params any) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch method {
	case "textDocument/publishDiagnostics":
		n.published++
	case "$/progress":
		p := params.(progressParams)
		// Re-encode the value so the test can read the typed struct as a map.
		data, err := json.Marshal(p.Value)
		if err != nil {
			return err
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		n.progress = append(n.progress, progressEvent{token: p.Token, value: value})
	}
	return nil
}

func (n *progressNotifier) snapshot() ([]progressEvent, int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]progressEvent(nil), n.progress...), n.published
}

func initializeWithProgress(t *testing.T, handler *Handler, root string, workDoneProgress bool) {
	t.Helper()
	rootURI := mustURI(t, root)
	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, map[string]any{
		"rootUri": rootURI,
		"capabilities": map[string]any{
			"window": map[string]any{"workDoneProgress": workDoneProgress},
		},
	})); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := handler.WaitForWorkspace(ctx); err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}
}

func waitForProgressEnd(t *testing.T, notifier *progressNotifier) []progressEvent {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		events, _ := notifier.snapshot()
		if len(events) > 0 && events[len(events)-1].value["kind"] == "end" {
			return events
		}
		time.Sleep(10 * time.Millisecond)
	}
	events, _ := notifier.snapshot()
	t.Fatalf("timed out waiting for progress end; got %+v", events)
	return nil
}

func TestHandlerIndexingProgressReportsBeginReportEnd(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &progressNotifier{}
	caller := &captureCaller{}
	handler.SetNotifier(notifier)
	handler.SetCaller(caller)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "a.nix"), "{}")
	writeFile(t, filepath.Join(root, "b.nix"), "{}")

	initializeWithProgress(t, handler, root, true)

	events := waitForProgressEnd(t, notifier)

	tokens := caller.createTokens()
	if len(tokens) != 1 {
		t.Fatalf("create requests = %d, want exactly 1 (%v)", len(tokens), tokens)
	}
	token := tokens[0]

	if events[0].value["kind"] != "begin" {
		t.Fatalf("first event = %+v, want begin", events[0].value)
	}
	if events[0].value["title"] != "Indexing Nix workspace" {
		t.Fatalf("begin title = %v, want Indexing Nix workspace", events[0].value["title"])
	}

	var reports int
	lastPct := -1.0
	for _, e := range events {
		if e.token != token {
			t.Fatalf("event token = %q, want %q", e.token, token)
		}
		if e.value["kind"] != "report" {
			continue
		}
		reports++
		pct, ok := e.value["percentage"].(float64)
		if !ok {
			t.Fatalf("report percentage missing/non-number: %+v", e.value)
		}
		if pct < 0 || pct > 100 {
			t.Fatalf("report percentage = %v, want 0..100", pct)
		}
		if pct < lastPct {
			t.Fatalf("report percentage decreased: %v then %v", lastPct, pct)
		}
		lastPct = pct
	}
	if reports < 1 {
		t.Fatalf("report events = %d, want >= 1", reports)
	}

	last := events[len(events)-1].value
	if last["kind"] != "end" {
		t.Fatalf("last event = %+v, want end", last)
	}
}

func TestHandlerNoProgressWithoutCapability(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &progressNotifier{}
	caller := &captureCaller{}
	handler.SetNotifier(notifier)
	handler.SetCaller(caller)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "import ./missing.nix")

	initializeWithProgress(t, handler, root, false)

	// Diagnostics still flow even without progress.
	flakeURI := mustURI(t, filepath.Join(root, "flake.nix"))
	if got := waitForDiagnostics(t, handler, flakeURI, 1); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want 1", got)
	}

	if tokens := caller.createTokens(); len(tokens) != 0 {
		t.Fatalf("create requests = %v, want none", tokens)
	}
	events, _ := notifier.snapshot()
	if len(events) != 0 {
		t.Fatalf("progress events = %+v, want none", events)
	}
}

func TestHandlerIndexingContinuesWhenCreateFails(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &progressNotifier{}
	caller := &captureCaller{failCall: true}
	handler.SetNotifier(notifier)
	handler.SetCaller(caller)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "import ./missing.nix")

	initializeWithProgress(t, handler, root, true)

	flakeURI := mustURI(t, filepath.Join(root, "flake.nix"))
	if got := waitForDiagnostics(t, handler, flakeURI, 1); len(got) != 1 {
		t.Fatalf("diagnostics = %+v, want 1 despite failed create", got)
	}

	if tokens := caller.createTokens(); len(tokens) != 1 {
		t.Fatalf("create requests = %v, want exactly 1 attempt", tokens)
	}
	events, _ := notifier.snapshot()
	if len(events) != 0 {
		t.Fatalf("progress events = %+v, want none after failed create", events)
	}
}
