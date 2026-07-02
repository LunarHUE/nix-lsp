package server

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// maxFollowsSuggestions bounds the did-you-mean replacements offered for a
// dangling follows target.
const maxFollowsSuggestions = 3

// maxFollowsDistance is the largest Levenshtein distance a declared input name
// may sit from a dangling target segment to be offered as a replacement.
const maxFollowsDistance = 2

// flakeCodeActions builds the edit-based quick fixes for the workspace root
// flake.nix: remove an unused input, add it to the outputs formals, and
// did-you-mean replacements for a dangling follows target. Each action is gated
// on the exact diagnostic it fixes, so it appears only where that diagnostic is.
// Any non-root file, missing model, or diagnostics error yields no actions.
func (h *Handler) flakeCodeActions(ctx context.Context, fileID, uri string, requested syntax.Range) []CodeAction {
	if !h.isRootFlakeURI(uri) {
		return nil
	}
	model, err := facts.FlakeModel(ctx, h.memo, fileID)
	if err != nil || model == nil {
		return nil
	}
	diagnostics, err := facts.FileDiagnostics(ctx, h.memo, fileID)
	if err != nil {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}

	var actions []CodeAction
	actions = append(actions, unusedInputActions(model, diagnostics, tree.Content(), uri, requested)...)
	actions = append(actions, danglingFollowsActions(model, diagnostics, uri, requested)...)
	return actions
}

// unusedInputActions offers, for each unused-input diagnostic overlapping the
// requested range, a "Remove input" edit plus (when the outputs formals accept
// it) an "Add to outputs" edit. Both anchor the same diagnostic.
func unusedInputActions(file *flake.File, diagnostics []syntax.Diagnostic, content []byte, uri string, requested syntax.Range) []CodeAction {
	var actions []CodeAction
	for _, in := range file.Inputs {
		if !rangesOverlap(in.NameRange, requested) {
			continue
		}
		diag, ok := matchDiagnostic(diagnostics, flake.CodeUnusedInput, in.NameRange)
		if !ok {
			continue
		}
		protoDiags := toProtocolDiagnostics([]syntax.Diagnostic{diag})
		if action, ok := removeInputAction(in, content, uri, protoDiags); ok {
			actions = append(actions, action)
		}
		if action, ok := addInputToOutputsAction(file, in, uri, protoDiags); ok {
			actions = append(actions, action)
		}
	}
	return actions
}

// removeInputAction deletes every binding that contributed to in. Each deletion
// covers the whole binding (including its trailing semicolon), expanded to full
// lines when the binding sits alone on its line(s). Edits are sorted descending
// by start position so sequential application never invalidates an earlier one.
func removeInputAction(in *flake.Input, content []byte, uri string, diags []protocolDiagnostic) (CodeAction, bool) {
	if len(in.BindingRanges) == 0 {
		return CodeAction{}, false
	}
	edits := make([]TextEdit, 0, len(in.BindingRanges))
	for _, r := range in.BindingRanges {
		edits = append(edits, TextEdit{Range: toProtocolRange(expandDeletionRange(r, content)), NewText: ""})
	}
	sort.SliceStable(edits, func(i, j int) bool {
		return protocolRangeLess(edits[j].Range, edits[i].Range)
	})
	return CodeAction{
		Title:       fmt.Sprintf("Remove input '%s'", in.Name),
		Kind:        "quickfix",
		Diagnostics: diags,
		Edit:        &WorkspaceEdit{Changes: map[string][]TextEdit{uri: edits}},
	}, true
}

// addInputToOutputsAction inserts `, <name>` after the last outputs formal so an
// unused input becomes consumed. It applies only to a strict formals list with a
// known insert anchor, and never when the input is already a formal.
func addInputToOutputsAction(file *flake.File, in *flake.Input, uri string, diags []protocolDiagnostic) (CodeAction, bool) {
	out := file.Outputs
	if out == nil || !out.HasFormals || !out.HasInsertAnchor {
		return CodeAction{}, false
	}
	if _, present := out.Formals[in.Name]; present {
		return CodeAction{}, false
	}
	anchor := toProtocolPosition(out.InsertAnchor)
	edit := TextEdit{Range: protocolRange{Start: anchor, End: anchor}, NewText: ", " + in.Name}
	return CodeAction{
		Title:       fmt.Sprintf("Add '%s' to outputs", in.Name),
		Kind:        "quickfix",
		Diagnostics: diags,
		Edit:        &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
	}, true
}

// danglingFollowsActions offers up to three did-you-mean replacements for each
// dangling follows target overlapping the requested range. Each edit rewrites
// the whole target string literal, preserving any nested path after the first
// slash.
func danglingFollowsActions(file *flake.File, diagnostics []syntax.Diagnostic, uri string, requested syntax.Range) []CodeAction {
	names := declaredInputNames(file)
	var actions []CodeAction
	consider := func(target string, r syntax.Range) {
		if !rangesOverlap(r, requested) {
			return
		}
		diag, ok := matchDiagnostic(diagnostics, flake.CodeDanglingFollows, r)
		if !ok {
			return
		}
		protoDiags := toProtocolDiagnostics([]syntax.Diagnostic{diag})
		seg, rest := splitFollowsTarget(target)
		for _, name := range suggestFollowsNames(seg, names) {
			edit := TextEdit{Range: toProtocolRange(r), NewText: "\"" + name + rest + "\""}
			actions = append(actions, CodeAction{
				Title:       fmt.Sprintf("Change follows target to '%s'", name),
				Kind:        "quickfix",
				Diagnostics: protoDiags,
				Edit:        &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
			})
		}
	}
	for _, in := range file.Inputs {
		if in.HasTopFollows {
			consider(in.TopFollows, in.TopFollowsRange)
		}
		for _, edge := range in.Follows {
			consider(edge.Target, edge.TargetRange)
		}
	}
	return actions
}

// matchDiagnostic returns the diagnostic with the given code anchored exactly on
// r, mirroring how gitAddCodeAction pairs an action with its diagnostic.
func matchDiagnostic(diagnostics []syntax.Diagnostic, code string, r syntax.Range) (syntax.Diagnostic, bool) {
	for _, d := range diagnostics {
		if d.Code == code && d.Range == r {
			return d, true
		}
	}
	return syntax.Diagnostic{}, false
}

// expandDeletionRange grows r to cover its full line(s) plus the trailing
// newline when only whitespace surrounds the binding on those lines; otherwise
// it returns r unchanged so an inline binding is deleted exactly.
func expandDeletionRange(r syntax.Range, content []byte) syntax.Range {
	lines := strings.Split(string(content), "\n")
	if r.Start.Line < 0 || r.Start.Line >= len(lines) || r.End.Line < 0 || r.End.Line >= len(lines) {
		return r
	}
	first := []rune(lines[r.Start.Line])
	last := []rune(lines[r.End.Line])
	if r.Start.Character > len(first) || !allWhitespace(first[:r.Start.Character]) {
		return r
	}
	if r.End.Character > len(last) || !allWhitespace(last[r.End.Character:]) {
		return r
	}
	return syntax.Range{
		Start: syntax.Position{Line: r.Start.Line, Character: 0},
		End:   syntax.Position{Line: r.End.Line + 1, Character: 0},
	}
}

// allWhitespace reports whether every rune in rs is a space or tab.
func allWhitespace(rs []rune) bool {
	for _, ru := range rs {
		if ru != ' ' && ru != '\t' {
			return false
		}
	}
	return true
}

// declaredInputNames returns the names of every declared input.
func declaredInputNames(file *flake.File) []string {
	names := make([]string, 0, len(file.Inputs))
	for _, in := range file.Inputs {
		names = append(names, in.Name)
	}
	return names
}

// splitFollowsTarget splits a follows target into its first slash-segment and
// the remainder starting at the first slash (empty when there is none).
func splitFollowsTarget(target string) (seg, rest string) {
	if i := strings.IndexByte(target, '/'); i >= 0 {
		return target[:i], target[i:]
	}
	return target, ""
}

// suggestFollowsNames returns up to maxFollowsSuggestions declared input names
// within maxFollowsDistance edits of target, best (smallest distance) first,
// ties broken by name.
func suggestFollowsNames(target string, names []string) []string {
	type candidate struct {
		name string
		dist int
	}
	var candidates []candidate
	for _, name := range names {
		if d := levenshtein(target, name); d <= maxFollowsDistance {
			candidates = append(candidates, candidate{name: name, dist: d})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].dist != candidates[j].dist {
			return candidates[i].dist < candidates[j].dist
		}
		return candidates[i].name < candidates[j].name
	})
	out := make([]string, 0, maxFollowsSuggestions)
	for _, c := range candidates {
		out = append(out, c.name)
		if len(out) == maxFollowsSuggestions {
			break
		}
	}
	return out
}

// levenshtein returns the edit distance between a and b using a rolling row.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		curr := make([]int, len(br)+1)
		curr[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			curr[j] = minInt(minInt(prev[j]+1, curr[j-1]+1), prev[j-1]+cost)
		}
		prev = curr
	}
	return prev[len(br)]
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
