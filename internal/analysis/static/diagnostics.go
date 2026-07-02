// Package static produces conservative static diagnostics from indexed facts.
package static

import (
	"fmt"
	"path/filepath"
	"strings"

	importedges "github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/project"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// FileDiagnostics returns static diagnostics for one file.
func FileDiagnostics(workspace project.Workspace, sourcePath string, content []byte) ([]syntax.Diagnostic, error) {
	tracked := trackedFiles(workspace)
	edges, err := importedges.Analyze(sourcePath, content, tracked)
	if err != nil {
		return nil, err
	}

	diagnostics := make([]syntax.Diagnostic, 0)
	for _, edge := range edges {
		if !edge.Exists {
			diagnostics = append(diagnostics, syntax.Diagnostic{
				Message: fmt.Sprintf("missing import target %s", edge.Literal),
				Range:   edge.Range,
			})
			continue
		}
		if shouldWarnUntracked(workspace, edge) {
			diagnostics = append(diagnostics, syntax.Diagnostic{
				Message: fmt.Sprintf("import target %s exists but is not git-tracked; Nix flakes only see git-tracked files, so run git add", edge.Literal),
				Range:   edge.Range,
			})
		}
	}
	return diagnostics, nil
}

// WorkspaceDiagnostics returns static diagnostics for every readable workspace
// file, keyed by file URI.
func WorkspaceDiagnostics(workspace project.Workspace, snapshot *vfs.Snapshot) map[string][]syntax.Diagnostic {
	diagnostics := make(map[string][]syntax.Diagnostic)
	if snapshot == nil {
		return diagnostics
	}

	for _, file := range workspace.Files {
		read, err := snapshot.ReadFile(file.Path)
		if err != nil {
			continue
		}
		fileDiagnostics, err := FileDiagnostics(workspace, file.Path, read.Content)
		if err != nil || len(fileDiagnostics) == 0 {
			continue
		}
		diagnostics[file.URI] = fileDiagnostics
	}
	return diagnostics
}

func shouldWarnUntracked(workspace project.Workspace, edge importedges.Edge) bool {
	if !workspace.HasFlake || !workspace.HasGit || edge.GitTracked {
		return false
	}
	if edge.TargetPath == "" || filepath.Ext(edge.TargetPath) != ".nix" {
		return false
	}
	return withinRoot(workspace.Root, edge.TargetPath)
}

func trackedFiles(workspace project.Workspace) map[string]bool {
	tracked := make(map[string]bool, len(workspace.Files))
	for _, file := range workspace.Files {
		if file.GitTracked {
			tracked[file.Path] = true
		}
	}
	return tracked
}

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
