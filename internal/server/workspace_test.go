package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

func TestHandlerInitializeAdvertisesWorkspaceSymbolCapability(t *testing.T) {
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
	if !init.Capabilities.WorkspaceSymbolProvider {
		t.Error("WorkspaceSymbolProvider = false, want true")
	}
	data, err := json.Marshal(init.Capabilities)
	if err != nil {
		t.Fatalf("Marshal capabilities error = %v", err)
	}
	if !strings.Contains(string(data), `"workspaceSymbolProvider":true`) {
		t.Errorf("serialized capabilities = %s, want workspaceSymbolProvider:true", data)
	}
}

func TestHandlerWorkspaceSymbolEmptyQueryReturnsSortedSymbolsAcrossFiles(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "a.nix"), "let alpha = 1; in alpha")
	writeFile(t, filepath.Join(root, "b.nix"), "{ beta = 1; }")
	initWorkspace(t, handler, root)

	symbols := requestWorkspaceSymbols(t, handler, "")

	alpha := symbolInfoByName(t, symbols, "alpha")
	if alpha.Kind != symbolKindVariable {
		t.Errorf("alpha kind = %d, want %d (Variable)", alpha.Kind, symbolKindVariable)
	}
	if alpha.Location.URI != mustURI(t, filepath.Join(root, "a.nix")) {
		t.Errorf("alpha uri = %q, want a.nix", alpha.Location.URI)
	}
	beta := symbolInfoByName(t, symbols, "beta")
	if beta.Kind != symbolKindField {
		t.Errorf("beta kind = %d, want %d (Field)", beta.Kind, symbolKindField)
	}

	// Deterministic order: sorted by URI, so a.nix's alpha precedes b.nix's beta.
	if !symbolSortedByURIThenRange(symbols) {
		t.Errorf("symbols not sorted by (uri, range): %+v", symbols)
	}
	alphaIdx, betaIdx := indexOfSymbol(symbols, "alpha"), indexOfSymbol(symbols, "beta")
	if alphaIdx > betaIdx {
		t.Errorf("alpha (a.nix) should precede beta (b.nix); got alpha=%d beta=%d", alphaIdx, betaIdx)
	}
}

func TestHandlerWorkspaceSymbolCaseInsensitiveSubstringMatch(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "a.nix"), "let alpha = 1; in alpha")
	writeFile(t, filepath.Join(root, "b.nix"), "{ beta = 1; }")
	initWorkspace(t, handler, root)

	// Uppercase query matches the lowercase binding name (case-insensitive).
	matches := requestWorkspaceSymbols(t, handler, "ALP")
	if len(matches) != 1 || matches[0].Name != "alpha" {
		t.Fatalf("query ALP = %+v, want [alpha]", matches)
	}

	// Substring in the middle of a name matches.
	mid := requestWorkspaceSymbols(t, handler, "et")
	if len(mid) != 1 || mid[0].Name != "beta" {
		t.Fatalf("query et = %+v, want [beta]", mid)
	}

	// A query matching nothing yields an empty (non-nil) result.
	none := requestWorkspaceSymbols(t, handler, "zzz")
	if len(none) != 0 {
		t.Fatalf("query zzz = %+v, want none", none)
	}
}

func TestHandlerWorkspaceSymbolCapsResults(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")

	// A single attrset with far more than the cap of bindings.
	var b strings.Builder
	b.WriteString("{\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&b, "  k%d = 1;\n", i)
	}
	b.WriteString("}\n")
	writeFile(t, filepath.Join(root, "big.nix"), b.String())
	initWorkspace(t, handler, root)

	symbols := requestWorkspaceSymbols(t, handler, "")
	if len(symbols) != workspaceSymbolCap {
		t.Fatalf("symbols = %d, want cap %d", len(symbols), workspaceSymbolCap)
	}
}

func TestHandlerWorkspaceSymbolExcludesParamInheritDynamic(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	// lib: inherit entry (let), arg: function param, ${keep}: dynamic key -> all
	// excluded. fn, keep (let bindings) and plain (attr key) -> included.
	src := "let\n" +
		"  inherit (pkgs) lib;\n" +
		"  fn = arg: arg;\n" +
		"  keep = lib;\n" +
		"in {\n" +
		"  ${keep} = 1;\n" +
		"  plain = 2;\n" +
		"}\n"
	writeFile(t, filepath.Join(root, "m.nix"), src)
	initWorkspace(t, handler, root)

	symbols := requestWorkspaceSymbols(t, handler, "")
	names := make(map[string]bool)
	for _, s := range symbols {
		names[s.Name] = true
	}
	for _, want := range []string{"fn", "keep", "plain"} {
		if !names[want] {
			t.Errorf("symbol %q missing from %+v", want, symbols)
		}
	}
	for _, bad := range []string{"lib", "arg", "${keep}"} {
		if names[bad] {
			t.Errorf("symbol %q should be excluded, got %+v", bad, symbols)
		}
	}
}

func TestHandlerWorkspaceSymbolBeforeDiscoveryReturnsEmpty(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// No initialize: workspace is not yet discovered.
	symbols := requestWorkspaceSymbols(t, handler, "")
	if len(symbols) != 0 {
		t.Fatalf("symbols before discovery = %+v, want none", symbols)
	}
}

func TestHandlerWatchedFilesChangedClearsDiagnosticOnFix(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	dep := filepath.Join(root, "dep.nix")
	writeFile(t, dep, "import ./missing.nix")
	depURI := mustURI(t, dep)
	initWorkspace(t, handler, root)

	// Discovery publishes the missing-import diagnostic for the non-open file.
	publish := waitForPublish(t, notifier, depURI, 1)
	if publish.Diagnostics[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("initial message = %q", publish.Diagnostics[0].Message)
	}

	// Fix the file on disk, then report the change.
	writeFile(t, dep, "{}")
	sendWatchedFiles(t, handler, []map[string]any{{"uri": depURI, "type": 2}})

	waitForPublish(t, notifier, depURI, 0)
}

func TestHandlerWatchedFilesDeletedClearsDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	dep := filepath.Join(root, "dep.nix")
	writeFile(t, dep, "import ./missing.nix")
	depURI := mustURI(t, dep)
	initWorkspace(t, handler, root)

	waitForPublish(t, notifier, depURI, 1)

	if err := os.Remove(dep); err != nil {
		t.Fatalf("Remove error = %v", err)
	}
	sendWatchedFiles(t, handler, []map[string]any{{"uri": depURI, "type": 3}})

	waitForPublish(t, notifier, depURI, 0)
	if got := waitForDiagnostics(t, handler, depURI, 0); len(got) != 0 {
		t.Fatalf("cached diagnostics = %+v, want none", got)
	}
}

func TestHandlerWatchedFilesIgnoresOpenBuffer(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	source := filepath.Join(t.TempDir(), "source.nix")
	sourceURI := mustURI(t, source)

	// The editor buffer holds an error; the disk copy is valid.
	openDocument(t, handler, sourceURI, "import ./missing.nix")
	if got := waitForDiagnostics(t, handler, sourceURI, 1); len(got) != 1 {
		t.Fatalf("buffer diagnostics = %+v, want 1", got)
	}
	writeFile(t, source, "{}")

	// A watched-file change for the OPEN document must not clobber the buffer's
	// diagnostics with the (valid) disk content. didChangeWatchedFiles handles an
	// open-buffer-only change synchronously and should submit no refresh task at
	// all (the overlay skip in the handler) — but the point of this test is to
	// catch a regression where it DOES schedule one, so drain the coalescer
	// before asserting: a wrongly scheduled recompute then lands its clobber
	// where the assertion sees it, deterministically.
	sendWatchedFiles(t, handler, []map[string]any{{"uri": sourceURI, "type": 2}})
	waitForDiagIdle(t, handler, sourceURI)

	got := handler.Diagnostics(sourceURI)
	if len(got) != 1 || got[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("diagnostics after watched change = %+v, want buffer's missing-import error", got)
	}
}

func TestHandlerWatchedFilesCreatedPublishesDiagnostic(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	initWorkspace(t, handler, root)

	created := filepath.Join(root, "new.nix")
	writeFile(t, created, "import ./missing.nix")
	createdURI := mustURI(t, created)
	sendWatchedFiles(t, handler, []map[string]any{{"uri": createdURI, "type": 1}})

	publish := waitForPublish(t, notifier, createdURI, 1)
	if publish.Diagnostics[0].Message != "missing import target ./missing.nix" {
		t.Fatalf("created message = %q", publish.Diagnostics[0].Message)
	}
}

func TestHandlerWatchedFilesGitTrackRefreshClearsUntrackedWarning(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 64)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	importer := filepath.Join(root, "importer.nix")
	target := filepath.Join(root, "target.nix")
	writeFile(t, importer, "import ./target.nix")
	writeFile(t, target, "{}")

	runGit(t, root, "init")
	runGit(t, root, "add", "flake.nix", "importer.nix")

	initWorkspace(t, handler, root)
	importerURI := mustURI(t, importer)

	// Open the importer so it is in the open-file set that gets refreshed after a
	// tracked-set change; its target exists but is untracked -> warning.
	openDocument(t, handler, importerURI, "import ./target.nix")
	warn := waitForPublish(t, notifier, importerURI, 1)
	if warn.Diagnostics[0].Severity != 2 {
		t.Fatalf("severity = %d, want 2 (warning)", warn.Diagnostics[0].Severity)
	}

	// Track the target, then report a change on it. Re-discovery refreshes the
	// git-tracked set; the open importer is recomputed and its warning clears.
	runGit(t, root, "add", "target.nix")
	sendWatchedFiles(t, handler, []map[string]any{{"uri": mustURI(t, target), "type": 2}})

	waitForPublish(t, notifier, importerURI, 0)
}

// TestHandlerGitIndexWatchClearsUntrackedWarningWithoutEdit replays the reported
// bug: a terminal `git add` (no editor edit) followed only by a .git/index
// watched-file change must clear the open file's untracked-import warning. Before
// the git-state input + .git/index watcher this required the quick fix or a
// restart.
func TestHandlerGitIndexWatchClearsUntrackedWarningWithoutEdit(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 64)}
	handler.SetNotifier(notifier)

	importerURI, lib, root := untrackedImportFixture(t, handler, notifier)

	// Stage the target outside the editor, then deliver only the .git/index change
	// (no textDocument/didChange). The open importer's warning must clear.
	runGit(t, root, "add", lib)
	sendWatchedFiles(t, handler, []map[string]any{
		{"uri": mustURI(t, filepath.Join(root, ".git", "index")), "type": 2},
	})

	waitForPublish(t, notifier, importerURI, 0)
}

func TestHandlerFlakeDiagnosticsAndLockRefresh(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 64)}
	handler.SetNotifier(notifier)

	root := t.TempDir()
	// a: locked. b: unlocked (missing from lock). c: locked but never used in the
	// strict outputs formals. a carries a dangling follows to an undeclared input.
	flakeSrc := "{\n" +
		"  inputs.a.url = \"u\";\n" +
		"  inputs.b.url = \"v\";\n" +
		"  inputs.c.url = \"w\";\n" +
		"  inputs.a.inputs.x.follows = \"nope\";\n" +
		"  outputs = { self, a, b }: {};\n" +
		"}\n"
	flakePath := filepath.Join(root, "flake.nix")
	writeFile(t, flakePath, flakeSrc)
	lockPath := filepath.Join(root, "flake.lock")
	writeFile(t, lockPath, `{"version":7,"root":"root","nodes":{"root":{"inputs":{"a":"a","c":"c"}},"a":{},"c":{}}}`)

	initWorkspace(t, handler, root)
	flakeURI := mustURI(t, flakePath)
	openDocument(t, handler, flakeURI, flakeSrc)

	// dangling-follows (nope), input-not-locked (b), unused-input (c).
	initial := waitForPublish(t, notifier, flakeURI, 3)
	got := publishedCodes(initial)
	for _, code := range []string{"dangling-follows", "input-not-locked", "unused-input"} {
		if !got[code] {
			t.Fatalf("initial flake diagnostics missing %q: %+v", code, initial.Diagnostics)
		}
	}

	// Rewrite the lock to also lock b, then report the lock change. b's
	// input-not-locked warning must clear; dangling and unused remain.
	writeFile(t, lockPath, `{"version":7,"root":"root","nodes":{"root":{"inputs":{"a":"a","b":"b","c":"c"}},"a":{},"b":{},"c":{}}}`)
	sendWatchedFiles(t, handler, []map[string]any{{"uri": mustURI(t, lockPath), "type": 2}})

	updated := waitForPublish(t, notifier, flakeURI, 2)
	updatedCodes := publishedCodes(updated)
	if updatedCodes["input-not-locked"] {
		t.Fatalf("input-not-locked persisted after locking b: %+v", updated.Diagnostics)
	}
	for _, code := range []string{"dangling-follows", "unused-input"} {
		if !updatedCodes[code] {
			t.Fatalf("expected %q to remain: %+v", code, updated.Diagnostics)
		}
	}
}

func publishedCodes(params publishDiagnosticsParams) map[string]bool {
	codes := make(map[string]bool)
	for _, d := range params.Diagnostics {
		codes[d.Code] = true
	}
	return codes
}

func initWorkspace(t *testing.T, handler *Handler, root string) {
	t.Helper()
	rootURI := mustURI(t, root)
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
}

func requestWorkspaceSymbols(t *testing.T, handler *Handler, query string) []SymbolInformation {
	t.Helper()
	result, err := handler.Handle(context.Background(), "workspace/symbol", mustJSON(t, map[string]any{"query": query}))
	if err != nil {
		t.Fatalf("workspace/symbol error = %v", err)
	}
	symbols, ok := result.([]SymbolInformation)
	if !ok {
		t.Fatalf("workspace/symbol result type = %T, want []SymbolInformation", result)
	}
	return symbols
}

func sendWatchedFiles(t *testing.T, handler *Handler, changes []map[string]any) {
	t.Helper()
	if _, err := handler.Handle(context.Background(), "workspace/didChangeWatchedFiles", mustJSON(t, map[string]any{
		"changes": changes,
	})); err != nil {
		t.Fatalf("didChangeWatchedFiles error = %v", err)
	}
}

func symbolInfoByName(t *testing.T, symbols []SymbolInformation, name string) SymbolInformation {
	t.Helper()
	for _, s := range symbols {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %+v", name, symbols)
	return SymbolInformation{}
}

func indexOfSymbol(symbols []SymbolInformation, name string) int {
	for i, s := range symbols {
		if s.Name == name {
			return i
		}
	}
	return -1
}

func symbolSortedByURIThenRange(symbols []SymbolInformation) bool {
	for i := 1; i < len(symbols); i++ {
		prev, cur := symbols[i-1], symbols[i]
		if prev.Location.URI > cur.Location.URI {
			return false
		}
		if prev.Location.URI == cur.Location.URI && protocolRangeLess(cur.Location.Range, prev.Location.Range) {
			return false
		}
	}
	return true
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v error = %v\n%s", args, err, out)
	}
}
