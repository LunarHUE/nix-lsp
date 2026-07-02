// Package server connects LSP protocol events to the analysis foundations.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/static"
	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

const errMethodNotFound = -32601

// Handler is the main LSP handler for nixls.
type Handler struct {
	vfs    *vfs.Store
	syntax *syntax.Analyzer
	tasks  *lsp.Scheduler

	mu            sync.RWMutex
	diagnostics   map[string][]syntax.Diagnostic
	contents      map[string][]byte
	notifier      lsp.Notifier
	workspace     project.Workspace
	workspaceOK   bool
	workspaceErr  error
	workspaceDone chan struct{}
}

// NewHandler creates a handler with empty in-memory state.
func NewHandler() *Handler {
	handler := &Handler{
		vfs:         vfs.New(),
		syntax:      syntax.NewAnalyzer(),
		tasks:       lsp.NewScheduler(64),
		diagnostics: make(map[string][]syntax.Diagnostic),
		contents:    make(map[string][]byte),
	}
	handler.tasks.Start(context.Background(), 2)
	return handler
}

// SetNotifier attaches the LSP notification sink.
func (h *Handler) SetNotifier(notifier lsp.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifier = notifier
}

// Close stops background work owned by the handler.
func (h *Handler) Close() {
	h.tasks.Stop()
}

// Handle implements lsp.Handler.
func (h *Handler) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	switch method {
	case "initialize":
		h.startWorkspaceDiscovery(ctx, params)
		return lsp.InitializeResult{
			Capabilities: lsp.ServerCapabilities{
				TextDocumentSync: 1,
			},
			ServerInfo: &lsp.ServerInfo{
				Name: "nix-lsp",
			},
		}, nil
	case "textDocument/didOpen":
		return nil, h.didOpen(params)
	case "textDocument/didChange":
		return nil, h.didChange(params)
	case "textDocument/didClose":
		return nil, h.didClose(params)
	case "textDocument/didSave", "workspace/didChangeConfiguration", "workspace/didChangeWatchedFiles":
		return nil, nil
	default:
		return nil, &lsp.ResponseError{Code: errMethodNotFound, Message: fmt.Sprintf("method not found: %s", method)}
	}
}

// Diagnostics returns a copy of the current diagnostics for uri.
func (h *Handler) Diagnostics(uri string) []syntax.Diagnostic {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return cloneDiagnostics(h.diagnostics[uri])
}

// Workspace returns the latest discovered workspace, if discovery has
// completed successfully.
func (h *Handler) Workspace() (project.Workspace, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.workspace, h.workspaceOK
}

// WaitForWorkspace waits for the current background workspace discovery.
func (h *Handler) WaitForWorkspace(ctx context.Context) (project.Workspace, error) {
	h.mu.RLock()
	done := h.workspaceDone
	h.mu.RUnlock()
	if done == nil {
		return project.Workspace{}, nil
	}

	select {
	case <-ctx.Done():
		return project.Workspace{}, ctx.Err()
	case <-done:
	}

	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.workspace, h.workspaceErr
}

// Snapshot returns an immutable view of the handler's VFS.
func (h *Handler) Snapshot() *vfs.Snapshot {
	return h.vfs.Snapshot()
}

func (h *Handler) startWorkspaceDiscovery(_ context.Context, params json.RawMessage) {
	start := initializeStartPath(params)
	if start == "" {
		return
	}

	done := make(chan struct{})
	h.mu.Lock()
	h.workspace = project.Workspace{}
	h.workspaceOK = false
	h.workspaceErr = nil
	h.workspaceDone = done
	h.mu.Unlock()

	result := h.tasks.Submit(context.Background(), lsp.LaneBackground, func(context.Context) error {
		workspace, err := project.Discover(start)
		workspaceDiagnostics := map[string][]syntax.Diagnostic{}
		if err == nil {
			workspaceDiagnostics = h.workspaceDiagnostics(workspace)
		}

		h.mu.Lock()
		defer h.mu.Unlock()
		h.workspace = workspace
		h.workspaceErr = err
		h.workspaceOK = err == nil
		if err == nil {
			for _, file := range workspace.Files {
				delete(h.diagnostics, file.URI)
			}
			for uri, diagnostics := range workspaceDiagnostics {
				h.diagnostics[uri] = cloneDiagnostics(diagnostics)
			}
		}
		close(done)

		if err == nil {
			go h.publishWorkspaceDiagnostics(workspace)
		}
		return err
	})

	go func() {
		taskResult := <-result
		if taskResult.Err == nil {
			return
		}

		h.mu.Lock()
		if h.workspaceDone == done {
			h.workspaceErr = taskResult.Err
			h.workspaceOK = false
			select {
			case <-done:
			default:
				close(done)
			}
		}
		h.mu.Unlock()
	}()
}

func (h *Handler) didOpen(params json.RawMessage) error {
	var decoded didOpenTextDocumentParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fmt.Errorf("decode didOpen params: %w", err)
	}
	path, err := vfs.URIToPath(decoded.TextDocument.URI)
	if err != nil {
		return err
	}
	if _, err := h.vfs.OpenBuffer(path, []byte(decoded.TextDocument.Text)); err != nil {
		return err
	}
	h.setDiagnostics(decoded.TextDocument.URI, path, []byte(decoded.TextDocument.Text))
	return nil
}

func (h *Handler) didChange(params json.RawMessage) error {
	var decoded didChangeTextDocumentParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fmt.Errorf("decode didChange params: %w", err)
	}
	if len(decoded.ContentChanges) == 0 {
		return nil
	}

	text := decoded.ContentChanges[len(decoded.ContentChanges)-1].Text
	path, err := vfs.URIToPath(decoded.TextDocument.URI)
	if err != nil {
		return err
	}
	if _, err := h.vfs.UpdateBuffer(path, []byte(text)); err != nil {
		if _, openErr := h.vfs.OpenBuffer(path, []byte(text)); openErr != nil {
			return openErr
		}
	}
	h.setDiagnostics(decoded.TextDocument.URI, path, []byte(text))
	return nil
}

func (h *Handler) didClose(params json.RawMessage) error {
	var decoded didCloseTextDocumentParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fmt.Errorf("decode didClose params: %w", err)
	}
	path, err := vfs.URIToPath(decoded.TextDocument.URI)
	if err != nil {
		return err
	}
	if err := h.vfs.CloseBuffer(path); err != nil {
		return err
	}

	h.mu.Lock()
	delete(h.diagnostics, decoded.TextDocument.URI)
	delete(h.contents, decoded.TextDocument.URI)
	h.mu.Unlock()
	h.publishDiagnostics(context.Background(), decoded.TextDocument.URI, nil, nil)
	return nil
}

func (h *Handler) setDiagnostics(uri string, path string, content []byte) {
	h.mu.RLock()
	workspace := h.workspace
	workspaceOK := h.workspaceOK
	h.mu.RUnlock()

	diagnostics := h.combinedDiagnostics(workspace, workspaceOK, path, content)

	h.mu.Lock()
	h.diagnostics[uri] = cloneDiagnostics(diagnostics)
	h.contents[uri] = cloneBytes(content)
	h.mu.Unlock()

	h.publishDiagnostics(context.Background(), uri, diagnostics, content)
}

func (h *Handler) workspaceDiagnostics(workspace project.Workspace) map[string][]syntax.Diagnostic {
	diagnostics := make(map[string][]syntax.Diagnostic)
	snapshot := h.vfs.Snapshot()
	for _, file := range workspace.Files {
		read, err := snapshot.ReadFile(file.Path)
		if err != nil {
			continue
		}
		h.mu.Lock()
		h.contents[file.URI] = cloneBytes(read.Content)
		h.mu.Unlock()
		fileDiagnostics := h.combinedDiagnostics(workspace, true, file.Path, read.Content)
		if len(fileDiagnostics) == 0 {
			continue
		}
		diagnostics[file.URI] = fileDiagnostics
	}
	return diagnostics
}

func (h *Handler) publishWorkspaceDiagnostics(workspace project.Workspace) {
	for _, file := range workspace.Files {
		h.mu.RLock()
		diagnostics := cloneDiagnostics(h.diagnostics[file.URI])
		content := cloneBytes(h.contents[file.URI])
		h.mu.RUnlock()
		h.publishDiagnostics(context.Background(), file.URI, diagnostics, content)
	}
}

func (h *Handler) publishDiagnostics(ctx context.Context, uri string, diagnostics []syntax.Diagnostic, content []byte) {
	h.mu.RLock()
	notifier := h.notifier
	h.mu.RUnlock()
	if notifier == nil {
		return
	}

	_ = notifier.Notify(ctx, "textDocument/publishDiagnostics", publishDiagnosticsParams{
		URI:         uri,
		Diagnostics: toProtocolDiagnostics(diagnostics, content),
	})
}

func (h *Handler) combinedDiagnostics(workspace project.Workspace, workspaceOK bool, path string, content []byte) []syntax.Diagnostic {
	diagnostics := h.syntax.Diagnostics(content)
	if workspaceOK {
		staticDiagnostics, err := static.FileDiagnostics(workspace, path, content)
		if err == nil {
			diagnostics = append(diagnostics, staticDiagnostics...)
		}
	}
	return diagnostics
}

func cloneDiagnostics(diagnostics []syntax.Diagnostic) []syntax.Diagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	cloned := make([]syntax.Diagnostic, len(diagnostics))
	copy(cloned, diagnostics)
	return cloned
}

func cloneBytes(content []byte) []byte {
	if len(content) == 0 {
		return nil
	}
	cloned := make([]byte, len(content))
	copy(cloned, content)
	return cloned
}

type textDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId,omitempty"`
	Version    int    `json:"version,omitempty"`
	Text       string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version,omitempty"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didOpenTextDocumentParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type didChangeTextDocumentParams struct {
	TextDocument   versionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []textDocumentContentChangeEvent `json:"contentChanges"`
}

type textDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

type didCloseTextDocumentParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type publishDiagnosticsParams struct {
	URI         string               `json:"uri"`
	Diagnostics []protocolDiagnostic `json:"diagnostics"`
}

type protocolDiagnostic struct {
	Range    protocolRange `json:"range"`
	Severity int           `json:"severity,omitempty"`
	Source   string        `json:"source,omitempty"`
	Message  string        `json:"message"`
}

type protocolRange struct {
	Start protocolPosition `json:"start"`
	End   protocolPosition `json:"end"`
}

type protocolPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

type initializeParams struct {
	RootURI          string            `json:"rootUri"`
	RootPath         string            `json:"rootPath"`
	WorkspaceFolders []workspaceFolder `json:"workspaceFolders"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name,omitempty"`
}

func initializeStartPath(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}

	var decoded initializeParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return ""
	}
	if len(decoded.WorkspaceFolders) > 0 && decoded.WorkspaceFolders[0].URI != "" {
		return decoded.WorkspaceFolders[0].URI
	}
	if decoded.RootURI != "" {
		return decoded.RootURI
	}
	return decoded.RootPath
}

func toProtocolDiagnostics(diagnostics []syntax.Diagnostic, content []byte) []protocolDiagnostic {
	if len(diagnostics) == 0 {
		return nil
	}

	protocolDiagnostics := make([]protocolDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		protocolDiagnostics = append(protocolDiagnostics, protocolDiagnostic{
			Range: protocolRange{
				Start: byteOffsetPosition(content, diagnostic.Range.Start),
				End:   byteOffsetPosition(content, diagnostic.Range.End),
			},
			Severity: 1,
			Source:   "nix-lsp",
			Message:  diagnostic.Message,
		})
	}
	return protocolDiagnostics
}

func byteOffsetPosition(content []byte, offset int) protocolPosition {
	if offset < 0 {
		offset = 0
	}
	if offset > len(content) {
		offset = len(content)
	}

	position := protocolPosition{}
	for i := 0; i < offset; i++ {
		if content[i] == '\n' {
			position.Line++
			position.Character = 0
			continue
		}
		position.Character++
	}
	return position
}
