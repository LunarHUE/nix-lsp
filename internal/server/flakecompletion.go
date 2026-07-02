package server

import (
	"context"
	"encoding/json"
	"sort"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// LSP CompletionItemKind values used across the completion handlers.
const (
	// completionItemKindFunction marks a well-known nixpkgs helper (mkShell, ...).
	completionItemKindFunction = 3
	// completionItemKindField marks a leaf option or a full package attribute.
	completionItemKindField = 5
	// completionItemKindVariable is used for every flake input-name completion and
	// for lexically visible local names.
	completionItemKindVariable = 6
	// completionItemKindModule marks an option group or an intermediate package
	// attribute segment (one that has deeper attributes under it).
	completionItemKindModule = 9
)

// CompletionItem is one LSP completion candidate. Flake input-name completions
// set only Label/Kind/Detail; the dataset- and scope-aware completions also set a
// TextEdit (so a partially typed segment is replaced rather than appended),
// optional Documentation, and SortText.
type CompletionItem struct {
	Label         string         `json:"label"`
	Kind          int            `json:"kind,omitempty"`
	Detail        string         `json:"detail,omitempty"`
	Documentation *MarkupContent `json:"documentation,omitempty"`
	TextEdit      *TextEditItem  `json:"textEdit,omitempty"`
	SortText      string         `json:"sortText,omitempty"`
}

// TextEditItem is an LSP TextEdit: the range to replace and the text to insert.
type TextEditItem struct {
	Range   protocolRange `json:"range"`
	NewText string        `json:"newText"`
}

// CompletionList is the LSP CompletionList result. The dataset-aware handlers
// return it (LSP accepts either a bare item array or a list); flake completions
// keep returning the bare []CompletionItem for exact behavioral parity.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// completion answers textDocument/completion. In the workspace root flake.nix it
// first offers declared input names inside a follows target string or the outputs
// formals, exactly as before. When that does not apply, any workspace .nix file
// falls through to dataset- and scope-aware contextual completion (option paths,
// nixpkgs attributes, with-pkgs names, and lexically visible local names). Every
// position that matches nothing returns null.
func (h *Handler) completion(ctx context.Context, params json.RawMessage) (any, error) {
	var decoded textDocumentPositionParams
	if err := json.Unmarshal(params, &decoded); err != nil {
		return nil, nil
	}
	pos := syntax.Position{Line: decoded.Position.Line, Character: decoded.Position.Character}

	// Root flake.nix flake completions win when they apply; their behavior and
	// priority are unchanged, so a follows/formals position never reaches the
	// contextual path below.
	if fileID, ok := h.rootFlakeInputForURI(decoded.TextDocument.URI); ok {
		if model, err := facts.FlakeModel(ctx, h.memo, fileID); err == nil && model != nil {
			if items := flakeCompletionAt(model, pos); len(items) > 0 {
				return items, nil
			}
		}
	}

	if list := h.contextCompletion(ctx, decoded.TextDocument.URI, pos); list != nil {
		return list, nil
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
