package facts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestRegisteredQueriesProduceDiagnosticsAndDependencies(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	content := []byte("import ./missing.nix")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}

	deps := keySet(engine.Dependencies(FileDiagnosticsKey(id)))
	if !deps[ParseTreeKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on ParseTree: %v", deps)
	}
	if !deps[ImportEdgesKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on ImportEdges: %v", deps)
	}
	if !deps[WorkspaceKey()] {
		t.Fatalf("FileDiagnostics did not depend on Workspace: %v", deps)
	}

	importDeps := keySet(engine.Dependencies(ImportEdgesKey(id)))
	if !importDeps[ParseTreeKey(id)] {
		t.Fatalf("ImportEdges did not depend on ParseTree: %v", importDeps)
	}
	if !importDeps[FileInputKey(id)] {
		t.Fatalf("ImportEdges did not depend on FileInput: %v", importDeps)
	}
}

// TestFileDiagnosticsDependsOnScopesAndReportsUnused verifies the scope-based
// binding diagnostics reach the diagnostics query through the memo path and that
// FileDiagnostics records its dependency on the Scopes query.
func TestFileDiagnosticsDependsOnScopesAndReportsUnused(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "let x = 1; in 2")
	content := []byte("let x = 1; in 2")
	id := FileID(source, vfs.ContentHash(content))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: content})

	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want 1", diagnostics)
	}
	if diagnostics[0].Message != `unused binding "x"` {
		t.Fatalf("message = %q, want unused binding", diagnostics[0].Message)
	}
	if diagnostics[0].Severity != syntax.SeverityWarning {
		t.Fatalf("severity = %v, want warning", diagnostics[0].Severity)
	}

	deps := keySet(engine.Dependencies(FileDiagnosticsKey(id)))
	if !deps[ScopesKey(id)] {
		t.Fatalf("FileDiagnostics did not depend on Scopes: %v", deps)
	}

	scopeDeps := keySet(engine.Dependencies(ScopesKey(id)))
	if !scopeDeps[ParseTreeKey(id)] {
		t.Fatalf("Scopes did not depend on ParseTree: %v", scopeDeps)
	}
}

// TestIdenticalContentDistinctPathsDoNotCollide guards the composite (path,
// hash) key design: two files with byte-identical content but different
// directories must resolve their relative imports independently, not share one
// memo entry whose stored path flip-flops with analysis order.
func TestIdenticalContentDistinctPathsDoNotCollide(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()

	// Same content in two sibling directories. dirA has the import target on
	// disk; dirB does not. With hash-only keys these would collide.
	content := []byte("import ./target.nix")
	hash := vfs.ContentHash(content)

	dirA := filepath.Join(root, "a")
	sourceA := writeFile(t, filepath.Join(dirA, "default.nix"), string(content))
	writeFile(t, filepath.Join(dirA, "target.nix"), "{}")

	dirB := filepath.Join(root, "b")
	sourceB := writeFile(t, filepath.Join(dirB, "default.nix"), string(content))

	idA := FileID(sourceA, hash)
	idB := FileID(sourceB, hash)
	if idA == idB {
		t.Fatalf("FileID collided for distinct paths: %q", idA)
	}

	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, idA, FileInput{Path: sourceA, Content: content})
	SetFileInput(engine, idB, FileInput{Path: sourceB, Content: content})

	diagA, err := FileDiagnostics(context.Background(), engine, idA)
	if err != nil {
		t.Fatalf("FileDiagnostics A error = %v", err)
	}
	diagB, err := FileDiagnostics(context.Background(), engine, idB)
	if err != nil {
		t.Fatalf("FileDiagnostics B error = %v", err)
	}

	// a/target.nix exists -> no missing-import diagnostic.
	if len(diagA) != 0 {
		t.Fatalf("dir A diagnostics = %+v, want none (target exists)", diagA)
	}
	// b/target.nix does not exist -> exactly one missing-import diagnostic.
	if len(diagB) != 1 || diagB[0].Message != "missing import target ./target.nix" {
		t.Fatalf("dir B diagnostics = %+v, want one missing-import", diagB)
	}

	// Re-evaluating A must not have been disturbed by B's input.
	diagA2, err := FileDiagnostics(context.Background(), engine, idA)
	if err != nil {
		t.Fatalf("re-eval A error = %v", err)
	}
	if len(diagA2) != 0 {
		t.Fatalf("dir A diagnostics after B = %+v, want none", diagA2)
	}
}

func TestFileInputInvalidationRecomputesDiagnostics(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	first := []byte("import ./missing.nix")
	second := []byte("import ./still-missing.nix")
	// The file identity is stable across the edit here (same path); tests that
	// mimic the handler recompute an ID per content hash. Keep this test focused
	// on invalidation, so hold the ID fixed and swap only the input value.
	id := FileID(source, vfs.ContentHash(first))
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, id, FileInput{Path: source, Content: first})
	if _, err := FileDiagnostics(context.Background(), engine, id); err != nil {
		t.Fatalf("first FileDiagnostics error = %v", err)
	}

	SetFileInput(engine, id, FileInput{Path: source, Content: second})
	diagnostics, err := FileDiagnostics(context.Background(), engine, id)
	if err != nil {
		t.Fatalf("second FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if diagnostics[0].Message != "missing import target ./still-missing.nix" {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}

	stats := engine.Stats()
	if stats.Recomputes[ParseTreeKey(id)] != 2 {
		t.Fatalf("ParseTree recomputes = %d, want 2", stats.Recomputes[ParseTreeKey(id)])
	}
	if stats.Recomputes[FileDiagnosticsKey(id)] != 2 {
		t.Fatalf("FileDiagnostics recomputes = %d, want 2", stats.Recomputes[FileDiagnosticsKey(id)])
	}
}

func NewEngineForTest() *memo.Engine {
	engine := memo.New()
	Register(engine)
	return engine
}

func writeFile(t *testing.T, path string, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	return path
}

func normalize(t *testing.T, path string) string {
	t.Helper()
	normalized, err := vfs.NormalizePath(path)
	if err != nil {
		t.Fatalf("NormalizePath error = %v", err)
	}
	return normalized
}

func keySet(keys []memo.Key) map[memo.Key]bool {
	set := make(map[memo.Key]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}
