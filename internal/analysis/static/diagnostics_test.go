package static

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	importedges "github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
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
	if diagnostics[0].Code != CodeMissingImport {
		t.Fatalf("code = %q, want %q", diagnostics[0].Code, CodeMissingImport)
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
	if diagnostics[0].Code != CodeUntrackedImport {
		t.Fatalf("code = %q, want %q", diagnostics[0].Code, CodeUntrackedImport)
	}
}

// TestBindingDiagnosticCodes asserts each binding diagnostic kind carries its
// stable machine-readable code, so the code-action handler and clients can key
// on them.
func TestBindingDiagnosticCodes(t *testing.T) {
	tests := []struct {
		name string
		src  string
		code string
	}{
		{"unused", "let x = 1; in 2", CodeUnusedBinding},
		{"duplicate", "{ a = 1; a = 2; }", CodeDuplicateBinding},
		{"bad inherit", "{ inherit missing; }", CodeBadInherit},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := bindingDiagnostics(t, tc.src)
			if len(got) == 0 {
				t.Fatalf("diagnostics = none, want one with code %q", tc.code)
			}
			if got[0].Code != tc.code {
				t.Fatalf("code = %q, want %q", got[0].Code, tc.code)
			}
		})
	}
}

// TestSyntaxDiagnosticCodes asserts ERROR and MISSING nodes are coded.
func TestSyntaxDiagnosticCodes(t *testing.T) {
	// An unterminated attrset yields a missing-syntax diagnostic (missing `}`).
	diagnostics := parse(t, "{").Diagnostics()
	if len(diagnostics) == 0 {
		t.Fatal("diagnostics = none, want a syntax diagnostic")
	}
	for _, d := range diagnostics {
		if d.Code != "syntax-error" && d.Code != "missing-syntax" {
			t.Fatalf("code = %q, want syntax-error or missing-syntax", d.Code)
		}
	}
}

// TestShouldWarnUntracked covers the exported predicate the code-action handler
// reuses to decide where the quick fix applies.
func TestShouldWarnUntracked(t *testing.T) {
	root := normalize(t, t.TempDir())
	target := filepath.Join(root, "lib.nix")
	base := project.Workspace{Root: root, HasFlake: true, HasGit: true}

	untracked := importedges.Edge{TargetPath: target, Exists: true, GitTracked: false}
	if !ShouldWarnUntracked(base, untracked) {
		t.Fatal("ShouldWarnUntracked(untracked) = false, want true")
	}

	tracked := untracked
	tracked.GitTracked = true
	if ShouldWarnUntracked(base, tracked) {
		t.Fatal("ShouldWarnUntracked(tracked) = true, want false")
	}

	noFlake := base
	noFlake.HasFlake = false
	if ShouldWarnUntracked(noFlake, untracked) {
		t.Fatal("ShouldWarnUntracked(no flake) = true, want false")
	}

	outside := importedges.Edge{TargetPath: normalize(t, filepath.Join(t.TempDir(), "other.nix")), Exists: true}
	if ShouldWarnUntracked(base, outside) {
		t.Fatal("ShouldWarnUntracked(outside root) = true, want false")
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
