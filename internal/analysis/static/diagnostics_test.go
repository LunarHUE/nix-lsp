package static

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestFileDiagnosticsMissingImport(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	workspace := project.Workspace{Root: normalize(t, root)}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./missing.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if !strings.Contains(diagnostics[0].Message, "missing import target") {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
}

func TestFileDiagnosticsUntrackedFlakeImport(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	workspace := project.Workspace{
		Root:     normalize(t, root),
		HasFlake: true,
		HasGit:   true,
		Files: []project.File{
			{Path: normalize(t, source), GitTracked: true},
			{Path: normalize(t, target), GitTracked: false},
		},
	}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./module.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %d, want 1", len(diagnostics))
	}
	if !strings.Contains(diagnostics[0].Message, "not git-tracked") {
		t.Fatalf("message = %q", diagnostics[0].Message)
	}
}

func TestFileDiagnosticsNoUntrackedWarningOutsideFlakeGitWorkspace(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./module.nix")
	target := writeFile(t, filepath.Join(root, "module.nix"), "{}")
	workspace := project.Workspace{
		Root:     normalize(t, root),
		HasFlake: true,
		HasGit:   false,
		Files: []project.File{
			{Path: normalize(t, source), GitTracked: true},
			{Path: normalize(t, target), GitTracked: false},
		},
	}

	diagnostics, err := FileDiagnostics(workspace, source, parse(t, "import ./module.nix"))
	if err != nil {
		t.Fatalf("FileDiagnostics error = %v", err)
	}
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diagnostics)
	}
}

func TestWorkspaceDiagnosticsReadsSnapshot(t *testing.T) {
	root := t.TempDir()
	source := writeFile(t, filepath.Join(root, "default.nix"), "import ./missing.nix")
	uri, err := vfs.PathToURI(source)
	if err != nil {
		t.Fatalf("PathToURI error = %v", err)
	}
	workspace := project.Workspace{
		Root: normalize(t, root),
		Files: []project.File{
			{Path: normalize(t, source), URI: uri, GitTracked: true},
		},
	}

	diagnostics := WorkspaceDiagnostics(workspace, vfs.New().Snapshot())
	if got := len(diagnostics[uri]); got != 1 {
		t.Fatalf("diagnostics for uri = %d, want 1", got)
	}
}

func writeFile(t *testing.T, path, content string) string {
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

func parse(t *testing.T, content string) *syntax.Tree {
	t.Helper()
	tree, err := syntax.Parse([]byte(content))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return tree
}
