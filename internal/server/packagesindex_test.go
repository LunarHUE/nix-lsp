package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/packages"
)

// TestFreshPackagesCacheMissesUnversionedFile is the versioning regression guard:
// a fresh trimmed cache file written by an older binary at the OLD, unversioned
// path (<channel>.json) must NOT be read by the current binary. Because the cache
// filename now embeds trimmedFormatVersion (packages.CacheFileName), the current
// path is v2-<channel>.json, so the old file is a miss and the loader re-downloads
// instead of decoding a possibly-incompatible trimmedDoc shape. Before the
// versioned filename existed the two paths coincided and this file was a silent
// hit, so this test failed.
func TestFreshPackagesCacheMissesUnversionedFile(t *testing.T) {
	dir := t.TempDir()
	channel := "nixos-unstable"

	// A v1-shaped trimmed cache at the old unversioned path, written "now" so
	// freshness cannot be the reason for a miss.
	oldPath := filepath.Join(dir, channel+".json")
	writeFile(t, oldPath, `{"claude-code":{"p":"claude-code","v":"2.1.193"}}`)

	curPath := filepath.Join(dir, packages.CacheFileName(channel))
	if _, ok := freshPackagesCache(curPath, time.Now()); ok {
		t.Fatalf("old-format cache at unversioned path %s was treated as a hit at current path %s; want a miss forcing re-download", oldPath, curPath)
	}
}

// TestFreshPackagesCacheVersionedRoundTrip covers save -> load through the
// versioned filename: marshalling the fixture index, writing it under
// packages.CacheFileName, and reading it back with freshPackagesCache yields the
// same index. This is the path loadPackagesAuto's own save (writeCacheFileAtomic
// at the versioned cachePath) and fresh-cache load share.
func TestFreshPackagesCacheVersionedRoundTrip(t *testing.T) {
	dir := t.TempDir()
	channel := "nixos-unstable"

	ix, err := loadRawFixtureIndex()
	if err != nil {
		t.Fatalf("load fixture index: %v", err)
	}
	trimmed, err := ix.MarshalTrimmed()
	if err != nil {
		t.Fatalf("MarshalTrimmed: %v", err)
	}
	cachePath := filepath.Join(dir, "nixls", "packages", packages.CacheFileName(channel))
	if !strings.Contains(filepath.Base(cachePath), "v2-") {
		t.Fatalf("cache filename %q missing version prefix", filepath.Base(cachePath))
	}
	if err := writeCacheFileAtomic(cachePath, trimmed); err != nil {
		t.Fatalf("writeCacheFileAtomic: %v", err)
	}

	back, ok := freshPackagesCache(cachePath, time.Now())
	if !ok {
		t.Fatal("freshPackagesCache miss for a freshly written versioned cache, want hit")
	}
	if back.Len() != ix.Len() {
		t.Errorf("round-trip Len = %d, want %d", back.Len(), ix.Len())
	}
	if doc, found := back.Lookup("claude-code"); !found || doc.Version != "2.1.193" {
		t.Errorf("round-trip claude-code = %+v, want version 2.1.193", doc)
	}
}

// loadRawFixtureIndex parses the shared RAW-shape packages.json fixture into an
// Index without networking, for cache round-trip tests.
func loadRawFixtureIndex() (*packages.Index, error) {
	path := filepath.Join("..", "analysis", "packages", "testdata", "packages.fixture.json")
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return packages.ParseStream(f)
}

// packagesFixturePath returns the absolute path to the RAW-shape packages.json
// fixture (6 well-formed entries) used to load an index without networking.
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
	if got := index.Len(); got != 6 {
		t.Errorf("index.Len() = %d, want 6", got)
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

// TestHandlerHoverWellknownFallback covers the curated well-known fallback: an
// attr absent from the dataset (runtimeShell is not a derivation, so it is never
// in packages.json) still hovers, inside a string interpolation, and carries no
// channel-provenance line even when a channel is recorded.
func TestHandlerHoverWellknownFallback(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const src = `{ pkgs, ... }: { x = "${pkgs.runtimeShell}"; }`
	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, src)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	// Record a channel as auto mode would: a well-known fallback hover must still
	// not claim it, since the curated table is not channel data.
	handler.setPackagesChannel("nixpkgs-unstable")
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, src)

	line, char := posOf(t, src, "runtimeShell", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want well-known fallback hover")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**runtimeShell**", "not a derivation"} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	if strings.Contains(value, "channel data") {
		t.Errorf("well-known fallback must not carry channel provenance:\n%s", value)
	}
}

// TestHandlerHoverWellknownUnderWithPkgs confirms the fallback also fires on the
// bare-identifier resolution path: it applies after attr resolution, so a
// `with pkgs;` name that misses the dataset lands in the curated table the same
// way a select does.
func TestHandlerHoverWellknownUnderWithPkgs(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const src = "{ pkgs, ... }: { shell = with pkgs; mkShell {}; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "shell.nix")
	writeFile(t, modPath, src)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, src)

	line, char := posOf(t, src, "mkShell", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want well-known fallback hover for with-pkgs mkShell")
	}
	if !strings.Contains(hover.Contents.Value, "development-shell") {
		t.Errorf("hover value missing mkShell description:\n%s", hover.Contents.Value)
	}
}

// withPkgsFixture supplies `go` through a `with pkgs;` scope rather than a
// `pkgs.<attr>` select, exercising the bare-identifier package hover path.
const withPkgsFixture = "{ pkgs, ... }: { corePackages = with pkgs; [ nodejs_22 go ]; }"

func TestHandlerHoverWithPkgsBareIdent(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, withPkgsFixture)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, withPkgsFixture)

	line, char := posOf(t, withPkgsFixture, "go", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want package-doc hover for `with pkgs;` go")
	}
	value := hover.Contents.Value
	for _, want := range []string{"**go**", "1.26.4"} {
		if !strings.Contains(value, want) {
			t.Errorf("hover value missing %q:\n%s", want, value)
		}
	}
	// Explicit-path mode records no channel, so no provenance line is appended.
	if strings.Contains(value, "channel data") {
		t.Errorf("explicit-path hover should not carry provenance:\n%s", value)
	}
	// The range spans exactly the hovered identifier.
	if hover.Range.Start.Character != char || hover.Range.End.Character != char+len("go") {
		t.Errorf("range = %d..%d, want %d..%d", hover.Range.Start.Character, hover.Range.End.Character, char, char+len("go"))
	}
}

// TestHandlerHoverWithPkgsShadowedFallsThrough proves the hover order: a name
// bound by a local `let` under `with pkgs;` resolves to that binding, so package
// hover declines and binding-value hover answers instead.
func TestHandlerHoverWithPkgsShadowedFallsThrough(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const src = "{ pkgs, ... }: let go = 42; in { corePackages = with pkgs; [ go ]; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, src)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, src)

	// The body use of `go` is the second occurrence (the first is the let name).
	line, char := posOf(t, src, "go", 1)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want binding-value hover for shadowed `go`")
	}
	value := hover.Contents.Value
	if !strings.Contains(value, "let binding") {
		t.Errorf("hover value missing \"let binding\" (fall-through failed):\n%s", value)
	}
	if strings.Contains(value, "1.26.4") {
		t.Errorf("shadowed `go` must not claim the nixpkgs go version:\n%s", value)
	}
}

// TestHandlerHoverBareIdentNoWithNull confirms a bare identifier with no
// enclosing `with pkgs;` and no local binding yields null: package hover declines
// and binding-value hover cannot resolve it.
func TestHandlerHoverBareIdentNoWithNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	const src = "{ pkgs, ... }: { corePackages = [ go ]; }"
	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, src)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, src)

	line, char := posOf(t, src, "go", 0)
	if hover := requestHover(t, handler, modURI, line, char+1); hover != nil {
		t.Fatalf("hover on bare `go` outside `with pkgs;` = %+v, want null", hover)
	}
}

// TestHandlerHoverPackageProvenanceLine asserts that when a channel is recorded
// (as auto mode does), package hover appends the provenance line.
func TestHandlerHoverPackageProvenanceLine(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	modPath := filepath.Join(root, "home.nix")
	writeFile(t, modPath, withPkgsFixture)
	initWithPackages(t, handler, root, packagesFixturePath(t))
	// Simulate an auto-mode load having recorded the channel.
	handler.setPackagesChannel("nixpkgs-unstable")
	modURI := mustURI(t, modPath)
	openDocument(t, handler, modURI, withPkgsFixture)

	line, char := posOf(t, withPkgsFixture, "go", 0)
	hover := requestHover(t, handler, modURI, line, char+1)
	if hover == nil {
		t.Fatal("hover = null, want package-doc hover")
	}
	value := hover.Contents.Value
	want := "*nixpkgs-unstable channel data — an overlay may change the actual version*"
	if !strings.Contains(value, want) {
		t.Errorf("hover value missing provenance line %q:\n%s", want, value)
	}
	if !strings.Contains(value, "1.26.4") {
		t.Errorf("hover value missing version alongside provenance:\n%s", value)
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
