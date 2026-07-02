package scopes

import (
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// posAtNth returns the LSP position of the first character of the nth (0-based)
// occurrence of sub in src (ASCII sources only).
func posAtNth(t *testing.T, src, sub string, nth int) syntax.Position {
	t.Helper()
	idx, from := -1, 0
	for i := 0; i <= nth; i++ {
		j := strings.Index(src[from:], sub)
		if j < 0 {
			t.Fatalf("occurrence %d of %q not found in %q", nth, sub, src)
		}
		idx = from + j
		from = idx + 1
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
	return syntax.Position{Line: line, Character: col}
}

func pathsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestOptionPathAt(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// target is the substring to place the cursor on (its first char); nth
		// selects the occurrence.
		target string
		nth    int
		// wantPath nil means ok=false is expected.
		wantPath []string
		// wantRange is the covered text of the returned segment range.
		wantRange string
	}{
		// Flat binding: path grows as the cursor moves right along the attrpath.
		{"flat root segment", `{ networking.firewall.allowedTCPPorts = [ 22 ]; }`, "networking", 0,
			[]string{"networking"}, "networking"},
		{"flat middle segment", `{ networking.firewall.allowedTCPPorts = [ 22 ]; }`, "firewall", 0,
			[]string{"networking", "firewall"}, "firewall"},
		{"flat leaf segment", `{ networking.firewall.allowedTCPPorts = [ 22 ]; }`, "allowedTCPPorts", 0,
			[]string{"networking", "firewall", "allowedTCPPorts"}, "allowedTCPPorts"},

		// Nested attrsets compose the full path (3 levels).
		{"nested compose", `{ networking = { firewall = { allowedTCPPorts = [ 22 ]; }; }; }`, "allowedTCPPorts", 0,
			[]string{"networking", "firewall", "allowedTCPPorts"}, "allowedTCPPorts"},

		// rec attrset behaves like a plain attrset.
		{"rec attrset", `rec { networking.firewall.enable = true; }`, "enable", 0,
			[]string{"networking", "firewall", "enable"}, "enable"},

		// Nested attrset whose inner binding has a multi-segment attrpath.
		{"mixed inner leaf", `{ networking = { firewall.allowedTCPPorts = [ 22 ]; }; }`, "allowedTCPPorts", 0,
			[]string{"networking", "firewall", "allowedTCPPorts"}, "allowedTCPPorts"},
		{"mixed inner interior", `{ networking = { firewall.allowedTCPPorts = [ 22 ]; }; }`, "firewall", 0,
			[]string{"networking", "firewall"}, "firewall"},

		// Leading config is stripped for binding attrpaths.
		{"config stripped", `{ config.networking.firewall.enable = true; }`, "enable", 0,
			[]string{"networking", "firewall", "enable"}, "enable"},
		{"config base itself", `{ config.networking.firewall.enable = true; }`, "config", 0,
			nil, ""},

		// Module function wrapper: the ascent stops at the function body.
		{"function wrapper", `{ pkgs, ... }: { networking.firewall.enable = true; }`, "enable", 0,
			[]string{"networking", "firewall", "enable"}, "enable"},

		// let-in body is transparent; let bindings are not option paths.
		{"let body", `let x = 1; in { networking.firewall.enable = true; }`, "enable", 0,
			[]string{"networking", "firewall", "enable"}, "enable"},
		{"let binding attrpath", `let networking.firewall = 1; in x`, "firewall", 0,
			nil, ""},
		{"plain let binding", `let foo = 1; in foo`, "foo", 0,
			nil, ""},

		// String attrpath segment: value unquoted, range includes the quotes.
		{"string segment", `{ services."my-svc".enable = true; }`, "my-svc", 0,
			[]string{"services", "my-svc"}, `"my-svc"`},

		// Dynamic segment anywhere on the path bails.
		{"dynamic segment", `{ services.${name}.enable = true; }`, "enable", 0,
			nil, ""},

		// Select expressions rooted at config.
		{"select interior", `{ foo = config.networking.firewall.enable; }`, "firewall", 0,
			[]string{"networking", "firewall"}, "firewall"},
		{"select leaf", `{ foo = config.networking.firewall.enable; }`, "enable", 0,
			[]string{"networking", "firewall", "enable"}, "enable"},
		{"select config base", `{ foo = config.networking.firewall.enable; }`, "config", 0,
			nil, ""},
		{"select non-config base", `{ foo = lib.mkIf true; }`, "mkIf", 0,
			nil, ""},

		// Positions that are not on an attrpath/select segment.
		{"on value", `{ foo = 42; }`, "42", 0,
			nil, ""},
		{"on let identifier reference", `let cfg = 1; in cfg`, "cfg", 1,
			nil, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			pos := posAtNth(t, tc.src, tc.target, tc.nth)
			path, r, ok := OptionPathAt(tree, pos)
			if tc.wantPath == nil {
				if ok {
					t.Fatalf("OptionPathAt ok = true (path %v, range %q), want ok=false",
						path, textAt(tc.src, r))
				}
				return
			}
			if !ok {
				t.Fatalf("OptionPathAt ok = false, want path %v", tc.wantPath)
			}
			if !pathsEqual(path, tc.wantPath) {
				t.Fatalf("OptionPathAt path = %v, want %v", path, tc.wantPath)
			}
			if got := textAt(tc.src, r); got != tc.wantRange {
				t.Fatalf("OptionPathAt range text = %q, want %q", got, tc.wantRange)
			}
		})
	}
}
