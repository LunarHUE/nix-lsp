package server

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// completionItemKindVariable is the LSP CompletionItemKind for a variable, used
// for every flake input-name completion.
const completionItemKindVariable = 6

// CompletionItem is one LSP completion candidate.
type CompletionItem struct {
	Label  string `json:"label"`
	Kind   int    `json:"kind,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// completion answers textDocument/completion. It offers declared input names
// only in the workspace root flake.nix, and only inside a follows target string
// or the outputs formals; every other file or position returns null.
func (h *Handler) completion(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded textDocumentPositionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	fileID, ok := h.rootFlakeInputForURI(decoded.TextDocument.URI)
	if !ok {
		return nil, nil
	}
	model, err := facts.FlakeModel(ctx, h.memo, fileID)
	if err != nil || model == nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}
	if items := flakeCompletionAt(model, pos); len(items) > 0 {
		return items, nil
	}
	return nil, nil
}

// flakeCompletionAt returns the input-name completions for pos, or nil when pos
// sits in neither a follows target string nor the outputs formals.
func flakeCompletionAt(file *flake.File, pos syntax.Position) []CompletionItem {
	if file == nil {
		return nil
	}
	if owner, ok := followsOwnerAt(file, pos); ok {
		return sortItems(inputItemsExcluding(file, owner))
	}
	out := file.Outputs
	if out != nil && out.HasFormals && rangeContainsPosition(out.FormalsRange, pos) {
		return sortItems(formalsItems(file))
	}
	return nil
}

// followsOwnerAt returns the name of the input whose follows-target string
// strictly contains pos. The target ranges span the string literal including its
// quotes, so a strict check keeps a cursor resting on a quote out of context.
func followsOwnerAt(file *flake.File, pos syntax.Position) (string, bool) {
	for _, in := range file.Inputs {
		if in.HasTopFollows && rangeContainsPositionStrict(in.TopFollowsRange, pos) {
			return in.Name, true
		}
		for _, edge := range in.Follows {
			if rangeContainsPositionStrict(edge.TargetRange, pos) {
				return in.Name, true
			}
		}
	}
	return "", false
}

// inputItemsExcluding offers every declared input except exclude (the input
// whose own follows override is being completed).
func inputItemsExcluding(file *flake.File, exclude string) []CompletionItem {
	items := make([]CompletionItem, 0, len(file.Inputs))
	for _, in := range file.Inputs {
		if in.Name == exclude {
			continue
		}
		items = append(items, inputCompletionItem(in))
	}
	return items
}

// formalsItems offers every declared input not already destructured in the
// outputs formals, plus `self` when it too is absent.
func formalsItems(file *flake.File) []CompletionItem {
	formals := file.Outputs.Formals
	items := make([]CompletionItem, 0, len(file.Inputs)+1)
	for _, in := range file.Inputs {
		if _, present := formals[in.Name]; present {
			continue
		}
		items = append(items, inputCompletionItem(in))
	}
	if _, present := formals["self"]; !present {
		items = append(items, CompletionItem{Label: "self", Kind: completionItemKindVariable, Detail: "flake self reference"})
	}
	return items
}

func inputCompletionItem(in *flake.Input) CompletionItem {
	item := CompletionItem{Label: in.Name, Kind: completionItemKindVariable}
	if in.HasURL {
		item.Detail = in.URL
	}
	return item
}

func sortItems(items []CompletionItem) []CompletionItem {
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return items
}

// rangeContainsPositionStrict reports whether pos lies strictly inside r, past
// both endpoints.
func rangeContainsPositionStrict(r syntax.Range, pos syntax.Position) bool {
	return positionLess(r.Start, pos) && positionLess(pos, r.End)
}
