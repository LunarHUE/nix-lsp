package server

import (
	"context"
	"path/filepath"
	"strings"
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
	// A group has nothing to resolve: no Data payload and no Documentation.
	if item.Data != nil || item.Documentation != nil {
		t.Errorf("firewall group data = %+v, documentation = %+v, want both nil", item.Data, item.Documentation)
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
	// The markdown ships only on resolve now: the leaf carries the resolve payload
	// (its concrete path) and no Documentation.
	if item.Documentation != nil {
		t.Errorf("enable documentation = %+v, want nil (filled on resolve)", item.Documentation)
	}
	if item.Data == nil || item.Data.Source != "option" {
		t.Fatalf("enable data = %+v, want option source", item.Data)
	}
	wantPath := []string{"networking", "firewall", "enable"}
	if strings.Join(item.Data.Path, ".") != strings.Join(wantPath, ".") {
		t.Errorf("enable data path = %v, want %v", item.Data.Path, wantPath)
	}
}

func TestHandlerCompletionOptionEmptyAttrsetBody(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// Cursor inside the empty braces of a wildcard-instance submodule value: the
	// enclosing binding path resolves through <name> and its options complete
	// with nothing typed.
	src := "{ config, pkgs, ... }: { networking.wireguard.interfaces.wg0 = {  }; }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "= { ")
	list := requestCompletionList(t, handler, uri, line, char)

	ips := itemByLabel(t, list, "ips")
	if ips.Kind != completionItemKindField {
		t.Errorf("ips kind = %d, want %d (Field leaf)", ips.Kind, completionItemKindField)
	}
	if ips.Detail != "list of string" {
		t.Errorf("ips detail = %q, want list of string", ips.Detail)
	}
	itemByLabel(t, list, "peers")
	itemByLabel(t, list, "privateKey")
	// The edit inserts at the cursor: a zero-width replace range.
	if ips.TextEdit == nil {
		t.Fatal("ips TextEdit = nil, want an insert edit")
	}
	r := ips.TextEdit.Range
	if r.Start != r.End || r.Start.Line != line || r.Start.Character != char {
		t.Errorf("TextEdit range = %+v, want zero-width at %d:%d", r, line, char)
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
	// The leaf carries a package resolve payload (its full dotted attr) and no
	// Documentation; the markdown is filled on resolve.
	if item.Documentation != nil {
		t.Errorf("claude-code documentation = %+v, want nil (filled on resolve)", item.Documentation)
	}
	if item.Data == nil || item.Data.Source != "package" || item.Data.Attr != "claude-code" {
		t.Fatalf("claude-code data = %+v, want package source with attr claude-code", item.Data)
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
	// The well-known helper carries a resolve payload and no Documentation.
	if item.Documentation != nil {
		t.Errorf("mkShell documentation = %+v, want nil (filled on resolve)", item.Documentation)
	}
	if item.Data == nil || item.Data.Source != "wellknown" || item.Data.Attr != "mkShell" {
		t.Fatalf("mkShell data = %+v, want wellknown source with attr mkShell", item.Data)
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
	// A local name has nothing to resolve: no Data payload and no Documentation.
	if item.Data != nil || item.Documentation != nil {
		t.Errorf("foobar data = %+v, documentation = %+v, want both nil", item.Data, item.Documentation)
	}
	// A builtin (e.g. map, true) is never offered as a local name.
	for _, label := range listLabels(list) {
		if label == "map" || label == "true" || label == "builtins" {
			t.Errorf("local-name completion included builtin %q", label)
		}
	}
}

// requestResolve echoes item back through completionItem/resolve, exactly as a
// client does when the item is selected, and returns the resolved item.
func requestResolve(t *testing.T, handler *Handler, item CompletionItem) CompletionItem {
	t.Helper()
	result, err := handler.Handle(context.Background(), "completionItem/resolve", mustJSON(t, item))
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	resolved, ok := result.(CompletionItem)
	if !ok {
		t.Fatalf("resolve result type = %T, want CompletionItem", result)
	}
	return resolved
}

func TestHandlerCompletionResolveOption(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ config, pkgs, ... }: { networking.firewall.e }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "firewall.e")
	list := requestCompletionList(t, handler, uri, line, char)
	item := requestResolve(t, handler, itemByLabel(t, list, "enable"))

	if item.Documentation == nil {
		t.Fatal("resolved option documentation = nil, want markdown")
	}
	if !strings.Contains(item.Documentation.Value, "networking.firewall.enable") {
		t.Errorf("resolved option markdown = %q, want the concrete option header", item.Documentation.Value)
	}
	if !strings.Contains(item.Documentation.Value, "*Type:*") {
		t.Errorf("resolved option markdown = %q, want a Type line", item.Documentation.Value)
	}
}

func TestHandlerCompletionResolvePackageProvenance(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: { x = pkgs.cl; }"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "pkgs.cl")
	list := requestCompletionList(t, handler, uri, line, char)
	leaf := itemByLabel(t, list, "claude-code")

	// No channel recorded (fixture load): the description resolves without a
	// provenance line.
	item := requestResolve(t, handler, leaf)
	if item.Documentation == nil || !strings.Contains(item.Documentation.Value, "Agentic coding tool") {
		t.Fatalf("resolved package markdown = %+v, want the package description", item.Documentation)
	}
	if strings.Contains(item.Documentation.Value, "channel data") {
		t.Errorf("resolved package markdown = %q, want no provenance line without a channel", item.Documentation.Value)
	}

	// With a channel recorded, the same resolve appends the provenance line, exactly
	// as package hover does.
	handler.setPackagesChannel("nixpkgs-unstable")
	item = requestResolve(t, handler, leaf)
	if item.Documentation == nil || !strings.Contains(item.Documentation.Value, "nixpkgs-unstable channel data") {
		t.Errorf("resolved package markdown = %+v, want the nixpkgs-unstable provenance line", item.Documentation)
	}
}

func TestHandlerCompletionResolveWellknown(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "{ pkgs, ... }: with pkgs; [ mkS ]"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "[ mkS")
	list := requestCompletionList(t, handler, uri, line, char)
	item := requestResolve(t, handler, itemByLabel(t, list, "mkShell"))

	if item.Documentation == nil {
		t.Fatal("resolved well-known documentation = nil, want markdown")
	}
	if !strings.Contains(item.Documentation.Value, "mkShell") {
		t.Errorf("resolved well-known markdown = %q, want the mkShell header", item.Documentation.Value)
	}
}

func TestHandlerCompletionResolveNoDataRoundTrips(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := "let foobar = 1; in foo"
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, "in foo")
	list := requestCompletionList(t, handler, uri, line, char)
	item := requestResolve(t, handler, itemByLabel(t, list, "foobar"))

	// A local name carries no Data; resolve must round-trip it unchanged.
	if item.Documentation != nil {
		t.Errorf("resolved no-data item documentation = %+v, want nil (round-trip)", item.Documentation)
	}
	if item.Label != "foobar" {
		t.Errorf("resolved no-data item label = %q, want foobar", item.Label)
	}
}

func TestHandlerCompletionResolveUnknownAttrRoundTrips(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// Datasets are loaded, but the attr names no known package.
	src := "{ pkgs, ... }: { x = pkgs.cl; }"
	completionModule(t, handler, src)

	item := CompletionItem{
		Label: "nope",
		Data:  &CompletionData{Source: "package", Attr: "no-such-package"},
	}
	resolved := requestResolve(t, handler, item)
	if resolved.Documentation != nil {
		t.Errorf("resolved unknown attr documentation = %+v, want nil (round-trip)", resolved.Documentation)
	}
}

func TestHandlerCompletionResolveNilIndexRoundTrips(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// No initialize: both indexes are nil. Resolve must degrade to round-trip.
	item := CompletionItem{
		Label: "enable",
		Data:  &CompletionData{Source: "option", Path: []string{"networking", "firewall", "enable"}},
	}
	resolved := requestResolve(t, handler, item)
	if resolved.Documentation != nil {
		t.Errorf("resolved with nil index documentation = %+v, want nil (round-trip)", resolved.Documentation)
	}
}

func TestHandlerCompletionValueStringEnum(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := `{ config, pkgs, ... }: { services.openssh.settings.PermitRootLogin = "pro"; }`
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, `"pro`)
	list := requestCompletionList(t, handler, uri, line, char)

	item := itemByLabel(t, list, "prohibit-password")
	if item.Kind != completionItemKindValue {
		t.Errorf("kind = %d, want %d (Value)", item.Kind, completionItemKindValue)
	}
	if item.TextEdit == nil || item.TextEdit.NewText != "prohibit-password" {
		t.Fatalf("TextEdit = %+v, want NewText prohibit-password", item.TextEdit)
	}
	// The replaced range is the inside-quotes content ("pro"), not the quotes.
	proLine, proCol := posOf(t, src, "pro", 0)
	r := item.TextEdit.Range
	if r.Start.Line != proLine || r.Start.Character != proCol || r.End.Character != proCol+len("pro") {
		t.Errorf("TextEdit range = %+v, want replace of %q at %d:%d", r, "pro", proLine, proCol)
	}
	// The fragment filters the list: only prohibit-password starts with "pro".
	if got := listLabels(list); len(got) != 1 {
		t.Errorf("labels = %v, want only prohibit-password", got)
	}
}

func TestHandlerCompletionValueStringUnclosed(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// Mid-edit: the quote is open and there is no closing quote or semicolon.
	src := `{ config, pkgs, ... }: { services.openssh.settings.PermitRootLogin = "pro`
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, `"pro`)
	list := requestCompletionList(t, handler, uri, line, char)
	itemByLabel(t, list, "prohibit-password")
}

func TestHandlerCompletionValueStringEmptyOffersAll(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	src := `{ config, pkgs, ... }: { services.openssh.settings.PermitRootLogin = ""; }`
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, `= "`)
	list := requestCompletionList(t, handler, uri, line, char)
	if got := listLabels(list); len(got) != 5 {
		t.Fatalf("labels = %v, want all 5 enum members", got)
	}
	itemByLabel(t, list, "yes")
	itemByLabel(t, list, "no")
}

func TestHandlerCompletionValueStringNonEnumNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	// system.stateVersion is a plain string, not an enum: no value completion.
	src := `{ config, pkgs, ... }: { system.stateVersion = "24"; }`
	uri := completionModule(t, handler, src)

	line, char := posAfter(t, src, `"24`)
	if list := requestCompletionList(t, handler, uri, line, char); list != nil {
		t.Fatalf("non-enum value completion = %v, want null", listLabels(list))
	}
}
