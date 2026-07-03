package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/flake"
)

// optionsFixturePath returns the absolute path to the shared decompressed
// options.json fixture (20 entries) used to load an index without networking.
func optionsFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "analysis", "options", "testdata", "options.fixture.json"))
	if err != nil {
		t.Fatalf("Abs error = %v", err)
	}
	return path
}

// initWithOptions initializes handler with rootUri and an explicit optionsPath,
// then waits for workspace discovery. The explicit-path load is synchronous, so
// the index is published by the time initialize returns.
func initWithOptions(t *testing.T, handler *Handler, root, optionsPath string) {
	t.Helper()
	params := map[string]any{"rootUri": mustURI(t, root)}
	if optionsPath != "" {
		params["initializationOptions"] = map[string]any{"optionsPath": optionsPath}
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

func TestOptionsLoadFromFixture(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithOptions(t, handler, t.TempDir(), optionsFixturePath(t))

	index := handler.optionsSnapshot()
	if index == nil {
		t.Fatal("optionsSnapshot = nil, want loaded index")
	}
	if got := index.Len(); got != 20 {
		t.Errorf("index.Len() = %d, want 20", got)
	}
}

func TestOptionsPathOffStaysNil(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithOptions(t, handler, t.TempDir(), "off")

	if index := handler.optionsSnapshot(); index != nil {
		t.Errorf("optionsSnapshot = %v, want nil for optionsPath \"off\"", index)
	}
}

func TestOptionsPathMissingFileStaysNil(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	missing := filepath.Join(t.TempDir(), "does-not-exist.json")
	initWithOptions(t, handler, t.TempDir(), missing)

	if index := handler.optionsSnapshot(); index != nil {
		t.Errorf("optionsSnapshot = %v, want nil for a missing file", index)
	}
}

func TestOptionsChannelSelection(t *testing.T) {
	release := `{
  "version": 7,
  "root": "root",
  "nodes": {
    "root": { "inputs": { "nixpkgs": "nixpkgs" } },
    "nixpkgs": { "original": { "type": "github", "owner": "NixOS", "repo": "nixpkgs", "ref": "nixos-25.05" } }
  }
}`
	branch := `{
  "version": 7,
  "root": "root",
  "nodes": {
    "root": { "inputs": { "nixpkgs": "nixpkgs" } },
    "nixpkgs": { "original": { "type": "github", "owner": "NixOS", "repo": "nixpkgs", "ref": "master" } }
  }
}`

	tests := []struct {
		name string
		lock string
		want string
	}{
		{name: "release ref used verbatim", lock: release, want: "nixos-25.05"},
		{name: "git branch falls back", lock: branch, want: "nixos-unstable"},
		{name: "no lock falls back", lock: "", want: "nixos-unstable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lock *flake.Lock
			hasLock := false
			if tt.lock != "" {
				parsed, err := flake.ParseLock([]byte(tt.lock))
				if err != nil {
					t.Fatalf("ParseLock error = %v", err)
				}
				lock, hasLock = parsed, true
			}
			if got := optionsChannel(lock, hasLock); got != tt.want {
				t.Errorf("optionsChannel = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestOptionsCacheFresh(t *testing.T) {
	now := time.Now()
	if !optionsCacheFresh(now.Add(-time.Hour), now) {
		t.Error("cacheFresh(1h old) = false, want true")
	}
	if optionsCacheFresh(now.Add(-8*24*time.Hour), now) {
		t.Error("cacheFresh(8d old) = true, want false")
	}
}

// modFixture is a NixOS module snippet: a nested option binding plus a value list
// so the hover tests can target both an option segment and a plain value.
const modFixture = "{ pkgs, ... }: { networking.firewall.allowedTCPPorts = [ 22 ]; }"

func TestHandlerHoverOptionDoc(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	line, char := posOf(t, modFixture, "allowedTCPPorts", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want option-doc hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{
		"**networking.firewall.allowedTCPPorts**",
		"List of TCP ports",
	} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	if hover.Contents.Kind != "markdown" {
		t.Errorf("kind = %q, want markdown", hover.Contents.Kind)
	}
	// The range spans exactly the hovered segment.
	if hover.Range.Start.Line != line || hover.Range.Start.Character != char {
		t.Errorf("range start = %d:%d, want %d:%d", hover.Range.Start.Line, hover.Range.Start.Character, line, char)
	}
	wantEnd := char + len("allowedTCPPorts")
	if hover.Range.End.Line != line || hover.Range.End.Character != wantEnd {
		t.Errorf("range end = %d:%d, want %d:%d", hover.Range.End.Line, hover.Range.End.Character, line, wantEnd)
	}
}

// TestHandlerHoverOptionDeclarationLink mirrors the packages provenance test: it
// records an options channel directly (as an auto-mode load would) and asserts
// the "Declared in" path renders as a GitHub blob link on that channel branch.
func TestHandlerHoverOptionDeclarationLink(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	// Simulate an auto-mode load having recorded the channel.
	handler.setOptionsChannel("nixos-25.05")
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	line, char := posOf(t, modFixture, "allowedTCPPorts", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want option-doc hover")
	}
	value := hover.Contents.Value
	want := "https://github.com/NixOS/nixpkgs/blob/nixos-25.05/nixos/modules/services/networking/firewall.nix"
	if !strings.Contains(value, want) {
		t.Errorf("hover value missing declaration link %q:\n%s", want, value)
	}
}

// TestHandlerHoverOptionFixtureNoLink is the negative counterpart: a fixture-mode
// (explicit-path) load records no channel, so the "Declared in" line stays and no
// GitHub link is emitted.
func TestHandlerHoverOptionFixtureNoLink(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	line, char := posOf(t, modFixture, "allowedTCPPorts", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want option-doc hover")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "Declared in") {
		t.Errorf("hover value missing \"Declared in\":\n%s", value)
	}
	if strings.Contains(value, "github.com") {
		t.Errorf("fixture-mode hover carries a github.com link (channel should be empty):\n%s", value)
	}
}

func TestHandlerHoverOptionOffReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, "off")
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	line, char := posOf(t, modFixture, "allowedTCPPorts", 0)
	if hover := requestHover(t, handler, modURI, line, char+1); hover != nil {
		t.Fatalf("hover with optionsPath \"off\" = %+v, want null", hover)
	}
}

func TestHandlerHoverOptionOnValueReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, modFixture)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, modFixture)

	// The `22` inside the value list is not an option path.
	line, char := posOf(t, modFixture, "22", 0)
	if hover := requestHover(t, handler, modURI, line, char); hover != nil {
		t.Fatalf("hover on value = %+v, want null", hover)
	}
}

func TestHandlerHoverOptionInstanceSegmentFallsBackToPrefix(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const mod = "{ config, ... }: { systemd.services.demo-web.serviceConfig = {}; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, mod)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, mod)

	// `demo-web` names a wildcard instance: no doc at the <name> node, so the
	// hover falls back to the systemd.services attrsOf doc and says so honestly.
	line, char := posOf(t, mod, "demo-web", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on instance segment = null, want attrsOf prefix hover")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "**systemd.services**") {
		t.Errorf("hover header does not name the matched prefix:\n%s", value)
	}
	if !strings.Contains(value, "service units") {
		t.Errorf("hover value missing the systemd.services description:\n%s", value)
	}
}

func TestHandlerHoverOptionWildcardHeaderUsesInstanceName(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const mod = "{ config, ... }: { systemd.services.demo-web = { description = \"demo\"; }; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "mod.nix")
	writeFile(t, modPath, mod)
	initWithOptions(t, handler, root, optionsFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, mod)

	// `description` resolves through the <name> wildcard; the header must carry
	// the user's own instance name, never a stripped-placeholder "..".
	line, char := posOf(t, mod, "description", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on wildcard-resolved option = null, want doc hover")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "**systemd.services.demo-web.description**") {
		t.Errorf("hover header does not name the concrete instance:\n%s", value)
	}
	if strings.Contains(value, "..") {
		t.Errorf("hover header carries a stripped-placeholder \"..\":\n%s", value)
	}
}
