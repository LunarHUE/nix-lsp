package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

func TestHandlerInitializeAdvertisesCompletionCapability(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	result, err := handler.Handle(context.Background(), "initialize", nil)
	if err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	init, ok := result.(lsp.InitializeResult)
	if !ok {
		t.Fatalf("result type = %T, want lsp.InitializeResult", result)
	}
	if init.Capabilities.CompletionProvider == nil {
		t.Fatal("CompletionProvider = nil, want set")
	}
	data, err := json.Marshal(init.Capabilities)
	if err != nil {
		t.Fatalf("Marshal capabilities error = %v", err)
	}
	if !strings.Contains(string(data), `"completionProvider":{"triggerCharacters":["\""]}`) {
		t.Errorf("serialized capabilities = %s, want completionProvider triggerCharacters [\"]", data)
	}
}

// completionWorkspace writes src as the root flake.nix, discovers the workspace,
// opens the file, and returns its URI.
func completionWorkspace(t *testing.T, handler *Handler, src string) string {
	t.Helper()
	root := t.TempDir()
	flakePath := filepath.Join(root, "flake.nix")
	writeFile(t, flakePath, src)
	initWorkspace(t, handler, root)
	uri := mustURI(t, flakePath)
	openDocument(t, handler, uri, src)
	return uri
}

func requestCompletion(t *testing.T, handler *Handler, uri string, line, character int) []CompletionItem {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/completion", positionParams(t, uri, line, character))
	if err != nil {
		t.Fatalf("completion error = %v", err)
	}
	if result == nil {
		return nil
	}
	items, ok := result.([]CompletionItem)
	if !ok {
		t.Fatalf("completion result type = %T, want []CompletionItem", result)
	}
	return items
}

func labelsOf(items []CompletionItem) []string {
	labels := make([]string, 0, len(items))
	for _, item := range items {
		labels = append(labels, item.Label)
	}
	return labels
}

func detailByLabel(items []CompletionItem, label string) (string, bool) {
	for _, item := range items {
		if item.Label == label {
			return item.Detail, true
		}
	}
	return "", false
}

func TestHandlerCompletionFollowsTargetOffersOtherInputs(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// The follows override sits on `home`, so home excludes itself; nixpkgs and
	// extra remain, carrying their url details, sorted by label.
	uri := completionWorkspace(t, handler, flakeFixture)

	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	quoteCol := char + len("follows = ")
	items := requestCompletion(t, handler, uri, line, quoteCol+1)

	if got := labelsOf(items); strings.Join(got, ",") != "extra,nixpkgs" {
		t.Fatalf("labels = %v, want [extra nixpkgs] (sorted, home self-excluded)", got)
	}
	if d, _ := detailByLabel(items, "nixpkgs"); d != "github:NixOS/nixpkgs" {
		t.Errorf("nixpkgs detail = %q, want its url", d)
	}
	if d, _ := detailByLabel(items, "extra"); d != "github:foo/extra" {
		t.Errorf("extra detail = %q, want its url", d)
	}
	for _, item := range items {
		if item.Kind != completionItemKindVariable {
			t.Errorf("item %q kind = %d, want %d", item.Label, item.Kind, completionItemKindVariable)
		}
	}
}

func TestHandlerCompletionFollowsTargetOnOpeningQuoteReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := completionWorkspace(t, handler, flakeFixture)

	// The cursor exactly on the opening quote is on the range start, not strictly
	// inside it, so nothing completes.
	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	quoteCol := char + len("follows = ")
	if items := requestCompletion(t, handler, uri, line, quoteCol); items != nil {
		t.Fatalf("completion on opening quote = %v, want null", items)
	}
}

func TestHandlerCompletionOutputsFormalsOffersMissingInputs(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// formals {self, nixpkgs}; inputs nixpkgs+hm+extra. nixpkgs and self are
	// present, so only hm and extra are offered.
	src := "{\n" +
		"  inputs.nixpkgs.url = \"u1\";\n" +
		"  inputs.hm.url = \"u2\";\n" +
		"  inputs.extra.url = \"u3\";\n" +
		"  outputs = { self, nixpkgs }: {};\n" +
		"}\n"
	uri := completionWorkspace(t, handler, src)

	line, char := posOf(t, src, "self, nixpkgs", 0)
	items := requestCompletion(t, handler, uri, line, char+1)

	if got := labelsOf(items); strings.Join(got, ",") != "extra,hm" {
		t.Fatalf("labels = %v, want [extra hm] (nixpkgs+self present)", got)
	}
}

func TestHandlerCompletionOutputsFormalsOffersSelfWhenAbsent(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// formals {nixpkgs} with self absent; self is offered, nixpkgs is not.
	src := "{\n" +
		"  inputs.nixpkgs.url = \"u1\";\n" +
		"  outputs = { nixpkgs }: {};\n" +
		"}\n"
	uri := completionWorkspace(t, handler, src)

	// occurrence 1 of "nixpkgs" is the formal (occurrence 0 is inputs.nixpkgs).
	line, char := posOf(t, src, "nixpkgs", 1)
	items := requestCompletion(t, handler, uri, line, char+1)

	if got := labelsOf(items); strings.Join(got, ",") != "self" {
		t.Fatalf("labels = %v, want [self]", got)
	}
	if d, _ := detailByLabel(items, "self"); d != "flake self reference" {
		t.Errorf("self detail = %q, want 'flake self reference'", d)
	}
}

func TestHandlerCompletionInertPositionReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := completionWorkspace(t, handler, flakeFixture)

	// The opening `{` at 0:0 is in neither completion context.
	if items := requestCompletion(t, handler, uri, 0, 0); items != nil {
		t.Fatalf("completion on inert position = %v, want null", items)
	}
}

func TestHandlerCompletionNonRootFileReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	other := filepath.Join(root, "other.nix")
	writeFile(t, other, flakeFixture)
	initWorkspace(t, handler, root)
	otherURI := mustURI(t, other)
	openDocument(t, handler, otherURI, flakeFixture)

	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	quoteCol := char + len("follows = ")
	if items := requestCompletion(t, handler, otherURI, line, quoteCol+1); items != nil {
		t.Fatalf("completion on non-root file = %v, want null", items)
	}
}

func TestHandlerCompletionNoWorkspaceReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// No initialize: no workspace, so the root-flake gate declines.
	root := t.TempDir()
	flakePath := filepath.Join(root, "flake.nix")
	writeFile(t, flakePath, flakeFixture)
	uri := mustURI(t, flakePath)
	openDocument(t, handler, uri, flakeFixture)

	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	quoteCol := char + len("follows = ")
	if items := requestCompletion(t, handler, uri, line, quoteCol+1); items != nil {
		t.Fatalf("completion without workspace = %v, want null", items)
	}
}
