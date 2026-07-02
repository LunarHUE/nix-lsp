package scopes

import "testing"

func TestPkgPathAt(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// target is the substring to place the cursor on (its first char); nth
		// selects the occurrence.
		target string
		nth    int
		// wantAttr "" means ok=false is expected.
		wantAttr string
		// wantRange is the covered text of the returned segment range.
		wantRange string
	}{
		// Single-segment select in a list element (the common home.packages shape).
		{"single segment", `{ home.packages = [ pkgs.claude-code ]; }`, "claude-code", 0,
			"claude-code", "claude-code"},

		// Nested chain: path grows as the cursor moves right.
		{"nested interior", `{ x = pkgs.python312Packages.requests; }`, "python312Packages", 0,
			"python312Packages", "python312Packages"},
		{"nested leaf", `{ x = pkgs.python312Packages.requests; }`, "requests", 0,
			"python312Packages.requests", "requests"},

		// The pkgs base is not a hover target.
		{"base itself", `{ x = pkgs.claude-code; }`, "pkgs", 0,
			"", ""},

		// A dynamic segment anywhere on the covered span bails.
		{"dynamic segment", `{ x = pkgs.${name}.foo; }`, "foo", 0,
			"", ""},

		// A non-pkgs base bails.
		{"non-pkgs base", `{ x = lib.mkIf; }`, "mkIf", 0,
			"", ""},

		// A different identifier that only starts with pkgs is not the pkgs base.
		{"lookalike base", `{ x = pkgsCross.foo; }`, "foo", 0,
			"", ""},

		// Positions that are not on a select segment.
		{"on value", `{ x = 42; }`, "42", 0,
			"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			pos := posAtNth(t, tc.src, tc.target, tc.nth)
			attr, r, ok := PkgPathAt(tree, pos)
			if tc.wantAttr == "" {
				if ok {
					t.Fatalf("PkgPathAt ok = true (attr %q, range %q), want ok=false",
						attr, textAt(tc.src, r))
				}
				return
			}
			if !ok {
				t.Fatalf("PkgPathAt ok = false, want attr %q", tc.wantAttr)
			}
			if attr != tc.wantAttr {
				t.Fatalf("PkgPathAt attr = %q, want %q", attr, tc.wantAttr)
			}
			if got := textAt(tc.src, r); got != tc.wantRange {
				t.Fatalf("PkgPathAt range text = %q, want %q", got, tc.wantRange)
			}
		})
	}
}

func TestWithPkgsAttrAt(t *testing.T) {
	tests := []struct {
		name   string
		src    string
		target string
		nth    int
		// wantAttr "" means ok=false is expected.
		wantAttr string
	}{
		// A bare, unresolved name in a `with pkgs;` list is a nixpkgs attribute.
		{"with pkgs list member", `with pkgs; [ nodejs go ]`, "go", 0, "go"},

		// A let binding shadows the name: it resolves locally, so it is not a
		// nixpkgs attribute. The body use is the second occurrence of `go`.
		{"shadowed by let", `let go = 1; in with pkgs; go`, "go", 1, ""},

		// No enclosing `with` at all.
		{"no with", `[ go ]`, "go", 0, ""},

		// The subject is a select, not the bare identifier `pkgs`.
		{"with select subject", `with pkgs.python3Packages; requests`, "requests", 0, ""},

		// Nested withs: the inner `with pkgs;` supplies the name even though the
		// outer `with lib;` does not.
		{"nested with pkgs inner", `with lib; with pkgs; go`, "go", 0, "go"},

		// The identifier that is itself the with subject sits in the environment,
		// not the body, so it is not a supplied attribute.
		{"with subject itself", `with pkgs; go`, "pkgs", 0, ""},

		// A builtin resolves via the scope model, so it is never a nixpkgs attr.
		{"builtin under with", `with pkgs; toString`, "toString", 0, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			file := Analyze(tree)
			pos := posAtNth(t, tc.src, tc.target, tc.nth)
			attr, r, ok := WithPkgsAttrAt(file, tree, pos)
			if tc.wantAttr == "" {
				if ok {
					t.Fatalf("WithPkgsAttrAt ok = true (attr %q, range %q), want ok=false",
						attr, textAt(tc.src, r))
				}
				return
			}
			if !ok {
				t.Fatalf("WithPkgsAttrAt ok = false, want attr %q", tc.wantAttr)
			}
			if attr != tc.wantAttr {
				t.Fatalf("WithPkgsAttrAt attr = %q, want %q", attr, tc.wantAttr)
			}
			if got := textAt(tc.src, r); got != tc.wantAttr {
				t.Fatalf("WithPkgsAttrAt range text = %q, want %q", got, tc.wantAttr)
			}
		})
	}
}
