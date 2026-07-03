package scopes

import (
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// posAtByte converts an absolute ASCII byte index into an LSP position.
func posAtByte(src string, idx int) syntax.Position {
	line, col := 0, 0
	for i := 0; i < idx && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return syntax.Position{Line: line, Character: col}
}

// posAtEnd returns the position just past the end of the nth (0-based)
// occurrence of sub in src (ASCII only) - i.e. where the cursor sits right after
// having typed sub.
func posAtEnd(t *testing.T, src, sub string, nth int) syntax.Position {
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
	return posAtByte(src, idx+len(sub))
}

func TestCompletionContextAt(t *testing.T) {
	tests := []struct {
		name string
		src  string
		// pos is chosen per-case via a small closure so both start- and
		// end-of-token cursors can be expressed.
		at func(t *testing.T, src string) syntax.Position
		// wantOK false means ok=false is expected; other fields ignored.
		wantOK      bool
		wantKind    CompletionKind
		wantPrefix  []string
		wantPartial string
		// wantRange, when non-empty, is asserted against the Replace range text.
		wantRange string
		// wantEmptyRange asserts a zero-width Replace at the cursor.
		wantEmptyRange bool
	}{
		// OptionPath, broken binding attrpaths.
		{"option trailing dot", `{ networking. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "networking.", 0) },
			true, OptionPath, []string{"networking"}, "", "", true},
		{"option partial segment", `{ networking.fire }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "fire", 0) },
			true, OptionPath, []string{"networking"}, "fire", "fire", false},
		{"option module function wrapper", `{ config, ... }: { services.openssh.e }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, ".e", 0) },
			true, OptionPath, []string{"services", "openssh"}, "e", "e", false},
		{"option nested module", `{ networking = { firewall.e }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.e", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "e", "e", false},
		{"option deep flattened", `{ networking.firewall. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},

		// Trailing dot two or more segments deep in the shapes where the
		// enclosing attrset (or a sibling binding) survives the broken parse: the
		// typed path is then a whole attrpath node inside an ERROR, with the dot
		// in a separate sibling ERROR.
		{"option two-deep dot under wrapper", "{ config, ... }:\n{\n  networking.firewall.\n}\n",
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},
		{"option three-deep dot", `{ networking.firewall.allowedTCPPorts. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "allowedTCPPorts.", 0) },
			true, OptionPath, []string{"networking", "firewall", "allowedTCPPorts"}, "", "", true},
		{"option three-deep dot terse", `{ a.b.c. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "a.b.c.", 0) },
			true, OptionPath, []string{"a", "b", "c"}, "", "", true},
		{"option two-deep dot after sibling binding", "{\n  services.openssh.enable = true;\n  networking.firewall.\n}\n",
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},

		// Nested attrset + trailing dot: the enclosing binding's attrpath
		// collapses into the same ERROR, separated by `= {`.
		{"option nested attrset dot", `{ networking = { firewall. }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},
		{"option nested attrset dot multiline", "{\n  networking = {\n    firewall.\n  };\n}\n",
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},
		{"option doubly nested attrset dot", `{ a = { b = { c. }; }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "c.", 0) },
			true, OptionPath, []string{"a", "b", "c"}, "", "", true},
		{"option nested attrset deep dot", `{ networking = { firewall.allowedTCPPorts. }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "allowedTCPPorts.", 0) },
			true, OptionPath, []string{"networking", "firewall", "allowedTCPPorts"}, "", "", true},
		{"option nested rec attrset dot", `{ networking = rec { firewall. }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},

		// Config-prefixed binding attrpath with trailing dot: config stripped.
		{"option config-prefixed deep dot", `{ config.networking.firewall. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "firewall.", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "", "", true},

		// Partial typed after a deep dot: pinned (already worked via the
		// attrpath-under-ERROR segment path).
		{"option deep partial", `{ networking.firewall.allo }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "allo", 0) },
			true, OptionPath, []string{"networking", "firewall"}, "allo", "allo", false},

		// OptionPath via config-rooted select chains.
		{"config select trailing dot", `x = config.networking.`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "networking.", 0) },
			true, OptionPath, []string{"networking"}, "", "", true},
		{"config select partial", `x = config.networking.en`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "en", 0) },
			true, OptionPath, []string{"networking"}, "en", "en", false},

		// PkgAttr via pkgs-rooted select chains.
		{"pkgs trailing dot eof", `x = pkgs.`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "pkgs.", 0) },
			true, PkgAttr, nil, "", "", true},
		{"pkgs partial", `x = pkgs.cl`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "cl", 0) },
			true, PkgAttr, nil, "cl", "cl", false},
		{"pkgs nested partial", `x = pkgs.python312Packages.re`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "re", 0) },
			true, PkgAttr, []string{"python312Packages"}, "re", "re", false},
		{"pkgs nested trailing dot", `x = pkgs.python312Packages.`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "python312Packages.", 0) },
			true, PkgAttr, []string{"python312Packages"}, "", "", true},

		// WithPkgsName and LocalName.
		{"with pkgs list member", `with pkgs; [ ht ]`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "ht", 0) },
			true, WithPkgsName, nil, "ht", "", false},
		{"with pkgs shadowed by let", `let go = 1; in with pkgs; go`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "go", 1) },
			true, LocalName, nil, "go", "", false},
		{"local name fragment", `let foo = 1; in fo`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "fo", 1) },
			true, LocalName, nil, "fo", "", false},

		// Empty attrset body in option-binding position: the enclosing binding
		// path alone classifies, with nothing typed yet.
		{"empty attrset body simple", `{ networking = {  }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "= { ", 0) },
			true, OptionPath, []string{"networking"}, "", "", true},
		{"empty attrset body wildcard instance", "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    };\n  };\n}\n",
			func(t *testing.T, s string) syntax.Position { return posAtByte(s, strings.Index(s, "{\n      \n")+4) },
			true, OptionPath, []string{"networking", "wireguard", "interfaces", "wg0"}, "", "", true},
		{"empty attrset body config stripped", `{ config.networking = {  }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "= { ", 0) },
			true, OptionPath, []string{"networking"}, "", "", true},

		// Empty-body declines: no enclosing binding, a let value, a bare config
		// prefix that strips to nothing, or a body that is not empty.
		{"empty attrset function body", `{ pkgs }: {  }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, ": { ", 0) },
			false, CompletionNone, nil, "", "", false},
		{"empty attrset let value", `let x = {  }; in x`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "= { ", 0) },
			false, CompletionNone, nil, "", "", false},
		{"empty attrset bare config binding", `{ config = {  }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "= { ", 0) },
			false, CompletionNone, nil, "", "", false},
		{"non-empty attrset body declines", `{ networking = { a = 1; }; }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "a = 1; ", 0) },
			false, CompletionNone, nil, "", "", false},

		// Bails.
		{"inside comment", `# networking.foo`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "networking.", 0) },
			false, CompletionNone, nil, "", "", false},
		{"inside string", `x = "pkgs.foo"`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "pkgs.", 0) },
			false, CompletionNone, nil, "", "", false},
		{"dynamic pkg prefix", `x = pkgs.${name}.re`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "re", 0) },
			false, CompletionNone, nil, "", "", false},
		{"dynamic option segment", `{ services.${name}. }`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "}.", 0) },
			false, CompletionNone, nil, "", "", false},
		{"member select on local", `x = foo.bar.`,
			func(t *testing.T, s string) syntax.Position { return posAtEnd(t, s, "bar.", 0) },
			false, CompletionNone, nil, "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tree := parse(t, tc.src)
			file := Analyze(tree)
			pos := tc.at(t, tc.src)
			got, ok := CompletionContextAt(file, tree, pos)
			if !tc.wantOK {
				if ok {
					t.Fatalf("CompletionContextAt ok = true (%+v), want ok=false", got)
				}
				return
			}
			if !ok {
				t.Fatalf("CompletionContextAt ok = false, want kind %v", tc.wantKind)
			}
			if got.Kind != tc.wantKind {
				t.Fatalf("Kind = %v, want %v", got.Kind, tc.wantKind)
			}
			if !pathsEqual(got.Prefix, tc.wantPrefix) {
				t.Fatalf("Prefix = %v, want %v", got.Prefix, tc.wantPrefix)
			}
			if got.Partial != tc.wantPartial {
				t.Fatalf("Partial = %q, want %q", got.Partial, tc.wantPartial)
			}
			if tc.wantEmptyRange {
				if got.Replace.Start != got.Replace.End || got.Replace.Start != pos {
					t.Fatalf("Replace = %+v, want zero-width at %+v", got.Replace, pos)
				}
			}
			if tc.wantRange != "" {
				if txt := textAt(tc.src, got.Replace); txt != tc.wantRange {
					t.Fatalf("Replace text = %q, want %q", txt, tc.wantRange)
				}
			}
		})
	}
}

func TestCompletionContextEmptyListWithPkgs(t *testing.T) {
	src := `with pkgs; [  ]`
	tree := parse(t, src)
	file := Analyze(tree)
	// Cursor between the two spaces inside the brackets.
	pos := posAtByte(src, strings.Index(src, "[ ")+2)
	got, ok := CompletionContextAt(file, tree, pos)
	if !ok {
		t.Fatalf("CompletionContextAt ok = false, want WithPkgsName")
	}
	if got.Kind != WithPkgsName || got.Partial != "" {
		t.Fatalf("got %+v, want WithPkgsName with empty partial", got)
	}
}

func TestVisibleBindings(t *testing.T) {
	src := `let foo = 1; bar = 2; in fo`
	tree := parse(t, src)
	file := Analyze(tree)
	pos := posAtEnd(t, src, "fo", 1) // the body `fo`

	names := map[string]bool{}
	for _, b := range VisibleBindings(file, pos) {
		names[b.Name] = true
		if b.Kind == Builtin {
			t.Fatalf("VisibleBindings included a builtin %q", b.Name)
		}
	}
	if !names["foo"] || !names["bar"] {
		t.Fatalf("VisibleBindings = %v, want foo and bar", names)
	}
	if names["map"] || names["true"] {
		t.Fatalf("VisibleBindings unexpectedly included a builtin: %v", names)
	}
}
