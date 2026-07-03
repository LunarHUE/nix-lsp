package server

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlerDefinitionOnBareBindingPathJumps covers the user's exact shape: a
// path literal as a plain binding value, outside any import/imports/callPackage
// context, still follows to the target file's top.
func TestHandlerDefinitionOnBareBindingPathJumps(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "flake.nix")
	target := filepath.Join(root, "modules", "service.nix")
	src := "{ nixosModules.web-service = ./modules/service.nix; }"
	writeFile(t, source, src)
	writeFile(t, target, "{}")
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./modules/service.nix", 0)
	location := requestDefinition(t, handler, sourceURI, line, char+1)
	if location == nil {
		t.Fatal("definition on bare binding path = null, want target file")
	}
	wantURI := mustURI(t, mustNormalize(t, target))
	if location.URI != wantURI {
		t.Errorf("location uri = %q, want %q", location.URI, wantURI)
	}
	if (location.Range != protocolRange{}) {
		t.Errorf("location range = %+v, want zero range (top of file)", location.Range)
	}
}

// TestHandlerDefinitionOnListPathOutsideImportsJumps checks a path in a list
// that is NOT the `imports` binding still follows.
func TestHandlerDefinitionOnListPathOutsideImportsJumps(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	target := filepath.Join(root, "a.nix")
	src := "{ extras = [ ./a.nix ]; }"
	writeFile(t, source, src)
	writeFile(t, target, "{}")
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./a.nix", 0)
	location := requestDefinition(t, handler, sourceURI, line, char+1)
	if location == nil {
		t.Fatal("definition on non-imports list path = null, want target file")
	}
	if location.URI != mustURI(t, mustNormalize(t, target)) {
		t.Errorf("location uri = %q, want target a.nix", location.URI)
	}
}

// TestHandlerDefinitionOnDirectoryPathJumpsToDefault checks a path naming a
// directory follows to that directory's default.nix.
func TestHandlerDefinitionOnDirectoryPathJumpsToDefault(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	target := filepath.Join(root, "modules", "default.nix")
	src := "{ a = ./modules; }"
	writeFile(t, source, src)
	writeFile(t, target, "{}")
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./modules", 0)
	location := requestDefinition(t, handler, sourceURI, line, char+1)
	if location == nil {
		t.Fatal("definition on directory path = null, want default.nix")
	}
	if location.URI != mustURI(t, mustNormalize(t, target)) {
		t.Errorf("location uri = %q, want directory default.nix", location.URI)
	}
}

// TestHandlerDefinitionOnUnfollowablePathsReturnsNull covers missing,
// interpolated, and `<nixpkgs>` search-path targets, all of which yield null.
func TestHandlerDefinitionOnUnfollowablePathsReturnsNull(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		cursor string
	}{
		{"missing", "{ a = ./missing.nix; }", "./missing.nix"},
		{"interpolated", "{ a = ./x/${n}.nix; }", "./x/"},
		{"search path", "{ a = <nixpkgs>; }", "<nixpkgs>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewHandler()
			defer handler.Close()

			root := t.TempDir()
			source := filepath.Join(root, "default.nix")
			writeFile(t, source, tc.src)
			sourceURI := mustURI(t, source)
			openDocument(t, handler, sourceURI, tc.src)

			line, char := posOf(t, tc.src, tc.cursor, 0)
			result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, sourceURI, line, char+1))
			if err != nil {
				t.Fatalf("definition error = %v", err)
			}
			if result != nil {
				t.Fatalf("definition on %s path = %+v, want null", tc.name, result)
			}
		})
	}
}

func TestHandlerPathHoverExists(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "flake.nix")
	target := filepath.Join(root, "modules", "service.nix")
	src := "{ nixosModules.web-service = ./modules/service.nix; }"
	writeFile(t, source, src)
	writeFile(t, target, "{}")
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./modules/service.nix", 0)
	hover := requestHover(t, handler, sourceURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on path = null, want resolved-path hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**./modules/service.nix**", "resolves to", mustNormalize(t, target), "exists"} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	if strings.Contains(value, "does not exist") {
		t.Errorf("hover claims missing for an existing target:\n%s", value)
	}
}

func TestHandlerPathHoverMissing(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	src := "{ a = ./missing.nix; }"
	writeFile(t, source, src)
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./missing.nix", 0)
	hover := requestHover(t, handler, sourceURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on missing path = null, want resolved-path hover")
	}
	if !strings.Contains(hover.Contents.Value, "target does not exist") {
		t.Errorf("hover missing 'target does not exist':\n%s", hover.Contents.Value)
	}
}

func TestHandlerPathHoverDirectoryImportLabeled(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	writeFile(t, source, "{ a = ./modules; }")
	writeFile(t, filepath.Join(root, "modules", "default.nix"), "{}")
	src := "{ a = ./modules; }"
	sourceURI := mustURI(t, source)
	openDocument(t, handler, sourceURI, src)

	line, char := posOf(t, src, "./modules", 0)
	hover := requestHover(t, handler, sourceURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on directory path = null, want resolved-path hover")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "(directory import)") {
		t.Errorf("hover missing '(directory import)':\n%s", value)
	}
	if !strings.Contains(value, filepath.Join("modules", "default.nix")) {
		t.Errorf("hover does not name resolved default.nix:\n%s", value)
	}
}

// TestHandlerPathHoverUntracked reuses the flake+git untracked fixture: hover on
// the path to an existing-but-untracked target shows the git-tracking warning,
// drawn from the same check the diagnostic uses.
func TestHandlerPathHoverUntracked(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	handler := NewHandler()
	defer handler.Close()
	notifier := &captureNotifier{messages: make(chan publishDiagnosticsParams, 32)}
	handler.SetNotifier(notifier)

	importerURI, _, _ := untrackedImportFixture(t, handler, notifier)

	src := "import ./lib.nix"
	line, char := posOf(t, src, "./lib.nix", 0)
	hover := requestHover(t, handler, importerURI, line, char+1)
	if hover == nil {
		t.Fatal("hover on untracked path = null, want resolved-path hover")
	}
	if !strings.Contains(hover.Contents.Value, "not git-tracked") {
		t.Errorf("hover missing untracked warning:\n%s", hover.Contents.Value)
	}
}
