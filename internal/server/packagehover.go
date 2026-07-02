package server

import (
	"context"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// packageHover answers a package-version hover for a nixpkgs attribute named in
// any workspace .nix file, in two positions: a `pkgs.<attr>` select, and a bare
// identifier supplied by an enclosing `with pkgs;` scope. It resolves the
// attribute under pos, looks it up in the loaded channel-packages index, and
// renders the package's name, version, and description as markdown, plus a
// provenance line when the dataset's channel is known. It returns nil when the
// index is not loaded, the position names no nixpkgs attribute, or the attr is
// not a known package, so the hover degrades to null.
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
		// Not a `pkgs.<attr>` select; try a bare identifier under `with pkgs;`. That
		// resolution needs the scope model so a locally-bound name or builtin (which
		// belongs to binding-value hover) is never mistaken for a nixpkgs attribute.
		file, ferr := facts.Scopes(ctx, h.memo, fileID)
		if ferr != nil || file == nil {
			return nil
		}
		attr, r, ok = scopes.WithPkgsAttrAt(file, tree, pos)
		if !ok {
			return nil
		}
	}
	doc, ok := index.Lookup(attr)
	if !ok {
		return nil
	}
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: h.packageHoverMarkdown(doc)},
		Range:    toProtocolRange(r),
	}
}

// packageHoverMarkdown renders a package Doc plus, when the dataset's channel is
// known (auto mode only), a provenance line that keeps channel data from being
// read as evaluated truth. Explicit-path and fixture loads record no channel and
// so append nothing.
func (h *Handler) packageHoverMarkdown(doc *packages.Doc) string {
	value := doc.Markdown()
	if channel := h.packagesChannelString(); channel != "" {
		value += "\n\n*" + channel + " channel data — an overlay may change the actual version*"
	}
	return value
}
