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
