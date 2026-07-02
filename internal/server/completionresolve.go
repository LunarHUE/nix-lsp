package server

import (
	"encoding/json"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
)

// completionResolve answers completionItem/resolve. The completion list ships
// documented items (options, packages, well-known helpers) without their
// markdown to stay lean; the client echoes the full item — including the opaque
// Data payload — back here when one is selected, and this fills the deferred
// Documentation. It is a purely cosmetic enrichment, so it never errors: a
// missing Data, an unloaded index, or an unknown entry all round-trip the item
// unchanged.
func (h *Handler) completionResolve(params json.RawMessage) (any, error) {
	var item CompletionItem
	if err := json.Unmarshal(params, &item); err != nil {
		return nil, nil
	}
	if item.Data == nil {
		return item, nil
	}
	switch item.Data.Source {
	case "option":
		h.resolveOptionDoc(&item)
	case "package":
		h.resolvePackageDoc(&item)
	case "wellknown":
		h.resolveWellknownDoc(&item)
	}
	return item, nil
}

// resolveOptionDoc fills an option item's markdown from the options dataset,
// rendering the same channel-linked declarations as option hover via the nearest
// documented prefix of the concrete path. It leaves the item untouched when the
// index is unloaded, the payload carries no path, or no prefix resolves.
func (h *Handler) resolveOptionDoc(item *CompletionItem) {
	index := h.optionsSnapshot()
	if index == nil || len(item.Data.Path) == 0 {
		return
	}
	doc, matched, ok := index.LookupNearest(item.Data.Path)
	if !ok {
		return
	}
	item.Documentation = &MarkupContent{Kind: "markdown", Value: doc.MarkdownForChannel(matched, h.optionsChannelString())}
}

// resolvePackageDoc fills a package item's markdown from the packages dataset,
// appending the channel provenance line through the same packageHoverMarkdown
// helper package hover uses (the item's source is the dataset, so fromDataset is
// true). It leaves the item untouched when the index is unloaded, the payload
// carries no attr, or the attr is not a known package.
func (h *Handler) resolvePackageDoc(item *CompletionItem) {
	index := h.packagesSnapshot()
	if index == nil || item.Data.Attr == "" {
		return
	}
	doc, ok := index.Lookup(item.Data.Attr)
	if !ok {
		return
	}
	item.Documentation = &MarkupContent{Kind: "markdown", Value: h.packageHoverMarkdown(doc, true)}
}

// resolveWellknownDoc fills a well-known helper item's markdown from the curated
// table. These are not channel data, so no provenance line is appended. It
// leaves the item untouched when the payload carries no attr or the attr is not
// a known helper.
func (h *Handler) resolveWellknownDoc(item *CompletionItem) {
	if item.Data.Attr == "" {
		return
	}
	doc, ok := packages.Wellknown(item.Data.Attr)
	if !ok {
		return
	}
	item.Documentation = &MarkupContent{Kind: "markdown", Value: doc.Markdown()}
}
