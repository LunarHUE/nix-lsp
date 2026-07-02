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

func TestHandlerInitializeAdvertisesFeatureCapabilities(t *testing.T) {
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
	caps := init.Capabilities
	if !caps.DocumentSymbolProvider {
		t.Error("DocumentSymbolProvider = false, want true")
	}
	if !caps.DefinitionProvider {
		t.Error("DefinitionProvider = false, want true")
	}
	if !caps.DocumentHighlightProvider {
		t.Error("DocumentHighlightProvider = false, want true")
	}
}

func TestHandlerDocumentSymbolReturnsHierarchy(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	// Line layout (0-based):
	//   0: let
	//   1:   x = 1;
	//   2:   cfg = {
	//   3:     enable = true;
	//   4:     nested = { value = 2; };
	//   5:   };
	//   6: in
	//   7:   cfg
	src := "let\n  x = 1;\n  cfg = {\n    enable = true;\n    nested = { value = 2; };\n  };\nin\n  cfg"
	openDocument(t, handler, uri, src)

	symbols := requestDocumentSymbols(t, handler, uri)
	if len(symbols) != 2 {
		t.Fatalf("top-level symbols = %d (%+v), want 2", len(symbols), symbols)
	}

	x := symbolByName(t, symbols, "x")
	if x.Kind != 13 {
		t.Errorf("x kind = %d, want 13 (Variable)", x.Kind)
	}
	if x.SelectionRange.Start.Line != 1 || x.SelectionRange.Start.Character != 2 {
		t.Errorf("x selectionRange start = %+v, want 1:2", x.SelectionRange.Start)
	}

	cfg := symbolByName(t, symbols, "cfg")
	if cfg.Kind != 13 {
		t.Errorf("cfg kind = %d, want 13 (Variable, let binding)", cfg.Kind)
	}
	if len(cfg.Children) != 2 {
		t.Fatalf("cfg children = %d (%+v), want 2", len(cfg.Children), cfg.Children)
	}

	enable := symbolByName(t, cfg.Children, "enable")
	if enable.Kind != 8 {
		t.Errorf("enable kind = %d, want 8 (Field)", enable.Kind)
	}
	nested := symbolByName(t, cfg.Children, "nested")
	if nested.Kind != 19 {
		t.Errorf("nested kind = %d, want 19 (Object)", nested.Kind)
	}
	value := symbolByName(t, nested.Children, "value")
	if value.Kind != 8 {
		t.Errorf("nested.value kind = %d, want 8 (Field)", value.Kind)
	}
}

func TestHandlerDocumentSymbolAnswersDespiteSyntaxError(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	// Missing closing brace: still parses the let binding.
	openDocument(t, handler, uri, "let\n  x = 1;\nin {\n  a = x;")

	symbols := requestDocumentSymbols(t, handler, uri)
	if symbolByName(t, symbols, "x").Name != "x" {
		t.Fatalf("symbols = %+v, want at least binding x", symbols)
	}
}

func TestHandlerDefinitionOnReferenceReturnsBindingNameRange(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	// 0: let
	// 1:   x = 1;   (x def at 1:2)
	// 2:   y = x;   (x use at 2:6)
	// 3: in
	// 4:   x        (x use at 4:2)
	openDocument(t, handler, uri, "let\n  x = 1;\n  y = x;\nin\n  x")

	location := requestDefinition(t, handler, uri, 2, 6)
	if location == nil {
		t.Fatal("definition on use = null, want binding location")
	}
	if location.URI != uri {
		t.Errorf("location uri = %q, want %q", location.URI, uri)
	}
	if location.Range.Start.Line != 1 || location.Range.Start.Character != 2 {
		t.Errorf("location start = %+v, want 1:2", location.Range.Start)
	}
	if location.Range.End.Character != 3 {
		t.Errorf("location end char = %d, want 3", location.Range.End.Character)
	}
}

func TestHandlerDefinitionOnBindingReturnsOwnRange(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "let\n  x = 1;\n  y = x;\nin\n  x")

	location := requestDefinition(t, handler, uri, 1, 2)
	if location == nil {
		t.Fatal("definition on binding name = null, want own location")
	}
	if location.Range.Start.Line != 1 || location.Range.Start.Character != 2 {
		t.Errorf("location start = %+v, want 1:2", location.Range.Start)
	}
}

func TestHandlerDefinitionOnUnresolvedReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	// 3:   z is unresolved.
	openDocument(t, handler, uri, "let\n  x = 1;\nin\n  z")

	result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, uri, 3, 2))
	if err != nil {
		t.Fatalf("definition error = %v", err)
	}
	if result != nil {
		t.Fatalf("definition on unresolved = %+v, want null", result)
	}
}

func TestHandlerDocumentHighlightOnUseReturnsWriteAndReads(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "let\n  x = 1;\n  y = x;\nin\n  x")

	// Cursor on the final use of x (4:2).
	highlights := requestHighlights(t, handler, uri, 4, 2)
	assertHighlightSet(t, highlights)
}

func TestHandlerDocumentHighlightOnDefinitionReturnsSameSet(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	path := filepath.Join(t.TempDir(), "test.nix")
	uri := mustURI(t, path)

	openDocument(t, handler, uri, "let\n  x = 1;\n  y = x;\nin\n  x")

	// Cursor on the definition of x (1:2).
	highlights := requestHighlights(t, handler, uri, 1, 2)
	assertHighlightSet(t, highlights)
}

func TestHandlerFeatureRequestsOnUnopenedURIReturnNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// A URI that was never opened and does not exist on disk.
	uri := mustURI(t, filepath.Join(t.TempDir(), "absent.nix"))

	cases := []struct {
		method string
		params json.RawMessage
	}{
		{"textDocument/documentSymbol", mustJSON(t, map[string]any{"textDocument": map[string]any{"uri": uri}})},
		{"textDocument/definition", positionParams(t, uri, 0, 0)},
		{"textDocument/documentHighlight", positionParams(t, uri, 0, 0)},
	}
	for _, tc := range cases {
		result, err := handler.Handle(context.Background(), tc.method, tc.params)
		if err != nil {
			t.Fatalf("%s error = %v, want nil", tc.method, err)
		}
		if result != nil {
			t.Fatalf("%s result = %+v, want null", tc.method, result)
		}
	}
}

// assertHighlightSet checks the highlights for the shared `x` fixture: one write
// at the definition (1:2) and two reads at the uses (2:6 and 4:2).
func assertHighlightSet(t *testing.T, highlights []DocumentHighlight) {
	t.Helper()
	if len(highlights) != 3 {
		t.Fatalf("highlights = %d (%+v), want 3", len(highlights), highlights)
	}
	var writes, reads int
	for _, h := range highlights {
		switch h.Kind {
		case 3:
			writes++
			if h.Range.Start.Line != 1 || h.Range.Start.Character != 2 {
				t.Errorf("write highlight start = %+v, want 1:2", h.Range.Start)
			}
		case 2:
			reads++
		default:
			t.Errorf("unexpected highlight kind %d", h.Kind)
		}
	}
	if writes != 1 {
		t.Errorf("write highlights = %d, want 1", writes)
	}
	if reads != 2 {
		t.Errorf("read highlights = %d, want 2", reads)
	}
}

func requestDocumentSymbols(t *testing.T, handler *Handler, uri string) []DocumentSymbol {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/documentSymbol", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}))
	if err != nil {
		t.Fatalf("documentSymbol error = %v", err)
	}
	symbols, ok := result.([]DocumentSymbol)
	if !ok {
		t.Fatalf("documentSymbol result type = %T, want []DocumentSymbol", result)
	}
	return symbols
}

func requestDefinition(t *testing.T, handler *Handler, uri string, line, character int) *Location {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, uri, line, character))
	if err != nil {
		t.Fatalf("definition error = %v", err)
	}
	if result == nil {
		return nil
	}
	location, ok := result.(*Location)
	if !ok {
		t.Fatalf("definition result type = %T, want *Location", result)
	}
	return location
}

func requestHighlights(t *testing.T, handler *Handler, uri string, line, character int) []DocumentHighlight {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/documentHighlight", positionParams(t, uri, line, character))
	if err != nil {
		t.Fatalf("documentHighlight error = %v", err)
	}
	highlights, ok := result.([]DocumentHighlight)
	if !ok {
		t.Fatalf("documentHighlight result type = %T, want []DocumentHighlight", result)
	}
	return highlights
}

func positionParams(t *testing.T, uri string, line, character int) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": character},
	})
}

func symbolByName(t *testing.T, symbols []DocumentSymbol, name string) DocumentSymbol {
	t.Helper()
	for _, symbol := range symbols {
		if symbol.Name == name {
			return symbol
		}
	}
	t.Fatalf("symbol %q not found in %+v", name, symbols)
	return DocumentSymbol{}
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
