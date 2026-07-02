package server

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

// untrackedImportFixture builds a git+flake workspace whose main.nix imports an
// existing-but-untracked ./lib.nix, opens main.nix, and waits for the
// untracked-import warning to land. It returns the importer URI, the normalized
// absolute lib.nix path, and the workspace root. lib.nix is deliberately NOT
// git-added.
func untrackedImportFixture(t *testing.T, handler *Handler, notifier *captureNotifier) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	importer := filepath.Join(root, "main.nix")
	lib := filepath.Join(root, "lib.nix")
	writeFile(t, importer, "import ./lib.nix")
	writeFile(t, lib, "{}")

	runGit(t, root, "init")
	runGit(t, root, "add", "flake.nix", "main.nix")

	initWorkspace(t, handler, root)
	importerURI := mustURI(t, importer)

	openDocument(t, handler, importerURI, "import ./lib.nix")
	if notifier != nil {
		warn := waitForPublish(t, notifier, importerURI, 1)
		if warn.Diagnostics[0].Code != "untracked-import" {
			t.Fatalf("warning code = %q, want untracked-import", warn.Diagnostics[0].Code)
		}
	} else {
		waitForDiagnostics(t, handler, importerURI, 1)
	}
	return importerURI, mustNormalize(t, lib), root
}

func requestCodeActions(t *testing.T, handler *Handler, uri string, startLine, startChar, endLine, endChar int, only []string) []CodeAction {
	t.Helper()
	rng := map[string]any{
		"start": map[string]any{"line": startLine, "character": startChar},
		"end":   map[string]any{"line": endLine, "character": endChar},
	}
	ctxMap := map[string]any{}
	if only != nil {
		ctxMap["only"] = only
	}
	result, err := handler.Handle(context.Background(), "textDocument/codeAction", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"range":        rng,
		"context":      ctxMap,
	}))
	if err != nil {
		t.Fatalf("codeAction error = %v", err)
	}
	if result == nil {
		return nil
	}
	actions, ok := result.([]CodeAction)
	if !ok {
		t.Fatalf("codeAction result type = %T, want []CodeAction", result)
	}
	return actions
}

func TestHandlerCodeActionOffersGitAddQuickfix(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	importerURI, libPath, _ := untrackedImportFixture(t, handler, notifier)

	actions := requestCodeActions(t, handler, importerURI, 0, 0, 0, 100, nil)
	if len(actions) != 1 {
		t.Fatalf("actions = %d (%+v), want 1", len(actions), actions)
	}
	action := actions[0]
	if action.Kind != "quickfix" {
		t.Errorf("kind = %q, want quickfix", action.Kind)
	}
	if !action.IsPreferred {
		t.Error("isPreferred = false, want true")
	}
	if !strings.Contains(action.Title, "lib.nix") {
		t.Errorf("title = %q, want it to contain relative path lib.nix", action.Title)
	}
	if action.Command == nil {
		t.Fatal("command = nil, want git add command")
	}
	if action.Command.Command != "nix-lsp.gitAdd" {
		t.Errorf("command = %q, want nix-lsp.gitAdd", action.Command.Command)
	}
	if len(action.Command.Arguments) != 1 {
		t.Fatalf("arguments = %+v, want one", action.Command.Arguments)
	}
	arg, ok := action.Command.Arguments[0].(string)
	if !ok || arg != libPath {
		t.Errorf("argument = %v, want absolute lib path %q", action.Command.Arguments[0], libPath)
	}
	// The attached diagnostic is exactly the untracked-import warning.
	if len(action.Diagnostics) != 1 || action.Diagnostics[0].Code != "untracked-import" {
		t.Errorf("diagnostics = %+v, want one untracked-import", action.Diagnostics)
	}
}

func TestHandlerCodeActionOnlyFilterRejectsNonQuickfix(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	importerURI, _, _ := untrackedImportFixture(t, handler, notifier)

	if actions := requestCodeActions(t, handler, importerURI, 0, 0, 0, 100, []string{"refactor"}); actions != nil {
		t.Fatalf("actions with only=[refactor] = %+v, want null", actions)
	}
	// A quickfix request still returns the action.
	if actions := requestCodeActions(t, handler, importerURI, 0, 0, 0, 100, []string{"quickfix"}); len(actions) != 1 {
		t.Fatalf("actions with only=[quickfix] = %+v, want 1", actions)
	}
}

func TestHandlerCodeActionRangeElsewhereReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	importerURI, _, _ := untrackedImportFixture(t, handler, notifier)

	// A range on a line that does not touch the import edge yields no action.
	if actions := requestCodeActions(t, handler, importerURI, 5, 0, 5, 1, nil); actions != nil {
		t.Fatalf("actions for range elsewhere = %+v, want null", actions)
	}
}

func TestHandlerCodeActionTrackedImportReturnsNull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	importer := filepath.Join(root, "main.nix")
	lib := filepath.Join(root, "lib.nix")
	writeFile(t, importer, "import ./lib.nix")
	writeFile(t, lib, "{}")

	runGit(t, root, "init")
	// lib.nix IS tracked here, so no warning and no quick fix.
	runGit(t, root, "add", "flake.nix", "main.nix", "lib.nix")

	initWorkspace(t, handler, root)
	importerURI := mustURI(t, importer)
	openDocument(t, handler, importerURI, "import ./lib.nix")
	waitForDiagnostics(t, handler, importerURI, 0)

	if actions := requestCodeActions(t, handler, importerURI, 0, 0, 0, 100, nil); actions != nil {
		t.Fatalf("actions for tracked import = %+v, want null", actions)
	}
}

func TestHandlerCodeActionMissingImportReturnsNull(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	importer := filepath.Join(root, "main.nix")
	writeFile(t, importer, "import ./missing.nix")

	runGit(t, root, "init")
	runGit(t, root, "add", "flake.nix", "main.nix")

	initWorkspace(t, handler, root)
	importerURI := mustURI(t, importer)
	openDocument(t, handler, importerURI, "import ./missing.nix")
	// The missing-import diagnostic is present, but it is not fixable by git add.
	waitForDiagnostics(t, handler, importerURI, 1)

	if actions := requestCodeActions(t, handler, importerURI, 0, 0, 0, 100, nil); actions != nil {
		t.Fatalf("actions for missing import = %+v, want null", actions)
	}
}

func TestHandlerExecuteCommandUnknownCommandErrors(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	_, err := handler.Handle(context.Background(), "workspace/executeCommand", mustJSON(t, map[string]any{
		"command":   "nix-lsp.somethingElse",
		"arguments": []any{"x"},
	}))
	assertInvalidParams(t, err)
}

func TestHandlerExecuteCommandBadArgumentsError(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	cases := []struct {
		name string
		args []any
	}{
		{"no args", []any{}},
		{"two args", []any{"a", "b"}},
		{"non-string", []any{123}},
		{"empty string", []any{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := handler.Handle(context.Background(), "workspace/executeCommand", mustJSON(t, map[string]any{
				"command":   "nix-lsp.gitAdd",
				"arguments": tc.args,
			}))
			assertInvalidParams(t, err)
		})
	}
}

func TestHandlerExecuteCommandPathGuardsError(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	writeFile(t, filepath.Join(root, "main.nix"), "{}")
	runGit(t, root, "init")
	runGit(t, root, "add", "flake.nix", "main.nix")
	initWorkspace(t, handler, root)

	// A .nix path outside the workspace root is rejected.
	outside := filepath.Join(t.TempDir(), "other.nix")
	_, err := handler.Handle(context.Background(), "workspace/executeCommand", mustJSON(t, map[string]any{
		"command":   "nix-lsp.gitAdd",
		"arguments": []any{outside},
	}))
	assertInvalidParams(t, err)

	// A non-.nix path inside the root is rejected.
	notNix := filepath.Join(root, "notes.txt")
	_, err = handler.Handle(context.Background(), "workspace/executeCommand", mustJSON(t, map[string]any{
		"command":   "nix-lsp.gitAdd",
		"arguments": []any{notNix},
	}))
	assertInvalidParams(t, err)
}

func TestHandlerExecuteCommandGitAddStagesAndClearsWarning(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 64)}
	handler.SetNotifier(notifier)

	importerURI, libPath, root := untrackedImportFixture(t, handler, notifier)

	result, err := handler.Handle(context.Background(), "workspace/executeCommand", mustJSON(t, map[string]any{
		"command":   "nix-lsp.gitAdd",
		"arguments": []any{libPath},
	}))
	if err != nil {
		t.Fatalf("executeCommand error = %v", err)
	}
	if result != nil {
		t.Fatalf("executeCommand result = %+v, want null", result)
	}

	// git now tracks lib.nix.
	out, gitErr := gitOutput(t, root, "ls-files", "--", "lib.nix")
	if gitErr != nil {
		t.Fatalf("git ls-files error = %v", gitErr)
	}
	if !strings.Contains(out, "lib.nix") {
		t.Fatalf("git ls-files = %q, want lib.nix listed", out)
	}

	// The background refresh recomputes the open importer; its warning clears.
	waitForPublish(t, notifier, importerURI, 0)
}

func assertInvalidParams(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want -32602 ResponseError")
	}
	var rpcErr *lsp.ResponseError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error = %v (%T), want *lsp.ResponseError", err, err)
	}
	if rpcErr.Code != -32602 {
		t.Fatalf("error code = %d, want -32602", rpcErr.Code)
	}
}

func gitOutput(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
