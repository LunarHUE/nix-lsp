package datadiag

import (
	"strings"
	"testing"

	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// userSnippetModule is the misclassification report's exact buffer (a wg0
// submodule binding missing the `;` after its inner brace) in its module
// wrapper. The parser reports one missing-';' syntax error on the wg0 binding.
const userSnippetModule = "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    }\n  };\n}\n"

// enriched parses src, takes its real parse diagnostics, and runs the
// enrichment against the shared fixture index.
func enriched(t *testing.T, src string) ([]syntax.Diagnostic, []syntax.Diagnostic) {
	t.Helper()
	tree := mustParse(t, src)
	in := tree.Diagnostics()
	out := EnrichSyntaxDiagnostics(tree, loadOptionsIndex(t), in)
	return in, out
}

func TestEnrichSyntaxUserSnippetAddsOptionGuidance(t *testing.T) {
	in, out := enriched(t, userSnippetModule)
	if len(out) != 1 {
		t.Fatalf("got %d diagnostics, want 1: %+v", len(out), out)
	}
	want := "syntax error: missing ';' after binding — networking.wireguard.interfaces.wg0 accepts options like ips, peers, privateKey"
	if out[0].Message != want {
		t.Errorf("message = %q, want %q", out[0].Message, want)
	}
	// Only the message changes: same range, code, severity, count.
	if out[0].Range != in[0].Range || out[0].Code != in[0].Code || out[0].Severity != in[0].Severity {
		t.Errorf("enrichment changed more than the message: in=%+v out=%+v", in[0], out[0])
	}
	// The input slice is never mutated (it may be memoized upstream).
	if strings.Contains(in[0].Message, "accepts options") {
		t.Errorf("input diagnostic mutated in place: %q", in[0].Message)
	}
}

func TestEnrichSyntaxNonModuleFileUnchanged(t *testing.T) {
	// The same broken shape in a bare data attrset (no config formal, no exact
	// option hits) fails the module gate: messages stay untouched.
	src := "{\n  foo.bar.baz = {\n    qux = {\n      \n    }\n  };\n}\n"
	in, out := enriched(t, src)
	if len(in) == 0 {
		t.Fatal("probe source produced no syntax errors; test needs one")
	}
	for i := range out {
		if out[i].Message != in[i].Message {
			t.Errorf("message %d changed to %q despite failed module gate", i, out[i].Message)
		}
	}
}

func TestEnrichSyntaxUnresolvablePathUnchanged(t *testing.T) {
	// Module gate passes (config formal), but the enclosing path names no known
	// option group, so the error keeps its plain message.
	src := "{ config, ... }:\n{\n  totally.unknown.thing = {\n    sub = {\n      \n    }\n  };\n}\n"
	in, out := enriched(t, src)
	if len(in) == 0 {
		t.Fatal("probe source produced no syntax errors; test needs one")
	}
	for i := range out {
		if out[i].Message != in[i].Message {
			t.Errorf("message %d changed to %q for an unresolvable path", i, out[i].Message)
		}
	}
}

func TestEnrichSyntaxErrorOutsideBindingUnchanged(t *testing.T) {
	// A parse error at the top level of a module (not inside any binding) has no
	// enclosing option path; nothing is appended.
	src := "{ config, ... }:\n{\n  networking.firewall.enable = true;\n  ;\n}\n"
	tree := mustParse(t, src)
	in := tree.Diagnostics()
	if len(in) == 0 {
		t.Skip("probe produced no syntax error; recovery changed")
	}
	out := EnrichSyntaxDiagnostics(tree, loadOptionsIndex(t), in)
	for i := range out {
		if out[i].Message != in[i].Message {
			t.Errorf("message %d changed to %q for an error outside bindings", i, out[i].Message)
		}
	}
}

func TestEnrichSyntaxNilInputsUnchanged(t *testing.T) {
	tree := mustParse(t, userSnippetModule)
	in := tree.Diagnostics()
	if out := EnrichSyntaxDiagnostics(nil, loadOptionsIndex(t), in); len(out) != len(in) || out[0].Message != in[0].Message {
		t.Errorf("nil tree changed diagnostics: %+v", out)
	}
	if out := EnrichSyntaxDiagnostics(tree, nil, in); len(out) != len(in) || out[0].Message != in[0].Message {
		t.Errorf("nil index changed diagnostics: %+v", out)
	}
	if out := EnrichSyntaxDiagnostics(tree, loadOptionsIndex(t), nil); out != nil {
		t.Errorf("nil diags = %+v, want nil", out)
	}
}

func TestOptionChildrenHintCapsAndSorts(t *testing.T) {
	// A synthetic index with six children under a group: the hint lists the first
	// four alphabetically.
	index := mustIndex(t, `{
	  "g.f": {"loc":["g","f"],"type":"boolean"},
	  "g.e": {"loc":["g","e"],"type":"boolean"},
	  "g.d": {"loc":["g","d"],"type":"boolean"},
	  "g.c": {"loc":["g","c"],"type":"boolean"},
	  "g.b": {"loc":["g","b"],"type":"boolean"},
	  "g.a": {"loc":["g","a"],"type":"boolean"}
	}`)
	root, ok := index.Root()
	if !ok {
		t.Fatal("index has no root")
	}
	hint, ok := optionChildrenHint(root, []string{"g"})
	if !ok {
		t.Fatal("optionChildrenHint = !ok, want a hint")
	}
	if want := "g accepts options like a, b, c, d"; hint != want {
		t.Errorf("hint = %q, want %q", hint, want)
	}
}
