package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
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
	if got := handler.Diagnostics(uri); len(got) != 1 {
		t.Fatalf("diagnostics = %v, want one syntax diagnostic", got)
	}
}

func TestHandlerDidChangeUpdatesOverlayAndDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "{")
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
	if got := handler.Diagnostics(uri); len(got) != 0 {
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
	if got := handler.Diagnostics(sourceURI); len(got) != 0 {
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

	diagnostics := handler.Diagnostics(sourceURI)
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

	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)
	input := strings.Join([]string{
		frame(`{"jsonrpc":"2.0","method":"textDocument/didOpen","params":{"textDocument":{"uri":` + quoteJSON(t, uri) + `,"languageId":"nix","version":1,"text":"{"}}}`),
		frame(`{"jsonrpc":"2.0","method":"exit"}`),
	}, "")

	var out bytes.Buffer
	if err := lsp.NewServer(strings.NewReader(input), &out, handler).Run(context.Background()); err != nil {
		t.Fatalf("Run error = %v", err)
	}

	messages := readMessages(t, &out)
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].Method != "textDocument/publishDiagnostics" {
		t.Fatalf("method = %q, want publishDiagnostics", messages[0].Method)
	}
	var params publishDiagnosticsParams
	if err := json.Unmarshal(messages[0].Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
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

func readMessages(t *testing.T, r io.Reader) []*lsp.Message {
	t.Helper()
	reader := lsp.NewReader(r)
	var messages []*lsp.Message
	for {
		message, err := reader.ReadMessage()
		if err == io.EOF {
			return messages
		}
		if err != nil {
			t.Fatalf("ReadMessage error = %v", err)
		}
		messages = append(messages, message)
	}
}

func frame(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}

func quoteJSON(t *testing.T, value string) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	return string(data)
}
