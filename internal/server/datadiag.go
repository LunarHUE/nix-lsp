package server

import (
	"context"
	"fmt"
	"os"

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
	out = appendDataset(out, datadiag.OptionTypeDiagnostics(tree, optionsIndex))
	out = appendDataset(out, datadiag.PackageDiagnostics(tree, packagesIndex))
	return out
}

// enrichSyntaxDiagnostics appends option-schema guidance ("— <path> accepts
// options like ...") to syntax-error messages whose range sits under a binding
// path that resolves to an option group in the loaded index. Like the dataset
// diagnostics it depends on the index identity, so it runs where they are
// appended (computeFileDiagnostics) rather than in the memoized static set; the
// datadiag helper copies before changing any message, so the memoized slice is
// never mutated. With no index or an unparseable file it returns diags unchanged.
func (h *Handler) enrichSyntaxDiagnostics(ctx context.Context, fileID string, diags []syntax.Diagnostic) []syntax.Diagnostic {
	optionsIndex := h.optionsSnapshot()
	if optionsIndex == nil || len(diags) == 0 {
		return diags
	}
	tree, err := facts.ParseTree(ctx, h.memo, fileID)
	if err != nil || tree == nil {
		return diags
	}
	return datadiag.EnrichSyntaxDiagnostics(tree, optionsIndex, diags)
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
		// Enum type mismatches carry did-you-mean replacements (a legal value close
		// to the wrong literal); the kind and string-constraint mismatches carry
		// none and so add no action below.
		rich = append(rich, datadiag.OptionTypeDiagnostics(tree, optionsIndex)...)
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
	// Non-blocking: this fires from a dataset-load goroutine, so a blocking Submit
	// on a full queue would park that loader for no benefit. A dropped refresh
	// (queue full) is logged, not lost silently: the diagnostics still gain their
	// dataset warnings on the next edit through the coalescer, and a later dataset
	// load re-arms this path. Overflow is not reachable in practice (coarse tasks).
	if _, ok := h.tasks.TrySubmit(context.Background(), lsp.LaneBackground, func(ctx context.Context) error {
		for _, file := range open {
			_ = h.computeFileDiagnostics(ctx, snapshot, file.uri, file.path, h.nextGeneration(), false)
		}
		return nil
	}); !ok {
		fmt.Fprintln(os.Stderr, "nix-lsp: dropped open-diagnostics refresh (scheduler queue full)")
	}
}
