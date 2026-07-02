package flake

import (
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// posBefore reports whether a is strictly before b.
func posBefore(a, b syntax.Position) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Character < b.Character
}

func analyze(t *testing.T, src string) *File {
	t.Helper()
	tree, err := syntax.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse error = %v", err)
	}
	return AnalyzeInputs(tree)
}

func inputByName(file *File, name string) *Input {
	for _, in := range file.Inputs {
		if in.Name == name {
			return in
		}
	}
	return nil
}

func TestAnalyzeInputs(t *testing.T) {
	cases := []struct {
		name   string
		src    string
		verify func(t *testing.T, f *File)
	}{
		{
			name: "nested attrset form",
			src: `{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs";
    hm = { url = "github:x/hm"; flake = false; };
  };
  outputs = { self, nixpkgs, hm }: {};
}`,
			verify: func(t *testing.T, f *File) {
				if !f.HasInputs {
					t.Fatal("HasInputs = false")
				}
				nix := inputByName(f, "nixpkgs")
				if nix == nil || !nix.HasURL || nix.URL != "github:NixOS/nixpkgs" {
					t.Fatalf("nixpkgs = %+v", nix)
				}
				hm := inputByName(f, "hm")
				if hm == nil || hm.URL != "github:x/hm" {
					t.Fatalf("hm url = %+v", hm)
				}
				if hm.Flake == nil || *hm.Flake != false {
					t.Fatalf("hm flake = %+v, want false", hm.Flake)
				}
			},
		},
		{
			name: "attrpath sugar form",
			src: `{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs";
  inputs.hm.url = "github:x/hm";
}`,
			verify: func(t *testing.T, f *File) {
				if len(f.Inputs) != 2 {
					t.Fatalf("inputs = %d, want 2", len(f.Inputs))
				}
				if in := inputByName(f, "nixpkgs"); in == nil || in.URL != "github:NixOS/nixpkgs" {
					t.Fatalf("nixpkgs = %+v", in)
				}
			},
		},
		{
			name: "mixed and merged forms",
			src: `{
  inputs.hm.url = "github:x/hm";
  inputs.hm.flake = false;
  inputs = { hm.inputs.nixpkgs.follows = "nixpkgs"; nixpkgs.url = "u"; };
}`,
			verify: func(t *testing.T, f *File) {
				hm := inputByName(f, "hm")
				if hm == nil {
					t.Fatal("hm missing")
				}
				if hm.URL != "github:x/hm" {
					t.Errorf("hm url = %q", hm.URL)
				}
				if hm.Flake == nil || *hm.Flake {
					t.Errorf("hm flake = %+v, want false", hm.Flake)
				}
				if len(hm.Follows) != 1 || hm.Follows[0].Child != "nixpkgs" || hm.Follows[0].Target != "nixpkgs" {
					t.Errorf("hm follows = %+v", hm.Follows)
				}
				// hm appears once despite three bindings.
				count := 0
				for _, in := range f.Inputs {
					if in.Name == "hm" {
						count++
					}
				}
				if count != 1 {
					t.Errorf("hm entries = %d, want 1", count)
				}
			},
		},
		{
			name: "top-level follows alias",
			src: `{
  inputs.nixpkgs.url = "u";
  inputs.other.follows = "nixpkgs";
}`,
			verify: func(t *testing.T, f *File) {
				other := inputByName(f, "other")
				if other == nil || !other.HasTopFollows || other.TopFollows != "nixpkgs" {
					t.Fatalf("other = %+v", other)
				}
			},
		},
		{
			name: "nested follows edges",
			src: `{
  inputs.hm = {
    url = "u";
    inputs.nixpkgs.follows = "nixpkgs";
  };
}`,
			verify: func(t *testing.T, f *File) {
				hm := inputByName(f, "hm")
				if hm == nil || len(hm.Follows) != 1 {
					t.Fatalf("hm follows = %+v", hm)
				}
				if hm.Follows[0].Child != "nixpkgs" || hm.Follows[0].Target != "nixpkgs" {
					t.Errorf("edge = %+v", hm.Follows[0])
				}
			},
		},
		{
			name: "outputs formals with ellipsis",
			src:  `{ outputs = { self, nixpkgs, ... }: {}; }`,
			verify: func(t *testing.T, f *File) {
				if f.Outputs == nil || !f.Outputs.HasFormals {
					t.Fatalf("outputs = %+v", f.Outputs)
				}
				if !f.Outputs.HasEllipsis {
					t.Error("HasEllipsis = false, want true")
				}
				if f.Outputs.HasAtPattern {
					t.Error("HasAtPattern = true, want false")
				}
				if _, ok := f.Outputs.Formals["nixpkgs"]; !ok {
					t.Error("nixpkgs formal missing")
				}
			},
		},
		{
			name: "outputs formals no ellipsis",
			src:  `{ outputs = { self, nixpkgs }: {}; }`,
			verify: func(t *testing.T, f *File) {
				if f.Outputs == nil || !f.Outputs.HasFormals || f.Outputs.HasEllipsis {
					t.Fatalf("outputs = %+v", f.Outputs)
				}
				// FormalsRange spans the whole `{ self, nixpkgs }` node, so it must
				// enclose every individual formal's range.
				fr := f.Outputs.FormalsRange
				if fr == (syntax.Range{}) {
					t.Fatal("FormalsRange = zero, want the formals node range")
				}
				nixpkgs, ok := f.Outputs.Formals["nixpkgs"]
				if !ok {
					t.Fatal("nixpkgs formal missing")
				}
				if posBefore(nixpkgs.Start, fr.Start) || posBefore(fr.End, nixpkgs.End) {
					t.Errorf("FormalsRange %+v does not enclose nixpkgs formal %+v", fr, nixpkgs)
				}
			},
		},
		{
			name: "outputs at-pattern",
			src:  `{ outputs = { self, nixpkgs } @ args: {}; }`,
			verify: func(t *testing.T, f *File) {
				if f.Outputs == nil || !f.Outputs.HasAtPattern {
					t.Fatalf("outputs = %+v, want at-pattern", f.Outputs)
				}
			},
		},
		{
			name: "outputs plain arg has no formals",
			src:  `{ outputs = args: {}; }`,
			verify: func(t *testing.T, f *File) {
				if f.Outputs == nil {
					t.Fatal("Outputs nil")
				}
				if f.Outputs.HasFormals {
					t.Error("HasFormals = true, want false for plain arg")
				}
			},
		},
		{
			name: "interpolated url ignored",
			src:  `{ inputs.a.url = "github:${owner}/repo"; }`,
			verify: func(t *testing.T, f *File) {
				a := inputByName(f, "a")
				if a == nil {
					t.Fatal("a missing")
				}
				if a.HasURL {
					t.Errorf("HasURL = true for interpolated url, want false")
				}
			},
		},
		{
			name: "dynamic key skipped",
			src:  `{ inputs.${dyn}.url = "u"; inputs.ok.url = "v"; }`,
			verify: func(t *testing.T, f *File) {
				if inputByName(f, "ok") == nil {
					t.Error("ok input missing")
				}
				// The dynamic binding contributes no input.
				if len(f.Inputs) != 1 {
					t.Errorf("inputs = %d, want 1 (dynamic skipped)", len(f.Inputs))
				}
			},
		},
		{
			name: "non-attrset top level",
			src:  `[ 1 2 3 ]`,
			verify: func(t *testing.T, f *File) {
				if f.HasInputs || len(f.Inputs) != 0 || f.Outputs != nil {
					t.Errorf("non-attrset produced model %+v", f)
				}
			},
		},
		{
			name: "function-wrapped flake",
			src:  `system: { inputs.nixpkgs.url = "u"; }`,
			verify: func(t *testing.T, f *File) {
				if inputByName(f, "nixpkgs") == nil {
					t.Error("nixpkgs missing under function wrapper")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.verify(t, analyze(t, tc.src))
		})
	}
}

func TestAnalyzeInputsBindingRanges(t *testing.T) {
	t.Run("sugar form records whole top-level bindings", func(t *testing.T) {
		src := "{\n" +
			"  inputs.hm.url = \"github:x/hm\";\n" +
			"  inputs.hm.flake = false;\n" +
			"}\n"
		f := analyze(t, src)
		hm := inputByName(f, "hm")
		if hm == nil {
			t.Fatal("hm missing")
		}
		if len(hm.BindingRanges) != 2 {
			t.Fatalf("BindingRanges = %d, want 2 (one per sugar binding)", len(hm.BindingRanges))
		}
		// Each binding range covers its own line including the trailing semicolon.
		for i, want := range []string{
			"inputs.hm.url = \"github:x/hm\";",
			"inputs.hm.flake = false;",
		} {
			if got := textAt(src, hm.BindingRanges[i]); got != want {
				t.Errorf("BindingRanges[%d] text = %q, want %q", i, got, want)
			}
		}
	})

	t.Run("nested form records inner bindings", func(t *testing.T) {
		src := "{\n" +
			"  inputs = { hm.url = \"github:x/hm\"; nixpkgs.url = \"u\"; };\n" +
			"}\n"
		f := analyze(t, src)
		hm := inputByName(f, "hm")
		if hm == nil || len(hm.BindingRanges) != 1 {
			t.Fatalf("hm BindingRanges = %+v, want one", hm)
		}
		if got, want := textAt(src, hm.BindingRanges[0]), "hm.url = \"github:x/hm\";"; got != want {
			t.Errorf("hm binding text = %q, want inner binding %q", got, want)
		}
	})
}

func TestAnalyzeInputsInsertAnchor(t *testing.T) {
	src := `{ outputs = { self, nixpkgs }: {}; }`
	f := analyze(t, src)
	if f.Outputs == nil || !f.Outputs.HasInsertAnchor {
		t.Fatalf("outputs = %+v, want an insert anchor", f.Outputs)
	}
	// The anchor sits immediately after the last formal (`nixpkgs`); inserting
	// `, extra` there yields `{ self, nixpkgs, extra }`.
	nixpkgs := f.Outputs.Formals["nixpkgs"]
	if f.Outputs.InsertAnchor != nixpkgs.End {
		t.Errorf("InsertAnchor = %+v, want end of last formal %+v", f.Outputs.InsertAnchor, nixpkgs.End)
	}
}

func TestAnalyzeInputsInsertAnchorAbsentWithoutFormals(t *testing.T) {
	f := analyze(t, `{ outputs = args: {}; }`)
	if f.Outputs == nil {
		t.Fatal("Outputs nil")
	}
	if f.Outputs.HasInsertAnchor {
		t.Error("HasInsertAnchor = true, want false for a plain-arg outputs")
	}
}

// textAt returns the source substring covered by an ASCII single-line range.
func textAt(src string, r syntax.Range) string {
	lines := splitLines(src)
	if r.Start.Line != r.End.Line || r.Start.Line >= len(lines) {
		return ""
	}
	line := lines[r.Start.Line]
	if r.End.Character > len(line) {
		return ""
	}
	return line[r.Start.Character:r.End.Character]
}

func splitLines(src string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(src); i++ {
		if src[i] == '\n' {
			lines = append(lines, src[start:i])
			start = i + 1
		}
	}
	lines = append(lines, src[start:])
	return lines
}

func TestAnalyzeInputsNilTree(t *testing.T) {
	f := AnalyzeInputs(nil)
	if f == nil || f.HasInputs || len(f.Inputs) != 0 {
		t.Fatalf("nil tree model = %+v", f)
	}
}
