package server

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWatchedFilesRefreshDoesNotClobberNewerBuffer is the regression guard for
// the stale-semicolon bug: a watched-files refresh (VS Code fires one for
// .git/index after every autosave in a git repo) pins a VFS snapshot, chews
// through changed files, and then republishes open files. An edit that lands
// while the refresh is chewing must win — the refresh must never publish the
// pinned (older) buffer content over the newer one. Before the fix the refresh
// took its generations after the slow work, so its stale-content publish
// carried the newest generation and stuck until restart.
func TestWatchedFilesRefreshDoesNotClobberNewerBuffer(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	dir := t.TempDir()

	// Enough changed-on-disk files to hold the refresh busy between its snapshot
	// pin and its open-file republish loop while the fix-up edit lands.
	var changed []map[string]any
	for k := range 300 {
		path := filepath.Join(dir, fmt.Sprintf("changed-%d.nix", k))
		if err := os.WriteFile(path, fmt.Appendf(nil, "{ file = %d; }\n", k), 0o644); err != nil {
			t.Fatalf("write changed file: %v", err)
		}
		changed = append(changed, map[string]any{"uri": mustURI(t, path), "type": 2})
	}

	path := filepath.Join(dir, "target.nix")
	uri := mustURI(t, path)
	broken := "{\n  value = 42\n  other = 1;\n}\n"
	clean := "{\n  value = 42;\n  other = 1;\n}\n"
	if err := os.WriteFile(path, []byte(broken), 0o644); err != nil {
		t.Fatalf("write target: %v", err)
	}

	openDocument(t, handler, uri, broken)
	if got := waitForDiagnostics(t, handler, uri, 1); len(got) == 0 {
		t.Fatal("expected a syntax diagnostic for the broken buffer")
	}

	// The autosave aftermath: watcher reports the changed files. The refresh
	// task pins its snapshot (broken buffer) and starts chewing the big files.
	if _, err := handler.Handle(context.Background(), "workspace/didChangeWatchedFiles", mustJSON(t, map[string]any{
		"changes": changed,
	})); err != nil {
		t.Fatalf("didChangeWatchedFiles error = %v", err)
	}

	// The user re-adds the semicolon while the refresh is mid-chew.
	time.Sleep(20 * time.Millisecond)
	if _, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{{"text": clean}},
	})); err != nil {
		t.Fatalf("didChange error = %v", err)
	}

	// No further edits: the published set must converge to the clean buffer and
	// STAY clean once the refresh finishes.
	if got := waitForDiagnostics(t, handler, uri, 0); len(got) != 0 {
		t.Fatalf("diagnostics after fix-up edit = %+v, want none", got)
	}
	time.Sleep(1500 * time.Millisecond)
	handler.mu.RLock()
	got := handler.diagnostics[uri]
	handler.mu.RUnlock()
	if len(got) != 0 {
		t.Fatalf("stale diagnostics resurrected by the watched-files refresh: %+v", got)
	}
}

// TestDatasetRefreshDoesNotClobberNewerBuffer covers the same hazard on the
// dataset-load republish path: refreshOpenDiagnostics pinned a snapshot on the
// loader goroutine and later recomputed open files with fresh generations
// against it, so an edit landing in between could be overwritten by stale
// content that then stuck.
func TestDatasetRefreshDoesNotClobberNewerBuffer(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	dir := t.TempDir()
	path := filepath.Join(dir, "target.nix")
	uri := mustURI(t, path)
	broken := "{\n  value = 42\n  other = 1;\n}\n"
	clean := "{\n  value = 42;\n  other = 1;\n}\n"

	openDocument(t, handler, uri, broken)
	if got := waitForDiagnostics(t, handler, uri, 1); len(got) == 0 {
		t.Fatal("expected a syntax diagnostic for the broken buffer")
	}

	// Dataset load completes (snapshot would pin broken), then the edit lands.
	handler.refreshOpenDiagnostics()
	if _, err := handler.Handle(context.Background(), "textDocument/didChange", mustJSON(t, map[string]any{
		"textDocument":   map[string]any{"uri": uri, "version": 2},
		"contentChanges": []map[string]any{{"text": clean}},
	})); err != nil {
		t.Fatalf("didChange error = %v", err)
	}

	if got := waitForDiagnostics(t, handler, uri, 0); len(got) != 0 {
		t.Fatalf("diagnostics after fix-up edit = %+v, want none", got)
	}
	time.Sleep(500 * time.Millisecond)
	handler.mu.RLock()
	got := handler.diagnostics[uri]
	handler.mu.RUnlock()
	if len(got) != 0 {
		t.Fatalf("stale diagnostics resurrected by the dataset refresh: %+v", got)
	}
}
