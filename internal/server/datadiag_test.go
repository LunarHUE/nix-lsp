package server

import (
	"testing"
	"time"

	"github.com/wesleybaldwin/nix-lsp/internal/analysis/datadiag"
	"github.com/wesleybaldwin/nix-lsp/internal/syntax"
)

// waitForDiagnosticCode blocks until a diagnostic with the given code is present
// for uri, returning it, or fails loudly after a deadline. It wakes on the
// handler's publish broadcast instead of a fixed poll so it converges as fast as
// the recompute lands (and stays reliable under -race).
func waitForDiagnosticCode(t *testing.T, handler *Handler, uri, code string) syntax.Diagnostic {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		// State and broadcast channel under one lock hold, else a publish between
		// the two reads is a missed wakeup (see waitForDiagnostics).
		handler.mu.RLock()
		diagnostics := cloneDiagnostics(handler.diagnostics[uri])
		ch := handler.diagPublished
		handler.mu.RUnlock()
		for _, d := range diagnostics {
			if d.Code == code {
				return d
			}
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out waiting for diagnostic code %q on %s; have %+v", code, uri, handler.Diagnostics(uri))
			return syntax.Diagnostic{}
		}
	}
}

// diagnosticWithCode returns the first diagnostic with code, or ok=false.
func diagnosticWithCode(diags []syntax.Diagnostic, code string) (syntax.Diagnostic, bool) {
	for _, d := range diags {
		if d.Code == code {
			return d, true
		}
	}
	return syntax.Diagnostic{}, false
}

func TestDatasetNoOptionDiagnosticsWithoutIndex(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// No optionsPath: the dataset never loads, so a module with a typo gets no
	// dataset diagnostics.
	initWorkspace(t, handler, t.TempDir())
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	before := publishedGen(handler, uri)
	openDocument(t, handler, uri, src)
	// Wait for the didOpen recompute to actually publish, then assert none carry
	// the dataset code (proving absence, not just checking before any compute).
	waitForDiagnosticsGen(t, handler, uri, before+1)
	if _, ok := diagnosticWithCode(handler.Diagnostics(uri), datadiag.CodeUnknownOption); ok {
		t.Fatalf("unexpected unknown-option diagnostic without a loaded index: %+v", handler.Diagnostics(uri))
	}
}

func TestDatasetOptionDiagnosticAndCodeAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
	if want := "unknown option: networking.firewal (did you mean firewall?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}

	// A code action request on the flagged range offers the did-you-mean fix.
	actions := requestCodeActions(t, handler, uri,
		d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, nil)
	action := actionByTitle(t, actions, "Change to 'firewall'")
	if action.Edit == nil {
		t.Fatal("action has no edit")
	}
	edits := action.Edit.Changes[uri]
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1", len(edits))
	}
	got := applyEdits(t, src, edits)
	want := `{ config, ... }:
{
  networking.firewall.enable = true;
}
`
	if got != want {
		t.Errorf("applied edit =\n%q\nwant\n%q", got, want)
	}
}

func TestDatasetPackageDiagnosticAndCodeAction(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), "", packagesFixturePath(t))
	uri := mustURI(t, "/tmp/pkg.nix")
	src := `{ x = pkgs.htoop; }`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownPackage)
	if want := "unknown package: pkgs.htoop (did you mean htop?)"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}

	actions := requestCodeActions(t, handler, uri,
		d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, nil)
	action := actionByTitle(t, actions, "Change to 'htop'")
	got := applyEdits(t, src, action.Edit.Changes[uri])
	if want := `{ x = pkgs.htop; }`; got != want {
		t.Errorf("applied edit = %q, want %q", got, want)
	}
}

func TestDatasetBothDiagnosticsInOneFile(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), packagesFixturePath(t))
	uri := mustURI(t, "/tmp/both.nix")
	src := `{ config, pkgs, ... }:
{
  networking.firewal.enable = true;
  environment.systemPackages = [ pkgs.htoop ];
}
`
	openDocument(t, handler, uri, src)

	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownPackage)
}

func TestDatasetOptionTypeMismatchPublished(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewall.enable = "yes";
}
`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeOptionTypeMismatch)
	if want := "type mismatch: networking.firewall.enable expects boolean, got string"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
	// The flagged range must cover exactly the "yes" value expression.
	got := applyEdits(t, src, []TextEdit{{Range: toProtocolRange(d.Range), NewText: "REPL"}})
	want := `{ config, ... }:
{
  networking.firewall.enable = REPL;
}
`
	if got != want {
		t.Errorf("range did not cover the value; replaced source =\n%q", got)
	}
}

func TestDatasetUserRealModuleNoDiagnostics(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/real-module.nix")
	// A user's real minimal-profile module, verbatim: an interpolated import, a
	// real-but-undocumented option (system.disableInstallerTools is declared
	// internal and absent from options.json while its sibling stateVersion is
	// present), a freeform nix.settings leaf descent, and a lib.mkDefault value.
	// The module gate arms via the >=2 exact-hits path (no config formal), and
	// every conservative rule must hold: zero diagnostics of any kind.
	src := `{ modulesPath, lib, ... }:
{
  imports = [
    "${modulesPath}/profiles/minimal.nix"
  ];
  system.disableInstallerTools = true;
  boot.initrd.systemd.enable = true;
  boot.loader.systemd-boot.configurationLimit = lib.mkDefault 2;
  nix.settings.auto-optimise-store = true;
}
`
	before := publishedGen(handler, uri)
	openDocument(t, handler, uri, src)
	// Wait for the didOpen recompute to publish, then assert nothing was flagged.
	waitForDiagnosticsGen(t, handler, uri, before+1)
	if diags := handler.Diagnostics(uri); len(diags) != 0 {
		t.Fatalf("diagnostics = %+v, want none", diags)
	}
}

func TestDatasetSyntaxErrorOptionGuidancePublished(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	// The misclassification report's exact buffer in its module wrapper: the wg0
	// binding misses its `;`, and the published message must both classify the
	// missing semicolon and carry the option guidance for the enclosing path.
	src := "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    }\n  };\n}\n"
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, "syntax-error")
	want := "syntax error: missing ';' after binding — networking.wireguard.interfaces.wg0 accepts options like ips, peers, privateKey"
	if d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
}

func TestDatasetSyntaxErrorNoIndexKeepsPlainMessage(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// No options index: the same buffer publishes the classified hint with no
	// option guidance appended.
	initWorkspace(t, handler, t.TempDir())
	uri := mustURI(t, "/tmp/mod.nix")
	src := "{ config, ... }:\n{\n  networking.wireguard.interfaces = {\n    wg0 = {\n      \n    }\n  };\n}\n"
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, "syntax-error")
	if want := "syntax error: missing ';' after binding"; d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
}

func TestDatasetRefreshOnLoadRepublishes(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	// Open the module BEFORE any index exists: no dataset diagnostics yet.
	initWorkspace(t, handler, t.TempDir())
	uri := mustURI(t, "/tmp/mod.nix")
	src := `{ config, ... }:
{
  networking.firewal.enable = true;
}
`
	before := publishedGen(handler, uri)
	openDocument(t, handler, uri, src)
	// Wait for the didOpen recompute to publish, then assert no dataset code yet.
	waitForDiagnosticsGen(t, handler, uri, before+1)
	if _, ok := diagnosticWithCode(handler.Diagnostics(uri), datadiag.CodeUnknownOption); ok {
		t.Fatalf("unexpected unknown-option before the index loaded")
	}

	// Inject the index through the same seam the async loader uses; its re-publish
	// hook must recompute diagnostics for the open document with no further edit.
	handler.loadOptionsFromFile(optionsFixturePath(t))
	waitForDiagnosticCode(t, handler, uri, datadiag.CodeUnknownOption)
}

func TestDatasetEnumMismatchDidYouMean(t *testing.T) {
	handler := NewHandler()
	defer handler.Close()

	initWithDatasets(t, handler, t.TempDir(), optionsFixturePath(t), "")
	uri := mustURI(t, "/tmp/mod.nix")
	// "noo" is one edit from the legal "no", so a quoted did-you-mean is offered.
	src := `{ config, ... }:
{
  services.openssh.settings.PermitRootLogin = "noo";
}
`
	openDocument(t, handler, uri, src)

	d := waitForDiagnosticCode(t, handler, uri, datadiag.CodeOptionTypeMismatch)
	actions := requestCodeActions(t, handler, uri,
		d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character, nil)
	action := actionByTitle(t, actions, `Change to '"no"'`)
	edits := action.Edit.Changes[uri]
	if len(edits) != 1 {
		t.Fatalf("got %d edits, want 1", len(edits))
	}
	got := applyEdits(t, src, edits)
	want := `{ config, ... }:
{
  services.openssh.settings.PermitRootLogin = "no";
}
`
	if got != want {
		t.Errorf("applied edit =\n%q\nwant\n%q", got, want)
	}
}
