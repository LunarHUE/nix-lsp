package server

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// initWithDatasets initializes handler with rootUri plus explicit options and
// packages fixture paths (either may be ""), then waits for workspace discovery.
// The explicit-path loads are synchronous, so both indices are published by the
// time initialize returns.
func initWithDatasets(t *testing.T, handler *Handler, root, optionsPath, packagesPath string) {
	t.Helper()
	params := map[string]any{"rootUri": mustURI(t, root)}
	opts := map[string]any{}
	if optionsPath != "" {
		opts["optionsPath"] = optionsPath
	}
	if packagesPath != "" {
		opts["packagesPath"] = packagesPath
	}
	if len(opts) > 0 {
		params["initializationOptions"] = opts
	}
	if _, err := handler.Handle(context.Background(), "initialize", mustJSON(t, params)); err != nil {
		t.Fatalf("initialize error = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := handler.WaitForWorkspace(ctx); err != nil {
		t.Fatalf("WaitForWorkspace error = %v", err)
	}
}

// completionModule writes src as a non-flake module file in a fresh workspace,
// loads both fixture datasets, opens the file, and returns its URI.
func completionModule(t *testing.T, handler *Handler, src string) string {
	t.Helper()
	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, src)
	initWithDatasets(t, handler, root, optionsFixturePath(t), packagesFixturePath(t))
	uri := mustURI(t, modPath)
	openDocument(t, handler, uri, src)
	return uri
}

// requestCompletionList issues a completion request and expects a *CompletionList
// (the dataset-aware result), or nil for a null response.
func requestCompletionList(t *testing.T, handler *Handler, uri string, line, character int) *CompletionList {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/completion", positionParams(t, uri, line, character))
	if err != nil {
		t.Fatalf("completion error = %v", err)
	}
	if result == nil {
		return nil
	}
	list, ok := result.(*CompletionList)
	if !ok {
		t.Fatalf("completion result type = %T, want *CompletionList", result)
	}
	return list
}

// posAfter returns the position just past the first occurrence of sub in src.
// All completion fixtures here are single-line, so it only advances the column.
func posAfter(t *testing.T, src, sub string) (int, int) {
	t.Helper()
	line, col := posOf(t, src, sub, 0)
	return line, col + len(sub)
}

// itemByLabel returns the item with the given label, failing if it is absent.
func itemByLabel(t *testing.T, list *CompletionList, label string) CompletionItem {
	t.Helper()
	if list == nil {
		t.Fatalf("completion list = nil, want item %q", label)
	}
	for _, item := range list.Items {
		if item.Label == label {
			return item
		}
	}
	t.Fatalf("item %q not found in %v", label, listLabels(list))
	return CompletionItem{}
}

func listLabels(list *CompletionList) []string {
	if list == nil {
		return nil
	}
	labels := make([]string, 0, len(list.Items))
	for _, item := range list.Items {
		labels = append(labels, item.Label)
	}
	return labels
}

func TestHandlerCompletionOptionPathGroup(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ config, pkgs, ... }: { networking. }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "networking.")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "firewall")
	if item.Kind != completionItemKindModule {
		t.Errorf("firewall kind = %d, want %d (Module group)", item.Kind, completionItemKindModule)
	}
}

func TestHandlerCompletionOptionPathPartialReplace(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ config, pkgs, ... }: { networking.fire }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "fire")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "firewall")
	if item.TextEdit == nil {
		t.Fatal("firewall TextEdit = nil, want a replace edit")
	}
	if item.TextEdit.NewText != "firewall" {
		t.Errorf("firewall NewText = %q, want firewall", item.TextEdit.NewText)
	}
	// The replaced range must cover the typed "fire" fragment.
	fireLine, fireCol := posOf(t, src, "fire", 0)
	r := item.TextEdit.Range
	if r.Start.Line != fireLine || r.Start.Character != fireCol || r.End.Character != fireCol+len("fire") {
		t.Errorf("TextEdit range = %+v, want replace of %q at %d:%d", r, "fire", fireLine, fireCol)
	}
}

func TestHandlerCompletionOptionPathLeaf(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ config, pkgs, ... }: { networking.firewall.e }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "firewall.e")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "enable")
	if item.Kind != completionItemKindField {
		t.Errorf("enable kind = %d, want %d (Field leaf)", item.Kind, completionItemKindField)
	}
	if item.Detail != "boolean" {
		t.Errorf("enable detail = %q, want boolean", item.Detail)
	}
	if item.Documentation == nil || item.Documentation.Value == "" {
		t.Errorf("enable documentation = %+v, want non-empty markdown", item.Documentation)
	}
}

func TestHandlerCompletionOptionPathNilIndexReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// Options disabled: an option-path context must degrade to null.
	root := t.TempDir()
	src := "{ config, pkgs, ... }: { networking. }"
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, src)
	initWithDatasets(t, handler, root, "off", packagesFixturePath(t))
	uri := mustURI(t, modPath)
	openDocument(t, handler, uri, src)

	line, char := posAfter(t, src, "networking.")
	if list := requestCompletionList(t, handler, uri, line, char); list != nil {
		t.Fatalf("completion with no options index = %v, want null", listLabels(list))
	}
}

func TestHandlerCompletionPkgAttr(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: { x = pkgs.cl; }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "pkgs.cl")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "claude-code")
	if item.Kind != completionItemKindField {
		t.Errorf("claude-code kind = %d, want %d (Field leaf)", item.Kind, completionItemKindField)
	}
	if item.Detail != "2.1.193" {
		t.Errorf("claude-code detail = %q, want 2.1.193", item.Detail)
	}
	if item.TextEdit == nil || item.TextEdit.NewText != "claude-code" {
		t.Errorf("claude-code TextEdit = %+v, want NewText claude-code", item.TextEdit)
	}
	clLine, clCol := posOf(t, src, "pkgs.cl", 0)
	clCol += len("pkgs.")
	r := item.TextEdit.Range
	if r.Start.Line != clLine || r.Start.Character != clCol || r.End.Character != clCol+len("cl") {
		t.Errorf("TextEdit range = %+v, want replace of %q at %d:%d", r, "cl", clLine, clCol)
	}
}

func TestHandlerCompletionPkgAttrNestedLeaf(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: [ pkgs.python312Packages. ]"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "python312Packages.")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "requests")
	if item.Kind != completionItemKindField {
		t.Errorf("requests kind = %d, want %d (Field leaf)", item.Kind, completionItemKindField)
	}
}

func TestHandlerCompletionPkgAttrSegmentDedupe(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: { x = pkgs.p; }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "pkgs.p")
	list := requestCompletionList(t, handler, uri, line, char)

	if list == nil || len(list.Items) != 1 {
		t.Fatalf("items = %v, want exactly one (python312Packages group)", listLabels(list))
	}
	item := list.Items[0]
	if item.Label != "python312Packages" {
		t.Errorf("label = %q, want python312Packages", item.Label)
	}
	if item.Kind != completionItemKindModule {
		t.Errorf("kind = %d, want %d (Module group)", item.Kind, completionItemKindModule)
	}
	if item.Detail != "" {
		t.Errorf("group detail = %q, want empty (no version on a group)", item.Detail)
	}
}

func TestHandlerCompletionWithPkgsName(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: with pkgs; [ ht ]"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "[ ht")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "htop")
	if item.Kind != completionItemKindField {
		t.Errorf("htop kind = %d, want %d (Field leaf)", item.Kind, completionItemKindField)
	}
}

func TestHandlerCompletionWithPkgsSingleCharReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: with pkgs; [ h ]"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "[ h")
	if list := requestCompletionList(t, handler, uri, line, char); list != nil {
		t.Fatalf("single-char with-pkgs completion = %v, want null", listLabels(list))
	}
}

func TestHandlerCompletionWithPkgsWellknownMerge(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: with pkgs; [ mkS ]"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "[ mkS")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "mkShell")
	if item.Kind != completionItemKindFunction {
		t.Errorf("mkShell kind = %d, want %d (Function)", item.Kind, completionItemKindFunction)
	}
	if item.Detail != "" {
		t.Errorf("mkShell detail = %q, want empty (no version)", item.Detail)
	}
}

func TestHandlerCompletionLocalName(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "let foobar = 1; in foo"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "in foo")
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "foobar")
	if item.Kind != completionItemKindVariable {
		t.Errorf("foobar kind = %d, want %d (Variable)", item.Kind, completionItemKindVariable)
	}
	if item.Detail != "let binding" {
		t.Errorf("foobar detail = %q, want 'let binding'", item.Detail)
	}
	// A builtin (e.g. map, true) is never offered as a local name.
	for _, label := range listLabels(list) {
		if label == "map" || label == "true" || label == "builtins" {
			t.Errorf("local-name completion included builtin %q", label)
		}
	}
}
