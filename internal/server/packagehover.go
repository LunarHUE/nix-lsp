package server

import (
	"context"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// packageHover answers a package-version hover for a `pkgs.<attr>` select in any
// workspace .nix file. It resolves the static attribute path under pos from the
// parsed tree, looks it up in the loaded channel-packages index, and renders the
// package's name, version, and description as markdown. It returns nil when the
// index is not loaded, the position is not on a `pkgs` select, or the attr is not
// a known package, so the hover degrades to null.
func (h *Handler) packageHover(ctx context.Context, uri string, pos syntax.Position) *Hover {
	index := h.packagesSnapshot()
	if index == nil {
		return nil
	}
	fileID, ok := h.optionFileInputForURI(uri)
	if !ok {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}
	attr, r, ok := scopes.PkgPathAt(tree, pos)
	if !ok {
		return nil
	}
	doc, ok := index.Lookup(attr)
	if !ok {
		return nil
	}
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: doc.Markdown()},
		Range:    toProtocolRange(r),
	}
}
