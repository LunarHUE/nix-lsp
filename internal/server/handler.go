// Package server connects LSP protocol events to the analysis foundations.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	diag      *diagScheduler

	mu             sync.RWMutex
	diagnostics    map[string][]syntax.Diagnostic
	diagGeneration map[string]uint64
	// diagPublished is a broadcast channel closed (and replaced) under mu every
	// time a URI's published diagnostic generation advances. It lets in-process
	// waiters (tests) block for a specific generation to land instead of polling
	// with sleeps. Never send on it; only close/replace signals a broadcast.
	diagPublished chan struct{}
	workspace     project.Workspace
	workspaceOK   bool
	workspaceErr  error
	workspaceDone chan struct{}
	generation    uint64

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

	// bgCtx is the parent context for coalesced background diagnostics work. It is
	// cancelled by Close so an in-flight recompute stops promptly on shutdown; the
	// diag coalescer derives a per-iteration cancellable child from it so a newer
	// edit can also abandon a superseded compute. Threading a real context here
	// (rather than context.Background()) is what gives that per-iteration child an
	// effective parent, so cancellation flows to the memo query boundaries and the
	// tree-sitter parse.
	bgCtx    context.Context
	bgCancel context.CancelFunc
	// optionsChannel names the channel an auto-mode options load resolved the
	// dataset from (e.g. "nixos-25.05"). It is recorded only when auto mode
	// resolves the channel and drives option hover's "Declared in" source links;
	// explicit-path and fixture loads leave it empty, so those hovers keep the
	// declarations backticked. Guarded by mu.
	optionsChannel string

	// packages holds the channel packages dataset for package-version hover. It
	// mirrors the options fields: packagesIndex is swapped atomically once a load
	// publishes it, and packagesOnce guards the single load kicked off from
	// initialize. Auto mode reuses optionsDownloadEnabled (the one download gate)
	// and optionsCtx (cancelled by Close).
	packagesIndex atomic.Pointer[packages.Index]
	packagesOnce  sync.Once
	// packagesChannel names the channel an auto-mode load resolved the dataset
	// from (e.g. "nixpkgs-unstable"). It is recorded only when auto mode publishes
	// an index and drives package hover's provenance line; explicit-path and
	// fixture loads leave it empty, so those hovers append no provenance. Guarded
	// by mu.
	packagesChannel string
}

// setPackagesChannel records the channel an auto-mode packages load resolved.
func (h *Handler) setPackagesChannel(channel string) {
	h.mu.Lock()
	h.packagesChannel = channel
	h.mu.Unlock()
}

// packagesChannelString returns the recorded auto-mode packages channel, or ""
// when the dataset came from an explicit path or has not loaded.
func (h *Handler) packagesChannelString() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.packagesChannel
}

// setOptionsChannel records the channel an auto-mode options load resolved.
func (h *Handler) setOptionsChannel(channel string) {
	h.mu.Lock()
	h.optionsChannel = channel
	h.mu.Unlock()
}

// optionsChannelString returns the recorded auto-mode options channel, or ""
// when the dataset came from an explicit path or has not loaded.
func (h *Handler) optionsChannelString() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.optionsChannel
}

// NewHandler creates a handler with empty in-memory state.
func NewHandler() *Handler {
	engine := memo.New()
	facts.Register(engine)
	facts.SetWorkspace(engine, project.Workspace{})
	facts.SetFlakeLock(engine, nil)
	facts.SetGitState(engine, "")

	handler := &Handler{
		vfs:            vfs.New(),
		tasks:          lsp.NewScheduler(64),
		memo:           engine,
		publisher:      newDiagnosticsPublisher(),
		diagnostics:    make(map[string][]syntax.Diagnostic),
		diagGeneration: make(map[string]uint64),
		diagPublished:  make(chan struct{}),
	}
	handler.optionsCtx, handler.optionsCancel = context.WithCancel(context.Background())
	handler.bgCtx, handler.bgCancel = context.WithCancel(context.Background())
	// Give the publisher a way to ask for a URI's CURRENT document version at
	// publish time, so it can drop diagnostics computed from a superseded buffer.
	// Injected as a func (like the notifier) rather than coupling the publisher to
	// the Handler.
	handler.publisher.SetVersionLookup(handler.currentDocumentVersion)
	handler.diag = newDiagScheduler(handler.enqueueBackground, handler.runFileDiagnostics)
	handler.tasks.Start(context.Background(), 2)
	return handler
}

// enqueueBackground submits run on the background lane without blocking, so a
// full queue can never park a notification-path caller (and thus the LSP read
// loop). It reports whether a worker will run the task; the diagnostics coalescer
// re-arms on a false (queue-full) return.
func (h *Handler) enqueueBackground(run func(context.Context)) bool {
	// Submit under the handler-lifetime bgCtx (not context.Background()) so the ctx
	// the worker hands to run — and the per-iteration child the diag coalescer
	// derives from it — has an effective parent: Close cancels bgCtx, aborting any
	// in-flight recompute instead of letting a stale multi-second compute run out.
	_, ok := h.tasks.TrySubmit(h.bgCtx, lsp.LaneBackground, func(ctx context.Context) error {
		run(ctx)
		return nil
	})
	return ok
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
	h.bgCancel()
	h.tasks.Stop()
	h.publisher.Stop()
}

// handledMethods lists every LSP method the dispatch switch in Handle answers
// (i.e. every method that does not fall through to the method-not-found default).
// It is the runtime handle on the switch that lets methods_test.go assert the
// capability<->handler correspondence exhaustively: keep it in lockstep with the
// switch below, since a case added here without a table update (or vice versa)
// is exactly the drift those tests exist to catch.
var handledMethods = []string{
	lsp.MethodInitialize,
	lsp.MethodTextDocumentDidOpen,
	lsp.MethodTextDocumentDidChange,
	lsp.MethodTextDocumentDidClose,
	lsp.MethodTextDocumentDocumentSymbol,
	lsp.MethodTextDocumentDefinition,
	lsp.MethodTextDocumentHover,
	lsp.MethodTextDocumentCompletion,
	lsp.MethodCompletionItemResolve,
	lsp.MethodTextDocumentDocumentHighlight,
	lsp.MethodTextDocumentReferences,
	lsp.MethodTextDocumentFoldingRange,
	lsp.MethodTextDocumentCodeAction,
	lsp.MethodWorkspaceSymbol,
	lsp.MethodWorkspaceExecuteCommand,
	lsp.MethodWorkspaceDidChangeWatchedFiles,
	lsp.MethodTextDocumentDidSave,
	lsp.MethodWorkspaceDidChangeConfiguration,
}

// Handle implements lsp.Handler.
func (h *Handler) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	switch method {
	case lsp.MethodInitialize:
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
				CompletionProvider:        &lsp.CompletionOptions{TriggerCharacters: []string{"\"", "."}, ResolveProvider: true},
			},
			ServerInfo: &lsp.ServerInfo{
				Name: "nix-lsp",
			},
		}, nil
	case lsp.MethodTextDocumentDidOpen:
		return nil, h.didOpen(params)
	case lsp.MethodTextDocumentDidChange:
		return nil, h.didChange(params)
	case lsp.MethodTextDocumentDidClose:
		return nil, h.didClose(params)
	case lsp.MethodTextDocumentDocumentSymbol:
		return h.documentSymbol(ctx, params)
	case lsp.MethodTextDocumentDefinition:
		return h.definition(ctx, params)
	case lsp.MethodTextDocumentHover:
		return h.hover(ctx, params)
	case lsp.MethodTextDocumentCompletion:
		return h.completion(ctx, params)
	case lsp.MethodCompletionItemResolve:
		return h.completionResolve(params)
	case lsp.MethodTextDocumentDocumentHighlight:
		return h.documentHighlight(ctx, params)
	case lsp.MethodTextDocumentReferences:
		return h.references(ctx, params)
	case lsp.MethodTextDocumentFoldingRange:
		return h.foldingRange(ctx, params)
	case lsp.MethodTextDocumentCodeAction:
		return h.codeAction(ctx, params)
	case lsp.MethodWorkspaceSymbol:
		return h.workspaceSymbol(ctx, params)
	case lsp.MethodWorkspaceExecuteCommand:
		return h.executeCommand(ctx, params)
	case lsp.MethodWorkspaceDidChangeWatchedFiles:
		return nil, h.didChangeWatchedFiles(params)
	case lsp.MethodTextDocumentDidSave, lsp.MethodWorkspaceDidChangeConfiguration:
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

	// Runs off the read loop (initialize is a request, dispatched on its own
	// goroutine) and must not be dropped: it is the one initial workspace index.
	// A blocking Submit is safe here because the queue is empty at initialize.
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
			h.refreshGitState()
			total := len(workspace.Files)
			lastPct := -1
			for i, file := range workspace.Files {
				// Per-file fresh read, not the pinned snapshot: indexing takes long
				// enough that an open buffer edited mid-walk would otherwise be
				// republished from its stale pre-walk copy under a newer generation.
				h.republishFileDiagnostics(ctx, file.URI, file.Path, false)
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
	if _, err := h.vfs.OpenBuffer(path, []byte(decoded.TextDocument.Text), int32(decoded.TextDocument.Version)); err != nil {
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
	version := int32(decoded.TextDocument.Version)
	if _, err := h.vfs.UpdateBuffer(path, []byte(text), version); err != nil {
		if _, openErr := h.vfs.OpenBuffer(path, []byte(text), version); openErr != nil {
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
	h.signalDiagPublished()
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
	// A .git/index change (a terminal git add/commit, a branch switch) mutates the
	// git-tracked set with no .nix write, so it too drives no per-file recompute of
	// its own; it only forces a re-discovery + git-state refresh that clears stale
	// untracked-import warnings on the open files.
	gitIndexChanged := false
	for _, change := range decoded.Changes {
		path, err := vfs.URIToPath(change.URI)
		if err != nil {
			continue
		}
		isNix := filepath.Ext(path) == ".nix"
		isLock := filepath.Base(path) == "flake.lock"
		// The client watches "**/.git/index" specifically (VS Code's default
		// files.watcherExclude does not exclude it — that exclusion is load-bearing
		// on the extension side). Match by the index file under a .git directory
		// rather than a bare basename so an unrelated file named "index" is ignored.
		isGitIndex := filepath.Base(path) == "index" && filepath.Base(filepath.Dir(path)) == ".git"
		if !isNix && !isLock && !isGitIndex {
			continue
		}
		if isGitIndex {
			// Never treat .git/index as a workspace .nix file; it only signals a
			// git-tracked-set change routed through the re-discovery below.
			gitIndexChanged = true
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
	if len(changes) == 0 && !lockChanged && !gitIndexChanged {
		return nil
	}

	h.mu.RLock()
	root := h.workspace.Root
	h.mu.RUnlock()

	// This runs synchronously on the LSP read loop, so it must never block: use
	// the non-blocking submit. A dropped refresh (queue full) is logged rather
	// than lost silently; open-buffer diagnostics keep flowing through the
	// coalescer, and the next watched-files/edit event re-triggers the refresh.
	// Overflow is not reachable in practice because background tasks are coarse.
	if _, ok := h.tasks.TrySubmit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		return h.refreshWatchedFiles(ctx, root, changes)
	}); !ok {
		fmt.Fprintln(os.Stderr, "nix-lsp: dropped watched-files refresh (scheduler queue full)")
	}
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

	// Refresh the git-state token so a .git/index change (a terminal git add, the
	// gitAdd command, or any re-discovery) invalidates the cached import edges and
	// clears stale untracked-import warnings.
	h.refreshGitState()

	changed := make(map[string]bool, len(changes))
	for _, change := range changes {
		changed[change.path] = true
		if change.deleted {
			// Mirror didClose: clear squiggles for a file that no longer exists.
			h.publishEmptyDiagnostics(change.uri)
			continue
		}
		h.republishFileDiagnostics(ctx, change.uri, change.path, false)
	}

	// Recomputing every workspace file after a tracked-set change is unbounded.
	// Open files are few, so refresh them to pick up untracked-import warnings
	// that changed elsewhere while staying bounded. This MUST go through the
	// coalescer, not a direct compute against the snapshot pinned above: the
	// changed-files loop can run long, and a direct compute here would take a
	// generation newer than any edit that landed mid-loop while publishing the
	// pinned (older) buffer content — a stale error that then sticks until the
	// next edit (the reported semicolon-stays-missing bug).
	for _, open := range openFiles(h.vfs.Snapshot()) {
		h.diag.schedule(open.uri, open.path, true)
	}

	// The root flake.nix flake diagnostics depend on the lock and the re-discovered
	// workspace, so recompute it here unless it was already handled as a changed
	// file above (open flake buffers were rescheduled with the open files).
	if root != "" {
		flakePath := filepath.Join(root, "flake.nix")
		if !changed[flakePath] {
			if uri, err := vfs.PathToURI(flakePath); err == nil {
				h.republishFileDiagnostics(ctx, uri, flakePath, false)
			}
		}
	}
	return nil
}

// republishFileDiagnostics recomputes and republishes diagnostics for one file
// from background refresh paths (watched-files, dataset loads, indexing). An
// open document routes through the per-URI coalescer so a republish serializes
// with the didChange edit path instead of racing it; anything else recomputes
// inline via runFileDiagnostics, whose generation-before-content-read ordering
// guarantees a concurrently arriving newer buffer always wins the publish.
// Never pass a previously pinned snapshot's content here: reading fresh state
// under a fresh generation is the whole point.
func (h *Handler) republishFileDiagnostics(ctx context.Context, uri string, path string, debounce bool) {
	if open, err := h.vfs.Snapshot().HasOverlay(path); err == nil && open {
		h.diag.schedule(uri, path, debounce)
		return
	}
	h.runFileDiagnostics(ctx, uri, path, debounce)
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

// refreshGitState stats the workspace root's git index and stores the resulting
// version token as the memo git-state input, so a token change invalidates the
// cached import edges (and thus untracked-import warnings). It is cheap enough — a
// single stat — to call on every diagnostics recompute, which is what lets a
// terminal `git add` reach the coalescer path without a .nix file change.
func (h *Handler) refreshGitState() {
	h.mu.RLock()
	root := h.workspace.Root
	h.mu.RUnlock()
	facts.SetGitState(h.memo, gitIndexToken(root))
}

// gitIndexToken returns a cheap version token for the git index under root: the
// index modification time and size joined, or the empty token when the index is
// absent. It is deliberately a stat rather than a git invocation so it can run on
// every recompute. A worktree checkout keeps its index outside `root/.git/index`
// (`.git` is a file pointing at the real gitdir), which discovery does not resolve
// today either; such a checkout yields the empty token rather than a stale one.
func gitIndexToken(root string) string {
	if root == "" {
		return ""
	}
	info, err := os.Stat(filepath.Join(root, ".git", "index"))
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%d-%d", info.ModTime().UnixNano(), info.Size())
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
	// recomputes the open files, clearing their untracked-import warnings. This
	// runs off the read loop (executeCommand is a request) and must not be dropped,
	// so a blocking Submit is acceptable here.
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

// scheduleFileDiagnostics requests a diagnostics recompute for an open document
// after a didOpen/didChange. It routes through the per-URI coalescer so a burst
// of keystrokes collapses to a single in-flight recompute (converging to the
// latest buffer) and never enqueues per-keystroke work that could fill the
// background queue. It is called synchronously on the LSP read loop, so it must
// never block — the coalescer's schedule is non-blocking.
func (h *Handler) scheduleFileDiagnostics(uri string, path string, debounce bool) {
	h.diag.schedule(uri, path, debounce)
}

// runFileDiagnostics is the coalescer's exec: it takes a fresh generation and VFS
// snapshot at run time (so it reflects the newest buffer, not the buffer at the
// time the recompute was requested) and computes and publishes diagnostics for
// the URI.
func (h *Handler) runFileDiagnostics(ctx context.Context, uri, path string, debounce bool) {
	// Refresh the git-state token before recomputing so a terminal `git add` that
	// only touched the index (no .nix change, so no re-discovery) still invalidates
	// this file's cached import edges and its stale untracked-import warning. The
	// token is a single stat, cheap enough to run on every coalesced recompute.
	h.refreshGitState()
	generation := h.nextGeneration()
	snapshot := h.vfs.Snapshot()
	_ = h.computeFileDiagnostics(ctx, snapshot, uri, path, generation, debounce)
}

// diagnosticInputs names the complete set of inputs the diagnostics publish path
// reads for one file. The publish path spans this function plus the facts
// FileDiagnostics query it drives (which folds in the workspace tracked-file set,
// the git-index token, and the root flake.lock) and the datasetDiagnostics /
// enrichSyntaxDiagnostics passes appended below (which read the options and
// packages index snapshots directly, outside the memo layer).
//
// The law: if the computation reads it, it lives here. Adding a read to any leg
// of the publish path without adding a field to this struct is a review-visible
// omission — this type is documentation-as-code, mirroring gopls's typeCheckInputs
// sitting next to its hasher. It is NOT a hash key and is never hashed; the memo
// engine performs the actual invalidation from the same inputs (recorded as memo
// inputs elsewhere), and this struct records them in one place next to their
// consumer so the true key set is visible rather than scattered.
type diagnosticInputs struct {
	// path is the file's normalized absolute path — a genuine input, because
	// relative import resolution is path-dependent.
	path string
	// contentHash is the SHA-256 of the file content. (path, contentHash) is the
	// fileID that keys every file-derived memo query.
	contentHash string
	// workspace is the discovered workspace snapshot. Its tracked-file set feeds
	// import-edge resolution and untracked-import warnings, and its Root gates the
	// flake diagnostics to the root flake.nix. It reaches the FileDiagnostics query
	// as the SetWorkspace memo input; recorded live here for the full picture.
	workspace project.Workspace
	// optionsIndex is the loaded NixOS options index identity (nil = not loaded).
	// datasetDiagnostics and the syntax-error enrichment read it directly, outside
	// the memo layer, so it must be recorded here to keep the input set honest.
	optionsIndex *options.Index
	// packagesIndex is the loaded packages index identity (nil = not loaded).
	// datasetDiagnostics reads it directly, outside the memo layer.
	packagesIndex *packages.Index
	// gitIndexToken and flakeLockHash are the two remaining publish-path inputs.
	// Both are singleton memo inputs — the git-index version token set by
	// refreshGitState (SetGitState) and the raw flake.lock bytes set by
	// refreshFlakeLock (SetFlakeLock) — which the memo engine folds into the
	// import-edges and flake diagnostics respectively. They are named here for
	// completeness of the input set; their authoritative values live in the memo
	// inputs and are not re-fetched per compute (a per-keystroke stat and
	// flake.lock read would only duplicate work the memo layer already tracks).
	gitIndexToken string
	flakeLockHash string
}

func (h *Handler) computeFileDiagnostics(ctx context.Context, snapshot *vfs.Snapshot, uri string, path string, generation uint64, debounce bool) error {
	file, err := snapshot.ReadFile(path)
	if err != nil {
		return err
	}

	workspace, _ := h.Workspace()
	inputs := diagnosticInputs{
		path:          file.Path,
		contentHash:   file.Hash,
		workspace:     workspace,
		optionsIndex:  h.optionsSnapshot(),
		packagesIndex: h.packagesSnapshot(),
	}

	fileID := facts.FileInputFor(h.memo, inputs.path, inputs.contentHash, file.Content)
	diagnostics, err := facts.FileDiagnostics(ctx, h.memo, fileID)
	if err != nil {
		return err
	}
	// Dataset diagnostics (unknown-option, unknown-package) depend on the loaded
	// index identity rather than file content, so they cannot be memoized in the
	// FileDiagnostics fact; append them here so every publish path includes them
	// once the indexes are loaded. The same index dependence applies to the
	// option-schema guidance appended to syntax-error messages, so that
	// enrichment also happens here, on a copy, never on the memoized slice.
	diagnostics = append(diagnostics, h.datasetDiagnostics(ctx, fileID)...)
	diagnostics = h.enrichSyntaxDiagnostics(ctx, fileID, diagnostics)

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
	h.signalDiagPublished()
	h.mu.Unlock()

	// Record the document version this compute actually read (overlay files
	// only; disk files have none). The publisher uses it as a version backstop:
	// if the live document has advanced past this version by publish time, the
	// update is stale and gets dropped — a correctness layer independent of the
	// generation ordering above.
	h.publisher.Publish(diagnosticUpdate{
		URI:         uri,
		Diagnostics: diagnostics,
		Generation:  generation,
		Debounce:    debounce,
		Version:     file.Version,
		Versioned:   file.Overlay,
	})
	return nil
}

// currentDocumentVersion reports the live LSP document version for uri, if a
// buffer is open for it. It is the publisher's version backstop: the second
// result is false for a URI with no open buffer (an unversioned/disk publish),
// which the publisher passes through unchanged.
func (h *Handler) currentDocumentVersion(uri string) (int32, bool) {
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return vfs.NoVersion, false
	}
	return h.vfs.Version(path)
}

func (h *Handler) nextGeneration() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.generation++
	return h.generation
}

// signalDiagPublished broadcasts that some URI's published diagnostic generation
// advanced. Callers must hold h.mu. Closing the current channel wakes every
// waiter blocked on it; a fresh channel replaces it for the next round.
func (h *Handler) signalDiagPublished() {
	if h.diagPublished != nil {
		close(h.diagPublished)
		h.diagPublished = make(chan struct{})
	}
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
	// Version has no omitempty: the LSP spec makes it required, and omitempty
	// would silently drop a legitimate version 0 if this struct were ever
	// re-encoded. (Asymmetry note: the outbound publishDiagnosticsParams.Version
	// IS a pointer with omitempty, because there "absent" must be distinguishable
	// from 0 on the wire.)
	Version int    `json:"version"`
	Text    string `json:"text"`
}

type versionedTextDocumentIdentifier struct {
	URI string `json:"uri"`
	// Version has no omitempty: required by the LSP spec, and dropping a zero on
	// re-encode would corrupt the version backstop. See textDocumentItem.Version.
	Version int `json:"version"`
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
	URI string `json:"uri"`
	// Version is a pointer with omitempty so "no version" (a disk-file publish)
	// is encoded as an absent field, distinct from a real version 0. This is the
	// deliberate asymmetry with the inbound decode structs (textDocumentItem /
	// versionedTextDocumentIdentifier), whose Version is a plain required int:
	// there absent-vs-0 does not matter, here it does.
	Version     *int                 `json:"version,omitempty"`
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
