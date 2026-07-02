package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
)

// flakeFixture is a root flake.nix with a locked input (nixpkgs), an unlocked
// input (extra), a follows override, and a strict outputs signature.
const flakeFixture = "{\n" +
	"  inputs.nixpkgs.url = \"github:NixOS/nixpkgs\";\n" +
	"  inputs.extra.url = \"github:foo/extra\";\n" +
	"  inputs.home.url = \"github:nix-community/home-manager\";\n" +
	"  inputs.home.inputs.nixpkgs.follows = \"nixpkgs\";\n" +
	"  outputs = { self, nixpkgs, extra, home }: {};\n" +
	"}\n"

// flakeLockFixture locks nixpkgs (with rev + lastModified) and home; extra is
// deliberately absent so its hover reports it is not in flake.lock.
const flakeLockFixture = `{
  "version": 7,
  "root": "root",
  "nodes": {
    "root": { "inputs": { "nixpkgs": "nixpkgs", "home": "home" } },
    "nixpkgs": {
      "locked": {
        "type": "github",
        "owner": "NixOS",
        "repo": "nixpkgs",
        "rev": "abcdef0123456789aaaaaaaaaaaaaaaaaaaaaaaa",
        "lastModified": 1704067200
      }
    },
    "home": {
      "locked": { "type": "github", "owner": "nix-community", "repo": "home-manager" }
    }
  }
}`

func TestHandlerInitializeAdvertisesHoverCapability(t *testing.T) {
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
	if !init.Capabilities.HoverProvider {
		t.Error("HoverProvider = false, want true")
	}
	data, err := json.Marshal(init.Capabilities)
	if err != nil {
		t.Fatalf("Marshal capabilities error = %v", err)
	}
	if !strings.Contains(string(data), `"hoverProvider":true`) {
		t.Errorf("serialized capabilities = %s, want hoverProvider:true", data)
	}
}

// flakeWorkspace writes flakeFixture (and, when lock != "", flakeLockFixture)
// into a fresh workspace, initializes discovery, opens flake.nix, and returns
// its URI.
func flakeWorkspace(t *testing.T, handler *Handler, lock string) (root, flakeURI string) {
	t.Helper()
	root = t.TempDir()
	flakePath := filepath.Join(root, "flake.nix")
	writeFile(t, flakePath, flakeFixture)
	if lock != "" {
		writeFile(t, filepath.Join(root, "flake.lock"), lock)
	}
	initWorkspace(t, handler, root)
	flakeURI = mustURI(t, flakePath)
	openDocument(t, handler, flakeURI, flakeFixture)
	return root, flakeURI
}

func requestHover(t *testing.T, handler *Handler, uri string, line, character int) *Hover {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/hover", positionParams(t, uri, line, character))
	if err != nil {
		t.Fatalf("hover error = %v", err)
	}
	if result == nil {
		return nil
	}
	hover, ok := result.(*Hover)
	if !ok {
		t.Fatalf("hover result type = %T, want *Hover", result)
	}
	return hover
}

func TestHandlerHoverLockedInputName(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// Cursor on the `nixpkgs` name of `inputs.nixpkgs.url`.
	line, char := posOf(t, flakeFixture, "nixpkgs", 0)
	hover := requestHover(t, handler, flakeURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want locked input hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{
		"**input** `nixpkgs`",
		"url: `github:NixOS/nixpkgs`",
		"locked: `github:NixOS/nixpkgs`",
		"rev: `abcdef012345`",
		"lastModified: 2024-01-01",
	} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	// The rev is truncated to 12 chars, not the full hash.
	if strings.Contains(value, "abcdef0123456789") {
		t.Errorf("rev not truncated to 12 chars:\n%s", value)
	}
	if hover.Contents.Kind != "markdown" {
		t.Errorf("kind = %q, want markdown", hover.Contents.Kind)
	}
}

func TestHandlerHoverInputShowsOriginalRef(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// A lock whose nixpkgs node carries an original ref (the pinned channel). The
	// hover surfaces it as a `ref:` line alongside the locked rev.
	lock := `{
  "version": 7,
  "root": "root",
  "nodes": {
    "root": { "inputs": { "nixpkgs": "nixpkgs" } },
    "nixpkgs": {
      "locked": { "type": "github", "owner": "NixOS", "repo": "nixpkgs", "rev": "abcdef0123456789aaaaaaaaaaaaaaaaaaaaaaaa" },
      "original": { "type": "github", "owner": "NixOS", "repo": "nixpkgs", "ref": "nixos-25.05" }
    }
  }
}`
	_, flakeURI := flakeWorkspace(t, handler, lock)

	line, char := posOf(t, flakeFixture, "nixpkgs", 0)
	hover := requestHover(t, handler, flakeURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want input hover with ref")
	}
	if !strings.Contains(hover.Contents.Value, "ref: `nixos-25.05`") {
		t.Errorf("hover missing 'ref: `nixos-25.05`':\n%s", hover.Contents.Value)
	}
}

func TestHandlerHoverUnlockedInput(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// `extra` is declared but absent from the lock's root inputs.
	line, char := posOf(t, flakeFixture, "extra", 0)
	hover := requestHover(t, handler, flakeURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want unlocked input hover")
	}
	if !strings.Contains(hover.Contents.Value, "not in flake.lock") {
		t.Errorf("hover value missing 'not in flake.lock':\n%s", hover.Contents.Value)
	}
}

func TestHandlerHoverFollowsTargetDescribesTarget(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// Cursor on the `nixpkgs` inside the follows string "nixpkgs" (the value of
	// inputs.home.inputs.nixpkgs.follows). It describes the nixpkgs input.
	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	strCol := char + len(`follows = "`)
	hover := requestHover(t, handler, flakeURI, line, strCol+1)
	if hover == nil {
		t.Fatal("hover on follows target = null, want target input hover")
	}
	if !strings.Contains(hover.Contents.Value, "**input** `nixpkgs`") {
		t.Errorf("follows-target hover does not describe nixpkgs:\n%s", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "rev: `abcdef012345`") {
		t.Errorf("follows-target hover missing target's lock detail:\n%s", hover.Contents.Value)
	}
}

func TestHandlerHoverFollowsRefRendersLockedViaFollows(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// A lock where the root's `nixpkgs` input is itself a follows array ref.
	lock := `{
  "version": 7,
  "root": "root",
  "nodes": {
    "root": { "inputs": { "nixpkgs": ["home", "nixpkgs"], "extra": "extra", "home": "home" } },
    "extra": {},
    "home": {}
  }
}`
	_, flakeURI := flakeWorkspace(t, handler, lock)

	line, char := posOf(t, flakeFixture, "nixpkgs", 0)
	hover := requestHover(t, handler, flakeURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want follows-ref hover")
	}
	if !strings.Contains(hover.Contents.Value, "locked via follows: `home/nixpkgs`") {
		t.Errorf("hover missing 'locked via follows':\n%s", hover.Contents.Value)
	}
}

func TestHandlerHoverNonFlakePositionReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// Cursor on the opening `{` at 0:0: not an input name, url, or follows target.
	result, err := handler.Handle(context.Background(), "textDocument/hover", positionParams(t, flakeURI, 0, 0))
	if err != nil {
		t.Fatalf("hover error = %v", err)
	}
	if result != nil {
		t.Fatalf("hover on inert position = %+v, want null", result)
	}
}

func TestHandlerHoverNonFlakeFileReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), flakeFixture)
	other := filepath.Join(root, "other.nix")
	writeFile(t, other, flakeFixture) // same content, different file
	initWorkspace(t, handler, root)
	otherURI := mustURI(t, other)
	openDocument(t, handler, otherURI, flakeFixture)

	// Same position that would hover on the root flake.nix, but this is other.nix.
	line, char := posOf(t, flakeFixture, "nixpkgs", 0)
	result, err := handler.Handle(context.Background(), "textDocument/hover", positionParams(t, otherURI, line, char+1))
	if err != nil {
		t.Fatalf("hover error = %v", err)
	}
	if result != nil {
		t.Fatalf("hover on non-root file = %+v, want null", result)
	}
}

func TestHandlerHoverNestedFlakeFileReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	nested := filepath.Join(root, "sub", "flake.nix")
	writeFile(t, nested, flakeFixture)
	initWorkspace(t, handler, root)
	nestedURI := mustURI(t, nested)
	openDocument(t, handler, nestedURI, flakeFixture)

	line, char := posOf(t, flakeFixture, "nixpkgs", 0)
	result, err := handler.Handle(context.Background(), "textDocument/hover", positionParams(t, nestedURI, line, char+1))
	if err != nil {
		t.Fatalf("hover error = %v", err)
	}
	if result != nil {
		t.Fatalf("hover on nested flake.nix = %+v, want null", result)
	}
}

func TestHandlerFlakeDefinitionFollowsTargetJumpsToInput(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// gd on the `nixpkgs` inside the follows string jumps to the nixpkgs input
	// name declaration.
	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	strCol := char + len(`follows = "`)
	location := requestDefinition(t, handler, flakeURI, line, strCol+1)
	if location == nil {
		t.Fatal("definition on follows target = null, want input declaration")
	}
	wantLine, wantChar := posOf(t, flakeFixture, "nixpkgs", 0)
	if location.URI != flakeURI {
		t.Errorf("location uri = %q, want %q", location.URI, flakeURI)
	}
	if location.Range.Start.Line != wantLine || location.Range.Start.Character != wantChar {
		t.Errorf("range start = %d:%d, want %d:%d", location.Range.Start.Line, location.Range.Start.Character, wantLine, wantChar)
	}
}

func TestHandlerFlakeDefinitionOutputsFormalJumpsToInput(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// gd on the `extra` formal in the outputs signature jumps to inputs.extra.
	line, char := posOf(t, flakeFixture, "self, nixpkgs, extra", 0)
	formalCol := char + len("self, nixpkgs, ")
	location := requestDefinition(t, handler, flakeURI, line, formalCol+1)
	if location == nil {
		t.Fatal("definition on outputs formal = null, want input declaration")
	}
	wantLine, wantChar := posOf(t, flakeFixture, "extra", 0) // the inputs.extra name
	if location.Range.Start.Line != wantLine || location.Range.Start.Character != wantChar {
		t.Errorf("range start = %d:%d, want %d:%d", location.Range.Start.Line, location.Range.Start.Character, wantLine, wantChar)
	}
}

func TestHandlerFlakeDefinitionSelfFormalReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	_, flakeURI := flakeWorkspace(t, handler, flakeLockFixture)

	// `self` is not an input; the flake helper declines and scope resolution
	// self-defines it, landing back on the formal itself (not an input).
	line, char := posOf(t, flakeFixture, "self, nixpkgs", 0)
	location := requestDefinition(t, handler, flakeURI, line, char+1)
	if location == nil {
		t.Fatal("definition on self = null, want scope self-definition")
	}
	// It must resolve to the `self` formal, not to any input declaration.
	if location.Range.Start.Line != line || location.Range.Start.Character != char {
		t.Errorf("self resolved to %d:%d, want the formal at %d:%d", location.Range.Start.Line, location.Range.Start.Character, line, char)
	}
}

func TestHandlerFlakeDefinitionNonRootFileUnchanged(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// A non-flake file with the same shape: the follows target sits in a plain
	// string, so definition must stay null (no flake nav off the root flake.nix).
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "flake.nix"), "{}")
	other := filepath.Join(root, "other.nix")
	writeFile(t, other, flakeFixture)
	initWorkspace(t, handler, root)
	otherURI := mustURI(t, other)
	openDocument(t, handler, otherURI, flakeFixture)

	line, char := posOf(t, flakeFixture, `follows = "nixpkgs"`, 0)
	strCol := char + len(`follows = "`)
	result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, otherURI, line, strCol+1))
	if err != nil {
		t.Fatalf("definition error = %v", err)
	}
	if result != nil {
		t.Fatalf("definition on non-root follows = %+v, want null", result)
	}
}
