package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/lsp"
	"github.com/wesleybaldwin/nix-lsp/internal/vfs"
)

func TestHandlerInitializeAdvertisesReferencesAndFoldingCapabilities(t *testing.T) {
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
	if !init.Capabilities.ReferencesProvider {
		t.Error("ReferencesProvider = false, want true")
	}
	if !init.Capabilities.FoldingRangeProvider {
		t.Error("FoldingRangeProvider = false, want true")
	}
}

// The shared `x` fixture: definition at 1:2, uses at 2:6 and 4:2.
const referencesFixture = "let\n  x = 1;\n  y = x;\nin\n  x"

func TestHandlerReferencesOnUseExcludesDeclarationByDefault(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	openDocument(t, handler, uri, referencesFixture)

	// Cursor on the use at 2:6, includeDeclaration = false.
	locations := requestReferences(t, handler, uri, 2, 6, false)
	if len(locations) != 2 {
		t.Fatalf("references = %d (%+v), want 2 (both uses)", len(locations), locations)
	}
	assertNoDeclaration(t, locations)
}

func TestHandlerReferencesOnUseIncludesDeclarationWhenRequested(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	openDocument(t, handler, uri, referencesFixture)

	locations := requestReferences(t, handler, uri, 2, 6, true)
	if len(locations) != 3 {
		t.Fatalf("references = %d (%+v), want 3 (declaration + two uses)", len(locations), locations)
	}
	assertHasDeclaration(t, locations)
}

func TestHandlerReferencesOnDeclarationNameResolves(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	openDocument(t, handler, uri, referencesFixture)

	// Cursor on the definition name at 1:2.
	withDecl := requestReferences(t, handler, uri, 1, 2, true)
	if len(withDecl) != 3 {
		t.Fatalf("references with declaration = %d (%+v), want 3", len(withDecl), withDecl)
	}
	assertHasDeclaration(t, withDecl)

	withoutDecl := requestReferences(t, handler, uri, 1, 2, false)
	if len(withoutDecl) != 2 {
		t.Fatalf("references without declaration = %d (%+v), want 2", len(withoutDecl), withoutDecl)
	}
	assertNoDeclaration(t, withoutDecl)
}

func TestHandlerReferencesOnUnresolvedReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	openDocument(t, handler, uri, referencesFixture)

	// Cursor on the `let` keyword (0:0): no binding, no reference.
	result, err := handler.Handle(context.Background(), "textDocument/references", referenceParamsJSON(t, uri, 0, 0, true))
	if err != nil {
		t.Fatalf("references error = %v", err)
	}
	if result != nil {
		t.Fatalf("references on unresolved = %+v, want null", result)
	}
}

func TestHandlerReferencesOnBuiltinExcludesDeclaration(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	// A bare builtin reference: `true` at 0:0.
	openDocument(t, handler, uri, "true")

	// includeDeclaration = true, but builtins have no declaration site.
	locations := requestReferences(t, handler, uri, 0, 0, true)
	if len(locations) != 1 {
		t.Fatalf("references = %d (%+v), want 1 (the use only)", len(locations), locations)
	}
	if locations[0].Range.Start.Line != 0 || locations[0].Range.Start.Character != 0 {
		t.Errorf("reference start = %+v, want 0:0", locations[0].Range.Start)
	}
}

func TestHandlerFoldingRangeReportsNestedConstructsSorted(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))

	// Line layout (0-based):
	//   0: let
	//   1:   x = 1;
	//   2:   list = [
	//   3:     1
	//   4:     2
	//   5:   ];
	//   6:   attrs = {
	//   7:     a = 1;
	//   8:     b = 2;
	//   9:   };
	//  10: in
	//  11:   attrs
	src := "let\n  x = 1;\n  list = [\n    1\n    2\n  ];\n  attrs = {\n    a = 1;\n    b = 2;\n  };\nin\n  attrs"
	openDocument(t, handler, uri, src)

	ranges := requestFoldingRanges(t, handler, uri)
	if len(ranges) != 3 {
		t.Fatalf("folding ranges = %d (%+v), want 3", len(ranges), ranges)
	}

	// Sorted by start line, ascending.
	for i := 1; i < len(ranges); i++ {
		if ranges[i-1].StartLine > ranges[i].StartLine {
			t.Fatalf("folding ranges not sorted by start line: %+v", ranges)
		}
	}

	// let_expression: 0..11; list_expression: 2..5; attrset_expression: 6..9.
	want := []FoldingRange{
		{StartLine: 0, EndLine: 11},
		{StartLine: 2, EndLine: 5},
		{StartLine: 6, EndLine: 9},
	}
	for i, w := range want {
		if ranges[i].StartLine != w.StartLine || ranges[i].EndLine != w.EndLine {
			t.Errorf("range[%d] = %d..%d, want %d..%d", i, ranges[i].StartLine, ranges[i].EndLine, w.StartLine, w.EndLine)
		}
	}
}

func TestHandlerFoldingRangeSingleLineYieldsNone(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))
	openDocument(t, handler, uri, "{ a = 1; b = 2; }")

	ranges := requestFoldingRanges(t, handler, uri)
	if len(ranges) != 0 {
		t.Fatalf("folding ranges = %+v, want none for a single-line attrset", ranges)
	}
}

func TestHandlerFoldingRangeDedupesIdenticalParentChildSpan(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()
	uri := mustURI(t, filepath.Join(t.TempDir(), "test.nix"))

	// `x: { ... }`: the function_expression and its attrset body span the same
	// lines, so only one folding range should be emitted.
	//   0: x: {
	//   1:   a = 1;
	//   2: }
	openDocument(t, handler, uri, "x: {\n  a = 1;\n}")

	ranges := requestFoldingRanges(t, handler, uri)
	if len(ranges) != 1 {
		t.Fatalf("folding ranges = %d (%+v), want 1 after dedup", len(ranges), ranges)
	}
	if ranges[0].StartLine != 0 || ranges[0].EndLine != 2 {
		t.Errorf("range = %d..%d, want 0..2", ranges[0].StartLine, ranges[0].EndLine)
	}
}

func TestHandlerDefinitionOnImportPathJumpsToTargetFile(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	target := filepath.Join(root, "target.nix")
	writeFile(t, source, "import ./target.nix")
	writeFile(t, target, "{}")
	sourceURI := mustURI(t, source)

	openDocument(t, handler, sourceURI, "import ./target.nix")

	// Cursor inside `./target.nix` (starts at column 7).
	location := requestDefinition(t, handler, sourceURI, 0, 10)
	if location == nil {
		t.Fatal("definition on import path = null, want target file location")
	}
	wantURI := mustURI(t, mustNormalize(t, target))
	if location.URI != wantURI {
		t.Errorf("location uri = %q, want %q", location.URI, wantURI)
	}
	zero := protocolRange{}
	if location.Range != zero {
		t.Errorf("location range = %+v, want zero range", location.Range)
	}
}

func TestHandlerDefinitionOnMissingImportPathReturnsNull(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	source := filepath.Join(root, "default.nix")
	writeFile(t, source, "import ./missing.nix")
	sourceURI := mustURI(t, source)

	openDocument(t, handler, sourceURI, "import ./missing.nix")

	result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, sourceURI, 0, 10))
	if err != nil {
		t.Fatalf("definition error = %v", err)
	}
	if result != nil {
		t.Fatalf("definition on missing import = %+v, want null", result)
	}
}

func requestReferences(t *testing.T, handler *Handler, uri string, line, character int, includeDeclaration bool) []Location {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/references", referenceParamsJSON(t, uri, line, character, includeDeclaration))
	if err != nil {
		t.Fatalf("references error = %v", err)
	}
	locations, ok := result.([]Location)
	if !ok {
		t.Fatalf("references result type = %T, want []Location", result)
	}
	return locations
}

func requestFoldingRanges(t *testing.T, handler *Handler, uri string) []FoldingRange {
	t.Helper()
	result, err := handler.Handle(context.Background(), "textDocument/foldingRange", mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
	}))
	if err != nil {
		t.Fatalf("foldingRange error = %v", err)
	}
	if result == nil {
		return nil
	}
	ranges, ok := result.([]FoldingRange)
	if !ok {
		t.Fatalf("foldingRange result type = %T, want []FoldingRange", result)
	}
	return ranges
}

func referenceParamsJSON(t *testing.T, uri string, line, character int, includeDeclaration bool) json.RawMessage {
	t.Helper()
	return mustJSON(t, map[string]any{
		"textDocument": map[string]any{"uri": uri},
		"position":     map[string]any{"line": line, "character": character},
		"context":      map[string]any{"includeDeclaration": includeDeclaration},
	})
}

// assertHasDeclaration checks that the `x` fixture's declaration at 1:2 is
// present exactly once among the locations.
func assertHasDeclaration(t *testing.T, locations []Location) {
	t.Helper()
	count := 0
	for _, l := range locations {
		if l.Range.Start.Line == 1 && l.Range.Start.Character == 2 {
			count++
		}
	}
	if count != 1 {
		t.Errorf("declaration (1:2) appears %d times in %+v, want 1", count, locations)
	}
}

func assertNoDeclaration(t *testing.T, locations []Location) {
	t.Helper()
	for _, l := range locations {
		if l.Range.Start.Line == 1 && l.Range.Start.Character == 2 {
			t.Errorf("declaration (1:2) present in %+v, want only uses", locations)
		}
	}
}

func mustNormalize(t *testing.T, path string) string {
	t.Helper()
	normalized, err := vfs.NormalizePath(path)
	if err != nil {
		t.Fatalf("NormalizePath error = %v", err)
	}
	return normalized
}
