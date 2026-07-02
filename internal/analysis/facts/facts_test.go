package facts

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/memo"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestRegisteredQueriesProduceDiagnosticsAndDependencies(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	content := []byte("import ./missing.nix")
	hash := vfs.ContentHash(content)
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, hash, FileInput{Path: source, Content: content})

	diagnostics, err := FileDiagnostics(context.Background(), engine, hash)
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}

	deps := keySet(engine.Dependencies(FileDiagnosticsKey(hash)))
	if !deps[ParseTreeKey(hash)] {
		t.Fatalf("FileDiagnostics did not depend on ParseTree: %v", deps)
	}
	if !deps[ImportEdgesKey(hash)] {
		t.Fatalf("FileDiagnostics did not depend on ImportEdges: %v", deps)
	}
	if !deps[WorkspaceKey()] {
		t.Fatalf("FileDiagnostics did not depend on Workspace: %v", deps)
	}

	importDeps := keySet(engine.Dependencies(ImportEdgesKey(hash)))
	if !importDeps[ParseTreeKey(hash)] {
		t.Fatalf("ImportEdges did not depend on ParseTree: %v", importDeps)
	}
	if !importDeps[FileInputKey(hash)] {
		t.Fatalf("ImportEdges did not depend on FileInput: %v", importDeps)
	}
}

func TestFileInputInvalidationRecomputesDiagnostics(t *testing.T) {
	engine := NewEngineForTest()
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	first := []byte("import ./missing.nix")
	second := []byte("import ./still-missing.nix")
	hash := vfs.ContentHash(first)
	SetWorkspace(engine, project.Workspace{Root: normalize(t, root)})
	SetFileInput(engine, hash, FileInput{Path: source, Content: first})
	if _, err := FileDiagnostics(context.Background(), engine, hash); err != nil {
		t.Fatalf("first FileDiagnostics error = %v", err)
	}

	SetFileInput(engine, hash, FileInput{Path: source, Content: second})
	diagnostics, err := FileDiagnostics(context.Background(), engine, hash)
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
	if stats.Recomputes[ParseTreeKey(hash)] != 2 {
		t.Fatalf("ParseTree recomputes = %d, want 2", stats.Recomputes[ParseTreeKey(hash)])
	}
	if stats.Recomputes[FileDiagnosticsKey(hash)] != 2 {
		t.Fatalf("FileDiagnostics recomputes = %d, want 2", stats.Recomputes[FileDiagnosticsKey(hash)])
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
