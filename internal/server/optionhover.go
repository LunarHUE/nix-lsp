package server

import (
	"context"
	"path/filepath"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

// optionHover answers a NixOS option-documentation hover for any workspace .nix
// file. It resolves the static option attribute path under pos from the parsed
// tree, looks it up in the loaded options index (falling back to the nearest
// documented prefix, so hovering a wildcard instance segment like
// systemd.services.demo-web shows the attrsOf doc), and renders the doc as
// markdown headed by the concrete matched path. It returns nil when the index is
// not loaded, the position is not on a static option path, or no prefix of the
// path is a known option, so the hover degrades to null.
func (h *Handler) optionHover(ctx context.Context, uri string, pos syntax.Position) *Hover {
	index := h.optionsSnapshot()
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
	path, r, ok := scopes.OptionPathAt(tree, pos)
	if !ok {
		return nil
	}
	doc, matched, ok := index.LookupNearest(path)
	if !ok {
		return nil
	}
	// The hover range stays the hovered segment even when a shorter prefix
	// matched; the header names the matched path so it never overclaims.
	return &Hover{
		Contents: MarkupContent{Kind: "markdown", Value: doc.MarkdownForChannel(matched, h.optionsChannelString())},
		Range:    toProtocolRange(r),
	}
}

// optionFileInputForURI resolves uri to a readable .nix file, registers its file
// input on the memo engine so the shared ParseTree query can serve it, and
// returns the fileID. It returns ok=false for a file that cannot be read or is
// not a .nix file.
func (h *Handler) optionFileInputForURI(uri string) (string, bool) {
	path, err := vfs.URIToPath(uri)
	if err != nil {
		return "", false
	}
	if filepath.Ext(path) != ".nix" {
		return "", false
	}
	file, err := h.vfs.Snapshot().ReadFile(path)
	if err != nil {
		return "", false
	}
	return facts.FileInputFor(h.memo, file.Path, file.Hash, file.Content), true
}
