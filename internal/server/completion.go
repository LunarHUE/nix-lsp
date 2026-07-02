package server

import (
	"context"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/scopes"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

const (
	// pkgCompleteQueryLimit bounds the raw package-attr scan a single request
	// pulls from the index before dedupe; hitting it marks the list incomplete.
	pkgCompleteQueryLimit = 200
	// completionItemCap bounds how many items any single contextual completion
	// returns after dedupe; hitting it also marks the list incomplete.
	completionItemCap = 100
	// withPkgsMinPartial is the shortest bare-name fragment that offers with-pkgs
	// completions; a single character would flood the client with nixpkgs attrs.
	withPkgsMinPartial = 2
)

// contextCompletion serves the dataset- and scope-aware completions for any
// workspace .nix file: NixOS option paths, nixpkgs attributes, bare with-pkgs
// names, and lexically visible local names. It resolves the file the same way
// hover does (via the shared ParseTree/Scopes queries over the vfs snapshot, so
// it sees the same mid-edit buffer the client just changed), classifies the
// cursor, and dispatches on the completion kind. It returns nil (LSP null) for a
// file it cannot resolve, an unclassifiable position, or a missing dataset.
func (h *Handler) contextCompletion(ctx context.Context, uri string, pos syntax.Position) *CompletionList {
	fileID, ok := h.optionFileInputForURI(uri)
	if !ok {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}
	file, err := facts.Scopes(ctx, h.memo, fileID)
	if err != nil || file == nil {
		return nil
	}

	cctx, ok := scopes.CompletionContextAt(file, tree, pos)
	if !ok {
		return nil
	}
	switch cctx.Kind {
	case scopes.OptionPath:
		return h.optionPathCompletion(cctx)
	case scopes.PkgAttr:
		return h.pkgAttrCompletion(cctx)
	case scopes.WithPkgsName:
		return h.withPkgsCompletion(cctx)
	case scopes.LocalName:
		return h.localNameCompletion(file, pos, cctx)
	}
	return nil
}

// optionPathCompletion offers the completable children of the resolved option
// group, filtered by the typed fragment. A child that is itself a documented
// option is a leaf (Field kind, its type as detail, its rendered doc as
// markdown); an interior-only child is a group (Module kind). Leaves sort before
// groups so a concrete option is offered ahead of a deeper path. It returns nil
// when the options dataset is unloaded or nothing matches.
func (h *Handler) optionPathCompletion(cctx scopes.CompletionContext) *CompletionList {
	index := h.optionsSnapshot()
	if index == nil {
		return nil
	}
	edit := toProtocolRange(cctx.Replace)
	items := make([]CompletionItem, 0)
	for _, child := range index.Children(cctx.Prefix) {
		if !strings.HasPrefix(child.Name, cctx.Partial) {
			continue
		}
		item := CompletionItem{
			Label:    child.Name,
			TextEdit: &TextEditItem{Range: edit, NewText: child.Name},
		}
		if child.Doc != nil {
			// A documented node is a leaf even when it also has sub-options.
			path := append(append([]string{}, cctx.Prefix...), child.Name)
			item.Kind = completionItemKindField
			item.Detail = child.Doc.Type
			item.Documentation = &MarkupContent{Kind: "markdown", Value: child.Doc.MarkdownFor(path)}
			item.SortText = "0" + child.Name
		} else {
			item.Kind = completionItemKindModule
			item.SortText = "1" + child.Name
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	return &CompletionList{Items: items}
}

// pkgAttrCompletion offers nixpkgs attribute segments under a `pkgs.<path>`
// select. It queries the flat, dotted package index with the completed prefix and
// collapses each candidate to just its next segment, deduping so many deeper
// attrs sharing a segment yield a single group. It returns nil when the packages
// dataset is unloaded or nothing matches.
func (h *Handler) pkgAttrCompletion(cctx scopes.CompletionContext) *CompletionList {
	index := h.packagesSnapshot()
	if index == nil {
		return nil
	}
	typed := ""
	if len(cctx.Prefix) > 0 {
		typed = strings.Join(cctx.Prefix, ".") + "."
	}
	return pkgSegmentCompletion(index, typed, cctx.Partial, cctx.Replace)
}

// withPkgsCompletion offers bare nixpkgs attribute names supplied by an enclosing
// `with pkgs;`. It is the empty-prefix package completion, guarded by a minimum
// fragment length so a one-character fragment does not flood the client, then
// merged with the curated well-known helpers (mkShell, callPackage, ...) whose
// name carries the fragment. It returns nil when the dataset is unloaded, the
// fragment is too short, or nothing matches.
func (h *Handler) withPkgsCompletion(cctx scopes.CompletionContext) *CompletionList {
	index := h.packagesSnapshot()
	if index == nil {
		return nil
	}
	if len(cctx.Partial) < withPkgsMinPartial {
		return nil
	}

	list := pkgSegmentCompletion(index, "", cctx.Partial, cctx.Replace)
	edit := toProtocolRange(cctx.Replace)
	var items []CompletionItem
	incomplete := false
	if list != nil {
		items = list.Items
		incomplete = list.IsIncomplete
	}
	seen := make(map[string]bool, len(items))
	for _, item := range items {
		seen[item.Label] = true
	}
	// Well-known attrs are not derivations, so they never appear in the dataset;
	// merge those matching the fragment as Function-kind items with no version.
	for _, name := range packages.WellknownNames() {
		if !strings.HasPrefix(name, cctx.Partial) || seen[name] {
			continue
		}
		seen[name] = true
		doc, _ := packages.Wellknown(name)
		items = append(items, CompletionItem{
			Label:         name,
			Kind:          completionItemKindFunction,
			TextEdit:      &TextEditItem{Range: edit, NewText: name},
			Documentation: &MarkupContent{Kind: "markdown", Value: doc.Markdown()},
		})
	}
	if len(items) == 0 {
		return nil
	}
	return &CompletionList{IsIncomplete: incomplete, Items: items}
}

// pkgSegmentCompletion builds the shared next-segment package completion for a
// completed dotted prefix (typed, "" or ending in a dot) plus a typed fragment.
// It trims typed off each candidate attr, keeps only the segment up to the next
// dot, and dedupes: a candidate with a remaining dot is an intermediate group
// (Module kind, no detail), a full leaf is a package (Field kind, version detail,
// rendered doc). The list is incomplete when the raw scan saturated or the cap
// truncated the deduped items. It returns nil when nothing matches.
func pkgSegmentCompletion(index *packages.Index, typed, partial string, replace syntax.Range) *CompletionList {
	docs := index.Complete(typed+partial, pkgCompleteQueryLimit)
	if len(docs) == 0 {
		return nil
	}
	edit := toProtocolRange(replace)
	incomplete := len(docs) == pkgCompleteQueryLimit
	seen := make(map[string]bool)
	items := make([]CompletionItem, 0)
	for _, doc := range docs {
		rest, ok := strings.CutPrefix(doc.Attr, typed)
		if !ok || rest == "" {
			continue
		}
		seg, _, hasDot := strings.Cut(rest, ".")
		if seen[seg] {
			continue
		}
		if len(items) >= completionItemCap {
			incomplete = true
			break
		}
		seen[seg] = true
		item := CompletionItem{
			Label:    seg,
			TextEdit: &TextEditItem{Range: edit, NewText: seg},
		}
		if hasDot {
			item.Kind = completionItemKindModule
		} else {
			item.Kind = completionItemKindField
			item.Detail = doc.Version
			item.Documentation = &MarkupContent{Kind: "markdown", Value: doc.Markdown()}
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return nil
	}
	return &CompletionList{IsIncomplete: incomplete, Items: items}
}

// localNameCompletion offers the lexically visible binding names carrying the
// typed fragment, innermost scope first so a shadowing binding wins the dedupe.
// Builtins are already excluded by VisibleBindings. It returns nil when nothing
// visible matches.
func (h *Handler) localNameCompletion(file *scopes.File, pos syntax.Position, cctx scopes.CompletionContext) *CompletionList {
	edit := toProtocolRange(cctx.Replace)
	seen := make(map[string]bool)
	items := make([]CompletionItem, 0)
	for _, b := range scopes.VisibleBindings(file, pos) {
		if !strings.HasPrefix(b.Name, cctx.Partial) || seen[b.Name] {
			continue
		}
		if len(items) >= completionItemCap {
			break
		}
		seen[b.Name] = true
		items = append(items, CompletionItem{
			Label:    b.Name,
			Kind:     completionItemKindVariable,
			Detail:   bindingKindLabel(b.Kind),
			TextEdit: &TextEditItem{Range: edit, NewText: b.Name},
		})
	}
	if len(items) == 0 {
		return nil
	}
	return &CompletionList{Items: items}
}
