// Package server connects LSP protocol events to the analysis foundations.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

const errMethodNotFound = -32601

// Handler is the main LSP handler for nixls.
type Handler struct {
	vfs       *vfs.Store
	tasks     *lsp.Scheduler
	memo      *memo.Engine
	publisher *diagnosticsPublisher

	mu             sync.RWMutex
	diagnostics    map[string][]syntax.Diagnostic
	diagGeneration map[string]uint64
	workspace      project.Workspace
	workspaceOK    bool
	workspaceErr   error
	workspaceDone  chan struct{}
	generation     uint64
}

// NewHandler creates a handler with empty in-memory state.
func NewHandler() *Handler {
	engine := memo.New()
	facts.Register(engine)
	facts.SetWorkspace(engine, project.Workspace{})

	handler := &Handler{
		vfs:            vfs.New(),
		tasks:          lsp.NewScheduler(64),
		memo:           engine,
		publisher:      newDiagnosticsPublisher(),
		diagnostics:    make(map[string][]syntax.Diagnostic),
		diagGeneration: make(map[string]uint64),
	}
	handler.tasks.Start(context.Background(), 2)
	return handler
}

// SetNotifier attaches the LSP notification sink.
func (h *Handler) SetNotifier(notifier lsp.Notifier) {
	h.publisher.SetNotifier(notifier)
}

// Close stops background work owned by the handler.
func (h *Handler) Close() {
	h.tasks.Stop()
	h.publisher.Stop()
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
		h.startWorkspaceDiscovery(params)
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

func (h *Handler) startWorkspaceDiscovery(params json.RawMessage) {
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

	h.tasks.Submit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		workspace, err := project.Discover(start)
		h.mu.Lock()
		h.workspace = workspace
		h.workspaceErr = err
		h.workspaceOK = err == nil
		h.mu.Unlock()

		if err == nil {
			facts.SetWorkspace(h.memo, workspace)
			snapshot := h.vfs.Snapshot()
			for _, file := range workspace.Files {
				_ = h.computeFileDiagnostics(ctx, snapshot, file.URI, file.Path, h.nextGeneration(), false)
			}
		}

		h.mu.Lock()
		close(done)
		h.mu.Unlock()
		return err
	})
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
	h.scheduleFileDiagnostics(decoded.TextDocument.URI, path, true)
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
	h.scheduleFileDiagnostics(decoded.TextDocument.URI, path, true)
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

	generation := h.nextGeneration()
	h.mu.Lock()
	delete(h.diagnostics, decoded.TextDocument.URI)
	h.diagGeneration[decoded.TextDocument.URI] = generation
	h.mu.Unlock()
	h.publisher.Publish(diagnosticUpdate{
		URI:        decoded.TextDocument.URI,
		Generation: generation,
		Debounce:   false,
	})
	return nil
}

func (h *Handler) scheduleFileDiagnostics(uri string, path string, debounce bool) {
	generation := h.nextGeneration()
	snapshot := h.vfs.Snapshot()
	h.tasks.Submit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		return h.computeFileDiagnostics(ctx, snapshot, uri, path, generation, debounce)
	})
}

func (h *Handler) computeFileDiagnostics(ctx context.Context, snapshot *vfs.Snapshot, uri string, path string, generation uint64, debounce bool) error {
	file, err := snapshot.ReadFile(path)
	if err != nil {
		return err
	}

	fileID := facts.FileID(file.Path, file.Hash)
	facts.SetFileInput(h.memo, fileID, facts.FileInput{
		Path:    file.Path,
		Content: file.Content,
	})
	diagnostics, err := facts.FileDiagnostics(ctx, h.memo, fileID)
	if err != nil {
		return err
	}

	// Guard the in-memory cache by generation: a slower, older-generation
	// compute (e.g. a didOpen task that lands after a newer didChange) must not
	// overwrite fresher diagnostics. The publisher applies the same ordering to
	// its sends; this keeps the handler's own cache consistent with it.
	h.mu.Lock()
	if generation < h.diagGeneration[uri] {
		h.mu.Unlock()
		return nil
	}
	h.diagGeneration[uri] = generation
	h.diagnostics[uri] = cloneDiagnostics(diagnostics)
	h.mu.Unlock()

	h.publisher.Publish(diagnosticUpdate{
		URI:         uri,
		Diagnostics: diagnostics,
		Generation:  generation,
		Debounce:    debounce,
	})
	return nil
}

func (h *Handler) nextGeneration() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	return h.generation
}

func cloneDiagnostics(diagnostics []syntax.Diagnostic) []syntax.Diagnostic {
	if len(diagnostics) == 0 {
		return nil
	}
	cloned := make([]syntax.Diagnostic, len(diagnostics))
	copy(cloned, diagnostics)
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

func toProtocolDiagnostics(diagnostics []syntax.Diagnostic) []protocolDiagnostic {
	if len(diagnostics) == 0 {
		return nil
	}

	protocolDiagnostics := make([]protocolDiagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		protocolDiagnostics = append(protocolDiagnostics, protocolDiagnostic{
			Range: protocolRange{
				Start: toProtocolPosition(diagnostic.Range.Start),
				End:   toProtocolPosition(diagnostic.Range.End),
			},
			Severity: 1,
			Source:   "nix-lsp",
			Message:  diagnostic.Message,
		})
	}
	return protocolDiagnostics
}

func toProtocolPosition(position syntax.Position) protocolPosition {
	return protocolPosition{Line: position.Line, Character: position.Character}
}
