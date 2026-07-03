package server

import (
	"context"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/datadiag"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/facts"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/options"
	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// datadiag.go wires the dataset-backed diagnostics (unknown-option and
// unknown-package) into the server. Unlike the memoized static diagnostics these
// depend on the loaded index identity, not file content alone, so they cannot live
// in the FileDiagnostics fact: they are appended where diagnostics are assembled
// for publishing (computeFileDiagnostics), and a fresh index publish re-runs that
// path for every open document via refreshOpenDiagnostics.

// datasetDiagnostics computes the unknown-option and unknown-package diagnostics
// for the file at fileID, or nil when neither dataset is loaded or the tree cannot
// be parsed. It reads the current index snapshots, so a caller invoked after a
// dataset load sees the new warnings.
func (h *Handler) datasetDiagnostics(ctx context.Context, fileID string) []syntax.Diagnostic {
	optionsIndex := h.optionsSnapshot()
	packagesIndex := h.packagesSnapshot()
	if optionsIndex == nil && packagesIndex == nil {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}

	var out []syntax.Diagnostic
	out = appendDataset(out, datadiag.OptionDiagnostics(tree, optionsIndex))
	out = appendDataset(out, datadiag.PackageDiagnostics(tree, packagesIndex))
	return out
}

// appendDataset appends the plain syntax.Diagnostic of each rich dataset
// diagnostic (dropping the per-fix suggestions, which the code-action path
// recomputes) onto dst.
func appendDataset(dst []syntax.Diagnostic, rich []datadiag.Diagnostic) []syntax.Diagnostic {
	for _, d := range rich {
		dst = append(dst, d.Diagnostic)
	}
	return dst
}

// datasetCodeActions offers, for each unknown-option / unknown-package diagnostic
// overlapping the requested range, one "Change to '<name>'" quick fix per
// suggestion whose TextEdit replaces exactly the flagged range. It recomputes the
// rich diagnostics from the same tree and indexes the publish path used, so each
// action pairs with the exact diagnostic it fixes, mirroring danglingFollowsActions.
func (h *Handler) datasetCodeActions(ctx context.Context, fileID, uri string, requested syntax.Range) []CodeAction {
	optionsIndex := h.optionsSnapshot()
	packagesIndex := h.packagesSnapshot()
	if optionsIndex == nil && packagesIndex == nil {
		return nil
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return nil
	}

	var rich []datadiag.Diagnostic
	if optionsIndex != nil {
		rich = append(rich, datadiag.OptionDiagnostics(tree, optionsIndex)...)
	}
	if packagesIndex != nil {
		rich = append(rich, datadiag.PackageDiagnostics(tree, packagesIndex)...)
	}

	var actions []CodeAction
	for _, d := range rich {
		if len(d.Suggestions) == 0 || !rangesOverlap(d.Range, requested) {
			continue
		}
		protoDiags := toProtocolDiagnostics([]syntax.Diagnostic{d.Diagnostic})
		for _, name := range d.Suggestions {
			edit := TextEdit{Range: toProtocolRange(d.Range), NewText: name}
			actions = append(actions, CodeAction{
				Title:       "Change to '" + name + "'",
				Kind:        "quickfix",
				Diagnostics: protoDiags,
				Edit:        &WorkspaceEdit{Changes: map[string][]TextEdit{uri: {edit}}},
			})
		}
	}
	return actions
}

// storeOptionsIndex publishes a freshly loaded options index and re-runs
// diagnostics for every open document so a module opened before the dataset
// existed gains its unknown-option warnings without a further edit.
func (h *Handler) storeOptionsIndex(index *options.Index) {
	h.optionsIndex.Store(index)
	h.refreshOpenDiagnostics()
}

// storePackagesIndex publishes a freshly loaded packages index and re-runs
// diagnostics for every open document, the packages twin of storeOptionsIndex.
func (h *Handler) storePackagesIndex(index *packages.Index) {
	h.packagesIndex.Store(index)
	h.refreshOpenDiagnostics()
}

// refreshOpenDiagnostics recomputes and republishes diagnostics for every open
// document on a background task, reusing the generation-guarded
// computeFileDiagnostics path so a concurrent edit's newer generation still wins.
// It is the re-publish hook a dataset load triggers; with no open documents it is
// a cheap no-op (the common case at initialize, before any didOpen).
func (h *Handler) refreshOpenDiagnostics() {
	snapshot := h.vfs.Snapshot()
	open := openFiles(snapshot)
	if len(open) == 0 {
		return
	}
	h.tasks.Submit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		for _, file := range open {
			_ = h.computeFileDiagnostics(ctx, snapshot, file.uri, file.path, h.nextGeneration(), false)
		}
		return nil
	})
}
