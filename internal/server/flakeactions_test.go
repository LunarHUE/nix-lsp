package server

import (
	"path/filepath"
	"sort"
	"testing"
)

// flakeActionWorkspace writes src as the root flake.nix, discovers the
// workspace, opens the file, and waits for wantDiagnostics diagnostics to land.
// It returns the flake.nix URI.
func flakeActionWorkspace(t *testing.T, handler *Handler, src string, wantDiagnostics int) string {
	t.Helper()
	root := t.TempDir()
	flakePath := filepath.Join(root, "flake.nix")
	writeFile(t, flakePath, src)
	initWorkspace(t, handler, root)
	uri := mustURI(t, flakePath)
	openDocument(t, handler, uri, src)
	waitForDiagnostics(t, handler, uri, wantDiagnostics)
	return uri
}

// actionByTitle returns the action with the given title, or fails.
func actionByTitle(t *testing.T, actions []CodeAction, title string) CodeAction {
	t.Helper()
	for _, a := range actions {
		if a.Title == title {
			return a
		}
	}
	t.Fatalf("action %q not found in %+v", title, actionTitles(actions))
	return CodeAction{}
}

func actionTitles(actions []CodeAction) []string {
	titles := make([]string, 0, len(actions))
	for _, a := range actions {
		titles = append(titles, a.Title)
	}
	return titles
}

// applyEdits applies edits (for a single document) to src and returns the
// result. It sorts descending by start so sequential application over the
// original offsets stays valid, matching how a conforming LSP client applies a
// non-overlapping edit set.
func applyEdits(t *testing.T, src string, edits []TextEdit) string {
	t.Helper()
	sorted := append([]TextEdit(nil), edits...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return protocolRangeLess(sorted[j].Range, sorted[i].Range)
	})
	out := src
	for _, e := range sorted {
		start := offsetOf(t, out, e.Range.Start)
		end := offsetOf(t, out, e.Range.End)
		out = out[:start] + e.NewText + out[end:]
	}
	return out
}

// offsetOf converts an ASCII protocol position into a byte offset in src.
func offsetOf(t *testing.T, src string, pos protocolPosition) int {
	t.Helper()
	line, col := 0, 0
	for i := 0; i <= len(src); i++ {
		if line == pos.Line && col == pos.Character {
			return i
		}
		if i == len(src) {
			break
		}
		if src[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	t.Fatalf("position %+v out of range in %q", pos, src)
	return 0
}

// editsFor returns the single document's edits from an action's WorkspaceEdit.
func editsFor(t *testing.T, action CodeAction, uri string) []TextEdit {
	t.Helper()
	if action.Edit == nil {
		t.Fatalf("action %q has no edit", action.Title)
	}
	edits, ok := action.Edit.Changes[uri]
	if !ok {
		t.Fatalf("action %q edit has no changes for %s (%+v)", action.Title, uri, action.Edit.Changes)
	}
	return edits
}

const unusedSugarFlake = "{\n" +
	"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
	"  inputs.dead.url = \"github:x/dead\";\n" +
	"  outputs = { self, nixpkgs }: {};\n" +
	"}\n"

func TestFlakeActionRemoveUnusedInputSugar(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	uri := flakeActionWorkspace(t, handler, unusedSugarFlake, 1)

	line, char := posOf(t, unusedSugarFlake, "dead", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+4, nil)

	remove := actionByTitle(t, actions, "Remove input 'dead'")
	if remove.Kind != "quickfix" {
		t.Errorf("kind = %q, want quickfix", remove.Kind)
	}
	if remove.IsPreferred {
		t.Error("Remove IsPreferred = true, want false")
	}
	if len(remove.Diagnostics) != 1 || remove.Diagnostics[0].Code != "unused-input" {
		t.Errorf("diagnostics = %+v, want one unused-input", remove.Diagnostics)
	}

	// Applying the deletion removes the whole `dead` line including its newline.
	want := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	if got := applyEdits(t, unusedSugarFlake, editsFor(t, remove, uri)); got != want {
		t.Errorf("after Remove:\n got %q\nwant %q", got, want)
	}
}

func TestFlakeActionAddUnusedInputToOutputs(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	uri := flakeActionWorkspace(t, handler, unusedSugarFlake, 1)

	line, char := posOf(t, unusedSugarFlake, "dead", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+4, nil)

	add := actionByTitle(t, actions, "Add 'dead' to outputs")
	if add.IsPreferred {
		t.Error("Add IsPreferred = true, want false")
	}
	edits := editsFor(t, add, uri)
	if len(edits) != 1 {
		t.Fatalf("add edits = %d, want 1", len(edits))
	}
	if edits[0].NewText != ", dead" {
		t.Errorf("add newText = %q, want %q", edits[0].NewText, ", dead")
	}
	// The insert is zero-width at the anchor.
	if edits[0].Range.Start != edits[0].Range.End {
		t.Errorf("add range = %+v, want zero-width", edits[0].Range)
	}

	want := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.dead.url = \"github:x/dead\";\n" +
		"  outputs = { self, nixpkgs, dead }: {};\n" +
		"}\n"
	if got := applyEdits(t, unusedSugarFlake, edits); got != want {
		t.Errorf("after Add:\n got %q\nwant %q", got, want)
	}
}

func TestFlakeActionRemoveUnusedInputNested(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	src := "{\n" +
		"  inputs = {\n" +
		"    nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"    dead.url = \"github:x/dead\";\n" +
		"  };\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	uri := flakeActionWorkspace(t, handler, src, 1)

	// occurrence 0 of "dead" is the input name (occurrence 1 is inside the url).
	line, char := posOf(t, src, "dead", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+4, nil)

	remove := actionByTitle(t, actions, "Remove input 'dead'")
	// The inner `dead.url = ...;` binding sits alone on its line, so the deletion
	// takes the whole line including its newline.
	want := "{\n" +
		"  inputs = {\n" +
		"    nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  };\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	if got := applyEdits(t, src, editsFor(t, remove, uri)); got != want {
		t.Errorf("after nested Remove:\n got %q\nwant %q", got, want)
	}
}

func TestFlakeActionRemoveMultipleSugarBindings(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	src := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.dead.url = \"github:x/dead\";\n" +
		"  inputs.dead.flake = false;\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	uri := flakeActionWorkspace(t, handler, src, 1)

	line, char := posOf(t, src, "dead", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+4, nil)

	remove := actionByTitle(t, actions, "Remove input 'dead'")
	edits := editsFor(t, remove, uri)
	if len(edits) != 2 {
		t.Fatalf("remove edits = %d, want 2 (one per sugar binding)", len(edits))
	}
	// Edits are sorted descending by start: the later `flake` line first.
	if !protocolRangeLess(edits[1].Range, edits[0].Range) {
		t.Errorf("edits not sorted descending: %+v", edits)
	}
	want := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	if got := applyEdits(t, src, edits); got != want {
		t.Errorf("after multi Remove:\n got %q\nwant %q", got, want)
	}
}

func TestFlakeActionDidYouMeanFollows(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	src := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.home.url = \"github:nix-community/home-manager\";\n" +
		"  inputs.home.inputs.nixpkgs.follows = \"nixpkgz\";\n" +
		"  outputs = { self, nixpkgs, home }: {};\n" +
		"}\n"
	uri := flakeActionWorkspace(t, handler, src, 1)

	line, char := posOf(t, src, "\"nixpkgz\"", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+9, nil)

	change := actionByTitle(t, actions, "Change follows target to 'nixpkgs'")
	if change.Kind != "quickfix" {
		t.Errorf("kind = %q, want quickfix", change.Kind)
	}
	if len(change.Diagnostics) != 1 || change.Diagnostics[0].Code != "dangling-follows" {
		t.Errorf("diagnostics = %+v, want one dangling-follows", change.Diagnostics)
	}
	edits := editsFor(t, change, uri)
	if len(edits) != 1 || edits[0].NewText != "\"nixpkgs\"" {
		t.Fatalf("edits = %+v, want single newText \"nixpkgs\"", edits)
	}
	// Applying the edit replaces the whole quoted string.
	want := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.home.url = \"github:nix-community/home-manager\";\n" +
		"  inputs.home.inputs.nixpkgs.follows = \"nixpkgs\";\n" +
		"  outputs = { self, nixpkgs, home }: {};\n" +
		"}\n"
	if got := applyEdits(t, src, edits); got != want {
		t.Errorf("after did-you-mean:\n got %q\nwant %q", got, want)
	}
}

func TestFlakeActionDidYouMeanPreservesRemainder(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	src := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.home.url = \"github:nix-community/home-manager\";\n" +
		"  inputs.home.inputs.nixpkgs.follows = \"nixpkgz/sub\";\n" +
		"  outputs = { self, nixpkgs, home }: {};\n" +
		"}\n"
	uri := flakeActionWorkspace(t, handler, src, 1)

	line, char := posOf(t, src, "\"nixpkgz/sub\"", 0)
	actions := requestCodeActions(t, handler, uri, line, char, line, char+13, nil)

	change := actionByTitle(t, actions, "Change follows target to 'nixpkgs'")
	edits := editsFor(t, change, uri)
	if len(edits) != 1 || edits[0].NewText != "\"nixpkgs/sub\"" {
		t.Fatalf("edits = %+v, want newText preserving /sub", edits)
	}
}

func TestFlakeActionDidYouMeanNoSuggestionWhenFar(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	// "zzz" is distance >2 from both nixpkgs and home, so no replacement is offered
	// even though the dangling-follows diagnostic is present.
	src := "{\n" +
		"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
		"  inputs.home.url = \"github:nix-community/home-manager\";\n" +
		"  inputs.home.inputs.nixpkgs.follows = \"zzz\";\n" +
		"  outputs = { self, nixpkgs, home }: {};\n" +
		"}\n"
	uri := flakeActionWorkspace(t, handler, src, 1)

	line, char := posOf(t, src, "\"zzz\"", 0)
	if actions := requestCodeActions(t, handler, uri, line, char, line, char+5, nil); actions != nil {
		t.Fatalf("actions for distance-3 target = %+v, want null", actions)
	}
}

func TestFlakeActionUsedInputHasNoAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	uri := flakeActionWorkspace(t, handler, unusedSugarFlake, 1)

	// nixpkgs is consumed by the outputs formals, so it carries no diagnostic and
	// no remove/add action.
	line, char := posOf(t, unusedSugarFlake, "nixpkgs", 0)
	if actions := requestCodeActions(t, handler, uri, line, char, line, char+7, nil); actions != nil {
		t.Fatalf("actions on used input = %+v, want null", actions)
	}
}

func TestFlakeActionRangeElsewhereHasNoAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	uri := flakeActionWorkspace(t, handler, unusedSugarFlake, 1)

	// The opening `{` line does not overlap the unused-input diagnostic on `dead`.
	if actions := requestCodeActions(t, handler, uri, 0, 0, 0, 1, nil); actions != nil {
		t.Fatalf("actions for range elsewhere = %+v, want null", actions)
	}
}

func TestFlakeActionAbsentOnNonRootFile(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	other := filepath.Join(root, "other.nix")
	writeFile(t, other, unusedSugarFlake)
	initWorkspace(t, handler, root)
	otherURI := mustURI(t, other)
	openDocument(t, handler, otherURI, unusedSugarFlake)

	line, char := posOf(t, unusedSugarFlake, "dead", 0)
	if actions := requestCodeActions(t, handler, otherURI, line, char, line, char+4, nil); actions != nil {
		t.Fatalf("actions on non-root file = %+v, want null", actions)
	}
}
