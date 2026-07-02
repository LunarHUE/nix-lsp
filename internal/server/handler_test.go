package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestHandlerDidOpenStoresOverlayAndDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	_, err := handler.Handle(context.Background(), "textDocument/didOpen", mustJSON(t, map[string]any{
		"textDocument": map[string]any{
			"uri":        uri,
			"languageId": "nix",
			"version":    1,
			"text":       "{",
		},
	}))
	if err != nil {
		t.Fatalf("didOpen error = %v", err)
	}

	file, err := handler.Snapshot().ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if string(file.Content) != "{" || !file.Overlay {
		t.Fatalf("file = %q overlay=%v, want overlay content", file.Content, file.Overlay)
	}
	if got := waitForDiagnostics(t, handler, uri, 1); len(got) != 1 {
		t.Fatalf("diagnostics = %v, want one syntax diagnostic", got)
	}
}

func TestHandlerDidChangeUpdatesOverlayAndDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "{")
	waitForDiagnostics(t, handler, uri, 1)
	_, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{
			{"text": "{ ok = true; }"},
		},
	}))
	if err != nil {
		t.Fatalf("didChange error = %v", err)
	}

	file, err := handler.Snapshot().ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error = %v", err)
	}
	if string(file.Content) != "{ ok = true; }" {
		t.Fatalf("content = %q, want changed text", file.Content)
	}
	if got := waitForDiagnostics(t, handler, uri, 0); len(got) != 0 {
		t.Fatalf("diagnostics = %v, want none", got)
	}
}

func TestHandlerDidCloseRemovesOverlayAndDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "{")
	if _, err := handler.Handle(context.Background(), "textDocument/didClose", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
	})); err != nil {
		t.Fatalf("didClose error = %v", err)
	}

	hasOverlay, err := handler.Snapshot().HasOverlay(path)
	if err != nil {
		t.Fatalf("HasOverlay error = %v", err)
	}
	if hasOverlay {
		t.Fatal("overlay remained after didClose")
	}
	if got := handler.Diagnostics(uri); len(got) != 0 {
		t.Fatalf("diagnostics = %v, want none", got)
	}
}

func TestHandlerInitialize(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	result, err := handler.Handle(context.Background(), "initialize", nil)
	if err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	init, ok := result.(lsp.InitializeResult)
	if !ok {
		t.Fatalf("result type = %T, want lsp.InitializeResult", result)
	}
	if init.Capabilities.TextDocumentSync != 1 {
		t.Fatalf("TextDocumentSync = %d, want 1", init.Capabilities.TextDocumentSync)
	}
}

func TestHandlerInitializeDiscoversWorkspaceInBackground(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "module.nix"), "{}")
	rootURI := mustURI(t, root)

	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, map[string]any{
		"rootUri": rootURI,
	})); err != nil {
		t.Fatalf("initialize error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	workspace, err := handler.WaitForWorkspace(ctx)
	if err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}
	if workspace.Root != root {
		t.Fatalf("workspace root = %q, want %q", workspace.Root, root)
	}
	if len(workspace.Files) != 2 {
		t.Fatalf("workspace files = %d, want 2", len(workspace.Files))
	}
	if _, ok := handler.Workspace(); !ok {
		t.Fatal("Workspace() ok = false, want true")
	}
}

func TestHandlerInitializeStoresWorkspaceDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	flake := filepath.Join(root, "flake.nix")
	writeFile(t, flake, "import ./missing.nix")
	rootURI := mustURI(t, root)
	flakeURI := mustURI(t, flake)

	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, map[string]any{
		"rootUri": rootURI,
	})); err != nil {
		t.Fatalf("initialize error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := handler.WaitForWorkspace(ctx); err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}

	diagnostics := handler.Diagnostics(flakeURI)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
}

func TestHandlerDidChangeRefreshesStaticDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	source := filepath.Join(root, "default.nix")
	writeFile(t, source, "{}")
	rootURI := mustURI(t, root)
	sourceURI := mustURI(t, source)

	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, map[string]any{
		"rootUri": rootURI,
	})); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := handler.WaitForWorkspace(ctx); err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}

	openDocument(t, handler, sourceURI, "{}")
	if got := waitForDiagnostics(t, handler, sourceURI, 0); len(got) != 0 {
		t.Fatalf("diagnostics after open = %+v, want none", got)
	}

	_, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": sourceURI, "version": 2},
		"contentChanges": []map[string]any{
			{"text": "import ./missing.nix"},
		},
	}))
	if err != nil {
		t.Fatalf("didChange error = %v", err)
	}

	diagnostics := waitForDiagnostics(t, handler, sourceURI, 1)
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
}

func TestHandlerPublishesDiagnosticsThroughLSP(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 1)}
	handler.SetNotifier(notifier)

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "{")

	var params publishDiagnosticsParams
	select {
	case params = <-notifier.messages:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for publishDiagnostics")
	}
	if params.URI != uri {
		t.Fatalf("uri = %q, want %q", params.URI, uri)
	}
	if len(params.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(params.Diagnostics))
	}
	if params.Diagnostics[0].Range.Start.Line != 0 || params.Diagnostics[0].Range.Start.Character != 0 {
		t.Fatalf("start = %+v, want 0:0", params.Diagnostics[0].Range.Start)
	}
}

func TestSmokePublishesUnopenedWorkspaceAndDidChangeDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 16)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	flake := filepath.Join(root, "flake.nix")
	source := filepath.Join(root, "default.nix")
	writeFile(t, flake, "import ./missing.nix")
	writeFile(t, source, "{}")
	rootURI := mustURI(t, root)
	flakeURI := mustURI(t, flake)
	sourceURI := mustURI(t, source)

	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, map[string]any{
		"rootUri": rootURI,
	})); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := handler.WaitForWorkspace(ctx); err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}
	workspacePublish := waitForPublish(t, notifier, flakeURI, 1)
	if workspacePublish.Diagnostics[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("workspace message = %q", workspacePublish.Diagnostics[0].Message)
	}

	openDocument(t, handler, sourceURI, "{}")
	waitForPublish(t, notifier, sourceURI, 0)
	if _, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": sourceURI, "version": 2},
		"contentChanges": []map[string]any{
			{"text": "import ./other-missing.nix"},
		},
	})); err != nil {
		t.Fatalf("didChange error = %v", err)
	}
	editPublish := waitForPublish(t, notifier, sourceURI, 1)
	if editPublish.Diagnostics[0].Message != "missing import target ./other-missing.nix" {
		t.Fatalf("edit message = %q", editPublish.Diagnostics[0].Message)
	}
}

func TestHandlerPublishesWarningSeverityForUnusedBinding(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	handler.SetNotifier(notifier)

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "let x = 1; in 2")

	params := waitForPublish(t, notifier, uri, 1)
	if got := params.Diagnostics[0].Severity; got != 2 {
		t.Fatalf("severity = %d, want 2 (warning)", got)
	}
	if params.Diagnostics[0].Message != `unused binding "x"` {
		t.Fatalf("message = %q, want unused binding", params.Diagnostics[0].Message)
	}
}

func TestHandlerPublishesErrorSeverityForSyntaxDiagnostic(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 4)}
	handler.SetNotifier(notifier)

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	openDocument(t, handler, uri, "{")

	params := waitForPublish(t, notifier, uri, 1)
	if got := params.Diagnostics[0].Severity; got != 1 {
		t.Fatalf("severity = %d, want 1 (error)", got)
	}
}

func openDocument(t *testing.T, handler *Handler, uri string, text string) {
	t.Helper()
	_, err := handler.Handle(context.Background(), "textDocument/didOpen", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri, "text": text},
	}))
	if err != nil {
		t.Fatalf("didOpen error = %v", err)
	}
}

func mustURI(t *testing.T, path string) string {
	t.Helper()
	uri, err := vfs.PathToURI(path)
	if err != nil {
		t.Fatalf("PathToURI error = %v", err)
	}
	return uri
}

func mustJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	return data
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
}

func waitForDiagnostics(t *testing.T, handler *Handler, uri string, want int) []syntax.Diagnostic {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var diagnostics []syntax.Diagnostic
	for time.Now().Before(deadline) {
		diagnostics = handler.Diagnostics(uri)
		if len(diagnostics) == want {
			return diagnostics
		}
		time.Sleep(10 * time.Millisecond)
	}
	return diagnostics
}

func waitForPublish(t *testing.T, notifier *captureNotifier, uri string, diagnosticCount int) publishDiagnosticsParams {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case params := <-notifier.messages:
			if params.URI == uri && len(params.Diagnostics) == diagnosticCount {
				return params
			}
		case <-deadline:
			t.Fatalf("timed out waiting for publishDiagnostics uri=%s count=%d", uri, diagnosticCount)
		}
	}
}
