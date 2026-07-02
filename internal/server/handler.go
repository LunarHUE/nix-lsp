// Package server connects LSP protocol events to the analysis foundations.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

const (
	errMethodNotFound = -32601
	errInvalidParams  = -32602
)

// commandGitAdd is the workspace/executeCommand command that stages an
// untracked flake import target with git add.
const commandGitAdd = "nix-lsp.gitAdd"

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

	notifier          lsp.Notifier
	caller            lsp.Caller
	progressSupported bool
	progressSeq       uint64

	// options holds the NixOS option-documentation dataset. optionsIndex is
	// swapped atomically once a load publishes it (nil until then, or when the
	// feature is disabled). optionsOnce guards the single load kicked off from
	// initialize. optionsDownloadEnabled gates auto mode's network fetch: the real
	// server enables it, tests leave it off so none performs network I/O.
	// optionsCtx is cancelled by Close to abort an in-flight download.
	optionsIndex           atomic.Pointer[options.Index]
	optionsOnce            sync.Once
	optionsDownloadEnabled bool
	optionsCtx             context.Context
	optionsCancel          context.CancelFunc

	// packages holds the channel packages dataset for package-version hover. It
	// mirrors the options fields: packagesIndex is swapped atomically once a load
	// publishes it, and packagesOnce guards the single load kicked off from
	// initialize. Auto mode reuses optionsDownloadEnabled (the one download gate)
	// and optionsCtx (cancelled by Close).
	packagesIndex atomic.Pointer[packages.Index]
	packagesOnce  sync.Once
}

// NewHandler creates a handler with empty in-memory state.
func NewHandler() *Handler {
	engine := memo.New()
	facts.Register(engine)
	facts.SetWorkspace(engine, project.Workspace{})
	facts.SetFlakeLock(engine, nil)

	handler := &Handler{
		vfs:            vfs.New(),
		tasks:          lsp.NewScheduler(64),
		memo:           engine,
		publisher:      newDiagnosticsPublisher(),
		diagnostics:    make(map[string][]syntax.Diagnostic),
		diagGeneration: make(map[string]uint64),
	}
	handler.optionsCtx, handler.optionsCancel = context.WithCancel(context.Background())
	handler.tasks.Start(context.Background(), 2)
	return handler
}

// SetNotifier attaches the LSP notification sink. The publisher owns
// diagnostics; the handler keeps its own reference for progress notifications.
func (h *Handler) SetNotifier(notifier lsp.Notifier) {
	h.mu.Lock()
	h.notifier = notifier
	h.mu.Unlock()
	h.publisher.SetNotifier(notifier)
}

// SetCaller attaches the server-to-client request sink used for work-done
// progress creation during workspace indexing.
func (h *Handler) SetCaller(caller lsp.Caller) {
	h.mu.Lock()
	h.caller = caller
	h.mu.Unlock()
}

// Close stops background work owned by the handler.
func (h *Handler) Close() {
	// Cancel before stopping the scheduler so an in-flight options download aborts
	// rather than making the scheduler's Stop wait out its timeout.
	h.optionsCancel()
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
		h.mu.Lock()
		h.progressSupported = initializeProgressSupported(params)
		h.mu.Unlock()
		h.startWorkspaceDiscovery(params)
		h.startOptionsLoad(params)
		h.startPackagesLoad(params)
		return lsp.InitializeResult{
			Capabilities: lsp.ServerCapabilities{
				TextDocumentSync:          1,
				DocumentSymbolProvider:    true,
				DefinitionProvider:        true,
				HoverProvider:             true,
				DocumentHighlightProvider: true,
				ReferencesProvider:        true,
				FoldingRangeProvider:      true,
				WorkspaceSymbolProvider:   true,
				CodeActionProvider:        true,
				ExecuteCommandProvider:    &lsp.ExecuteCommandOptions{Commands: []string{commandGitAdd}},
				CompletionProvider:        &lsp.CompletionOptions{TriggerCharacters: []string{"\""}},
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
	case "textDocument/documentSymbol":
		return h.documentSymbol(ctx, params)
	case "textDocument/definition":
		return h.definition(ctx, params)
	case "textDocument/hover":
		return h.hover(ctx, params)
	case "textDocument/completion":
		return h.completion(ctx, params)
	case "textDocument/documentHighlight":
		return h.documentHighlight(ctx, params)
	case "textDocument/references":
		return h.references(ctx, params)
	case "textDocument/foldingRange":
		return h.foldingRange(ctx, params)
	case "textDocument/codeAction":
		return h.codeAction(ctx, params)
	case "workspace/symbol":
		return h.workspaceSymbol(ctx, params)
	case "workspace/executeCommand":
		return h.executeCommand(ctx, params)
	case "workspace/didChangeWatchedFiles":
		return nil, h.didChangeWatchedFiles(params)
	case "textDocument/didSave", "workspace/didChangeConfiguration":
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
		h.mu.RLock()
		caller := h.caller
		notifier := h.notifier
		progressOn := h.progressSupported
		h.mu.RUnlock()

		var progress *indexingProgress
		if progressOn {
			progress = h.startIndexingProgress(caller, notifier)
		}
		// Progress always ends once create succeeded, even if discovery errored.
		endMessage := "Indexing failed"
		if progress != nil {
			defer func() { progress.end(endMessage) }()
		}

		workspace, err := project.Discover(start)
		h.mu.Lock()
		h.workspace = workspace
		h.workspaceErr = err
		h.workspaceOK = err == nil
		h.mu.Unlock()

		if err == nil {
			facts.SetWorkspace(h.memo, workspace)
			snapshot := h.vfs.Snapshot()
			h.refreshFlakeLock(snapshot)
			total := len(workspace.Files)
			lastPct := -1
			for i, file := range workspace.Files {
				_ = h.computeFileDiagnostics(ctx, snapshot, file.URI, file.Path, h.nextGeneration(), false)
				if progress != nil && total > 0 {
					if pct := 100 * (i + 1) / total; pct != lastPct {
						lastPct = pct
						progress.report(fmt.Sprintf("%d/%d files", i+1, total), uint(pct))
					}
				}
			}
			endMessage = fmt.Sprintf("Indexed %d files", total)
		}

		h.mu.Lock()
		close(done)
		h.mu.Unlock()
		return err
	})
}

// indexingProgress reports a single work-done progress session over the LSP
// notifier. A nil *indexingProgress is a valid no-op receiver, so callers need
// no separate nil checks when progress is disabled or create was rejected.
type indexingProgress struct {
	notifier lsp.Notifier
	token    string
}

// startIndexingProgress asks the client to create a progress token, then emits
// the begin notification. It returns nil (progress disabled) when there is no
// caller/notifier or the client rejects the create request; indexing must still
// proceed in that case, so a bounded context keeps a mute client from stalling.
func (h *Handler) startIndexingProgress(caller lsp.Caller, notifier lsp.Notifier) *indexingProgress {
	if caller == nil || notifier == nil {
		return nil
	}
	token := fmt.Sprintf("nix-lsp/indexing/%d", atomic.AddUint64(&h.progressSeq, 1))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := caller.Call(ctx, "window/workDoneProgress/create", workDoneProgressCreateParams{Token: token}, nil); err != nil {
		return nil
	}
	progress := &indexingProgress{notifier: notifier, token: token}
	progress.notify(workDoneProgressBegin{Kind: "begin", Title: "Indexing Nix workspace"})
	return progress
}

func (p *indexingProgress) notify(value any) {
	if p == nil {
		return
	}
	_ = p.notifier.Notify(context.Background(), "$/progress", progressParams{Token: p.token, Value: value})
}

func (p *indexingProgress) report(message string, percentage uint) {
	p.notify(workDoneProgressReport{Kind: "report", Message: message, Percentage: percentage})
}

func (p *indexingProgress) end(message string) {
	p.notify(workDoneProgressEnd{Kind: "end", Message: message})
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

	h.publishEmptyDiagnostics(decoded.TextDocument.URI)
	return nil
}

// publishEmptyDiagnostics clears the diagnostics for uri under a fresh
// generation, so a stale set (from a closed or deleted file) cannot linger. It
// takes the same generation-guarded path the compute flow uses, so an in-flight
// older-generation compute for the same uri cannot resurrect the cleared set.
func (h *Handler) publishEmptyDiagnostics(uri string) {
	generation := h.nextGeneration()
	h.mu.Lock()
	delete(h.diagnostics, uri)
	h.diagGeneration[uri] = generation
	h.mu.Unlock()
	h.publisher.Publish(diagnosticUpdate{
		URI:        uri,
		Generation: generation,
		Debounce:   false,
	})
}

// watchedFileChange is one relevant on-disk change from a
// workspace/didChangeWatchedFiles notification: a non-open .nix file that was
// created, changed, or deleted.
type watchedFileChange struct {
	uri     string
	path    string
	deleted bool
}

const (
	fileChangeTypeDeleted = 3
)

// didChangeWatchedFiles reacts to external filesystem changes (branch switches,
// git operations, out-of-editor edits). Open buffers are ignored: the editor
// buffer is the source of truth for an open document, and didOpen/didChange
// already drive its diagnostics. Everything else is handled off the notification
// thread by a single background task per notification so a large batch (a branch
// switch touching many files) re-discovers the workspace only once.
func (h *Handler) didChangeWatchedFiles(params json.RawMessage) error {
	var decoded didChangeWatchedFilesParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fmt.Errorf("decode didChangeWatchedFiles params: %w", err)
	}
	if len(decoded.Changes) == 0 {
		return nil
	}

	snapshot := h.vfs.Snapshot()
	changes := make([]watchedFileChange, 0, len(decoded.Changes))
	// A flake.lock change drives no per-file recompute of its own (it is JSON, not
	// Nix); it only refreshes the lock input and the root flake.nix diagnostics.
	lockChanged := false
	for _, change := range decoded.Changes {
		path, err := vfs.URIToPath(change.URI)
		if err != nil {
			continue
		}
		isNix := filepath.Ext(path) == ".nix"
		isLock := filepath.Base(path) == "flake.lock"
		if !isNix && !isLock {
			continue
		}
		// An open buffer overrides disk; its diagnostics are the editor's, not the
		// filesystem's. Skip it so an external write cannot clobber the buffer.
		if open, err := snapshot.HasOverlay(path); err != nil || open {
			continue
		}
		if isLock {
			lockChanged = true
			continue
		}
		changes = append(changes, watchedFileChange{
			uri:     change.URI,
			path:    path,
			deleted: change.Type == fileChangeTypeDeleted,
		})
	}
	if len(changes) == 0 && !lockChanged {
		return nil
	}

	h.mu.RLock()
	root := h.workspace.Root
	h.mu.RUnlock()

	h.tasks.Submit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		return h.refreshWatchedFiles(ctx, root, changes)
	})
	return nil
}

// refreshWatchedFiles re-discovers the workspace once, then recomputes
// diagnostics for the changed files and the currently-open files. It reuses the
// same generation-guarded computeFileDiagnostics/publishEmptyDiagnostics path as
// didOpen/didChange/didClose, so ordering against concurrent edits stays sound.
func (h *Handler) refreshWatchedFiles(ctx context.Context, root string, changes []watchedFileChange) error {
	// Re-discover once so the file list and git-tracked set reflect disk. A
	// changed git-tracked set alters untracked-import warnings in other files.
	if root != "" {
		if workspace, err := project.Discover(root); err == nil {
			h.mu.Lock()
			h.workspace = workspace
			h.workspaceOK = true
			h.workspaceErr = nil
			h.mu.Unlock()
			facts.SetWorkspace(h.memo, workspace)
		}
	}

	// Pin a snapshot after re-discovery so changed files read their new disk
	// content and open files read their current buffers.
	snapshot := h.vfs.Snapshot()

	// Refresh the flake.lock input so the root flake.nix diagnostics reflect a
	// lock change (or any re-discovery) below.
	h.refreshFlakeLock(snapshot)

	changed := make(map[string]bool, len(changes))
	for _, change := range changes {
		changed[change.path] = true
		if change.deleted {
			// Mirror didClose: clear squiggles for a file that no longer exists.
			h.publishEmptyDiagnostics(change.uri)
			continue
		}
		_ = h.computeFileDiagnostics(ctx, snapshot, change.uri, change.path, h.nextGeneration(), false)
	}

	// Recomputing every workspace file after a tracked-set change is unbounded.
	// Open files are few, so refresh them to pick up untracked-import warnings
	// that changed elsewhere while staying bounded.
	for _, open := range openFiles(snapshot) {
		_ = h.computeFileDiagnostics(ctx, snapshot, open.uri, open.path, h.nextGeneration(), true)
	}

	// The root flake.nix flake diagnostics depend on the lock and the re-discovered
	// workspace, so recompute it here unless it was already handled as a changed or
	// open file above (its own generation path avoids a duplicate publish).
	if root != "" {
		flakePath := filepath.Join(root, "flake.nix")
		if open, err := snapshot.HasOverlay(flakePath); err == nil && !open && !changed[flakePath] {
			if uri, err := vfs.PathToURI(flakePath); err == nil {
				_ = h.computeFileDiagnostics(ctx, snapshot, uri, flakePath, h.nextGeneration(), false)
			}
		}
	}
	return nil
}

// refreshFlakeLock reads flake.lock from snapshot for the known workspace root
// and stores it as the memo lock input (nil on a missing or unreadable file).
func (h *Handler) refreshFlakeLock(snapshot *vfs.Snapshot) {
	h.mu.RLock()
	root := h.workspace.Root
	h.mu.RUnlock()
	if root == "" {
		return
	}
	var content []byte
	if file, err := snapshot.ReadFile(filepath.Join(root, "flake.lock")); err == nil {
		content = file.Content
	}
	facts.SetFlakeLock(h.memo, content)
}

// openFiles returns the open buffers in snapshot as watchedFileChange values
// (uri + path), sorted by path for deterministic processing.
func openFiles(snapshot *vfs.Snapshot) []watchedFileChange {
	paths := snapshot.OverlayPaths()
	sort.Strings(paths)
	files := make([]watchedFileChange, 0, len(paths))
	for _, path := range paths {
		uri, err := vfs.PathToURI(path)
		if err != nil {
			continue
		}
		files = append(files, watchedFileChange{uri: uri, path: path})
	}
	return files
}

// executeCommand handles workspace/executeCommand. It is a state-mutating
// request, so unlike the read-only feature handlers it returns real JSON-RPC
// errors: an unknown command, a malformed argument, or a path that fails the
// safety checks all yield -32602. The only supported command stages an
// untracked flake import target with git add, then triggers a background
// workspace refresh so the untracked-import warnings clear on their own.
func (h *Handler) executeCommand(_ context.Context, params json.RawMessage) (any, error) {
	var decoded executeCommandParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("decode executeCommand params: %v", err)}
	}
	if decoded.Command != commandGitAdd {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("unknown command %q", decoded.Command)}
	}
	if len(decoded.Arguments) != 1 {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s expects exactly one argument, got %d", commandGitAdd, len(decoded.Arguments))}
	}
	var arg string
	if err := json.Unmarshal(decoded.Arguments[0], &arg); err != nil || arg == "" {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s argument must be a non-empty path string", commandGitAdd)}
	}

	path, err := vfs.NormalizePath(arg)
	if err != nil {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s: invalid path: %v", commandGitAdd, err)}
	}

	workspace, ok := h.Workspace()
	root := workspace.Root
	// Guard against a broken or malicious client asking the server to git-add an
	// arbitrary path: the target must be a .nix file inside a known workspace root.
	if !ok || root == "" {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s: no workspace", commandGitAdd)}
	}
	if filepath.Ext(path) != ".nix" {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s: not a .nix file: %s", commandGitAdd, path)}
	}
	if !withinRoot(root, path) {
		return nil, &lsp.ResponseError{Code: errInvalidParams, Message: fmt.Sprintf("%s: path outside workspace: %s", commandGitAdd, path)}
	}

	if err := project.GitAdd(root, path); err != nil {
		return nil, err
	}

	// Re-discovery picks up the newly-tracked file; refreshWatchedFiles then
	// recomputes the open files, clearing their untracked-import warnings.
	h.tasks.Submit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		return h.refreshWatchedFiles(ctx, root, nil)
	})
	return nil, nil
}

// withinRoot reports whether path is root itself or nested beneath it. It
// mirrors the static package's own guard; the server keeps its own copy so the
// executeCommand safety check does not depend on an exported static helper.
func withinRoot(root, path string) bool {
	if root == "" || path == "" {
		return false
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "..")
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

type didChangeWatchedFilesParams struct {
	Changes []fileEvent `json:"changes"`
}

type fileEvent struct {
	URI string `json:"uri"`
	// Type is the LSP FileChangeType: 1=Created, 2=Changed, 3=Deleted.
	Type int `json:"type"`
}

type executeCommandParams struct {
	Command string `json:"command"`
	// Arguments stays raw so the single string argument can be decoded and
	// validated explicitly rather than through a loose any conversion.
	Arguments []json.RawMessage `json:"arguments"`
}

type publishDiagnosticsParams struct {
	URI         string               `json:"uri"`
	Diagnostics []protocolDiagnostic `json:"diagnostics"`
}

type protocolDiagnostic struct {
	Range    protocolRange `json:"range"`
	Severity int           `json:"severity,omitempty"`
	Code     string        `json:"code,omitempty"`
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
	RootURI          string             `json:"rootUri"`
	RootPath         string             `json:"rootPath"`
	WorkspaceFolders []workspaceFolder  `json:"workspaceFolders"`
	Capabilities     clientCapabilities `json:"capabilities"`
}

type clientCapabilities struct {
	Window windowClientCapabilities `json:"window"`
}

type windowClientCapabilities struct {
	WorkDoneProgress bool `json:"workDoneProgress"`
}

// workDoneProgressCreateParams is the payload of the
// window/workDoneProgress/create request.
type workDoneProgressCreateParams struct {
	Token string `json:"token"`
}

type progressParams struct {
	Token string `json:"token"`
	Value any    `json:"value"`
}

type workDoneProgressBegin struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

type workDoneProgressReport struct {
	Kind       string `json:"kind"`
	Message    string `json:"message,omitempty"`
	Percentage uint   `json:"percentage,omitempty"`
}

type workDoneProgressEnd struct {
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
}

func initializeProgressSupported(params json.RawMessage) bool {
	if len(params) == 0 {
		return false
	}
	var decoded initializeParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return false
	}
	return decoded.Capabilities.Window.WorkDoneProgress
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
			Severity: lspSeverity(diagnostic.Severity),
			Code:     diagnostic.Code,
			Source:   "nix-lsp",
			Message:  diagnostic.Message,
		})
	}
	return protocolDiagnostics
}

// lspSeverity maps a syntax severity to its LSP DiagnosticSeverity integer
// (Error=1, Warning=2, Information=3, Hint=4).
func lspSeverity(severity syntax.Severity) int {
	switch severity {
	case syntax.SeverityWarning:
		return 2
	case syntax.SeverityInformation:
		return 3
	case syntax.SeverityHint:
		return 4
	default:
		return 1
	}
}

func toProtocolPosition(position syntax.Position) protocolPosition {
	return protocolPosition{Line: position.Line, Character: position.Character}
}
