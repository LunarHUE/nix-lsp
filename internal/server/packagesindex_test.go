package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// packagesFixturePath returns the absolute path to the RAW-shape packages.json
// fixture (5 well-formed entries) used to load an index without networking.
func packagesFixturePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "analysis", "packages", "testdata", "packages.fixture.json"))
	if err != nil {
		t.Fatalf("Abs error = %v", err)
	}
	return path
}

// initWithPackages initializes handler with rootUri and an explicit packagesPath,
// then waits for workspace discovery. The explicit-path load is synchronous, so
// the index is published by the time initialize returns.
func initWithPackages(t *testing.T, handler *Handler, root, packagesPath string) {
	t.Helper()
	params := map[string]any{"rootUri": mustURI(t, root)}
	if packagesPath != "" {
		params["initializationOptions"] = map[string]any{"packagesPath": packagesPath}
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

func TestPackagesLoadFromFixture(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithPackages(t, handler, t.TempDir(), packagesFixturePath(t))

	index := handler.packagesSnapshot()
	if index == nil {
		t.Fatal("packagesSnapshot = nil, want loaded index")
	}
	if got := index.Len(); got != 5 {
		t.Errorf("index.Len() = %d, want 5", got)
	}
}

// pkgFixture is a home-manager style module that references pkgs.claude-code in a
// list, plus a plain value so hover tests can target both a package select and a
// non-package position.
const pkgFixture = "{ pkgs, ... }: { home.packages = [ pkgs.claude-code ]; }"

func TestHandlerHoverPackageDoc(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, pkgFixture)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, pkgFixture)

	line, char := posOf(t, pkgFixture, "claude-code", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want package-doc hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**claude-code**", "2.1.193"} {
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
	wantEnd := char + len("claude-code")
	if hover.Range.End.Line != line || hover.Range.End.Character != wantEnd {
		t.Errorf("range end = %d:%d, want %d:%d", hover.Range.End.Line, hover.Range.End.Character, line, wantEnd)
	}
}

func TestHandlerHoverPackageOffReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, pkgFixture)
	initWithPackages(t, handler, root, "off")
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, pkgFixture)

	line, char := posOf(t, pkgFixture, "claude-code", 0)
	if hover := requestHover(t, handler, modURI, line, char+1); hover != nil {
		t.Fatalf("hover with packagesPath \"off\" = %+v, want null", hover)
	}
}

func TestHandlerHoverPackageNonPkgsSelectReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const src = "{ x = lib.claude-code; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "other.nix")
	writeFile(t, modPath, src)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, src)

	// The select base is `lib`, not `pkgs`, so this is not a package select.
	line, char := posOf(t, src, "claude-code", 0)
	if hover := requestHover(t, handler, modURI, line, char+1); hover != nil {
		t.Fatalf("hover on non-pkgs select = %+v, want null", hover)
	}
}
