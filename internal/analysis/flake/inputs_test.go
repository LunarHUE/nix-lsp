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

func TestAnalyzeInputsNilTree(t *testing.T) {
	f := AnalyzeInputs(nil)
	if f == nil || f.HasInputs || len(f.Inputs) != 0 {
		t.Fatalf("nil tree model = %+v", f)
	}
}
