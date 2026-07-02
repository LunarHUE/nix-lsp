package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// posOf returns the zero-based line and character of the occurrence-th (0-based)
// appearance of substr in src (ASCII).
func posOf(t *testing.T, src, substr string, occurrence int) (int, int) {
	t.Helper()
	idx := 0
	for o := 0; ; o++ {
		i := strings.Index(src[idx:], substr)
		if i < 0 {
			t.Fatalf("substring %q occurrence %d not found in %q", substr, occurrence, src)
		}
		idx += i
		if o == occurrence {
			break
		}
		idx += len(substr)
	}
	line, col := 0, 0
	for i := 0; i < idx; i++ {
		if src[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}

// crossFileCase drives a definition request in a two-file workspace and asserts
// the jump lands on the expected attrpath text in the target file.
type crossFileCase struct {
	name       string
	mainSrc    string
	targetName string // e.g. "lib.nix"
	targetSrc  string
	cursor     string // substring in mainSrc the cursor sits on
	cursorOcc  int    // which occurrence of cursor
	wantText   string // attrpath text in targetSrc the jump must cover
	wantOcc    int    // which occurrence of wantText in targetSrc
}

func TestHandlerDefinitionThroughSelect(t *testing.T) {
	cases := []crossFileCase{
		{
			name:       "let-bound import",
			mainSrc:    "let lib = import ./lib.nix; in lib.foo",
			targetName: "lib.nix",
			targetSrc:  "{ foo = 1; bar = 2; }",
			cursor:     "foo", cursorOcc: 0,
			wantText: "foo", wantOcc: 0,
		},
		{
			name:       "inline import",
			mainSrc:    "(import ./lib.nix).foo",
			targetName: "lib.nix",
			targetSrc:  "{ foo = 1; bar = 2; }",
			cursor:     "foo", cursorOcc: 0,
			wantText: "foo", wantOcc: 0,
		},
		{
			name:       "called import unwraps function body",
			mainSrc:    "let pkgs = import ./pkgs.nix { }; in pkgs.hello",
			targetName: "pkgs.nix",
			targetSrc:  "{ ... }: { hello = 1; }",
			cursor:     "hello", cursorOcc: 0,
			wantText: "hello", wantOcc: 0,
		},
		{
			name:       "nested path leaf",
			mainSrc:    "let lib = import ./lib.nix; in lib.a.b",
			targetName: "lib.nix",
			targetSrc:  "{ a = { b = 1; }; }",
			cursor:     ".b", cursorOcc: 0, // the `b` in lib.a.b
			wantText: "b", wantOcc: 0,
		},
		{
			name:       "nested path interior",
			mainSrc:    "let lib = import ./lib.nix; in lib.a.b",
			targetName: "lib.nix",
			targetSrc:  "{ a = { b = 1; }; }",
			cursor:     ".a", cursorOcc: 0, // the `a` in lib.a.b
			wantText: "a", wantOcc: 0,
		},
		{
			name:       "attrpath sugar",
			mainSrc:    "let lib = import ./lib.nix; in lib.a.b",
			targetName: "lib.nix",
			targetSrc:  "{ a.b = 1; }",
			cursor:     ".b", cursorOcc: 0,
			wantText: "a.b", wantOcc: 0,
		},
		{
			name:       "inherit from import",
			mainSrc:    "{ inherit (import ./lib.nix) foo; }",
			targetName: "lib.nix",
			targetSrc:  "{ foo = 1; bar = 2; }",
			cursor:     "foo", cursorOcc: 0,
			wantText: "foo", wantOcc: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewHandler()
			defer handler.Close()

			root := t.TempDir()
			mainPath := filepath.Join(root, "main.nix")
			targetPath := filepath.Join(root, tc.targetName)
			writeFile(t, mainPath, tc.mainSrc)
			writeFile(t, targetPath, tc.targetSrc)

			mainURI := mustURI(t, mainPath)
			openDocument(t, handler, mainURI, tc.mainSrc)

			line, char := posOf(t, tc.mainSrc, tc.cursor, tc.cursorOcc)
			location := requestDefinition(t, handler, mainURI, line, char+1)
			if location == nil {
				t.Fatalf("definition = null, want jump into %s", tc.targetName)
			}

			wantURI := mustURI(t, mustNormalize(t, targetPath))
			if location.URI != wantURI {
				t.Fatalf("location uri = %q, want %q", location.URI, wantURI)
			}
			wantLine, wantChar := posOf(t, tc.targetSrc, tc.wantText, tc.wantOcc)
			if location.Range.Start.Line != wantLine || location.Range.Start.Character != wantChar {
				t.Errorf("range start = %d:%d, want %d:%d", location.Range.Start.Line, location.Range.Start.Character, wantLine, wantChar)
			}
			wantEndChar := wantChar + len(tc.wantText)
			if location.Range.End.Line != wantLine || location.Range.End.Character != wantEndChar {
				t.Errorf("range end = %d:%d, want %d:%d", location.Range.End.Line, location.Range.End.Character, wantLine, wantEndChar)
			}
		})
	}
}

func TestHandlerDefinitionThroughSelectSameFile(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	src := "let cfg = { port = 80; }; in cfg.port"
	uri := mustURI(t, filepath.Join(t.TempDir(), "main.nix"))
	openDocument(t, handler, uri, src)

	// Cursor on `port` in `cfg.port` (second occurrence).
	line, char := posOf(t, src, "port", 1)
	location := requestDefinition(t, handler, uri, line, char+1)
	if location == nil {
		t.Fatal("definition = null, want local port binding")
	}
	if location.URI != uri {
		t.Errorf("location uri = %q, want %q", location.URI, uri)
	}
	// Jumps to the `port` binding inside the attrset (first occurrence).
	wantLine, wantChar := posOf(t, src, "port", 0)
	if location.Range.Start.Line != wantLine || location.Range.Start.Character != wantChar {
		t.Errorf("range start = %d:%d, want %d:%d", location.Range.Start.Line, location.Range.Start.Character, wantLine, wantChar)
	}
}

// nullCase drives a definition request that must return null (no false jump).
type nullCase struct {
	name    string
	mainSrc string
	// target files to materialize on disk, keyed by base name.
	targets map[string]string
	cursor  string
	occ     int
}

func TestHandlerDefinitionThroughSelectNulls(t *testing.T) {
	cases := []nullCase{
		{
			name:    "dynamic segment in selected path",
			mainSrc: `let lib = import ./lib.nix; in lib.${x}`,
			targets: map[string]string{"lib.nix": "{ foo = 1; }"},
			cursor:  "${x}", occ: 0,
		},
		{
			name:    "base unresolved",
			mainSrc: `undefined.foo`,
			cursor:  "foo", occ: 0,
		},
		{
			name:    "base under with is uncertain",
			mainSrc: `with pkgs; lib.foo`,
			targets: map[string]string{"lib.nix": "{ foo = 1; }"},
			cursor:  "foo", occ: 0,
		},
		{
			name:    "two import edges in base value",
			mainSrc: `let lib = if true then import ./a.nix else import ./b.nix; in lib.foo`,
			targets: map[string]string{"a.nix": "{ foo = 1; }", "b.nix": "{ foo = 2; }"},
			cursor:  "foo", occ: 0,
		},
		{
			name:    "import target missing",
			mainSrc: `let lib = import ./missing.nix; in lib.foo`,
			cursor:  "foo", occ: 0,
		},
		{
			name:    "attr not found in target",
			mainSrc: `let lib = import ./lib.nix; in lib.nope`,
			targets: map[string]string{"lib.nix": "{ foo = 1; }"},
			cursor:  "nope", occ: 0,
		},
		{
			name:    "target not an attrset",
			mainSrc: `let lib = import ./lib.nix; in lib.foo`,
			targets: map[string]string{"lib.nix": "42"},
			cursor:  "foo", occ: 0,
		},
		{
			name:    "base is a function param",
			mainSrc: `pkgs: pkgs.hello`,
			cursor:  "hello", occ: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := NewHandler()
			defer handler.Close()

			root := t.TempDir()
			mainPath := filepath.Join(root, "main.nix")
			writeFile(t, mainPath, tc.mainSrc)
			for name, content := range tc.targets {
				writeFile(t, filepath.Join(root, name), content)
			}
			mainURI := mustURI(t, mainPath)
			openDocument(t, handler, mainURI, tc.mainSrc)

			line, char := posOf(t, tc.mainSrc, tc.cursor, tc.occ)
			// char+1 lands inside the cursor substring (half-open ranges exclude
			// the end position).
			result, err := handler.Handle(context.Background(), "textDocument/definition", positionParams(t, mainURI, line, char+1))
			if err != nil {
				t.Fatalf("definition error = %v", err)
			}
			if result != nil {
				t.Fatalf("definition = %+v, want null", result)
			}
		})
	}
}

func TestHandlerDefinitionSelectNoRegression(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	root := t.TempDir()
	mainSrc := "let lib = import ./lib.nix; in lib.foo"
	mainPath := filepath.Join(root, "main.nix")
	targetPath := filepath.Join(root, "lib.nix")
	writeFile(t, mainPath, mainSrc)
	writeFile(t, targetPath, "{ foo = 1; }")
	mainURI := mustURI(t, mainPath)
	openDocument(t, handler, mainURI, mainSrc)

	// gd on the base identifier `lib` (its use in `lib.foo`) still goes to the
	// let binding in the same file.
	useLine, useChar := posOf(t, mainSrc, "lib.foo", 0)
	location := requestDefinition(t, handler, mainURI, useLine, useChar+1)
	if location == nil {
		t.Fatal("definition on base identifier = null, want let binding")
	}
	if location.URI != mainURI {
		t.Errorf("base identifier uri = %q, want %q (same file)", location.URI, mainURI)
	}
	defLine, defChar := posOf(t, mainSrc, "lib", 0) // the binding name
	if location.Range.Start.Line != defLine || location.Range.Start.Character != defChar {
		t.Errorf("base identifier def = %d:%d, want %d:%d", location.Range.Start.Line, location.Range.Start.Character, defLine, defChar)
	}

	// gd on the import path literal still crosses to the target file top.
	pathLine, pathChar := posOf(t, mainSrc, "./lib.nix", 0)
	pathLoc := requestDefinition(t, handler, mainURI, pathLine, pathChar+2)
	if pathLoc == nil {
		t.Fatal("definition on import path = null, want target file")
	}
	wantURI := mustURI(t, mustNormalize(t, targetPath))
	if pathLoc.URI != wantURI {
		t.Errorf("import path uri = %q, want %q", pathLoc.URI, wantURI)
	}
	zero := protocolRange{}
	if pathLoc.Range != zero {
		t.Errorf("import path range = %+v, want zero range", pathLoc.Range)
	}
}
