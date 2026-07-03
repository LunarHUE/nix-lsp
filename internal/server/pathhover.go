package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/imports"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/static"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// pathHover answers a hover for a static path literal in any workspace .nix
// file. On a path expression that resolves to an existing file (or a
// directory's default.nix) it reports the resolved absolute path; it also flags
// a missing target and, in a flake+git workspace, an existing-but-untracked
// target the same way the untracked-import diagnostic does. It returns nil when
// pos is not on a followable path literal, so the hover degrades to null.
func (h *Handler) pathHover(ctx context.Context, uri string, pos syntax.Position) *Hover {
	edges, ok := h.pathEdges(ctx, uri)
	if !ok {
		return nil
	}
	for _, edge := range edges {
		if rangeContainsPosition(edge.Range, pos) {
			return h.renderPathHover(edge)
		}
	}
	return nil
}

// pathEdges resolves every static path literal in the .nix file at uri to an
// edge, reusing the imports package's resolution and git-tracking rules. It
// returns ok=false for a non-.nix or unreadable file, or when the tree cannot
// be parsed, so callers degrade to a null result.
func (h *Handler) pathEdges(ctx context.Context, uri string) ([]imports.Edge, bool) {
	fileID, ok := h.optionFileInputForURI(uri)
	if !ok {
		return nil, false
	}
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return nil, false
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil, false
	}
	// The git-tracking status is only needed for the untracked hover line and only
	// meaningful in a discovered workspace; outside one, nil leaves every edge
	// untracked and ShouldWarnUntracked declines anyway.
	var tracked map[string]bool
	if workspace, ok := h.Workspace(); ok {
		tracked = static.TrackedFiles(workspace)
	}
	edges, err := imports.AnalyzeAllPaths(path, tree, tracked)
	if err != nil {
		return nil, false
	}
	return edges, true
}

// renderPathHover renders the markdown hover for one resolved path edge: the
// literal as written, the resolved absolute path (noting a directory import),
// and a single status line drawn from the same untracked check the diagnostic
// applies.
func (h *Handler) renderPathHover(edge imports.Edge) *Hover {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n", edge.Literal)
	if edge.Exists && edge.ViaDefault {
		fmt.Fprintf(&b, "resolves to `%s` (directory import)\n", edge.TargetPath)
	} else {
		fmt.Fprintf(&b, "resolves to `%s`\n", edge.TargetPath)
	}
	switch {
	case !edge.Exists:
		b.WriteString("target does not exist")
	case h.pathTargetUntracked(edge):
		b.WriteString("exists but is not git-tracked — nix flakes only see tracked files")
	default:
		b.WriteString("exists")
	}
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: b.String()},
		Range:    toProtocolRange(edge.Range),
	}
}

// pathTargetUntracked reports whether edge trips the same untracked-import
// warning the diagnostic uses, so the hover status and the squiggle never
// disagree. It is a no-op outside a discovered workspace.
func (h *Handler) pathTargetUntracked(edge imports.Edge) bool {
	workspace, ok := h.Workspace()
	if !ok {
		return false
	}
	return static.ShouldWarnUntracked(workspace, edge)
}
